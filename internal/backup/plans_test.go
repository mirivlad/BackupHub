package backup

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestJobWithOwnedSchedule(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic missing")
	}
	svc := serviceFixture(t)
	ctx := context.Background()
	dir := privateDir(t)
	if err := os.WriteFile(dir+"/file.txt", []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := svc.Create(ctx, CreateSource{Name: "Documents", Kind: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := svc.ListJobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("source implicitly created job: %v err=%v", jobs, err)
	}
	schedule := ScheduleProfile{Mode: "interval", IntervalValue: 7, IntervalUnit: "days", FirstRun: "2026-11-30T02:00", Timezone: "Europe/Berlin"}
	job, err := svc.CreateJob(ctx, CreateBackupJob{Name: "Every 7 days", SourceID: src.ID, RepositoryID: "primary", Schedule: schedule})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = svc.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].SourceID != src.ID || jobs[0].Schedule.IntervalValue != 7 || jobs[0].Schedule.FirstRun != schedule.FirstRun {
		t.Fatalf("bad job: %+v err=%v", jobs, err)
	}
	next, _ := time.Parse(time.RFC3339, job.NextRun)
	if next.In(time.FixedZone("CET", 3600)).Format("2006-01-02T15:04") != "2026-11-30T02:00" {
		t.Fatalf("incorrect first run %s", job.NextRun)
	}
	var count int
	if err = svc.catalog.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schedule_profiles WHERE id=?", job.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("no private schedule: %v %d", err, count)
	}
	if err := svc.Pause(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	jobs, err = svc.ListJobs(ctx)
	if err != nil || jobs[0].Enabled {
		t.Fatalf("pause failed: %v", jobs)
	}
}
func TestSaveAndRunQueuesImmediately(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic missing")
	}
	svc := serviceFixture(t)
	ctx := context.Background()
	src, err := svc.Create(ctx, CreateSource{Name: "Immediately", Kind: "local", Path: privateDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.CreateJob(ctx, CreateBackupJob{Name: "Launch", SourceID: src.ID, RepositoryID: "primary",
		Schedule: ScheduleProfile{Mode: "daily", FirstRun: "2099-01-01T02:00", Timezone: "UTC"}, RunNow: true})
	if err != nil {
		t.Fatal(err)
	}
	if !job.Queued {
		t.Fatal("run_now did not queue job")
	}
	next, err := time.Parse(time.RFC3339, job.NextRun)
	if err != nil || next.After(time.Now().Add(time.Minute)) {
		t.Fatalf("manual start was not immediate: %s", job.NextRun)
	}
}
func TestScheduleValidation(t *testing.T) {
	cases := []ScheduleProfile{
		{Mode: "weekly", DaysOfWeek: "", FirstRun: "2026-11-22T02:00", Timezone: "UTC"},
		{Mode: "weekly", DaysOfWeek: "1,1", FirstRun: "2026-11-22T02:00", Timezone: "UTC"},
		{Mode: "daily", FirstRun: "2026-11-22T02:00", Timezone: "No/SuchTimeZone"},
		{Mode: "interval", IntervalValue: 1, IntervalUnit: "minutes", FirstRun: "2026-11-22T02:00", Timezone: "UTC"},
		{Mode: "interval", IntervalValue: 7, IntervalUnit: "days", FirstRun: "2026-03-29T02:30", Timezone: "Europe/Berlin"},
	}
	for _, p := range cases {
		if _, _, _, err := validateSchedule(p); err == nil {
			t.Errorf("accepted invalid schedule: %+v", p)
		}
	}
}
