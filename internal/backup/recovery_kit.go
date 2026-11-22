package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"github.com/mirivlad/BackupHub/internal/vault"
	"golang.org/x/sys/unix"
)

const (
	recoveryKitFormat   = "backuphub.recovery-kit.v1"
	recoveryKitManifest = "manifest.json"
)

type recoveryKitManifestV1 struct {
	Format string            `json:"format"`
	SHA256 map[string]string `json:"sha256"`
}

// ExportRecoveryKit publishes a private directory with a consistent SQLite
// snapshot and the keys referenced by that snapshot.
func ExportRecoveryKit(ctx context.Context, c *catalog.Catalog, destination string) error {
	target, staging, err := stageRecoveryDirectory(destination)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if _, err = c.DB.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(staging, "catalog.db")); err != nil {
		return fmt.Errorf("copy catalog: %w", err)
	}
	if err = os.Chmod(filepath.Join(staging, "catalog.db"), 0600); err != nil {
		return err
	}
	snapshot, err := catalog.Open(staging)
	if err != nil {
		return err
	}
	keys, keysErr := recoveryKeyNames(ctx, snapshot)
	if err = errors.Join(keysErr, snapshot.Close()); err != nil {
		return err
	}
	manifest := recoveryKitManifestV1{Format: recoveryKitFormat, SHA256: map[string]string{}}
	if manifest.SHA256["catalog.db"], err = copyRecoveryFile(staging, "", "catalog.db"); err != nil {
		return err
	}
	for _, name := range keys {
		if manifest.SHA256[name], err = copyRecoveryFile(c.DataDir, staging, name); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestFile, err := os.OpenFile(filepath.Join(staging, recoveryKitManifest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := manifestFile.Write(data)
	if err = errors.Join(writeErr, manifestFile.Sync(), manifestFile.Close()); err != nil {
		return err
	}
	return publishRecoveryDirectory(staging, target)
}

// RestoreRecoveryKit publishes a fresh state directory. Restored schedules,
// sessions and bootstrap tokens cannot trigger activity on the recovery host.
func RestoreRecoveryKit(ctx context.Context, kit, destination string) error {
	info, err := os.Lstat(kit)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("recovery kit must be a private directory")
	}
	manifestFile, err := openPrivateRecoveryFile(filepath.Join(kit, recoveryKitManifest))
	if err != nil {
		return err
	}
	const maxManifest = 1 << 20
	data, readErr := io.ReadAll(io.LimitReader(manifestFile, maxManifest+1))
	if err = errors.Join(readErr, manifestFile.Close()); err != nil {
		return err
	}
	var manifest recoveryKitManifestV1
	if len(data) > maxManifest || json.Unmarshal(data, &manifest) != nil || manifest.Format != recoveryKitFormat {
		return errors.New("invalid recovery kit manifest")
	}
	target, staging, err := stageRecoveryDirectory(destination)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	copyVerified := func(name string) error {
		expected, ok := manifest.SHA256[name]
		if !ok {
			return fmt.Errorf("recovery kit manifest is missing %s", name)
		}
		actual, err := copyRecoveryFile(kit, staging, name)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("recovery kit checksum mismatch for %s", name)
		}
		return nil
	}
	if err = copyVerified("catalog.db"); err != nil {
		return err
	}
	if err = prepareRecoveredCatalog(ctx, staging, manifest, copyVerified); err != nil {
		return err
	}
	return publishRecoveryDirectory(staging, target)
}

func prepareRecoveredCatalog(ctx context.Context, staging string, manifest recoveryKitManifestV1, copyVerified func(string) error) (err error) {
	c, err := catalog.Open(staging)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	var integrity string
	if err = c.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("recovered catalog failed SQLite integrity check")
	}
	var foreignKeyErrors bool
	if err = c.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_check)").Scan(&foreignKeyErrors); err != nil {
		return err
	}
	if foreignKeyErrors {
		return errors.New("recovered catalog has invalid SQLite references")
	}
	keys, err := recoveryKeyNames(ctx, c)
	if err != nil {
		return err
	}
	if len(manifest.SHA256) != len(keys)+1 {
		return errors.New("recovery kit manifest does not match catalog keys")
	}
	for _, key := range keys {
		if err = copyVerified(key); err != nil {
			return err
		}
	}
	if _, err = vault.Open(c); err != nil {
		return err
	}
	if _, err = scheduler.New(c, nil).RecoverInterrupted(ctx); err != nil {
		return err
	}
	for _, statement := range []string{
		"UPDATE schedule_entries SET enabled=0, manual_pending=0",
		"DELETE FROM sessions",
		"DELETE FROM bootstrap",
		"PRAGMA wal_checkpoint(TRUNCATE)",
	} {
		if _, err = c.DB.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func recoveryKeyNames(ctx context.Context, c *catalog.Catalog) ([]string, error) {
	rows, err := c.DB.QueryContext(ctx, "SELECT id, password_mode FROM storage_repositories WHERE path <> '' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{"master.key"}
	for rows.Next() {
		var id, mode string
		if err = rows.Scan(&id, &mode); err != nil {
			return nil, err
		}
		if !safeID(id) || !validPasswordMode(mode) {
			return nil, errors.New("invalid storage entry in recovery catalog")
		}
		if mode == PasswordMode {
			names = append(names, recoveryStorageKeyName(id))
		}
	}
	return names, rows.Err()
}

func recoveryStorageKeyName(id string) string {
	if id == PrimaryStorageID {
		return "restic.key"
	}
	return "restic-" + id + ".key"
}

func openPrivateRecoveryFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		err = fmt.Errorf("%s must be a private regular file", filepath.Base(path))
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// With no destination directory this hashes an already-staged file.
func copyRecoveryFile(source, destination, name string) (string, error) {
	input, err := openPrivateRecoveryFile(filepath.Join(source, name))
	if err != nil {
		return "", err
	}
	defer input.Close()
	digest := sha256.New()
	if destination == "" {
		if _, err = io.Copy(digest, input); err != nil {
			return "", err
		}
	} else {
		output, err := os.OpenFile(filepath.Join(destination, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(io.MultiWriter(output, digest), input)
		if err = errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func stageRecoveryDirectory(destination string) (string, string, error) {
	if destination == "" {
		return "", "", errors.New("destination required")
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return "", "", err
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			err = fmt.Errorf("destination already exists: %s", target)
		}
		return "", "", err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".backuphub-recovery-")
	return target, staging, err
}

func publishRecoveryDirectory(staging, target string) error {
	dir, err := os.Open(staging)
	if err != nil {
		return err
	}
	if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
		return err
	}
	// Atomic publication must also reject a destination created after staging.
	if err = unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("publish %s: %w", target, err)
	}
	parent, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
