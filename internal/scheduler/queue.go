package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mirivlad/BackupHub/internal/domain"
)

const activeOperationJoin = `LEFT JOIN backup_operations o ON o.schedule_id=e.id AND o.state IN ('running','waiting')`

// Recoverer resolves the publication boundary without starting another capture
// or spending another attempt. The original attempt remains interrupted.
type Recoverer interface {
	RecoverPublication(context.Context, Task) (bool, error)
}

// A retry yields to scheduled work already waiting when it became ready.
// New arrivals can overtake it for at most ten minutes of arrival times.
const retryPriorityGraceSeconds = 600

func (s *Scheduler) claim(ctx context.Context, tx *sql.Tx) (Task, func(), error) {
	var t Task
	now := s.now()
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.dataset_id,e.kind,e.interval_seconds,e.next_run_unix,
		e.retry_count,e.retry_interval_minutes,COALESCE(r.repository_id,''),COALESCE(o.id,''),
		COALESCE(o.attempt,0),COALESCE(o.recover_run_id,''),COALESCE(o.failure_code,''),
		CASE WHEN e.manual_pending=1 THEN 'manual' ELSE 'scheduled' END,
		CASE WHEN e.manual_pending=1 THEN COALESCE(e.manual_queued_unix,0) ELSE e.next_run_unix END AS requested
		FROM schedule_entries e LEFT JOIN schedule_repositories r ON r.schedule_id=e.id `+activeOperationJoin+`
		WHERE e.active_run_id IS NULL AND e.enabled=1
		AND ((o.id IS NULL AND (e.manual_pending=1 OR e.next_run_unix<=?)) OR (o.state='waiting' AND o.ready_unix<=?))
		ORDER BY CASE WHEN o.id IS NULL THEN requested ELSE o.ready_unix+? END,
		o.id IS NOT NULL,e.id`, now.Unix(), now.Unix(), retryPriorityGraceSeconds)
	if err != nil {
		return Task{}, nil, err
	}
	var seconds, next, requested int64
	var trigger string
	var release func()
	admitted := false
	for rows.Next() {
		err = rows.Scan(&t.ID, &t.DatasetID, &t.Kind, &seconds, &next, &t.retryCount, &t.retryMinutes,
			&t.RepositoryID, &t.OperationID, &t.Attempt, &t.RunID, &t.recoveryCode, &trigger, &requested)
		if err != nil {
			break
		}
		if gate, ok := s.execute.(Admission); ok {
			release, admitted = gate.TryReserve(t)
		} else {
			admitted = true
		}
		if admitted {
			break
		}
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil || !admitted {
		return Task{}, release, err
	}
	t.Interval = time.Duration(seconds) * time.Second
	t.NextRun, t.StartedAt, t.Enabled = time.Unix(next, 0).UTC(), now, true
	t.Recovering = t.RunID != ""
	if !t.Recovering {
		t.RunID, err = newID()
		if err != nil {
			return Task{}, release, err
		}
		t.Attempt++
	}
	if t.OperationID == "" {
		t.OperationID = t.RunID
		_, err = tx.ExecContext(ctx, `INSERT INTO backup_operations(id,schedule_id,trigger,requested_unix,planned_unix,state)
			VALUES(?,?,?,?,?,'running')`, t.OperationID, t.ID, trigger, requested, next)
		if err != nil {
			return Task{}, release, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedule_entries SET active_run_id=?,manual_pending=0,manual_queued_unix=NULL WHERE id=?`, t.RunID, t.ID); err != nil {
		return Task{}, release, err
	}
	if !t.Recovering {
		if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs(id,schedule_id,state,started_at,operation_id,attempt)
			VALUES(?,?,?,?,?,?)`, t.RunID, t.ID, domain.JobQueued, now.Format(time.RFC3339Nano), t.OperationID, t.Attempt); err != nil {
			return Task{}, release, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE backup_operations SET state='running',ready_unix=NULL,attempt=?,last_run_id=? WHERE id=?`,
		t.Attempt, t.RunID, t.OperationID)
	return t, release, err
}

// RunDue is the synchronous entry point used by tests and embedded callers.
func (s *Scheduler) RunDue(ctx context.Context) (bool, error) {
	run, err := s.ClaimDue(ctx)
	if run == nil {
		return false, err
	}
	return true, run()
}

