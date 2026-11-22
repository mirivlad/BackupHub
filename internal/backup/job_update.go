package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// UpdateJob modifies its source, destination and private schedule in a single
// transaction without changing its ID, run ledger or historical snapshots.
func (s *Service) UpdateJob(ctx context.Context, id string, in CreateBackupJob) (BackupJob, error) {
	var empty BackupJob
	if !safeID(id) || !safeID(in.SourceID) || !safeID(in.RepositoryID) {
		return empty, errors.New("invalid job, source or storage ID")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return empty, errors.New("invalid job name")
	}
	p, anchor, minutes, err := validateSchedule(in.Schedule)
	if err != nil {
		return empty, err
	}
	if _, err = s.repositoryFor(ctx, in.RepositoryID); err != nil {
		return empty, errors.New("destination storage unavailable")
	}
	var sourceName string
	if err = s.catalog.DB.QueryRowContext(ctx, "SELECT display_name FROM sources WHERE id=? AND kind<>?", in.SourceID, recoveredSourceKind).Scan(&sourceName); err != nil {
		return empty, errors.New("source not found or available only for restore")
	}
	now := time.Now().UTC()
	next, err := nextSchedule(p, anchor, now)
	if err != nil {
		return empty, err
	}
	newProfileID, err := randomID()
	if err != nil {
		return empty, err
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var enabled, manualPending, retries, step int
	var operationActive bool
	var active, oldProfile sql.NullString
	var oldNext int64
	err = tx.QueryRowContext(ctx, `SELECT enabled,active_run_id,profile_id,next_run_unix,manual_pending,retry_count,retry_interval_minutes,
		EXISTS(SELECT 1 FROM backup_operations WHERE schedule_id=e.id AND state IN ('running','waiting'))
		FROM schedule_entries e WHERE id=?`, id).Scan(&enabled, &active, &oldProfile, &oldNext, &manualPending, &retries, &step, &operationActive)
	if err != nil {
		return empty, err
	}
	if active.Valid {
		return empty, errors.New("cannot edit a running backup job")
	}
	if manualPending != 0 || operationActive {
		return empty, errors.New("cannot edit a queued backup job")
	}
	if enabled != 0 && oldNext <= now.Unix() {
		return empty, errors.New("cannot edit an already queued job; let it finish first")
	}
	retries, step, err = in.retrySettings(retries, step)
	if err != nil {
		return empty, err
	}
	created := now.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_profiles(id,name,mode,interval_minutes,days_of_week,hour,minute,timezone,created_at,start_at_unix,interval_unit,interval_value,cron_expression)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, newProfileID, "job-"+newProfileID, dbScheduleMode(p), minutes, p.DaysOfWeek, p.Hour, p.Minute, p.Timezone, created, anchor.Unix(), p.IntervalUnit, p.IntervalValue, p.CronExpression)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET dataset_id=? WHERE id=?", in.SourceID, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE schedule_entries SET dataset_id=?,display_name=?,profile_id=?,interval_seconds=?,next_run_unix=?,retry_count=?,retry_interval_minutes=? WHERE id=? AND active_run_id IS NULL`,
			in.SourceID, name, newProfileID, profileInterval(p, minutes), next.Unix(), retries, step, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO schedule_repositories(schedule_id,repository_id) VALUES(?,?)
 ON CONFLICT(schedule_id) DO UPDATE SET repository_id=excluded.repository_id`, id, in.RepositoryID)
	}
	if err == nil && oldProfile.Valid {
		_, err = tx.ExecContext(ctx, `DELETE FROM schedule_profiles WHERE id=? AND NOT EXISTS(SELECT 1 FROM schedule_entries WHERE profile_id=?)`, oldProfile.String, oldProfile.String)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return empty, fmt.Errorf("update job: %w", err)
	}
	s.notify()
	return BackupJob{ID: id, Name: name, SourceID: in.SourceID, SourceName: sourceName, RepositoryID: in.RepositoryID, Schedule: p, Enabled: enabled != 0, RetryCount: retries, RetryIntervalMinutes: step, NextRun: next.Format(time.RFC3339)}, nil
}
