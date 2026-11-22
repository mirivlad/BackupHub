package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/mirivlad/BackupHub/internal/domain"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"os"
	"path/filepath"
	"time"
)

func (s *Service) Execute(ctx context.Context, t scheduler.Task, report func(domain.JobState) error) error {
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
		return fmt.Errorf("%w: storage offline", scheduler.ErrSourceUnavailable)
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
		return err
	}
	defer cleanup()
	if err = report(domain.JobCommitting); err != nil {
		return err
	}
	native, err := destination.treeBackup(ctx, root, source.ID)
	if err != nil {
		return err
	}
	// Full data verification is a separate, potentially lengthy phase.
	if err = report(domain.JobVerifying); err != nil {
		return err
	}
	if err = destination.check(ctx, true); err != nil {
		return fmt.Errorf("restic full data verification failed: %w", err)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339Nano)
	consistency := "best_effort"
	fidelity := "basic_metadata"
	if source.Kind == "postgres" {
		consistency = "application_consistent"
		fidelity = "logical_dump"
	}
	if source.Kind == "smb" {
		fidelity = "content_and_timestamps"
	}
	marker := portableRecord{ID: id, SourceID: source.ID, SourceName: source.Name, Kind: source.Kind, Root: root, ResticID: native, At: at, DataVerified: true, Consistency: consistency, Fidelity: fidelity}
	markerID, err := destination.commit(ctx, marker)
	if err != nil {
		return err
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertCommittedTree(ctx, tx, repoID, markerID, marker); err != nil {
		return err
	}
	return tx.Commit()
}

func insertCommittedTree(ctx context.Context, tx *sql.Tx, repoID, markerID string, rec portableRecord) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(id,dataset_id,repository_id,state,manifest_ref,created_at) VALUES(?,?,?,?,?,?)`,
		rec.ID, rec.SourceID, repoID, "committed", markerID, rec.At); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO file_snapshots(snapshot_id,source_id,restic_id,root_path,committed_at,verification,fidelity,consistency) VALUES(?,?,?,?,?,?,?,?)`,
		rec.ID, rec.SourceID, rec.ResticID, rec.Root, rec.At, "data_sha256_verified", rec.Fidelity, rec.Consistency)
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
	case "smb":
		err = captureSMB(ctx, settings, password, root)
	case "sftp":
		err = s.captureSFTP(ctx, sourceID, settings, password, root)
	case "postgres":
		err = capturePostgres(ctx, settings, password, root)
	default:
		err = errors.New("unsupported capture kind")
	}
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return root, cleanup, nil
}
