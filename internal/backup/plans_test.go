package backup

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestIndependentSourceScheduleAndJob(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic missing")
	}
	svc := serviceFixture(t)
	ctx := context.Background()
	dir := privateDir(t)
	if err := os.WriteFile(dir+"/file.txt", []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := svc.CreateUnscheduled(ctx, CreateSource{Name: "Documents", Kind: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := svc.ListSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].ID != src.ID {
		t.Fatalf("source missing: %v err=%v", sources, err)
	}
	jobs, err := svc.ListJobs(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("source created implicit job: %v err=%v", jobs, err)
	}
	profile, err := svc.CreateSchedule(ctx, ScheduleProfile{Name: "Daily", Mode: "daily", Hour: 2, Minute: 0, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.CreateJob(ctx, CreateBackupJob{Name: "Documents daily", SourceID: src.ID, ScheduleID: profile.ID, RepositoryID: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err = svc.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].SourceID != src.ID || jobs[0].ScheduleID != profile.ID {
		t.Fatalf("bad job: %v err=%v", jobs, err)
	}
	if err := svc.DeleteSchedule(ctx, profile.ID); err == nil {
		t.Fatal("deleted schedule in use")
	}
	if err := svc.Pause(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	jobs, err = svc.ListJobs(ctx)
	if err != nil || jobs[0].Enabled {
		t.Fatalf("pause failed: %v", jobs)
	}
}
func TestScheduleValidation(t *testing.T) {
	cases := []ScheduleProfile{
		{Name: "Bad", Mode: "weekly", DaysOfWeek: "", Timezone: "UTC"},
		{Name: "Bad", Mode: "weekly", DaysOfWeek: "1,1", Timezone: "UTC"},
		{Name: "Bad", Mode: "daily", Hour: 25, Timezone: "UTC"},
		{Name: "Bad", Mode: "daily", Timezone: "No/SuchTimeZone"},
		{Name: "Bad", Mode: "interval", IntervalMinutes: 1, Timezone: "UTC"},
	}
	for _, c := range cases {
		if err := checkProfile(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
