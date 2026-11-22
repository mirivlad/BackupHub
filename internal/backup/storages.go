package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/storagehelper"
)

const PrimaryStorageID = "primary"

func pathOverlaps(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

type Storage struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Path          string      `json:"path"`
	MountPath     string      `json:"mount_path,omitempty"`
	PasswordMode  string      `json:"password_mode"`
	FreeBytes     uint64      `json:"free_bytes"`
	Status        string      `json:"status"`
	MetadataCheck CheckStatus `json:"metadata_check"`
	DataCheck     CheckStatus `json:"data_check"`
}

type CreateStorage struct {
	Name         string `json:"name"`
	ParentPath   string `json:"parent_path"`
	Folder       string `json:"folder"`
	PasswordMode string `json:"password_mode,omitempty"`
}

// Primary's location is recorded at upgrade. A changed BACKUPHUB_REPOSITORY
// cannot silently redirect existing snapshots to an empty repository.
func ensurePrimaryStorage(ctx context.Context, c *catalog.Catalog, path string) error {
	result, err := c.DB.ExecContext(ctx, "UPDATE storage_repositories SET path=? WHERE id='primary' AND path=''", path)
	if err != nil {
		return err
	}
	_ = result
	var stored string
	if err = c.DB.QueryRowContext(ctx, "SELECT path FROM storage_repositories WHERE id='primary'").Scan(&stored); err != nil {
		return err
	}
	if stored != path {
		return fmt.Errorf("configured primary repository differs from catalog (%s): move data using explicit migration, do not change BACKUPHUB_REPOSITORY", stored)
	}
	return nil
}

