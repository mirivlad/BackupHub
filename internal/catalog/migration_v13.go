package catalog

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateVerificationV13(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS repository_check_schedules (
			repository_id TEXT PRIMARY KEY REFERENCES storage_repositories(id),
			mode TEXT NOT NULL CHECK(mode IN ('metadata','data')),
			interval_hours INTEGER NOT NULL CHECK(interval_hours BETWEEN 0 AND 8760),
			next_run INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS verification_runs (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES storage_repositories(id),
			snapshot_id TEXT REFERENCES snapshots(id),
			kind TEXT NOT NULL CHECK(kind IN ('metadata','data','restore')),
			state TEXT NOT NULL CHECK(state IN ('queued','running','passed','failed','interrupted')),
			queued_at INTEGER NOT NULL,
			started_at INTEGER,
			finished_at INTEGER,
			detail TEXT NOT NULL DEFAULT '',
			progress REAL CHECK(progress BETWEEN 0 AND 1),
			files INTEGER,
			bytes INTEGER,
			CHECK((snapshot_id IS NOT NULL) = (kind = 'restore'))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS verification_active_repository ON verification_runs(repository_id)
			WHERE state IN ('queued','running')`,
		`CREATE INDEX IF NOT EXISTS verification_repository_latest ON verification_runs(repository_id, kind, queued_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS verification_snapshot_latest ON verification_runs(snapshot_id, queued_at DESC, id DESC)
			WHERE snapshot_id IS NOT NULL`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("v13 verification migration: %w", err)
		}
	}
	return nil
}
