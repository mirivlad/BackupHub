package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (s *Service) ListSnapshots(ctx context.Context, sourceID string) ([]Snapshot, error) {
	query := `SELECT f.snapshot_id,f.source_id,s.display_name,s.kind,f.restic_id,f.root_path,f.committed_at,f.verification,f.fidelity,f.consistency FROM file_snapshots f JOIN sources s ON f.source_id=s.id`
	args := []any{}
	if sourceID != "" {
		if !safeID(sourceID) {
			return nil, errors.New("invalid source ID")
		}
		query += " WHERE f.source_id=?"
		args = append(args, sourceID)
	}
	query += " ORDER BY f.committed_at DESC LIMIT 500"
	rows, err := s.catalog.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snaps := []Snapshot{}
	for rows.Next() {
		var v Snapshot
		if err = rows.Scan(&v.ID, &v.SourceID, &v.SourceName, &v.Kind, &v.ResticID, &v.Root, &v.At, &v.Verification, &v.Fidelity, &v.Consistency); err != nil {
			return nil, err
		}
		snaps = append(snaps, v)
	}
	return snaps, rows.Err()
}
func (s *Service) snapshot(ctx context.Context, id string) (Snapshot, error) {
	var snap Snapshot
	if !safeID(id) {
		return snap, errors.New("invalid snapshot ID")
	}
	err := s.catalog.DB.QueryRowContext(ctx, `SELECT f.snapshot_id,f.source_id,s.display_name,s.kind,f.restic_id,f.root_path,f.committed_at,f.verification,f.fidelity,f.consistency FROM file_snapshots f JOIN sources s ON f.source_id=s.id WHERE f.snapshot_id=?`, id).Scan(&snap.ID, &snap.SourceID, &snap.SourceName, &snap.Kind, &snap.ResticID, &snap.Root, &snap.At, &snap.Verification, &snap.Fidelity, &snap.Consistency)
	return snap, err
}
func (s *Service) Files(ctx context.Context, id string) ([]File, error) {
	snap, err := s.snapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	entries, err := s.repo.listTree(ctx, snap.ResticID)
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(entries))
	for _, e := range entries {
		rel, err := relativeTo(snap.Root, e.Path)
		if err != nil || rel == "." {
			continue
		}
		// Do not present special nodes as selectable regular files.
		kind := e.Type
		if kind != "file" && kind != "dir" {
			kind = "other"
		}
		out = append(out, File{Path: rel, Name: filepath.Base(rel), Type: kind, Size: e.Size, Modified: e.MTime})
	}
	return out, nil
}
func (s *Service) Download(ctx context.Context, id string, selections []string, single bool, w http.ResponseWriter) error {
	snap, err := s.snapshot(ctx, id)
	if err != nil {
		return err
	}
	if single {
		if len(selections) != 1 {
			return errors.New("single download requires exactly one path")
		}
		return s.repo.downloadSingle(ctx, snap.ResticID, snap.Root, selections[0], w)
	}
	if len(selections) > 64 {
		return errors.New("too many selections")
	}
	// Ensure there is at least one matching file before streaming headers. All
	// archive members come from one immutable Restic snapshot.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", snap.ID+".zip"))
	return s.repo.zipSelection(ctx, snap.ResticID, snap.Root, selections, w)
}
func (s *Service) Audit(ctx context.Context, actor, action, resource string) {
	_, err := s.catalog.DB.ExecContext(ctx, `INSERT INTO audit_events(occurred_at,actor,action,resource,metadata_json) VALUES(?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, action, resource, `{}`)
	if err != nil {
		slog.Error("unable to persist audit event", "error", safeFailure(err))
	}
}
func (s *Service) ResticCheck(ctx context.Context, all bool) error { return s.repo.check(ctx, all) }
func (s *Service) KeyFingerprint() (string, error) {
	data, err := os.ReadFile(s.repo.passwordFile)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:8]), nil
}
