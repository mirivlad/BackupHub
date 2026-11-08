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
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
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
	dialer := &smb2.Dialer{Credentials: smbauth.NTLMCredential{User: c.Username, Password: password}, RequireMessageSigning: true}
	session, err := dialer.Dial(ctx, c.Host)
	if err != nil {
		return fmt.Errorf("SMB connection failed: %w", err)
	}
	defer session.Close()
	share, err := session.Mount(ctx, c.Share)
	if err != nil {
		return fmt.Errorf("SMB share mount failed: %w", err)
	}
	defer share.Unmount(context.Background())
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
			// Do not follow symlinks/reparse points into other shares or drives.
			return nil
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
			return nil
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

func captureSFTP(ctx context.Context, c Settings, password, target string) error {
	address := c.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "22")
	}
	hostKeys, err := knownhosts.New("/etc/backuphub/known_hosts")
	if err != nil {
		return fmt.Errorf("SSH known_hosts is required and must be correctly populated: %w", err)
	}
	config := &ssh.ClientConfig{
		User: c.Username, Auth: []ssh.AuthMethod{ssh.Password(password)}, HostKeyCallback: hostKeys,
		Timeout: 15 * time.Second,
	}
	socket, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	sshConn, chans, requests, err := ssh.NewClientConn(socket, address, config)
	if err != nil {
		_ = socket.Close()
		return fmt.Errorf("SSH authentication or host key failed: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, requests)
	defer client.Close()
	remote, err := sftp.NewClient(client)
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
				// Fail-closed for links: do not follow symlink loops or outside paths.
				continue
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
	cmd := exec.CommandContext(ctx, pgDump, "--host", host, "--port", port, "--username", c.Username, "--dbname", c.Database, "--format=custom", "--no-owner", "--no-acl", "--file", output)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C", "PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=15"}
	var stderr boundedOutput
	stderr.max = 4096
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump failed: %w: %s", err, stderr.String())
	}
	// A valid TOC is not proof of a successful application restore, but detects
	// a truncated or invalid custom-format dump before a restic commit.
	check := exec.CommandContext(ctx, pgRestore, "--list", output)
	check.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	if err = check.Run(); err != nil {
		return fmt.Errorf("pg_restore TOC validation failed: %w", err)
	}
	return nil
}
