package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mirivlad/BackupHub/internal/domain"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"github.com/mirivlad/BackupHub/internal/storagehelper"
)

func (s *Service) Execute(parent context.Context, t scheduler.Task, report func(domain.JobState) error) error {
	ctx, stop := s.watchOperation(parent)
	defer stop()
	reportPhase := report
	report = func(state domain.JobState) error {
		noteProgress(ctx)
		return reportPhase(state)
	}
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
	repoID := t.RepositoryID
	if repoID == "" {
		return errors.New("missing destination storage")
	}
	destination, err := s.repositoryFor(ctx, repoID)
	if err != nil {
		return storageFailure(ctx, err)
	}
	pending, err := s.loadPublication(ctx, t)
	if err != nil {
		return err
	}
	if pending.runID != "" {
		return s.resumePublication(ctx, t, destination, pending, report)
	}
	if source.Kind == "local" {
		status, _ := assessLocalAccess(settings.Path)
		switch status {
		case "ok":
			if settings.LocalMount == "" {
				settings.LocalMount, err = localSourceMount(settings.Path)
				if err != nil {
					return sourceFailure(ctx, err)
				}
				encoded, encodeErr := json.Marshal(settings)
				if encodeErr != nil {
					return encodeErr
				}
				if _, err = s.catalog.DB.ExecContext(ctx, `UPDATE sources SET settings_json=? WHERE id=?`, string(encoded), source.ID); err != nil {
					return backupFailure(ctx, "snapshot_catalog_failed", err)
				}
			}
			if err = checkLocalMount(ctx, settings); err != nil {
				return err
			}
		case "missing":
			return fmt.Errorf("%w: local directory not found or hidden: %s", scheduler.ErrSourceUnavailable, settings.Path)
		case "denied":
			return fmt.Errorf("%w: backuphub cannot read local directory: %s", scheduler.ErrSourcePermissionDenied, settings.Path)
		case "error":
			return fmt.Errorf("%w: local directory could not be opened: %s", scheduler.ErrSourceUnavailable, settings.Path)
		default:
			return sourceFailure(ctx, errors.New("local source is not a readable directory"))
		}
	}
	if err = report(domain.JobPreparing); err != nil {
		return err
	}
	var password string
	if ref != "" {
		data, err := s.vault.Get(ctx, ref)
		if err != nil {
			return backupFailure(ctx, "source_configuration", err)
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
	if source.Kind != "local" {
		if err = s.cleanInterruptedStaging(ctx, staging); err != nil {
			return backupFailure(ctx, "snapshot_catalog_failed", err)
		}
	}
	root, cleanup, err := s.capture(ctx, t.RunID, source.ID, source.Kind, settings, password, staging)
	password = ""
	if err != nil {
		var failure *scheduler.BackupFailure
		if errors.As(err, &failure) {
			return err
		}
		return sourceFailure(ctx, err)
	}
	defer cleanup()
	if err = report(domain.JobCommitting); err != nil {
		return err
	}
	native, err := destination.treeBackup(ctx, root, source.ID)
	if err != nil {
		code := "restic_backup_failed"
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			code = "restic_partial_backup"
		}
		if source.Kind == "local" && ctx.Err() == nil {
			if mountErr := checkLocalMount(ctx, settings); mountErr != nil {
				return mountErr
			}
			if status, _ := assessLocalAccess(settings.Path); status == "missing" {
				return fmt.Errorf("%w: local source disappeared during backup", scheduler.ErrSourceUnavailable)
			}
		}
		return s.resticFailure(ctx, t.RepositoryID, code, err)
	}
	if source.Kind == "local" {
		if err = checkLocalMount(ctx, settings); err != nil {
			return err
		}
	}
	// Read back the new snapshot before publishing it in the catalog.
	if err = report(domain.JobVerifying); err != nil {
		return err
	}
	if err = destination.verifySnapshot(ctx, native); err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "restic snapshot verification failed", "run_id", t.RunID,
				"job_id", t.ID, "repository_id", repoID, "detail", err)
		}
		return s.resticFailure(ctx, t.RepositoryID, "restic_verification_failed", err)
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
	marker := portableRecord{Format: portableTreeFormat, ID: t.OperationID, AttemptID: t.RunID, SourceID: source.ID, SourceName: source.Name, Kind: source.Kind, Root: root, ResticID: native, At: at, DataVerified: true, Consistency: consistency, Fidelity: fidelity, CaptureDurationMS: &duration}
	if err = s.savePublication(ctx, t, marker); err != nil {
		return err
	}
	return s.publish(ctx, t, destination, marker, report, false)
}

func localSourceMount(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return storagehelper.ContainingMount(resolved)
}

func checkLocalMount(ctx context.Context, settings Settings) error {
	mount, err := localSourceMount(settings.Path)
	if err != nil {
		return sourceFailure(ctx, err)
	}
	if mount != settings.LocalMount {
		return fmt.Errorf("%w: source mount changed", scheduler.ErrSourceUnavailable)
	}
	return nil
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
func (s *Service) capture(ctx context.Context, runID, sourceID, kind string, settings Settings, password, staging string) (string, func(), error) {
	if kind == "local" {
		return filepath.Clean(settings.Path), func() {}, nil
	}
	root, cleanup, err := s.newStaging(ctx, staging, runID)
	if err != nil {
		failure := storageFailure(ctx, err)
		if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
			failure.Code = "storage_full"
		}
		return "", nil, failure
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
