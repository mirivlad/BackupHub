package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/storagehelper"
)

const recoveredSourceKind = "recovered"

type recoveryTree struct {
	record   portableRecord
	markerID string
}

type resticRecoverySnapshot struct {
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
	Tags  []string `json:"tags"`
}

// ReconcileRecoveryRepository imports committed tree manifests into the catalog.
// It only opens an existing repository, without creating storage or running jobs.
func ReconcileRecoveryRepository(ctx context.Context, c *catalog.Catalog, id string) (int, error) {
	if !safeID(id) {
		return 0, errors.New("invalid repository ID")
	}
	var path, mount, mode string
	if err := c.DB.QueryRowContext(ctx, "SELECT path,mount_path,password_mode FROM storage_repositories WHERE id=?", id).
		Scan(&path, &mount, &mode); err != nil {
		return 0, err
	}
	if mount != "" && !storagehelper.MountPresent(mount) {
		return 0, errors.New("storage volume not mounted")
	}
	repo, err := openRepositoryNamedMode(ctx, c.DataDir, path, filepath.Join(c.DataDir, recoveryStorageKeyName(id)), mode)
	if err != nil {
		return 0, err
	}
	trees, err := repo.committedRecoveryTrees(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	existing, err := indexedRecoveryTrees(ctx, tx, id, trees)
	if err != nil {
		return 0, err
	}
	// Preserve a meaningful label for a source created after the kit was made.
	ordered := make([]recoveryTree, 0, len(trees))
	for _, tree := range trees {
		if !existing[tree.record.ID] {
			ordered = append(ordered, tree)
		}
	}
	slices.SortFunc(ordered, func(a, b recoveryTree) int {
		return strings.Compare(b.record.At, a.record.At)
	})
	for _, tree := range ordered {
		if err = insertRecoveredSource(ctx, tx, tree.record); err != nil {
			return 0, err
		}
		if err = insertCommittedTree(ctx, tx, id, tree.markerID, tree.record); err != nil {
			return 0, fmt.Errorf("import snapshot %s: %w", tree.record.ID, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(ordered), nil
}

func (r *repository) committedRecoveryTrees(ctx context.Context) (map[string]recoveryTree, error) {
	raw, err := r.run(ctx, nil, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var all []resticRecoverySnapshot
	if err = json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("decode Restic snapshot list: %w", err)
	}
	byID := make(map[string]resticRecoverySnapshot, len(all))
	for _, snapshot := range all {
		byID[snapshot.ID] = snapshot
	}
	trees := map[string]recoveryTree{}
	for _, marker := range all {
		if len(marker.Paths) != 1 || marker.Paths[0] != "/"+portableTreeFile {
			continue
		}
		for _, tag := range marker.Tags {
			id, hasCommitTag := strings.CutPrefix(tag, "bh-commit-")
			if !hasCommitTag {
				continue
			}
			if !safeID(id) || !validResticID(marker.ID) || trees[id].markerID != "" {
				return nil, fmt.Errorf("invalid or ambiguous tree commit marker: %q", id)
			}
			raw, err := r.run(ctx, nil, "dump", marker.ID, "/"+portableTreeFile)
			if err != nil {
				return nil, fmt.Errorf("read tree commit %s: %w", id, err)
			}
			var rec portableRecord
			if len(raw) > 64<<10 || json.Unmarshal(raw, &rec) != nil || !validRecoveryTree(id, rec, byID[rec.ResticID]) {
				return nil, fmt.Errorf("invalid tree manifest or missing native snapshot: %s", id)
			}
			trees[id] = recoveryTree{record: rec, markerID: marker.ID}
		}
	}
	return trees, nil
}

func validRecoveryTree(id string, rec portableRecord, native resticRecoverySnapshot) bool {
	if rec.Format != portableTreeFormat || rec.ID != id || !safeID(rec.SourceID) || !validResticID(rec.ResticID) ||
		!rec.DataVerified || native.ID != rec.ResticID || !slices.Contains(native.Tags, "bh-source-"+rec.SourceID) ||
		len(native.Paths) != 1 || native.Paths[0] != rec.Root || !filepath.IsAbs(rec.Root) || filepath.Clean(rec.Root) != rec.Root {
		return false
	}
	if rec.SourceName == "" || utf8.RuneCountInString(rec.SourceName) > 120 || strings.ContainsAny(rec.SourceName, "\x00\r\n") {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.At); err != nil {
		return false
	}
	switch rec.Kind {
	case "postgres":
		return rec.Consistency == "application_consistent" && rec.Fidelity == "logical_dump"
	case postgres1CKind:
		return rec.Consistency == "application_consistent" && rec.Fidelity == "logical_dump_split_config"
	case "smb", smb1Kind:
		return rec.Consistency == "best_effort" && rec.Fidelity == "content_and_timestamps"
	case "local", "sftp":
		return rec.Consistency == "best_effort" && rec.Fidelity == "basic_metadata"
	default:
		return false
	}
}

func indexedRecoveryTrees(ctx context.Context, tx *sql.Tx, repoID string, trees map[string]recoveryTree) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.manifest_ref,f.source_id,f.restic_id,f.root_path,f.committed_at,f.fidelity,f.consistency
		FROM snapshots s JOIN file_snapshots f ON s.id=f.snapshot_id WHERE s.repository_id=? AND s.state='committed'`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var rec portableRecord
		var markerID string
		if err = rows.Scan(&rec.ID, &markerID, &rec.SourceID, &rec.ResticID, &rec.Root, &rec.At, &rec.Fidelity, &rec.Consistency); err != nil {
			return nil, err
		}
		current := trees[rec.ID]
		got := current.record
		if markerID != current.markerID || rec.SourceID != got.SourceID || rec.ResticID != got.ResticID || rec.Root != got.Root ||
			rec.At != got.At || rec.Fidelity != got.Fidelity || rec.Consistency != got.Consistency {
			return nil, fmt.Errorf("catalog snapshot %s does not match this repository", rec.ID)
		}
		found[rec.ID] = true
	}
	return found, rows.Err()
}

func insertRecoveredSource(ctx context.Context, tx *sql.Tx, rec portableRecord) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources(id,kind,display_name,endpoint,settings_json,created_at)
		VALUES(?,?,?,'','{}',?) ON CONFLICT(id) DO NOTHING`, rec.SourceID, recoveredSourceKind, rec.SourceName, rec.At); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO datasets(id,source_id,display_name,locator)
		VALUES(?,?,?,'') ON CONFLICT(id) DO NOTHING`, rec.SourceID, rec.SourceID, rec.SourceName); err != nil {
		return err
	}
	var owner string
	if err := tx.QueryRowContext(ctx, "SELECT source_id FROM datasets WHERE id=?", rec.SourceID).Scan(&owner); err != nil {
		return err
	}
	if owner != rec.SourceID {
		return errors.New("recovered source conflicts with an existing dataset")
	}
	return nil
}
