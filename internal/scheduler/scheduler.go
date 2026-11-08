// Package scheduler implements durable, single-instance orchestration.
// It is deliberately not started by the production HTTP server until at least
// one native capture connector is registered. No arbitrary script task exists.
package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/domain"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var ErrSourceUnavailable = errors.New("source unavailable")

const MaxInterval = 365 * 24 * time.Hour
const retryDelay = 5 * time.Minute

type Task struct {
	ID        string        `json:"id"`
	DatasetID string        `json:"dataset_id"`
	Kind      string        `json:"kind"`
	Interval  time.Duration `json:"-"`
	NextRun   time.Time     `json:"next_run"`
	Enabled   bool          `json:"enabled"`
}
type Run struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	State       domain.JobState `json:"state"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at,omitempty"`
	FailureCode string          `json:"failure_code,omitempty"`
}

// Executor is implemented only by registered native capture/verify/replica
// strategies. Phase transitions must be reported durably; a handler returning
// nil without reaching verifying is considered a failure, not a successful backup.
type Executor interface {
	Execute(ctx context.Context, task Task, report func(domain.JobState) error) error
}
type Scheduler struct {
	db      *sql.DB
	execute Executor
	now     func() time.Time
}

func New(c *catalog.Catalog, executor Executor) *Scheduler {
	return &Scheduler{db: c.DB, execute: executor, now: func() time.Time { return time.Now().UTC() }}
}
func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func validTask(t Task) error {
	if !idPattern.MatchString(t.ID) || !idPattern.MatchString(t.DatasetID) {
		return errors.New("invalid task/dataset ID")
	}
	switch t.Kind {
	case "backup", "verify", "replicate":
	default:
		return errors.New("unsupported task kind")
	}
	if t.Interval < time.Minute || t.Interval > MaxInterval || t.Interval%time.Second != 0 {
		return errors.New("interval must be an integral number of seconds between 1 minute and 1 year")
	}
	if t.NextRun.IsZero() {
		return errors.New("next_run is required")
	}
	return nil
}
func (s *Scheduler) Add(ctx context.Context, task Task) error {
	if err := validTask(task); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedule_entries(id,dataset_id,kind,interval_seconds,next_run_unix,enabled,created_at)
 VALUES(?,?,?,?,?,?,?)`, task.ID, task.DatasetID, task.Kind, int64(task.Interval/time.Second), task.NextRun.UTC().Unix(), boolInt(task.Enabled), s.now().Format(time.RFC3339Nano))
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// RecoverInterrupted runs at service startup before starting any workers.
// A previously in-flight operation never becomes successful after a crash.
// One retry is made eligible; missed intervals are NOT replayed in a burst.
func (s *Scheduler) RecoverInterrupted(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE schedule_runs SET state=?, finished_at=?, failure_code='process_restart'
 WHERE finished_at IS NULL AND state NOT IN (?,?,?,?)`, string(domain.JobInterrupted), s.now().Format(time.RFC3339Nano), string(domain.JobCompleted), string(domain.JobWarnings), string(domain.JobFailed), string(domain.JobCancelled))
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedule_entries SET active_run_id=NULL, next_run_unix=? WHERE active_run_id IS NOT NULL`, s.now().Unix()); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// RunDue performs at most one due job. The database transaction claims the
// schedule before invoking the potentially long-running executor. With one
// SQLite writer and an active_run_id guard, concurrent polls cannot overlap.
func (s *Scheduler) RunDue(ctx context.Context) (bool, error) {
	if s.execute == nil {
		return false, errors.New("no native executor is registered")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var t Task
	var seconds, nextUnix int64
	var enabled int
	err = tx.QueryRowContext(ctx, `SELECT id,dataset_id,kind,interval_seconds,next_run_unix,enabled
 FROM schedule_entries WHERE enabled=1 AND active_run_id IS NULL AND next_run_unix<=?
 ORDER BY next_run_unix,id LIMIT 1`, s.now().Unix()).Scan(&t.ID, &t.DatasetID, &t.Kind, &seconds, &nextUnix, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t.Interval = time.Duration(seconds) * time.Second
	t.NextRun = time.Unix(nextUnix, 0).UTC()
	t.Enabled = enabled != 0
	runID, err := newID()
	if err != nil {
		return false, err
	}
	now := s.now()
	res, err := tx.ExecContext(ctx, `UPDATE schedule_entries SET active_run_id=? WHERE id=? AND active_run_id IS NULL`, runID, t.ID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs(id,schedule_id,state,started_at)
 VALUES(?,?,?,?)`, runID, t.ID, string(domain.JobQueued), now.Format(time.RFC3339Nano)); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	current := domain.JobQueued
	report := func(next domain.JobState) error {
		if err := domain.ValidateTransition(current, next); err != nil {
			return err
		}
		if next == domain.JobCompleted || next == domain.JobWarnings || next == domain.JobFailed || next == domain.JobCancelled || next == domain.JobWaiting {
			return errors.New("executor may not self-report a terminal state")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE schedule_runs SET state=? WHERE id=? AND state=?`, string(next), runID, string(current))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("run state changed concurrently")
		}
		current = next
		return nil
	}
	var executeErr error
	if err = report(domain.JobPreflight); err != nil {
		executeErr = err
	} else {
		executeErr = s.execute.Execute(ctx, t, report)
	}
	finalState := domain.JobCompleted
	code := ""
	if executeErr != nil {
		finalState = domain.JobFailed
		code = "executor_error"
		if errors.Is(executeErr, ErrSourceUnavailable) && current == domain.JobPreflight {
			finalState = domain.JobWaiting
			code = "source_unavailable"
		}
	} else if current != domain.JobVerifying {
		finalState = domain.JobFailed
		code = "incomplete_phase_sequence"
		executeErr = errors.New("native executor did not finish required backup phases")
	}
	if err := domain.ValidateTransition(current, finalState); err != nil {
		finalState = domain.JobFailed
		code = "invalid_transition"
		executeErr = fmt.Errorf("cannot finish job %s: %w", runID, err)
	}
	completed := s.now()
	nextRun := completed.Add(t.Interval)
	if finalState == domain.JobWaiting {
		nextRun = completed.Add(min(t.Interval, retryDelay))
	}
	finishTx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return true, err
	}
	defer finishTx.Rollback()
	if _, err = finishTx.ExecContext(context.Background(), `UPDATE schedule_runs SET state=?,finished_at=?,failure_code=? WHERE id=?`, string(finalState), completed.Format(time.RFC3339Nano), code, runID); err != nil {
		return true, err
	}
	if _, err = finishTx.ExecContext(context.Background(), `UPDATE schedule_entries SET active_run_id=NULL,next_run_unix=? WHERE id=? AND active_run_id=?`, nextRun.Unix(), t.ID, runID); err != nil {
		return true, err
	}
	if err = finishTx.Commit(); err != nil {
		return true, err
	}
	return true, executeErr
}

func (s *Scheduler) RecentRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be 1 to 100")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,schedule_id,state,started_at,finished_at,failure_code
 FROM schedule_runs ORDER BY started_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]Run, 0)
	for rows.Next() {
		var run Run
		var started string
		var finished sql.NullString
		if err = rows.Scan(&run.ID, &run.TaskID, &run.State, &started, &finished, &run.FailureCode); err != nil {
			return nil, err
		}
		run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, err
		}
		if finished.Valid {
			v, parseErr := time.Parse(time.RFC3339Nano, finished.String)
			if parseErr != nil {
				return nil, parseErr
			}
			run.FinishedAt = &v
		}
		results = append(results, run)
	}
	return results, rows.Err()
}
