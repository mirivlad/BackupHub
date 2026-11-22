package scheduler

import (
	"context"
	"time"

	"github.com/mirivlad/BackupHub/internal/domain"
)

// RecoverInterrupted runs under the service lease before dispatch starts.
func (s *Scheduler) RecoverInterrupted(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := s.now()
	type interrupted struct {
		task  Task
		stage domain.JobState
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.interval_seconds,e.next_run_unix,e.retry_count,e.retry_interval_minutes,
		o.id,o.attempt,o.last_run_id,o.recover_run_id IS NOT NULL,r.state
		FROM backup_operations o JOIN schedule_entries e ON e.id=o.schedule_id
		JOIN schedule_runs r ON r.id=o.last_run_id WHERE o.state='running'`)
	if err != nil {
		return 0, err
	}
	var runs []interrupted
	for rows.Next() {
		var run interrupted
		var seconds, next int64
		t := &run.task
		if err = rows.Scan(&t.ID, &seconds, &next, &t.retryCount, &t.retryMinutes, &t.OperationID, &t.Attempt, &t.RunID, &t.Recovering, &run.stage); err != nil {
			rows.Close()
			return 0, err
		}
		t.Interval, t.NextRun = time.Duration(seconds)*time.Second, time.Unix(next, 0)
		if t.Recovering {
			run.stage = domain.JobPublishing
		}
		runs = append(runs, run)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, run := range runs {
		if err = finishAttempt(ctx, tx, run.task, run.stage, domain.JobInterrupted, "process_restart", now); err != nil {
			return 0, err
		}
	}
	// The old schema had no logical operation ID or durable manual origin.
	// Preserve its interrupted history and queue one catch-up.
	legacy, err := tx.ExecContext(ctx, `UPDATE schedule_runs SET failure_stage=state,state='interrupted',finished_at=?,
		failure_code='process_restart',failure_detail=? WHERE finished_at IS NULL AND operation_id IS NULL`,
		now.Format(time.RFC3339Nano), FailureDetail("process_restart"))
	if err != nil {
		return 0, err
	}
	count, err := legacy.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedule_entries SET active_run_id=NULL,manual_pending=1,manual_queued_unix=?
		WHERE active_run_id IN (SELECT id FROM schedule_runs WHERE operation_id IS NULL AND state='interrupted')`, now.Unix()); err != nil {
		return 0, err
	}
	return count + int64(len(runs)), tx.Commit()
}
