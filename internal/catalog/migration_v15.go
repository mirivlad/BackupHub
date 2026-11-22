package catalog

import (
	"context"
	"database/sql"
)

func migrateBackupQueue(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE schedule_entries ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 3 CHECK(retry_count BETWEEN 0 AND 10)`,
		`ALTER TABLE schedule_entries ADD COLUMN retry_interval_minutes INTEGER NOT NULL DEFAULT 30 CHECK(retry_interval_minutes BETWEEN 1 AND 525600)`,
		`ALTER TABLE schedule_entries ADD COLUMN manual_queued_unix INTEGER`,
		`CREATE TABLE backup_operations (
			id TEXT PRIMARY KEY,
			schedule_id TEXT NOT NULL REFERENCES schedule_entries(id) ON DELETE RESTRICT,
			trigger TEXT NOT NULL CHECK(trigger IN ('scheduled','manual')),
			requested_unix INTEGER NOT NULL,
			planned_unix INTEGER NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('running','waiting','completed','failed','cancelled')),
			attempt INTEGER NOT NULL DEFAULT 0,
			ready_unix INTEGER,
			last_run_id TEXT REFERENCES schedule_runs(id),
			recover_run_id TEXT REFERENCES schedule_runs(id),
			failure_code TEXT NOT NULL DEFAULT '',
			finished_at TEXT
		)`,
		`CREATE UNIQUE INDEX backup_operations_active ON backup_operations(schedule_id) WHERE state IN ('running','waiting')`,
		`ALTER TABLE schedule_runs ADD COLUMN operation_id TEXT REFERENCES backup_operations(id)`,
		`ALTER TABLE schedule_runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE schedule_runs ADD COLUMN failure_stage TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE schedule_runs ADD COLUMN failure_detail TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE schedule_runs ADD COLUMN next_retry_unix INTEGER`,
		`CREATE UNIQUE INDEX schedule_runs_attempt ON schedule_runs(operation_id,attempt) WHERE operation_id IS NOT NULL`,
		`CREATE TABLE backup_publications (
			operation_id TEXT PRIMARY KEY REFERENCES backup_operations(id),
			run_id TEXT NOT NULL REFERENCES schedule_runs(id),
			record_json TEXT NOT NULL
		)`,
		`CREATE TABLE backup_staging (
			run_id TEXT PRIMARY KEY REFERENCES schedule_runs(id),
			path TEXT NOT NULL,
			mount_path TEXT NOT NULL,
			token TEXT NOT NULL
		)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
