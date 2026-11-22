package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2/v2"
	smbauth "github.com/hirochachacha/go-smb2/v2/auth"
	"github.com/pkg/sftp"
)

func safeLocal(root, relative string) (string, error) {
	if relative == "" || relative == "." {
		return root, nil
	}
	if strings.HasPrefix(relative, "/") || strings.ContainsRune(relative, '\x00') {
		return "", errors.New("unsafe remote filename")
	}
	cleaned := path.Clean(relative)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("remote path escapes staging directory")
	}
	target := filepath.Join(root, filepath.FromSlash(cleaned))
	if rel, err := filepath.Rel(root, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("unsafe staged target")
	}
	return target, nil
}
func copyFile(ctx context.Context, target string, src io.Reader, mode os.FileMode, modified time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	copyErr := func() error {
		buf := make([]byte, 256*1024)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, readErr := src.Read(buf)
			if n > 0 {
				if _, err := file.Write(buf[:n]); err != nil {
					return err
				}
				noteProgress(ctx)
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			if n == 0 {
				return errors.New("source returned empty read without EOF")
			}
		}
	}()
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(target)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	// chmod does not grant more permissions than the original mode. ACL/xattr
	// cannot be fully preserved over every protocol and are not advertised.
	if err := os.Chmod(target, mode.Perm()); err != nil {
		return err
	}
	if !modified.IsZero() {
		if err := os.Chtimes(target, modified, modified); err != nil {
			return err
		}
	}
	return nil
}

func captureSMB(ctx context.Context, c Settings, password, target string) error {
	dialCtx, cancelDial := context.WithTimeout(ctx, connectTimeout(ctx))
	if c.Host == "" {
		cancelDial()
		return errors.New("missing SMB host")
	}
	dialer := &smb2.Dialer{
		Credentials:           smbauth.NTLMCredential{User: c.Username, Password: password},
		RequireMessageSigning: true,
		TransportDialer:       smbProgressDialer{ctx},
	}
	session, err := dialer.Dial(dialCtx, c.Host)
	cancelDial()
	if err != nil {
		if trySMB1(ctx, c.Host, err) {
			return captureSMB1(ctx, c, password, target)
		}
		return fmt.Errorf("SMB connection failed: %w", err)
	}
	defer session.Abort()
	stop := context.AfterFunc(ctx, func() { _ = session.Abort() })
	defer stop()
	share, err := session.Mount(ctx, c.Share)
	if err != nil {
		return fmt.Errorf("SMB share mount failed: %w", err)
	}
	defer share.Unmount(ctx)
	remote := strings.Trim(strings.ReplaceAll(c.Path, "\\", "/"), "/")
	if remote == "" {
		remote = "."
	}
	if !fs.ValidPath(remote) {
		return errors.New("invalid SMB share-relative directory")
	}
	fsys := share.WithContext(ctx)
	info, err := fs.Stat(fsys, remote)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("SMB source must be a directory")
	}
	files := 0
	err = fs.WalkDir(fsys, remote, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		noteProgress(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := strings.TrimPrefix(full, remote+"/")
		if full == remote {
			rel = "."
		}
		local, err := safeLocal(target, rel)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			if err := os.MkdirAll(local, 0700); err != nil {
				return err
			}
			return nil
		case entry.Type()&fs.ModeSymlink != 0:
			return errors.New("SMB contains a symbolic link/reparse point: use a capable connector; refusing incomplete snapshot")
		case entry.Type().IsRegular():
			source, err := fsys.Open(full)
			if err != nil {
				return err
			}
			stat, err := entry.Info()
			if err != nil {
				_ = source.Close()
				return err
			}
			err = copyFile(ctx, local, source, stat.Mode(), stat.ModTime())
			closeErr := source.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			files++
			return nil
		default:
			return errors.New("SMB contains an unsupported special object; refusing incomplete snapshot")
		}
	})
	if err != nil {
		return err
	}
	if files == 0 {
		return errors.New("SMB source contains no regular files (refusing empty success)")
	}
	return nil
}

