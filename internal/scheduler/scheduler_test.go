package scheduler

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/domain"
)

type executorFunc func(context.Context, Task, func(domain.JobState) error) error

func (f executorFunc) Execute(ctx context.Context, t Task, p func(domain.JobState) error) error {
	return f(ctx, t, p)
}
func fixture(t *testing.T) (*catalog.Catalog, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO sources(id,kind,display_name,endpoint,settings_json,created_at) VALUES('source-1','ssh','demo','localhost','{}','now')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO datasets(id,source_id,display_name,locator) VALUES('dataset-1','source-1','files','/demo')`)
	if err != nil {
		t.Fatal(err)
	}
	return c, func() { c.Close() }
}
func addDue(t *testing.T, s *Scheduler) {
	t.Helper()
	err := s.Add(context.Background(), Task{ID: "schedule-1", DatasetID: "dataset-1", Kind: "backup", Interval: time.Hour, Enabled: true, NextRun: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
}
func fullExecution(_ context.Context, _ Task, report func(domain.JobState) error) error {
	for _, stage := range []domain.JobState{domain.JobPreparing, domain.JobCapturing, domain.JobCommitting, domain.JobVerifying} {
		if err := report(stage); err != nil {
			return err
		}
	}
	return nil
}
func TestRunCompletesWithPersistedPhases(t *testing.T) {
	c, cleanup := fixture(t)
	defer cleanup()
	s := New(c, executorFunc(fullExecution))
	addDue(t, s)
	ctx := context.Background()
	ran, err := s.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("run: %v, %v", ran, err)
	}
	again, err := s.RunDue(ctx)
	if err != nil || again {
		t.Fatalf("duplicate run: %v, %v", again, err)
	}
	runs, err := s.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].State != domain.JobCompleted || runs[0].FinishedAt == nil {
		t.Fatalf("unexpected runs %+v", runs)
	}
	// State remains queryable via a new Scheduler instance over the same DB.
	restarted := New(c, executorFunc(fullExecution))
	runs, err = restarted.RecentRuns(ctx, 1)
	if err != nil || len(runs) != 1 || runs[0].State != domain.JobCompleted {
		t.Fatalf("history lost after restart: %+v %v", runs, err)
	}
}
func TestRunRefusesToClaimSameTaskTwice(t *testing.T) {
	c, cleanup := fixture(t)
	defer cleanup()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	var count atomic.Int32
	exec := executorFunc(func(ctx context.Context, t Task, report func(domain.JobState) error) error {
		count.Add(1)
		close(entered)
		<-release
		return fullExecution(ctx, t, report)
	})
	s := New(c, exec)
	addDue(t, s)
	go func() { _, err := s.RunDue(context.Background()); finished <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("task not started")
	}
	again, err := s.RunDue(context.Background())
	if err != nil || again {
		t.Fatalf("overlap: %v %v", again, err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatalf("launched %d overlapping tasks", count.Load())
	}
}
func TestCrashRecoveryMarksRunInterrupted(t *testing.T) {
	c, cleanup := fixture(t)
	defer cleanup()
	s := New(c, executorFunc(fullExecution))
	addDue(t, s)
	_, err := c.DB.Exec(`UPDATE schedule_entries SET active_run_id='in-flight' WHERE id='schedule-1'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO schedule_runs(id,schedule_id,state,started_at) VALUES('in-flight','schedule-1',?,'2026-01-01T00:00:00Z')`, domain.JobCapturing)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverInterrupted(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("recovered: %d %v", n, err)
	}
	runs, err := s.RecentRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].State != domain.JobInterrupted || runs[0].FinishedAt == nil {
		t.Fatalf("false success after crash: %+v", runs)
	}
	ran, err := s.RunDue(context.Background())
	if err != nil || !ran {
		t.Fatalf("missed catch-up: %v %v", ran, err)
	}
}
func TestUnavailableAndMissingPhasesNeverSucceed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		execute executorFunc
		want    domain.JobState
	}{
		{"unavailable", func(context.Context, Task, func(domain.JobState) error) error { return ErrSourceUnavailable }, domain.JobWaiting},
		{"incomplete", func(context.Context, Task, func(domain.JobState) error) error { return nil }, domain.JobFailed},
		{"error", func(context.Context, Task, func(domain.JobState) error) error { return errors.New("test error") }, domain.JobFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, cleanup := fixture(t)
			defer cleanup()
			s := New(c, tc.execute)
			addDue(t, s)
			ran, err := s.RunDue(context.Background())
			if !ran || err == nil {
				t.Fatalf("silent false success: %v %v", ran, err)
			}
			runs, err := s.RecentRuns(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].State != tc.want {
				t.Fatalf("run state: %+v, want %v", runs, tc.want)
			}
		})
	}
}
func TestInvalidScheduleAndNoExecutor(t *testing.T) {
	c, cleanup := fixture(t)
	defer cleanup()
	s := New(c, nil)
	if err := s.Add(context.Background(), Task{ID: "a", DatasetID: "dataset-1", Kind: "script", Interval: time.Hour, Enabled: true, NextRun: time.Now()}); err == nil {
		t.Fatal("accepted arbitrary shell script schedule")
	}
	if _, err := s.RunDue(context.Background()); err == nil {
		t.Fatal("ran without an executor")
	}
}
