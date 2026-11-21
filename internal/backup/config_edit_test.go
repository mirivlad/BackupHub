package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func requireIntegrationRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic unavailable: integration tests run in Restic CI")
	}
}
func TestEditSourcePreservesIDAndCredentials(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	ctx := context.Background()
	first, err := svc.Create(ctx, CreateSource{Name: "Shared", Kind: "smb", Host: "files.local", Share: "Shared", Username: "admin", Password: "original-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, initialRef, err := svc.source(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if initialRef == "" {
		t.Fatal("initial secret not saved")
	}
	updated, err := svc.UpdateSource(ctx, first.ID, CreateSource{Name: "Renamed share", Kind: "smb", Host: "files2.local", Share: "Changed", Username: "admin", Password: ""})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || updated.Host != "files2.local" {
		t.Fatalf("source identity changed: %+v", updated)
	}
	_, _, unchangedRef, err := svc.source(ctx, first.ID)
	if err != nil || initialRef != unchangedRef {
		t.Fatalf("password changed unexpectedly: %s -> %s", initialRef, unchangedRef)
	}
	secret, err := svc.vault.Get(ctx, unchangedRef)
	if err != nil || string(secret) != "original-secret" {
		t.Fatalf("existing secret unavailable: %v", err)
	}
	_, err = svc.UpdateSource(ctx, first.ID, CreateSource{Name: "Renamed share", Kind: "smb", Host: "files2.local", Share: "Changed", Username: "admin", Password: "rotated-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, rotatedRef, err := svc.source(ctx, first.ID)
	if err != nil || rotatedRef == initialRef {
		t.Fatalf("credential rotation failed: %s -> %s", initialRef, rotatedRef)
	}
	secret, err = svc.vault.Get(ctx, rotatedRef)
	if err != nil || string(secret) != "rotated-secret" {
		t.Fatalf("rotated secret incorrect: %v", err)
	}
	_, err = svc.vault.Get(ctx, initialRef)
	if !errors.Is(err, os.ErrNotExist) && err == nil {
		t.Fatal("old secret remains accessible")
	}
	entries, err := svc.ListSources(ctx)
	if err != nil || len(entries) != 1 || entries[0].Name != "Renamed share" {
		t.Fatalf("bad sources after edit: %+v err=%v", entries, err)
	}
}
func TestEditJobPreservesRunHistoryAndOwnSchedule(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	ctx := context.Background()
	dirA := privateDir(t)
	dirB := privateDir(t)
	srcA, err := svc.Create(ctx, CreateSource{Name: "A", Kind: "local", Path: dirA})
	if err != nil {
		t.Fatal(err)
	}
	srcB, err := svc.Create(ctx, CreateSource{Name: "B", Kind: "local", Path: dirB})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateJob(ctx, CreateBackupJob{Name: "Daily A", SourceID: srcA.ID, RepositoryID: "primary",
		Schedule: ScheduleProfile{Mode: "daily", FirstRun: "2099-01-01T02:00", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.catalog.DB.ExecContext(ctx, "INSERT INTO schedule_runs(id,schedule_id,state,started_at,finished_at) VALUES(?,?,?,?,?)",
		"historic-run", created.ID, "completed", "2026-01-01T00:00:00Z", "2026-01-01T00:05:00Z")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := svc.UpdateJob(ctx, created.ID, CreateBackupJob{Name: "Monthly B", SourceID: srcB.ID, RepositoryID: "primary",
		Schedule: ScheduleProfile{Mode: "cron", CronExpression: "15 8 1 * *", FirstRun: "2099-01-01T00:00", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	if edited.ID != created.ID || edited.SourceID != srcB.ID || edited.Schedule.Mode != "cron" {
		t.Fatalf("update not persisted %+v", edited)
	}
	jobs, err := svc.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Schedule.CronExpression != "15 8 1 * *" {
		t.Fatalf("wrong schedule %+v err=%v", jobs, err)
	}
	var count int
	if err = svc.catalog.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schedule_runs WHERE schedule_id=?", created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost run history: %d %v", count, err)
	}
	if err = svc.catalog.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schedule_profiles").Scan(&count); err != nil || count != 1 {
		t.Fatalf("profile leaked: %d %v", count, err)
	}
}
func TestEditSourceAccessStatus(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	dir := privateDir(t)
	src, err := svc.Create(context.Background(), CreateSource{Name: "test folder", Kind: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := svc.ListSources(context.Background())
	if err != nil || len(sources) != 1 || sources[0].AccessStatus != "ok" {
		t.Fatalf("accessible source marked bad %+v %v", sources, err)
	}
	if err = os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if os.Geteuid() != 0 {
		sources, err = svc.ListSources(context.Background())
		if err != nil || len(sources) != 1 || sources[0].AccessStatus != "denied" {
			t.Fatalf("cannot detect unreadable source %+v %v", sources, err)
		}
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	missing := dir + "/not-found"
	_, err = svc.UpdateSource(context.Background(), src.ID, CreateSource{Name: "moved", Kind: "local", Path: missing})
	if err != nil {
		t.Fatal(err)
	} // Allow saving a source before fixing its mount.
	sources, err = svc.ListSources(context.Background())
	if err != nil || sources[0].AccessStatus != "missing" {
		t.Fatalf("missing path not reported %+v %v", sources, err)
	}
}
func TestPreviewCronSchedule(t *testing.T) {
	p := ScheduleProfile{Mode: "cron", CronExpression: "0 12 * * 1-5", Timezone: "UTC", FirstRun: "2099-01-01T00:00"}
	next, err := PreviewSchedule(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 5 {
		t.Fatalf("unexpected times %v", next)
	}
	for i := 1; i < 5; i++ {
		if !next[i].After(next[i-1]) {
			t.Fatalf("nonmonotonic %v", next)
		}
	}
	// Editing the cron definition must not quietly change the name or password.
}
