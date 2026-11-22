package scheduler

import (
	"context"
	"testing"
	"time"
)

type admissionExecutor struct {
	executorFunc
	reserved map[string]bool
}

func (e admissionExecutor) TryReserve(task Task) (func(), bool) {
	if e.reserved[task.RepositoryID] {
		return nil, false
	}
	e.reserved[task.RepositoryID] = true
	return func() { delete(e.reserved, task.RepositoryID) }, true
}

func TestBusyRepositoryPreservesManualAndDueOccurrences(t *testing.T) {
	c, cleanup := fixture(t)
	defer cleanup()
	ctx := context.Background()
	gate := admissionExecutor{executorFunc: fullExecution, reserved: map[string]bool{"primary": true}}
	s := New(c, gate)
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Add(ctx, Task{ID: id, DatasetID: "dataset-1", Kind: "backup", Interval: time.Hour, Enabled: true, NextRun: due}); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"INSERT INTO storage_repositories(id,display_name,path,created_at) VALUES('other','other','/other','now')",
		"INSERT INTO schedule_repositories VALUES('a','primary'),('b','primary'),('c','other')",
		"UPDATE schedule_entries SET manual_pending=1 WHERE id='a'",
	} {
		if _, err := c.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if ran, err := s.RunDue(ctx); !ran || err != nil {
		t.Fatalf("independent backup blocked: %v %v", ran, err)
	}
	for i := 0; i < 5; i++ {
		if ran, err := s.RunDue(ctx); ran || err != nil {
			t.Fatalf("occupied repository generated a run: %v %v", ran, err)
		}
	}
	for _, id := range []string{"a", "b"} {
		var next int64
		var manual int
		if err := c.DB.QueryRow("SELECT next_run_unix,manual_pending FROM schedule_entries WHERE id=?", id).Scan(&next, &manual); err != nil ||
			next != due.Unix() || (manual == 1) != (id == "a") {
			t.Fatalf("lost occurrence for %s: %v %v %v", id, next, manual, err)
		}
	}
	delete(gate.reserved, "primary")
	for i := 0; i < 2; i++ {
		if ran, err := s.RunDue(ctx); !ran || err != nil {
			t.Fatalf("deferred backup did not run: %v %v", ran, err)
		}
	}
	if runs, err := s.RecentRuns(ctx, 10); err != nil || len(runs) != 3 {
		t.Fatalf("unexpected history: %v %v", runs, err)
	}
}
