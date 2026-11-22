package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/domain"
)

func TestRetryLedgerSurvivesRestartAndExhaustion(t *testing.T) {
	c, closeCatalog := fixture(t)
	defer closeCatalog()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	invocations := 0
	executor := executorFunc(func(context.Context, Task, func(domain.JobState) error) error {
		invocations++
		return ErrSourceUnavailable
	})
	newScheduler := func() *Scheduler {
		s := New(c, executor)
		s.now = func() time.Time { return now }
		return s
	}
	s := newScheduler()
	if err := s.Add(ctx, Task{ID: "job", DatasetID: "dataset-1", Kind: "backup", Interval: 10 * time.Minute, Enabled: true, NextRun: now}); err != nil {
		t.Fatal(err)
	}
	var opID string
	for attempt := 1; attempt <= 4; attempt++ {
		if ran, err := s.RunDue(ctx); !ran || !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("attempt %d: ran=%v err=%v", attempt, ran, err)
		}
		runs, err := s.RecentRuns(ctx, 10)
		if err != nil || len(runs) != attempt || runs[0].Attempt != attempt || runs[0].FailureStage != "preflight" {
			t.Fatalf("attempt %d: %+v %v", attempt, runs, err)
		}
		if opID == "" {
			opID = runs[0].OperationID
		}
		if runs[0].OperationID != opID || runs[0].Trigger != "scheduled" {
			t.Fatalf("operation identity changed: %+v", runs[0])
		}
		if attempt == 4 {
			if runs[0].NextRetry != nil || runs[0].OperationState != "failed" || runs[0].OperationFailure != "retry_exhausted" {
				t.Fatalf("failed to exhaust: %+v", runs[0])
			}
			break
		}
		next := now.Add(time.Duration(attempt) * 30 * time.Minute)
		if runs[0].NextRetry == nil || !runs[0].NextRetry.Equal(next) {
			t.Fatalf("wrong retry deadline: got %v want %s", runs[0].NextRetry, next)
		}
		if attempt == 2 {
			state := c.DataDir
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			c, err = catalog.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			s = newScheduler()
			if count, err := s.RecoverInterrupted(ctx); err != nil || count != 0 {
				t.Fatalf("waiting work changed on restart: %d %v", count, err)
			}
		}
		now = next.Add(-time.Second)
		if ran, err := s.RunDue(ctx); ran || err != nil {
			t.Fatalf("backoff skipped or missed occurrences accumulated: %v %v", ran, err)
		}
		now = next
	}
	if invocations != 4 {
		t.Fatalf("wanted exactly four executions, got %d", invocations)
	}
	var next int64
	if err := c.DB.QueryRow("SELECT next_run_unix FROM schedule_entries WHERE id='job'").Scan(&next); err != nil || next != now.Add(10*time.Minute).Unix() {
		t.Fatalf("ordinary scheduling lost: %v %v", next, err)
	}
	now = time.Unix(next, 0)
	if ran, _ := s.RunDue(ctx); !ran {
		t.Fatal("ordinary occurrence did not run after exhaustion")
	}
	runs, err := s.RecentRuns(ctx, 1)
	if err != nil || runs[0].OperationID == opID || runs[0].Attempt != 1 {
		t.Fatalf("ordinary occurrence inherited old budget: %+v %v", runs, err)
	}
}

