package backup

import (
	"context"
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
	if t.Kind != "backup" {return errors.New("unsupported executor kind")}
 repoID,err:=s.taskRepository(ctx,t.ID)
 if err!=nil{return fmt.Errorf("missing destination storage: %w",err)}
 destination,err:=s.repositoryFor(ctx,repoID)
 if err!=nil{return fmt.Errorf("%w: storage offline",scheduler.ErrSourceUnavailable)}
	if source.Kind == "local" {
		info, err := os.Lstat(settings.Path)
		if err != nil {
			return fmt.Errorf("%w: local path missing", scheduler.ErrSourceUnavailable)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("local source must be a real directory")
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
	root, cleanup, err := s.capture(ctx, source.Kind, settings, password)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshots(id,dataset_id,repository_id,state,manifest_ref,created_at) VALUES(?,?,?,?,?,?)`, id, source.ID, repoID, "committed", markerID, at)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO file_snapshots(snapshot_id,source_id,restic_id,root_path,committed_at,verification,fidelity,consistency) VALUES(?,?,?,?,?,?,?,?)`, id, source.ID, native, root, at, "data_sha256_verified", fidelity, consistency)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return report(domain.JobVerifying)
}
func (s *Service) capture(ctx context.Context, kind string, settings Settings, password string) (string, func(), error) {
	if kind == "local" {
		return filepath.Clean(settings.Path), func() {}, nil
	}
	dir, err := os.MkdirTemp(s.stageDir, "capture-")
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
		err = captureSFTP(ctx, settings, password, root)
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