func (s *Service) captureSFTP(ctx context.Context, sourceID string, c Settings, password, target string) error {
	client, err := s.connectSSH(ctx, sourceID, c, password)
	if err != nil {
		return err
	}
	defer client.Close()
	noteProgress(ctx)
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	input, err := session.StdinPipe()
	if err != nil {
		return err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	if err = session.RequestSubsystem("sftp"); err != nil {
		return err
	}
	// Watch SFTP payload. SSH keepalives must not extend a stalled transfer.
	remote, err := sftp.NewClientPipe(progressReader{ctx, output}, input)
	if err != nil {
		return err
	}
	defer remote.Close()
	base := path.Clean(c.Path)
	if !path.IsAbs(base) || base == "/" {
		return errors.New("SFTP source requires a non-root absolute directory")
	}
	stat, err := remote.Lstat(base)
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return errors.New("SFTP source must be a directory")
	}
	files := 0
	var visit func(string, string) error
	visit = func(src, rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := remote.ReadDir(src)
		if err != nil {
			return err
		}
		noteProgress(ctx)
		for _, info := range entries {
			name := info.Name()
			if name == "." || name == ".." || strings.Contains(name, "/") {
				return errors.New("invalid directory entry")
			}
			remotePath := path.Join(src, name)
			nextRel := path.Join(rel, name)
			localPath, err := safeLocal(target, nextRel)
			if err != nil {
				return err
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				return errors.New("SFTP source contains symlink; explicit symlink preservation is required before this source can be backed up")
			case info.IsDir():
				if err = os.MkdirAll(localPath, 0700); err != nil {
					return err
				}
				if err = visit(remotePath, nextRel); err != nil {
					return err
				}
				if err = os.Chmod(localPath, info.Mode().Perm()); err != nil {
					return err
				}
				if err = os.Chtimes(localPath, info.ModTime(), info.ModTime()); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				f, openErr := remote.Open(remotePath)
				if openErr != nil {
					return openErr
				}
				err = copyFile(ctx, localPath, f, info.Mode(), info.ModTime())
				closeErr := f.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
				files++
			default:
				return errors.New("SFTP contains unsupported special object; refusing incomplete snapshot")
			}
		}
		return nil
	}
	if err = visit(base, "."); err != nil {
		return err
	}
	if files == 0 {
		return errors.New("SFTP source contains no regular files (refusing empty success)")
	}
	return nil
}

func capturePostgres(ctx context.Context, c Settings, password, target string) error {
	pgDump, err := exec.LookPath("pg_dump")
	if err != nil {
		return errors.New("postgresql-client (pg_dump) not installed")
	}
	pgRestore, err := exec.LookPath("pg_restore")
	if err != nil {
		return errors.New("postgresql-client (pg_restore) not installed")
	}
	host := c.Host
	port := "5432"
	if h, p, splitErr := net.SplitHostPort(c.Host); splitErr == nil {
		host = h
		port = p
	}
	output := filepath.Join(target, "database.dump")
	// No shell, no user-supplied flags, no password on the command line.
	cmd := operationCommand(ctx, nil, pgDump, "--no-password", "--lock-wait-timeout="+connectSeconds(ctx)+"s", "--host", host, "--port", port, "--username", c.Username, "--dbname", c.Database, "--format=custom", "--no-owner", "--no-acl", "--file", output)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C", "PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=" + connectSeconds(ctx)}
	var stderr boundedOutput
	stderr.max = 4096
	cmd.Stderr = &stderr
	stop := watchFileProgress(ctx, output)
	err = cmd.Run()
	stop()
	if err != nil {
		return fmt.Errorf("pg_dump failed: %w: %s", err, stderr.String())
	}
	// A valid TOC is not proof of a successful application restore, but detects
	// a truncated or invalid custom-format dump before a restic commit.
	noteProgress(ctx)
	check := operationCommand(ctx, nil, pgRestore, "--list", output)
	check.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	if err = check.Run(); err != nil {
		return fmt.Errorf("pg_restore TOC validation failed: %w", err)
	}
	return nil
}

type smbProgressDialer struct{ ctx context.Context }

func (d smbProgressDialer) Dial(ctx context.Context, server string) (smb2.Transport, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "445")
	}
	socket, err := (&net.Dialer{}).DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	return smb2.NewTransport(progressConn{socket, d.ctx}), nil
}