func TestRetryFairnessAndCancellation(t *testing.T) {
	c, closeCatalog := fixture(t)
	defer closeCatalog()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	var order []string
	s := New(c, executorFunc(func(ctx context.Context, task Task, report func(domain.JobState) error) error {
		order = append(order, task.ID)
		if task.ID == "A" {
			if task.Attempt == 1 {
				now = now.Add(15 * time.Minute)
			} else {
				now = now.Add(20 * time.Minute)
			}
			return ErrSourceUnavailable
		}
		return fullExecution(ctx, task, report)
	}))
	s.now = func() time.Time { return now }
	for id, due := range map[string]time.Time{"A": now, "B": now.Add(45 * time.Minute)} {
		if err := s.Add(ctx, Task{ID: id, DatasetID: "dataset-1", Kind: "backup", Interval: 24 * time.Hour, Enabled: true, NextRun: due}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.DB.Exec("UPDATE schedule_entries SET retry_interval_minutes=15 WHERE id='A'"); err != nil {
		t.Fatal(err)
	}
	if ran, _ := s.RunDue(ctx); !ran {
		t.Fatal("02:00 A did not start")
	}
	now = now.Add(15 * time.Minute)
	if ran, _ := s.RunDue(ctx); !ran {
		t.Fatal("02:30 A did not retry")
	}
	if ran, err := s.RunDue(ctx); !ran || err != nil || now.Hour() != 2 || now.Minute() != 50 {
		t.Fatalf("B did not start at 02:50: %v %v %s", ran, err, now)
	}
	if len(order) != 3 || order[0] != "A" || order[1] != "A" || order[2] != "B" {
		t.Fatalf("wrong order: %v", order)
	}
	var id string
	if err := c.DB.QueryRow("SELECT id FROM backup_operations WHERE schedule_id='A'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeRetry(ctx, "A", "stale-id", true); !errors.Is(err, ErrNoPendingRetry) {
		t.Fatalf("stale cancellation: %v", err)
	}
	if err := s.ChangeRetry(ctx, "A", id, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if ran, err := s.RunDue(ctx); ran || err != nil {
		t.Fatalf("cancelled retry was dispatched: %v %v", ran, err)
	}
}

func TestReadyScheduledWorkAndAgedRetry(t *testing.T) {
	c, closeCatalog := fixture(t)
	defer closeCatalog()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	var selected string
	s := New(c, executorFunc(func(ctx context.Context, task Task, report func(domain.JobState) error) error {
		selected = task.ID
		if task.ID == "retry" && task.Attempt == 1 {
			return ErrSourceUnavailable
		}
		return fullExecution(ctx, task, report)
	}))
	s.now = func() time.Time { return now }
	for id, offset := range map[string]time.Duration{"retry": 0, "older": 25 * time.Minute, "newer": 41 * time.Minute} {
		if err := s.Add(ctx, Task{ID: id, DatasetID: "dataset-1", Kind: "backup", Interval: 24 * time.Hour, Enabled: true, NextRun: now.Add(offset)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"retry", "older", "retry", "newer"} {
		if ran, _ := s.RunDue(ctx); !ran || selected != want {
			t.Fatalf("wrong selection: ran=%v got=%s want=%s", ran, selected, want)
		}
		if want == "retry" && now.Hour() == 2 && now.Minute() == 0 {
			now = now.Add(50 * time.Minute)
		}
	}
}

func TestPermanentFailuresAndZeroRetries(t *testing.T) {
	for _, item := range []struct {
		code string
		want string
		stop bool
	}{
		{"source_authentication_failed", "source_authentication_failed", false},
		{"repository_integrity", "repository_integrity", false},
		{"restic_repository_locked", "restic_repository_locked", false},
		{"operation_timeout", "retry_exhausted", true},
	} {
		t.Run(item.code, func(t *testing.T) {
			c, done := fixture(t)
			defer done()
			s := New(c, executorFunc(func(context.Context, Task, func(domain.JobState) error) error {
				return &BackupFailure{item.code, errors.New("fixture error")}
			}))
			addDue(t, s)
			if item.stop {
				if _, err := c.DB.Exec("UPDATE schedule_entries SET retry_count=0"); err != nil {
					t.Fatal(err)
				}
			}
			if ran, err := s.RunDue(context.Background()); !ran || err == nil {
				t.Fatalf("failed attempt not executed: %v %v", ran, err)
			}
			runs, err := s.RecentRuns(context.Background(), 1)
			if err != nil || len(runs) != 1 || runs[0].NextRetry != nil || runs[0].OperationFailure != item.want {
				t.Fatalf("unexpected terminal run: %+v %v", runs, err)
			}
		})
	}
}

func TestCrashDuringRetryPreservesManualOriginAndBudget(t *testing.T) {
	c, done := fixture(t)
	defer done()
	ctx := context.Background()
	s := New(c, executorFunc(fullExecution))
	now := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	planned := now.Add(24 * time.Hour)
	if err := s.Add(ctx, Task{ID: "job", DatasetID: "dataset-1", Kind: "backup", Interval: 24 * time.Hour, Enabled: true, NextRun: planned}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE schedule_entries SET manual_pending=1,manual_queued_unix=?", now.Unix()); err != nil {
		t.Fatal(err)
	}
	// Claim without running simulates the durable boundary after a fatal crash.
	if run, err := s.ClaimDue(ctx); run == nil || err != nil {
		t.Fatalf("could not claim manual job: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if n, err := s.RecoverInterrupted(ctx); n != 1 || err != nil {
			t.Fatalf("restart %d: %d %v", attempt, n, err)
		}
		runs, err := s.RecentRuns(ctx, 1)
		if err != nil || runs[0].State != domain.JobInterrupted || runs[0].Attempt != attempt || runs[0].Trigger != "manual" || !runs[0].PlannedAt.Equal(planned) {
			t.Fatalf("restart lost history: %+v %v", runs, err)
		}
		now = now.Add(time.Duration(attempt) * 30 * time.Minute)
		if attempt == 1 {
			if run, err := s.ClaimDue(ctx); run == nil || err != nil {
				t.Fatalf("could not claim retry: %v", err)
			}
		}
	}
	if ran, err := s.RunDue(ctx); !ran || err != nil {
		t.Fatalf("second retry did not complete: %v %v", ran, err)
	}
	runs, err := s.RecentRuns(ctx, 3)
	if err != nil || len(runs) != 3 || runs[0].Attempt != 3 || runs[0].State != domain.JobCompleted || runs[0].OperationID != runs[2].OperationID {
		t.Fatalf("second retry history: %+v %v", runs, err)
	}
}