func (s *Service) ListStorages(ctx context.Context) ([]Storage, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT s.id,s.display_name,s.path,s.mount_path,s.password_mode,
		COALESCE(m.state,''),m.finished_at,COALESCE(d.state,''),d.finished_at,
		COALESCE(c.mode,''),COALESCE(c.interval_hours,0)
	 FROM storage_repositories s
	 LEFT JOIN repository_check_schedules c ON c.repository_id=s.id
	 LEFT JOIN verification_runs m ON m.id=(
	   SELECT v.id FROM verification_runs v WHERE v.repository_id=s.id AND v.kind='metadata'
	   ORDER BY v.queued_at DESC,v.id DESC LIMIT 1)
	 LEFT JOIN verification_runs d ON d.id=(
	   SELECT v.id FROM verification_runs v WHERE v.repository_id=s.id AND v.kind='data'
	   ORDER BY v.queued_at DESC,v.id DESC LIMIT 1)
	 ORDER BY s.created_at,s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Storage{}
	now := time.Now()
	for rows.Next() {
		var x Storage
		var metadata, data, mode string
		var metadataAt, dataAt sql.NullInt64
		var hours int
		if err = rows.Scan(&x.ID, &x.Name, &x.Path, &x.MountPath, &x.PasswordMode,
			&metadata, &metadataAt, &data, &dataAt, &mode, &hours); err != nil {
			return nil, err
		}
		metadataAge, dataAge := defaultCheckFreshness, defaultCheckFreshness
		if hours > 0 && mode == "metadata" {
			metadataAge = time.Duration(hours) * time.Hour
		}
		if hours > 0 && mode == "data" {
			dataAge = time.Duration(hours) * time.Hour
		}
		x.MetadataCheck = checkStatus(metadata, metadataAt, metadataAge, now)
		x.DataCheck = checkStatus(data, dataAt, dataAge, now)
		out = append(out, x)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	// Filesystem probes never hold the catalog connection.
	for i := range out {
		x := &out[i]
		x.Status = "unavailable"
		if x.MountPath != "" && !storagehelper.MountPresent(x.MountPath) {
			continue
		}
		var stats syscall.Statfs_t
		if syscall.Statfs(x.Path, &stats) == nil {
			if info, err := os.Lstat(filepath.Join(x.Path, "config")); err == nil && info.Mode().IsRegular() {
				x.FreeBytes = stats.Bavail * uint64(stats.Bsize)
				x.Status = "available"
			}
		}
	}
	return out, nil
}

func (s *Service) repositoryFor(ctx context.Context, id string) (*repository, error) {
	if id == "" || id == PrimaryStorageID {
		return existingRepository(s.repo.path, s.repo.passwordFile, PasswordMode)
	}
	if !safeID(id) {
		return nil, errors.New("invalid repository id")
	}
	var path, mountPath, mode string
	if err := s.catalog.DB.QueryRowContext(ctx, "SELECT path,mount_path,password_mode FROM storage_repositories WHERE id=?", id).Scan(&path, &mountPath, &mode); err != nil {
		return nil, err
	}
	if !validPasswordMode(mode) {
		return nil, errors.New("unknown repository password mode")
	}
	if mountPath != "" && !storagehelper.MountPresent(mountPath) {
		return nil, errStorageUnavailable
	}
	if path == s.repo.path {
		return nil, errors.New("repository overlaps primary storage")
	}
	key := filepath.Join(s.catalog.DataDir, "restic-"+id+".key")
	return existingRepository(path, key, mode)
}

// RegisterStorage is used after privileged directory provisioning, but is
// also testable with a directory owned by the backuphub service user.
func (s *Service) RegisterStorage(ctx context.Context, name, repoPath string) (Storage, error) {
	return s.registerStorage(ctx, name, repoPath, "", PasswordMode)
}
func (s *Service) registerStorage(ctx context.Context, name, repoPath, mountPath, mode string) (Storage, error) {
	var empty Storage
	if !validPasswordMode(mode) {
		return empty, errors.New("invalid repository password mode")
	}
	if mountPath != "" && !storagehelper.MountPresent(mountPath) {
		return empty, errors.New("storage volume is not mounted")
	}
	if strings.TrimSpace(name) == "" || len(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return empty, errors.New("invalid storage name")
	}
	if !filepath.IsAbs(repoPath) || filepath.Clean(repoPath) == "/" {
		return empty, errors.New("storage path must be absolute")
	}
	repoPath = filepath.Clean(repoPath)
	if pathOverlaps(repoPath, s.catalog.DataDir) || pathOverlaps(repoPath, s.stageDir) {
		return empty, errors.New("external storage cannot overlap BackupHub state")
	}
	// Never nest Restic repositories, even across unrelated backup tasks.
	rows, err := s.catalog.DB.QueryContext(ctx, "SELECT path FROM storage_repositories")
	if err != nil {
		return empty, err
	}
	var existing []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			_ = rows.Close()
			return empty, err
		}
		existing = append(existing, p)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return empty, err
	}
	_ = rows.Close()
	for _, p := range existing {
		if pathOverlaps(repoPath, p) {
			return empty, errors.New("repositories must not overlap")
		}
	}
	// Ensure a newly added destination is not within an existing local source
	// (otherwise that source would recursively back up its own repository).
	sourceRows, err := s.catalog.DB.QueryContext(ctx, "SELECT settings_json FROM sources WHERE kind='local'")
	if err != nil {
		return empty, err
	}
	for sourceRows.Next() {
		var encoded string
		if err = sourceRows.Scan(&encoded); err != nil {
			_ = sourceRows.Close()
			return empty, err
		}
		var cfg Settings
		if err = json.Unmarshal([]byte(encoded), &cfg); err != nil {
			_ = sourceRows.Close()
			return empty, err
		}
		if pathOverlaps(repoPath, cfg.Path) {
			_ = sourceRows.Close()
			return empty, errors.New("repository overlaps an existing local backup source")
		}
	}
	if err = sourceRows.Err(); err != nil {
		_ = sourceRows.Close()
		return empty, err
	}
	_ = sourceRows.Close()
	var exists int
	if err := s.catalog.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM storage_repositories WHERE path=?", repoPath).Scan(&exists); err != nil {
		return empty, err
	}
	if exists != 0 {
		return empty, errors.New("repository is already registered")
	}
	id, err := randomID()
	if err != nil {
		return empty, err
	}
	s.repoMu.Lock()
	defer s.repoMu.Unlock()
	key := filepath.Join(s.catalog.DataDir, "restic-"+id+".key")
	marker := "restic." + id + ".init-in-progress"
	if _, err = initRepositoryNamedMode(ctx, s.catalog.DataDir, repoPath, key, marker, mode); err != nil {
		return empty, err
	}
	_, err = s.catalog.DB.ExecContext(ctx, "INSERT INTO storage_repositories(id,display_name,path,mount_path,password_mode,created_at) VALUES(?,?,?,?,?,?)", id, name, repoPath, mountPath, mode, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return empty, err
	}
	return Storage{ID: id, Name: name, Path: repoPath, MountPath: mountPath, Status: "available", PasswordMode: mode,
		MetadataCheck: CheckStatus{State: "unknown"}, DataCheck: CheckStatus{State: "unknown"}}, nil
}

