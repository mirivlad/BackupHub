package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mirivlad/BackupHub/internal/storagehelper"
)

const stagingOwnerFile = ".backuphub-owner"

func privateDirectory(dir string) error {
	if err := validateRealParents(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("staging directory must be private and real")
	}
	return nil
}

func (s *Service) newStaging(ctx context.Context, base, runID string) (string, func(), error) {
	if err := validateRealParents(base); err != nil {
		return "", nil, err
	}
	if err := os.Mkdir(base, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", nil, err
	}
	if err := privateDirectory(base); err != nil {
		return "", nil, err
	}
	token, err := randomID()
	if err != nil {
		return "", nil, err
	}
	mount, err := storagehelper.ContainingMount(base)
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(base, "capture-"+runID+"-"+token)
	if _, err = s.catalog.DB.ExecContext(ctx, `INSERT INTO backup_staging(run_id,path,mount_path,token) VALUES(?,?,?,?)`, runID, dir, mount, token); err != nil {
		return "", nil, err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return "", nil, err
	}
	cleanup := func() { s.cleanStagingEntry(runID, dir, mount, token) }
	owner, err := os.OpenFile(filepath.Join(dir, stagingOwnerFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", nil, err
	}
	_, writeErr := owner.WriteString(token)
	if err = errors.Join(writeErr, owner.Sync(), owner.Close()); err != nil {
		return "", nil, err
	}
	for _, folder := range []string{dir, base} {
		handle, openErr := os.Open(folder)
		if openErr != nil {
			return "", nil, openErr
		}
		if err = errors.Join(handle.Sync(), handle.Close()); err != nil {
			return "", nil, err
		}
	}
	root := filepath.Join(dir, "tree")
	if err = os.Mkdir(root, 0700); err != nil {
		cleanup()
		return "", nil, err
	}
	return root, cleanup, nil
}

func (s *Service) cleanStagingEntry(runID, dir, mount, token string) {
	visibleMount, err := storagehelper.ContainingMount(dir)
	if err != nil || visibleMount != mount {
		return
	}
	err = removeOwnedStaging(runID, dir, token)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("could not clean staging", "run_id", runID, "error", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = s.catalog.DB.ExecContext(ctx, `DELETE FROM backup_staging WHERE run_id=?`, runID); err != nil {
		slog.Warn("could not retire staging record", "run_id", runID, "error", err)
	}
}

func removeOwnedStaging(runID, dir, token string) error {
	if !safeID(runID) || !safeID(token) || filepath.Base(dir) != "capture-"+runID+"-"+token {
		return errors.New("invalid staging ownership record")
	}
	if err := privateDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := privateDirectory(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(stagingOwnerFile)
	if err != nil {
		return fmt.Errorf("staging ownership proof unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != int64(len(token)) {
		return errors.New("invalid staging ownership proof")
	}
	raw, err := root.ReadFile(stagingOwnerFile)
	if err != nil || string(raw) != token {
		return errors.New("staging ownership proof did not match")
	}
	if err = root.RemoveAll("tree"); err != nil {
		return err
	}
	if err = root.Remove(stagingOwnerFile); err != nil {
		return err
	}
	return os.Remove(dir)
}

func (s *Service) cleanInterruptedStaging(ctx context.Context, base string) error {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT w.run_id,w.path,w.mount_path,w.token FROM backup_staging w
		JOIN schedule_runs r ON r.id=w.run_id WHERE r.finished_at IS NOT NULL`)
	if err != nil {
		return err
	}
	var records [][4]string
	for rows.Next() {
		var record [4]string
		if err = rows.Scan(&record[0], &record[1], &record[2], &record[3]); err != nil {
			rows.Close()
			return err
		}
		if base == "" || filepath.Dir(record[1]) == base {
			records = append(records, record)
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	s.stagingCleanup.Lock()
	defer s.stagingCleanup.Unlock()
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.cleanStagingEntry(record[0], record[1], record[2], record[3])
	}
	return nil
}
