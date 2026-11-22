// Package scheduler implements durable, single-instance orchestration.
// Its dispatcher admits only registered native executors.
package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/domain"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

const MaxInterval = 365 * 24 * time.Hour

type Task struct {
	ID           string        `json:"id"`
	DatasetID    string        `json:"dataset_id"`
	Kind         string        `json:"kind"`
	Interval     time.Duration `json:"-"`
	NextRun      time.Time     `json:"next_run"`
	Enabled      bool          `json:"enabled"`
	RepositoryID string        `json:"-"`
	RunID        string        `json:"-"`
	StartedAt    time.Time     `json:"-"`
	OperationID  string        `json:"-"`
	Attempt      int           `json:"-"`
	Recovering   bool          `json:"-"`
	recoveryCode string
	retryCount   int
	retryMinutes int
}
type Run struct {
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id"`
	State            domain.JobState `json:"state"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	FailureCode      string          `json:"failure_code,omitempty"`
	SnapshotID       string          `json:"snapshot_id,omitempty"`
	OperationID      string          `json:"operation_id,omitempty"`
	OperationState   string          `json:"operation_state,omitempty"`
	OperationFailure string          `json:"operation_failure,omitempty"`
	Attempt          int             `json:"attempt"`
	FailureStage     string          `json:"failure_stage,omitempty"`
	FailureDetail    string          `json:"failure_detail,omitempty"`
	NextRetry        *time.Time      `json:"next_retry,omitempty"`
	Trigger          string          `json:"trigger,omitempty"`
	RequestedAt      *time.Time      `json:"requested_at,omitempty"`
	PlannedAt        *time.Time      `json:"planned_at,omitempty"`
}

// Executor is implemented only by registered native capture/verify/replica
// strategies. Phase transitions must be reported durably; a handler returning
// nil without reaching verifying is considered a failure, not a successful backup.
type Executor interface {
	Execute(ctx context.Context, task Task, report func(domain.JobState) error) error
}

// Admission reserves a task's resource without waiting or querying SQLite.
// A declined task keeps its pending flag and due time and gets no run record.
type Admission interface {
	TryReserve(Task) (release func(), admitted bool)
}

type Scheduler struct {
	db              *sql.DB
	execute         Executor
	now             func() time.Time
	pendingFinishes sync.Map
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

func (s *Scheduler) RecentRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be 1 to 100")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.schedule_id,r.state,r.started_at,r.finished_at,r.failure_code,COALESCE(f.snapshot_id,''),COALESCE(r.operation_id,''),r.attempt,r.failure_stage,r.failure_detail,
 COALESCE(o.state,''),CASE WHEN o.last_run_id=r.id THEN o.failure_code ELSE '' END,r.next_retry_unix,COALESCE(o.trigger,''),o.requested_unix,o.planned_unix
 FROM schedule_runs r LEFT JOIN backup_operations o ON o.id=r.operation_id
 LEFT JOIN file_snapshots f ON f.run_id=r.id ORDER BY r.started_at DESC,r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]Run, 0)
	for rows.Next() {
		var run Run
		var started string
		var finished sql.NullString
		var retry, requested, planned sql.NullInt64
		if err = rows.Scan(&run.ID, &run.TaskID, &run.State, &started, &finished, &run.FailureCode, &run.SnapshotID, &run.OperationID, &run.Attempt, &run.FailureStage, &run.FailureDetail,
			&run.OperationState, &run.OperationFailure, &retry, &run.Trigger, &requested, &planned); err != nil {
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
		run.NextRetry, run.RequestedAt, run.PlannedAt = unixTime(retry), unixTime(requested), unixTime(planned)
		results = append(results, run)
	}
	return results, rows.Err()
}

func unixTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := time.Unix(value.Int64, 0).UTC()
	return &t
}