func (s *Service) ProvisionStorage(ctx context.Context, in CreateStorage) (Storage, error) {
	var empty Storage
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 120 {
		return empty, errors.New("storage name required")
	}
	if in.PasswordMode == "" {
		chosen, err := s.DefaultRepositoryPasswordMode(ctx)
		if err != nil {
			return empty, err
		}
		in.PasswordMode = chosen
	}
	if !validPasswordMode(in.PasswordMode) {
		return empty, errors.New("invalid repository password mode")
	}
	client := storagehelper.NewClient("/run/backuphub-storage.sock")
	directory, err := client.Provision(ctx, in.ParentPath, in.Folder)
	if err != nil {
		return empty, err
	}
	// Require the selected location to remain on the actual mounted volume,
	// not the system filesystem exposed after an unmount.
	mounts, err := client.Mounts(ctx)
	if err != nil {
		return empty, err
	}
	chosen := ""
	for _, m := range mounts {
		if (directory == m.Path || strings.HasPrefix(directory, m.Path+"/")) && len(m.Path) > len(chosen) {
			chosen = m.Path
		}
	}
	if chosen == "" || !storagehelper.MountPresent(chosen) {
		return empty, errors.New("destination volume disconnected during provisioning")
	}
	return s.registerStorage(ctx, in.Name, filepath.Join(directory, "repository"), chosen, in.PasswordMode)
}

func (s *Service) StorageMounts(ctx context.Context) ([]storagehelper.Mount, error) {
	return storagehelper.NewClient("/run/backuphub-storage.sock").Mounts(ctx)
}
func (s *Service) BrowseStorage(ctx context.Context, path string) ([]storagehelper.Entry, error) {
	return storagehelper.NewClient("/run/backuphub-storage.sock").Browse(ctx, path)
}

func (s *Service) SelectJobStorage(ctx context.Context, jobID, storageID string) error {
	if !safeID(jobID) || !safeID(storageID) {
		return errors.New("invalid job/storage ID")
	}
	// Confirm the new repository opens before storing the choice; preserve
	// snapshot.repository_id for every historical copy.
	if _, err := s.repositoryFor(ctx, storageID); err != nil {
		return err
	}
	// Don't switch the destination while an execution is underway.
	result, err := s.catalog.DB.ExecContext(ctx, `UPDATE schedule_repositories SET repository_id=?
 WHERE schedule_id=? AND EXISTS(
 SELECT 1 FROM schedule_entries WHERE id=? AND active_run_id IS NULL AND NOT EXISTS (
 SELECT 1 FROM backup_operations WHERE schedule_id=schedule_entries.id AND state IN ('running','waiting'))
 )`, storageID, jobID, jobID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) DefaultRepositoryPasswordMode(ctx context.Context) (string, error) {
	var mode string
	err := s.catalog.DB.QueryRowContext(ctx, "SELECT default_repository_password_mode FROM system_settings WHERE singleton=1").Scan(&mode)
	if err != nil {
		return "", err
	}
	if !validPasswordMode(mode) {
		return "", errors.New("invalid stored repository password mode")
	}
	return mode, nil
}

// This preference applies only to newly created repositories. Never rewrite
// the key policy of an existing repository or an existing snapshot.
func (s *Service) SetDefaultRepositoryPasswordMode(ctx context.Context, mode string) error {
	if !validPasswordMode(mode) {
		return errors.New("invalid default repository password mode")
	}
	if mode == NoPasswordMode {
		if err := checkPasswordlessRestic(ctx, s.repo.binary); err != nil {
			return err
		}
	}
	result, err := s.catalog.DB.ExecContext(ctx, "UPDATE system_settings SET default_repository_password_mode=? WHERE singleton=1", mode)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("missing system settings row")
	}
	return nil
}
