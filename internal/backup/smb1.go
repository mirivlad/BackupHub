package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const smb1Kind = "smb1"

var (
	smb1TarCount = regexp.MustCompile(`^tar: dumped ([0-9]+) files and [0-9]+ directories$`)
	smb1TarBytes = regexp.MustCompile(`^Total bytes written: ([0-9]+) \([0-9.]+ [A-Za-z]+/s\)$`)
)

func captureSMB1(ctx context.Context, settings Settings, password, target string) error {
	binary, err := exec.LookPath("smbclient")
	if err != nil {
		return errors.New("SMB1 requires the Samba smbclient package")
	}
	setpriv, err := exec.LookPath("setpriv")
	if err != nil {
		return errors.New("SMB1 requires setpriv to run the legacy client without daemon capabilities")
	}
	if strings.ContainsAny(settings.Username, "%\x00\r\n") || strings.ContainsAny(password, "\x00\r\n") {
		return errors.New("invalid SMB1 username or password")
	}
	remote := strings.Trim(strings.ReplaceAll(settings.Path, "\\", "/"), "/")
	if remote != "" && !fs.ValidPath(remote) {
		return errors.New("invalid SMB1 share-relative directory")
	}
	host, port := settings.Host, "445"
	if parsedHost, parsedPort, splitErr := net.SplitHostPort(host); splitErr == nil {
		host, port = parsedHost, parsedPort
	}
	secret, err := secretFile(password + "\n")
	if err != nil {
		return fmt.Errorf("prepare SMB1 credentials: %w", err)
	}
	defer secret.Close()
	args := []string{"--no-new-privs", "--ambient-caps=-all", "--inh-caps=-all", "--", binary,
		"-s", "/dev/null", "-d", "0", "-E", "-p", port, "-m", "NT1", "--option=client min protocol=NT1",
		"--use-kerberos=off", "--client-protection=sign", "--user=" + settings.Username, "//" + host + "/" + settings.Share}
	if remote != "" {
		args = append(args, "-D", remote)
	}
	cmd := exec.CommandContext(ctx, setpriv, append(args, "-Tc", "-")...)
	cmd.ExtraFiles = []*os.File{secret}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "PASSWD_FILE=/proc/self/fd/3"}
	var stderr boundedOutput
	stderr.max = 8192
	cmd.Stderr = &stderr
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	count, size, unpackErr := unpackSMB1Tar(ctx, stream, remote, target)
	if unpackErr == nil {
		_, unpackErr = io.Copy(io.Discard, stream)
	}
	if unpackErr != nil {
		_ = cmd.Process.Kill()
	}
	if err = cmd.Wait(); err != nil {
		return fmt.Errorf("SMB1 capture failed: %w (%s)", errors.Join(unpackErr, err), stderr.String())
	}
	if unpackErr != nil {
		return unpackErr
	}
	return validateSMB1Summary(stderr.String(), count, size)
}

func unpackSMB1Tar(ctx context.Context, stream io.Reader, remote, target string) (int64, int64, error) {
	var count, size int64
	reader := tar.NewReader(stream)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return count, size, nil
		}
		if err != nil {
			return count, size, err
		}
		name := strings.TrimPrefix(strings.TrimSuffix(header.Name, "/"), "./")
		if name == "." && header.Typeflag == tar.TypeDir {
			continue
		}
		if !fs.ValidPath(name) || strings.Contains(name, "\\") {
			return count, size, errors.New("SMB1 archive contains an invalid path")
		}
		if remote != "" {
			if name == remote && header.Typeflag == tar.TypeDir {
				continue
			}
			var underSource bool
			name, underSource = strings.CutPrefix(name, remote+"/")
			if !underSource {
				return count, size, errors.New("SMB1 archive contains an entry outside the selected directory")
			}
		}
		dest, err := safeLocal(target, name)
		if err != nil {
			return count, size, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dest, 0700)
		case tar.TypeReg:
			err = copyFile(ctx, dest, reader, os.FileMode(header.Mode)|0600, header.ModTime)
			count++
			size += header.Size
		default:
			err = errors.New("SMB1 archive contains a link or an unsupported special object")
		}
		if err != nil {
			return count, size, err
		}
	}
}

// Samba can return exit 0 even when individual files are denied. Treat every
// diagnostic besides the two accounting lines as a failed capture.
func validateSMB1Summary(diagnostics string, count, size int64) error {
	lines := strings.Split(strings.TrimSpace(diagnostics), "\n")
	if count == 0 {
		return errors.New("SMB1 capture returned no readable regular files")
	}
	if len(lines) == 2 {
		files := smb1TarCount.FindStringSubmatch(lines[0])
		bytes := smb1TarBytes.FindStringSubmatch(lines[1])
		if len(files) == 2 && len(bytes) == 2 && files[1] == strconv.FormatInt(count, 10) && bytes[1] == strconv.FormatInt(size, 10) {
			return nil
		}
	}
	return fmt.Errorf("SMB1 capture is incomplete or accounting output is unexpected: %s", diagnostics)
}
