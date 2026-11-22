package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func secretFile(content string) (*os.File, error) {
	fd, err := unix.MemfdCreate("backuphub-secret", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "backuphub-secret")
	if _, err = file.WriteString(content); err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// Convert on import so scheduled SSH runs use the existing Go signer and vault.
func normalizePPK(ctx context.Context, source *CreateSource) error {
	if source.Kind != "sftp" || source.AuthMethod != "key" ||
		!strings.HasPrefix(source.PrivateKey, "PuTTY-User-Key-File-") {
		return nil
	}
	if !strings.HasPrefix(source.PrivateKey, "PuTTY-User-Key-File-2: ") &&
		!strings.HasPrefix(source.PrivateKey, "PuTTY-User-Key-File-3: ") {
		return errors.New("unsupported PuTTY PPK version (expected version 2 or 3)")
	}
	if len(source.PrivateKey) > 65536 || len(source.KeyPassphrase) > 4096 ||
		strings.ContainsAny(source.KeyPassphrase, "\r\n\x00") {
		return errors.New("invalid PuTTY key or passphrase")
	}
	binary, err := exec.LookPath("puttygen")
	if err != nil {
		return errors.New("PuTTY PPK import requires puttygen (Debian package putty-tools)")
	}
	key, err := secretFile(source.PrivateKey)
	if err != nil {
		return fmt.Errorf("prepare PuTTY key import: %w", err)
	}
	defer key.Close()
	passphrase, err := secretFile(source.KeyPassphrase + "\n")
	if err != nil {
		return fmt.Errorf("prepare PuTTY passphrase: %w", err)
	}
	defer passphrase.Close()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "/proc/self/fd/3", "-O", "private-openssh-new",
		"-o", "/dev/stdout", "--old-passphrase", "/proc/self/fd/4")
	cmd.ExtraFiles = []*os.File{key, passphrase}
	var converted boundedOutput
	converted.max = 65537
	cmd.Stdout = &converted
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("PuTTY key import: %w", ctx.Err())
		}
		return errors.New("PuTTY key import failed: check the key format and passphrase")
	}
	if _, err = parseSSHKey(converted.String(), source.KeyPassphrase); err != nil {
		return err
	}
	source.PrivateKey = converted.String()
	return nil
}
