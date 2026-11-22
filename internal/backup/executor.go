package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mirivlad/BackupHub/internal/domain"
	"github.com/mirivlad/BackupHub/internal/scheduler"
)

func (s *Service) Execute(ctx context.Context, t scheduler.Task, report func(domain.JobState) error) error {
	started := t.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	source, settings, ref, err := s.source(ctx, t.DatasetID)
	if err != nil {
		return err
	}
	if t.Kind != "backup" {
		return errors.New("unsupported executor kind")
	}
	repoID, err := s.taskRepository(ctx, t.ID)
	if err != nil {
		return fmt.Errorf("missing destination storage: %w", err)
	}
	destination, err := s.repositoryFor(ctx, repoID)
	if err != nil {
		return &scheduler.BackupFailure{Code: "storage_unavailable", Err: errors.Join(scheduler.ErrSourceUnavailable, err)}
	}
	if source.Kind == "local" {
		status, _ := assessLocalAccess(settings.Path)
		switch status {
		case "ok":
		case "missing":
			return fmt.Errorf("%w: local directory not found or hidden: %s", scheduler.ErrSourceUnavailable, settings.Path)
		case "denied":
			return fmt.Errorf("%w: backuphub cannot read local directory: %s", scheduler.ErrSourcePermissionDenied, settings.Path)
		default:
			return errors.New("local source is not a readable directory")
		}
	}
	if err = report(domain.JobPreparing); err != nil {
		return err
	}
	var password string
	if ref != "" {
		data, err := s.vault.Get(ctx, ref)
		if err != nil {
			return err
		}
		password = string(data)
		for i := range data {
			data[i] = 0
		}
	}
	if err = report(domain.JobCapturing); err != nil {
		return err
	}
	staging := s.stageDir
	if repoID != PrimaryStorageID {
		// Stage remote trees on the SELECTED backup volume, not on the OS disk.
		staging = filepath.Join(filepath.Dir(destination.path), "staging")
	}
	root, cleanup, err := s.capture(ctx, source.ID, source.Kind, settings, password, staging)
	password = ""
	if err != nil {
		return &scheduler.BackupFailure{Code: "capture_failed", Err: err}
	}
	defer cleanup()
	if err = report(domain.JobCommitting); err != nil {
		return err
	}
	var native string
	err = destination.retryAfterStaleLock(ctx, func() error {
		var backupErr error
		native, backupErr = destination.treeBackup(ctx, root, source.ID)
		return backupErr
	})
	if err != nil {
		code := "restic_backup_failed"
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			code = "restic_partial_backup"
		}
		return resticStageFailure(code, err)
	}
	// Read back the new snapshot before publishing it in the catalog.
	if err = report(domain.JobVerifying); err != nil {
		return err
	}
	if err = destination.retryAfterStaleLock(ctx, func() error { return destination.verifySnapshot(ctx, native) }); err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "restic snapshot verification failed", "run_id", t.RunID,
				"job_id", t.ID, "repository_id", repoID, "detail", err)
		}
		return resticStageFailure("restic_verification_failed", err)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	captured := time.Now()
	at := captured.UTC().Format(time.RFC3339Nano)
	duration := max(int64(0), captured.Sub(started).Milliseconds())
	consistency := "best_effort"
	fidelity := "basic_metadata"
	if source.Kind == "postgres" {
		consistency = "application_consistent"
		fidelity = "logical_dump"
	}
	if source.Kind == postgres1CKind {
		consistency = "application_consistent"
		fidelity = "logical_dump_split_config"
	}
	if source.Kind == "smb" || source.Kind == smb1Kind {
		fidelity = "content_and_timestamps"
	}
	marker := portableRecord{ID: id, SourceID: source.ID, SourceName: source.Name, Kind: source.Kind, Root: root, ResticID: native, At: at, DataVerified: true, Consistency: consistency, Fidelity: fidelity, CaptureDurationMS: &duration}
	var markerID string
	err = destination.retryAfterStaleLock(ctx, func() error {
		var commitErr error
		markerID, commitErr = destination.commit(ctx, marker)
		return commitErr
	})
	if err != nil {
		return resticStageFailure("restic_manifest_failed", err)
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return &scheduler.BackupFailure{Code: "snapshot_catalog_failed", Err: err}
	}
	defer tx.Rollback()
	if err = insertCommittedTree(ctx, tx, repoID, markerID, t.RunID, marker); err != nil {
		return &scheduler.BackupFailure{Code: "snapshot_catalog_failed", Err: err}
	}
	if err = tx.Commit(); err != nil {
		return &scheduler.BackupFailure{Code: "snapshot_catalog_failed", Err: err}
	}
	return nil
}

func resticStageFailure(code string, err error) *scheduler.BackupFailure {
	if errors.Is(err, errResticRepositoryLocked) {
		code = "restic_repository_locked"
	}
	return &scheduler.BackupFailure{Code: code, Err: err}
}

func insertCommittedTree(ctx context.Context, tx *sql.Tx, repoID, markerID, runID string, rec portableRecord) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(id,dataset_id,repository_id,state,manifest_ref,created_at) VALUES(?,?,?,?,?,?)`,
		rec.ID, rec.SourceID, repoID, "committed", markerID, rec.At); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO file_snapshots(snapshot_id,source_id,restic_id,root_path,committed_at,verification,fidelity,consistency,run_id,capture_duration_ms)
	 VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),?)`,
		rec.ID, rec.SourceID, rec.ResticID, rec.Root, rec.At, "data_sha256_verified", rec.Fidelity, rec.Consistency, runID, rec.CaptureDurationMS)
	return err
}
func (s *Service) capture(ctx context.Context, sourceID, kind string, settings Settings, password, staging string) (string, func(), error) {
	if kind == "local" {
		return filepath.Clean(settings.Path), func() {}, nil
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		return "", func() {}, err
	}
	info, err := os.Lstat(staging)
	if err != nil {
		return "", func() {}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", func() {}, errors.New("staging directory must be private and real")
	}
	dir, err := os.MkdirTemp(staging, "capture-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	root := filepath.Join(dir, "tree")
	if err = os.Mkdir(root, 0700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	switch kind {
	case "smb", smb1Kind:
		err = captureSMB(ctx, settings, password, root)
	case "sftp":
		err = s.captureSFTP(ctx, sourceID, settings, password, root)
	case "postgres":
		err = capturePostgres(ctx, settings, password, root)
	case postgres1CKind:
		err = capturePostgres1C(ctx, settings, password, root)
	default:
		err = errors.New("unsupported capture kind")
	}
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return root, cleanup, nil
}
