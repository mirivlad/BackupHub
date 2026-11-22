package backup

import (
	"context"
	"testing"
	"time"
)

func TestWorkloadUsesScheduleAndMergesActualOverlaps(t *testing.T) {
	requireIntegrationRestic(t)
	ctx := context.Background()
	s := serviceFixture(t)
	source, err := s.Create(ctx, CreateSource{Name: "Workload test", Kind: "local", Path: privateDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2100, time.January, 10, 8, 0, 0, 0, time.UTC)
	createJob := func(name string, hour, minute int) BackupJob {
		t.Helper()
		start := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		job, err := s.CreateJob(ctx, CreateBackupJob{Name: name, SourceID: source.ID, RepositoryID: PrimaryStorageID,
			Schedule: ScheduleProfile{Mode: "daily", FirstRun: start.Format("2006-01-02T15:04"), Timezone: "UTC"}})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	long := createJob("Long job", 9, 30)
	short := createJob("Short job", 10, 0)
	historical := now.AddDate(0, 0, -7).Add(90 * time.Minute)
	insert := func(id string, job BackupJob, state string, start time.Time, duration time.Duration) {
		t.Helper()
		_, err := s.catalog.DB.ExecContext(ctx, "INSERT INTO schedule_runs(id,schedule_id,state,started_at,finished_at) VALUES(?,?,?,?,?)",
			id, job.ID, state, start.Format(time.RFC3339Nano), start.Add(duration).Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("long", long, "completed", historical, 75*time.Minute)
	insert("short", short, "completed", historical.Add(time.Hour), 15*time.Minute)
	insert("failure", short, "failed", historical.Add(2*time.Hour), 10*time.Minute)

	got, err := s.workloadAt(ctx, "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	slot := got.Days[0].Hours[10]
	if got.PlannedRuns != 14 || got.OverlapHours != 7 || !slot.Overlap || slot.Planned != 2 {
		t.Fatalf("expected two overlapping jobs every day: %#v", slot)
	}
	items := map[string]string{}
	for _, item := range slot.Items {
		items[item.JobID] = item.JobName
	}
	if items[long.ID] != long.Name || items[short.ID] != short.Name {
		t.Fatalf("missing stable edit targets: %#v", slot.Items)
	}
	if slot.BusySeconds != 45*60 || got.Days[0].Hours[11].BusySeconds != 10*60 || got.BusySeconds != 85*60 {
		t.Fatalf("actual occupancy must merge concurrent runs and count time spent on failures, got %d seconds", got.BusySeconds)
	}
	window := got.FreeWindow
	if window == nil || !window.Start.Equal(now.Add(165*time.Minute)) || !window.End.Equal(now.Add(25*time.Hour+30*time.Minute)) {
		t.Fatalf("unexpected longest available window: %v", window)
	}
	createJob("Unmeasured job", 11, 0)
	got, err = s.workloadAt(ctx, "UTC", now)
	if err != nil || got.UnmeasuredJobs != 1 || !got.Days[0].Hours[11].Unknown || got.FreeWindow != nil {
		t.Fatalf("unmeasured job should prevent an unqualified free window: count=%d, window=%v, err=%v", got.UnmeasuredJobs, got.FreeWindow, err)
	}
}

func TestWorkloadSplitsRepeatedHour(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.November, 1, 4, 0, 0, 0, time.UTC).In(zone)
	until := from.Add(4 * time.Hour)
	_, observed, busy := summarizeRuns([]workloadRun{
		{timeSpan: timeSpan{from.Add(30 * time.Minute), from.Add(150 * time.Minute)}, job: "job", completed: true},
	}, from, until)
	if observed[time.Sunday][1] != 7200 || busy[time.Sunday][1] != 5400 {
		t.Fatalf("repeated hour: observed=%d busy=%d", observed[time.Sunday][1], busy[time.Sunday][1])
	}
}
