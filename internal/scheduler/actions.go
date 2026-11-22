package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrNoPendingRetry = errors.New("no matching pending retry")

func (s *Scheduler) ChangeRetry(ctx context.Context, jobID, operationID string, cancel bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	var planned, seconds int64
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT e.next_run_unix,e.interval_seconds,e.enabled
		FROM backup_operations o JOIN schedule_entries e ON e.id=o.schedule_id
		WHERE o.id=? AND o.schedule_id=? AND o.state='waiting' AND o.recover_run_id IS NULL
		AND e.active_run_id IS NULL`, operationID, jobID).Scan(&planned, &seconds, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoPendingRetry
	}
	if err != nil {
		return err
	}
	if cancel {
		if _, err := tx.ExecContext(ctx, `UPDATE backup_operations SET state='cancelled',ready_unix=NULL,
			failure_code='retry_cancelled',finished_at=? WHERE id=?`, now.Format(time.RFC3339Nano), operationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM backup_publications WHERE operation_id=?`, operationID); err != nil {
			return err
		}
		if err := advanceSchedule(ctx, tx, jobID, time.Unix(planned, 0), time.Duration(seconds)*time.Second, now); err != nil {
			return err
		}
	} else {
		if !enabled {
			return ErrNoPendingRetry
		}
		if _, err := tx.ExecContext(ctx, `UPDATE backup_operations SET ready_unix=MIN(ready_unix,?) WHERE id=?`, now.Unix(), operationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
