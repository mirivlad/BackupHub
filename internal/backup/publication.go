package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mirivlad/BackupHub/internal/domain"
	"github.com/mirivlad/BackupHub/internal/scheduler"
)

type publication struct {
	record portableRecord
	runID  string
}

func (s *Service) loadPublication(ctx context.Context, t scheduler.Task) (publication, error) {
	var p publication
	var encoded string
	err := s.catalog.DB.QueryRowContext(ctx, `SELECT run_id,record_json FROM backup_publications WHERE operation_id=?`, t.OperationID).Scan(&p.runID, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, backupFailure(ctx, "snapshot_catalog_failed", err)
	}
	if err = json.Unmarshal([]byte(encoded), &p.record); err != nil || p.record.ID != t.OperationID || p.record.SourceID != t.DatasetID || p.record.AttemptID != p.runID {
		return p, backupFailure(ctx, "repository_integrity", errRepositoryIntegrity)
	}
	return p, nil
}

func (s *Service) savePublication(ctx context.Context, t scheduler.Task, rec portableRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err = s.catalog.DB.ExecContext(ctx, `INSERT INTO backup_publications(operation_id,run_id,record_json) VALUES(?,?,?)`, t.OperationID, t.RunID, string(raw)); err != nil {
		return backupFailure(ctx, "snapshot_catalog_failed", err)
	}
	return nil
}

func (r *repository) findPublication(ctx context.Context, expected portableRecord) (string, error) {
	tag := "bh-commit-" + expected.ID
	raw, err := r.run(ctx, nil, "snapshots", "--json", "--tag", tag)
	if err != nil {
		return "", err
	}
	var markers []resticRecoverySnapshot
	if err = json.Unmarshal(raw, &markers); err != nil {
		return "", errors.Join(errRepositoryIntegrity, err)
	}
	if len(markers) == 0 {
		return "", nil
	}
	if len(markers) != 1 || !validResticID(markers[0].ID) || len(markers[0].Paths) != 1 || markers[0].Paths[0] != "/"+portableTreeFile {
		return "", fmt.Errorf("%w: ambiguous or invalid publication marker", errRepositoryIntegrity)
	}
	raw, err = r.run(ctx, nil, "dump", markers[0].ID, "/"+portableTreeFile)
	if err != nil {
		return "", err
	}
	var found portableRecord
	if err = json.Unmarshal(raw, &found); err != nil {
		return "", errors.Join(errRepositoryIntegrity, err)
	}
	// Compare the full checkpoint, including original attempt, source, tree
	// and verification metadata. A marker from a different attempt is unsafe.
	a, errA := json.Marshal(found)
	b, errB := json.Marshal(expected)
	if errA != nil || errB != nil || string(a) != string(b) {
		return "", fmt.Errorf("%w: publication marker does not match checkpoint", errRepositoryIntegrity)
	}
	return markers[0].ID, nil
}

func (s *Service) RecoverPublication(parent context.Context, task scheduler.Task) (bool, error) {
	ctx, stop := s.watchOperation(parent)
	defer stop()
	p, err := s.loadPublication(ctx, task)
	if err != nil || p.runID == "" {
		return false, err
	}
	r, err := s.repositoryFor(ctx, task.RepositoryID)
	if err != nil {
		return false, storageFailure(ctx, err)
	}
	marker, err := r.findPublication(ctx, p.record)
	if err != nil {
		return false, s.resticFailure(ctx, task.RepositoryID, "restic_manifest_failed", err)
	}
	if marker == "" {
		return false, nil
	}
	if err = r.verifySnapshot(ctx, p.record.ResticID); err != nil {
		return false, s.resticFailure(ctx, task.RepositoryID, "restic_verification_failed", err)
	}
	return true, s.indexPublication(ctx, task, marker, p.record)
}

func (s *Service) resumePublication(ctx context.Context, t scheduler.Task, r *repository, p publication, report func(domain.JobState) error) error {
	if err := report(domain.JobVerifying); err != nil {
		return err
	}
	if err := r.verifySnapshot(ctx, p.record.ResticID); err != nil {
		return s.resticFailure(ctx, t.RepositoryID, "restic_verification_failed", err)
	}
	return s.publish(ctx, t, r, p.record, report, true)
}

func (s *Service) publish(ctx context.Context, t scheduler.Task, r *repository, rec portableRecord, report func(domain.JobState) error, resume bool) error {
	if err := report(domain.JobPublishing); err != nil {
		return err
	}
	var marker string
	var err error
	if resume {
		marker, err = r.findPublication(ctx, rec)
	}
	if err == nil && marker == "" {
		marker, err = r.commit(ctx, rec)
	}
	if err != nil {
		return s.resticFailure(ctx, t.RepositoryID, "restic_manifest_failed", err)
	}
	if err = report(domain.JobFinalizing); err != nil {
		return err
	}
	return s.indexPublication(ctx, t, marker, rec)
}

func (s *Service) indexPublication(ctx context.Context, t scheduler.Task, marker string, rec portableRecord) error {
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return backupFailure(ctx, "snapshot_catalog_failed", err)
	}
	defer tx.Rollback()
	var repoID, markerID, nativeID string
	err = tx.QueryRowContext(ctx, `SELECT s.repository_id,s.manifest_ref,f.restic_id FROM snapshots s
		LEFT JOIN file_snapshots f ON f.snapshot_id=s.id WHERE s.id=?`, rec.ID).Scan(&repoID, &markerID, &nativeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = insertCommittedTree(ctx, tx, t.RepositoryID, marker, t.RunID, rec)
	case err == nil && (repoID != t.RepositoryID || markerID != marker || nativeID != rec.ResticID):
		return backupFailure(ctx, "repository_integrity", errRepositoryIntegrity)
	case err == nil:
		// A crash after SQLite commit or a rebuild may have indexed this marker.
		_, err = tx.ExecContext(ctx, `UPDATE file_snapshots SET run_id=? WHERE snapshot_id=?`, t.RunID, rec.ID)
	}
	if err != nil {
		return backupFailure(ctx, "snapshot_catalog_failed", err)
	}
	if err = tx.Commit(); err != nil {
		return backupFailure(ctx, "snapshot_catalog_failed", err)
	}
	return nil
}
