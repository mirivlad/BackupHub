package scheduler

import (
	"context"
	"github.com/mirivlad/BackupHub/internal/domain"
	"testing"
	"time"
)

func TestManualQueueRunsWithoutChangingFutureSchedule(t *testing.T) {
	c, done := fixture(t)
	defer done()
	s := New(c, executorFunc(fullExecution))
	planned := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	if err := s.Add(context.Background(), Task{ID: "manual-test", DatasetID: "dataset-1", Kind: "backup", Interval: time.Hour, Enabled: true, NextRun: planned}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE schedule_entries SET manual_pending=1 WHERE id='manual-test'"); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := c.DB.QueryRow("SELECT next_run_unix FROM schedule_entries WHERE id='manual-test'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != planned.Unix() {
		t.Fatalf("manual queue changed next planned run %d", before)
	}
	ran, err := s.RunDue(context.Background())
	if err != nil || !ran {
		t.Fatalf("manual run: ran=%v err=%v", ran, err)
	}
	var after int64
	var pending int
	if err := c.DB.QueryRow("SELECT next_run_unix,manual_pending FROM schedule_entries WHERE id='manual-test'").Scan(&after, &pending); err != nil {
		t.Fatal(err)
	}
	if after != before || pending != 0 {
		t.Fatalf("manually triggered run shifted schedule or stayed queued: %d -> %d pending=%d", before, after, pending)
	}
	again, err := s.RunDue(context.Background())
	if err != nil || again {
		t.Fatalf("duplicate manual job ran=%v err=%v", again, err)
	}
	runs, err := s.RecentRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt == nil {
		t.Fatalf("unexpected runs %+v err=%v", runs, err)
	}
}

func TestManualQueueBlocksOverlappingRun(t *testing.T) {
	c, done := fixture(t)
	defer done()
	entered := make(chan struct{})
	release := make(chan struct{})
	s := New(c, executorFunc(func(ctx context.Context, t Task, report func(domain.JobState) error) error {
		close(entered)
		<-release
		return fullExecution(ctx, t, report)
	}))
	planned := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	if err := s.Add(context.Background(), Task{ID: "manually-queued", DatasetID: "dataset-1", Kind: "backup", Interval: time.Hour, Enabled: true, NextRun: planned}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec("UPDATE schedule_entries SET manual_pending=1 WHERE id='manually-queued'"); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := s.RunDue(context.Background()); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("manual run did not start")
	}
	var active string
	var pending int
	if err := c.DB.QueryRow("SELECT active_run_id,manual_pending FROM schedule_entries WHERE id='manually-queued'").Scan(&active, &pending); err != nil {
		t.Fatal(err)
	}
	if active == "" || pending != 0 {
		t.Fatalf("claimed run invalid: active=%q pending=%d", active, pending)
	}
	ran, err := s.RunDue(context.Background())
	if err != nil || ran {
		t.Fatalf("overlapping run allowed: ran=%v err=%v", ran, err)
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
