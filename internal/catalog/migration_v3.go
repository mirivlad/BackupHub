package catalog

import (
 "context"
 "database/sql"
 "fmt"
)

// Migration v3 introduces a durable scheduler WITHOUT replacing the existing
// job and job_run tables. It is intentionally a separate internal execution
// ledger until native connectors and job-scoped authorizations are integrated.
func migrateSchedulerV3(ctx context.Context,tx *sql.Tx)error{
 statements:=[]string{
  `CREATE TABLE schedule_entries (
   id TEXT PRIMARY KEY,
   dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE RESTRICT,
   kind TEXT NOT NULL CHECK(kind IN ('backup','verify','replicate')),
   interval_seconds INTEGER NOT NULL CHECK(interval_seconds >= 60 AND interval_seconds <= 31536000),
   next_run_unix INTEGER NOT NULL,
   enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
   active_run_id TEXT UNIQUE,
   created_at TEXT NOT NULL
  )`,
  `CREATE INDEX schedules_due ON schedule_entries(enabled,next_run_unix) WHERE active_run_id IS NULL`,
  `CREATE TABLE schedule_runs (
   id TEXT PRIMARY KEY,
   schedule_id TEXT NOT NULL REFERENCES schedule_entries(id) ON DELETE RESTRICT,
   state TEXT NOT NULL,
   started_at TEXT NOT NULL,
   finished_at TEXT,
   failure_code TEXT NOT NULL DEFAULT ''
  )`,
  `CREATE INDEX schedule_runs_recent ON schedule_runs(started_at DESC)`,
  `CREATE INDEX schedule_runs_for_task ON schedule_runs(schedule_id,started_at DESC)`,
 }
 for _,statement:=range statements{
  if _,err:=tx.ExecContext(ctx,statement);err!=nil{return fmt.Errorf("v3 scheduler migration: %w",err)}
 }
 return nil
}
