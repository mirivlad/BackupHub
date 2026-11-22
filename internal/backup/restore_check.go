package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const restoreCheckMaxFiles = 8
const restoreCheckMaxBytes = 256 << 20
const restoreCheckSpaceReserve = 32 << 20

func supportsRestoreCheck(kind string) bool {
	switch kind {
	case "local", "smb", smb1Kind, "sftp":
		return true
	default:
		return false
	}
}

func (s *Service) restoreCheckRoot() string {
	return filepath.Join(s.catalog.DataDir, "restore-checks")
}

// Only this daemon's scratch directories are removed. The service lease
// ensures that a second daemon cannot clean up an active restore check.
func (s *Service) cleanRestoreChecks() error {
	root := s.restoreCheckRoot()
	if err := privateRestoreRoot(root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if len(entry.Name()) == 32 && safeID(entry.Name()) {
			if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func privateRestoreRoot(root string) error {
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("restore check directory must be real and private")
	}
	return nil
}

func (s *Service) runRestoreCheck(ctx context.Context, repo *repository, run VerificationRun, progress func(float64)) (files, size int64, err error) {
	snap, err := s.Snapshot(ctx, run.SnapshotID)
	if err != nil {
		return 0, 0, err
	}
	if snap.Kind == recoveredSourceKind {
		// Reconciliation can create a placeholder source. Only its
		// authenticated portable marker knows the original source kind.
		var marker string
		if err := s.catalog.DB.QueryRowContext(ctx, "SELECT manifest_ref FROM snapshots WHERE id=?", snap.ID).Scan(&marker); err != nil {
			return 0, 0, err
		}
		if !validResticID(marker) {
			return 0, 0, errors.New("invalid snapshot commit marker")
		}
		raw, err := repo.run(ctx, nil, "dump", marker, "/"+portableTreeFile)
		if err != nil {
			return 0, 0, err
		}
		var rec portableRecord
		if json.Unmarshal(raw, &rec) != nil || rec.Format != portableTreeFormat || !rec.DataVerified ||
			rec.ID != snap.ID || rec.SourceID != snap.SourceID || rec.ResticID != snap.ResticID || rec.Root != snap.Root {
			return 0, 0, errors.New("snapshot commit marker does not match the catalog")
		}
		snap.Kind = rec.Kind
	}
	if !supportsRestoreCheck(snap.Kind) {
		return 0, 0, ErrRestoreCheckUnsupported
	}

	entries, size, err := planRestoreCheck(func(visit func(treeEntry) error) error {
		return repo.walkTree(ctx, snap.ResticID, visit)
	}, snap.Root, run.Selection)
	if err != nil {
		return 0, 0, err
	}
	root := s.restoreCheckRoot()
	if err = privateRestoreRoot(root); err != nil {
		return 0, 0, err
	}
	if err = requireRestoreSpace(root, size); err != nil {
		return 0, 0, err
	}
	dir := filepath.Join(root, run.ID)
	if err = os.Mkdir(dir, 0700); err != nil {
		return 0, 0, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("очистка пробного восстановления: %w", cleanupErr))
		}
	}()
	for i, entry := range entries {
		if err = ctx.Err(); err != nil {
			return 0, 0, err
		}
		relative, _ := relativeTo(snap.Root, entry.Path)
		out, err := safeLocal(dir, relative)
		if err != nil {
			return 0, 0, err
		}
		if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
			return 0, 0, err
		}
		if err = restoreAndVerifyFile(ctx, repo, snap.ResticID, entry, out); err != nil {
			return 0, 0, err
		}
		progress(float64(i+1) / float64(len(entries)))
	}
	return int64(len(entries)), size, nil
}

func requireRestoreSpace(dir string, bytes int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return err
	}
	if stat.Bavail*uint64(stat.Bsize) < uint64(bytes+restoreCheckSpaceReserve) {
		return errors.New("недостаточно свободного места для пробного восстановления")
	}
	return nil
}

// Restic checks each blob against the authenticated tree's content IDs on
// dump. Hash that trusted stream, then reopen and hash the restored file.
func restoreAndVerifyFile(ctx context.Context, repo *repository, nativeID string, entry treeEntry, out string) error {
	file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	wantHash := sha256.New()
	writer := &restoreSizeWriter{writer: io.MultiWriter(file, wantHash), left: int64(entry.Size)}
	dumpErr := repo.dumpFile(ctx, nativeID, entry.Path, writer)
	var syncErr error
	if dumpErr == nil {
		syncErr = file.Sync()
	}
	if err = errors.Join(dumpErr, syncErr, file.Close()); err != nil {
		return err
	}
	if writer.left != 0 {
		return errors.New("размер потока Restic не совпал с метаданными снимка")
	}
	return verifyRestoredFile(ctx, out, int64(entry.Size), wantHash.Sum(nil))
}

type restoreSizeWriter struct {
	writer io.Writer
	left   int64
}

func (w *restoreSizeWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, errors.New("поток Restic превышает размер файла в снимке")
	}
	n, err := w.writer.Write(p)
	w.left -= int64(n)
	return n, err
}

func verifyRestoredFile(ctx context.Context, out string, wantSize int64, wantHash []byte) error {
	file, err := os.Open(out)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	buf := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Read(buf)
		total += int64(n)
		if total > wantSize {
			return errors.New("размер восстановленного файла не совпал")
		}
		hash.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if total != wantSize || !bytes.Equal(hash.Sum(nil), wantHash) {
		return errors.New("размер или SHA-256 восстановленного файла не совпали")
	}
	return nil
}