// ClaimDue reserves a worker's repository and commits its attempt before
// returning the work to the dispatcher. Call the returned function exactly once.
func (s *Scheduler) ClaimDue(ctx context.Context) (func() error, error) {
	if s.execute == nil {
		return nil, errors.New("no native executor is registered")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	task, release, err := s.claim(ctx, tx)
	if err == nil && task.ID != "" {
		err = tx.Commit()
	}
	if err != nil || task.ID == "" {
		if release != nil {
			release()
		}
		return nil, err
	}
	return func() error {
		if release != nil {
			defer release()
		}
		return s.executeClaim(ctx, task)
	}, nil
}

func (s *Scheduler) executeClaim(ctx context.Context, task Task) error {
	current := domain.JobQueued
	report := func(next domain.JobState) error {
		if err := domain.ValidateTransition(current, next); err != nil {
			return err
		}
		if domain.IsTerminal(next) || next == domain.JobWaiting || next == domain.JobInterrupted {
			return errors.New("executor may not self-report a final state")
		}
		_, err := s.db.ExecContext(ctx, `UPDATE schedule_runs SET state=? WHERE id=?`, next, task.RunID)
		if err != nil {
			return &BackupFailure{Code: "snapshot_catalog_failed", Err: err}
		}
		current = next
		return nil
	}
	var executeErr error
	var code string
	state := domain.JobCompleted
	if task.Recovering {
		current = domain.JobPublishing
		recovered := false
		if executor, ok := s.execute.(Recoverer); ok {
			recovered, executeErr = executor.RecoverPublication(ctx, task)
		}
		if !recovered && executeErr == nil {
			executeErr = &BackupFailure{Code: task.recoveryCode, Err: errors.New("publication did not complete")}
		}
	} else if executeErr = report(domain.JobPreflight); executeErr == nil {
		executeErr = s.execute.Execute(ctx, task, report)
		if executeErr == nil && current != domain.JobVerifying && current != domain.JobFinalizing {
			executeErr = &BackupFailure{Code: "incomplete_phase_sequence", Err: errors.New("native executor did not finish required backup phases")}
		}
	}
	if executeErr != nil {
		state, code = domain.JobFailed, failureCode(executeErr)
		switch {
		case ctx.Err() != nil:
			state, code = domain.JobInterrupted, "process_restart"
		case task.Attempt <= task.retryCount && (code == "source_unavailable" || code == "storage_unavailable") && current == domain.JobPreflight:
			state = domain.JobWaiting
		}
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.finish(finishCtx, task, current, state, code, s.now()); err != nil {
		s.pendingFinishes.Store(task.RunID, pendingFinish{task, current, state, code, s.now()})
		return fmt.Errorf("finish backup attempt %s: %w", task.RunID, err)
	}
	return executeErr
}

func (s *Scheduler) finish(ctx context.Context, t Task, stage, state domain.JobState, code string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stillRunning bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backup_operations o
		JOIN schedule_entries e ON e.id=o.schedule_id WHERE o.id=? AND o.state='running' AND e.active_run_id=?)`,
		t.OperationID, t.RunID).Scan(&stillRunning); err != nil || !stillRunning {
		return err
	}
	if err = finishAttempt(ctx, tx, t, stage, state, code, now); err != nil {
		return err
	}
	return tx.Commit()
}

func finishAttempt(ctx context.Context, tx *sql.Tx, t Task, stage, state domain.JobState, code string, now time.Time) error {
	operationState, operationCode := "completed", code
	var ready, recovery, finished any
	finished = now.Format(time.RFC3339Nano)
	if code != "" {
		operationState = "failed"
		switch {
		case (stage == domain.JobPublishing || stage == domain.JobFinalizing) &&
			(state == domain.JobInterrupted || (!t.Recovering && retryable(code))):
			operationState, ready, recovery = "waiting", now.Unix(), t.RunID
		case retryable(code) && t.Attempt <= t.retryCount:
			operationState = "waiting"
			// Round up to the next second to avoid an early retry.
			ready = now.Add(time.Duration(t.Attempt*t.retryMinutes)*time.Minute + time.Second - 1).Unix()
		case retryable(code):
			operationCode = "retry_exhausted"
		}
	} else {
		stage = ""
	}
	if !t.Recovering {
		_, err := tx.ExecContext(ctx, `UPDATE schedule_runs SET state=?,finished_at=?,failure_code=?,failure_stage=?,
			failure_detail=?,next_retry_unix=? WHERE id=?`, state, finished, code, stage, FailureDetail(code), ready, t.RunID)
		if err != nil {
			return err
		}
	} else if ready != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_runs SET next_retry_unix=? WHERE id=?`, ready, t.RunID); err != nil {
			return err
		}
	}
	if operationState == "waiting" {
		finished = nil
	} else if err := advanceSchedule(ctx, tx, t.ID, t.NextRun, t.Interval, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_operations SET state=?,failure_code=?,ready_unix=?,recover_run_id=?,finished_at=?
		WHERE id=?`, operationState, operationCode, ready, recovery, finished, t.OperationID); err != nil {
		return err
	}
	if operationState != "waiting" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM backup_publications WHERE operation_id=?`, t.OperationID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE schedule_entries SET active_run_id=NULL WHERE id=? AND active_run_id=?`, t.ID, t.RunID)
	return err
}

func advanceSchedule(ctx context.Context, tx *sql.Tx, id string, planned time.Time, interval time.Duration, now time.Time) error {
	if planned.After(now) {
		return nil
	}
	next, err := nextRun(ctx, tx, id, now, interval)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE schedule_entries SET next_run_unix=? WHERE id=?`, next.Unix(), id)
	return err
}

// NextWake returns the earliest future eligibility. An already ready task can
// be blocked by a reservation, whose release wakes the dispatcher.
func (s *Scheduler) NextWake(ctx context.Context, cutoff time.Time) (*time.Time, error) {
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(ready) FROM (
		SELECT CASE WHEN o.id IS NOT NULL THEN o.ready_unix ELSE e.next_run_unix END AS ready
		FROM schedule_entries e `+activeOperationJoin+`
		WHERE e.enabled=1 AND e.active_run_id IS NULL AND (o.id IS NOT NULL OR e.manual_pending=0)
	) WHERE ready>?`, cutoff.Unix()).Scan(&next)
	return unixTime(next), err
}

type pendingFinish struct {
	task         Task
	stage, state domain.JobState
	code         string
	finishedAt   time.Time
}

// FlushFinishes lets the dispatcher release workers even if SQLite could
// not finish an attempt. The durable claim still excludes this same job.
func (s *Scheduler) FlushFinishes(ctx context.Context) (retry bool) {
	s.pendingFinishes.Range(func(key, value any) bool {
		item := value.(pendingFinish)
		if err := s.finish(ctx, item.task, item.stage, item.state, item.code, item.finishedAt); err != nil {
			retry = true
		} else {
			s.pendingFinishes.Delete(key)
		}
		return ctx.Err() == nil
	})
	return retry
}
