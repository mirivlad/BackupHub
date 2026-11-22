package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitForResticLock(t *testing.T, repo *repository) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(filepath.Join(repo.path, "locks"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Restic did not acquire a test lock")
}

func TestScheduledBackupPreservesActiveAndStaleResticLocks(t *testing.T) {
	requireIntegrationRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	service := serviceFixture(t)
	dir := privateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("lock recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := service.Create(ctx, CreateSource{Name: "Lock fixture", Kind: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, service, ctx, source, PrimaryStorageID)
	holder := service.repo.command(ctx, nil, "check", "--read-data")
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if holder.ProcessState == nil {
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
	}()
	waitForResticLock(t, service.repo)
	if err = holder.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); !ran || !errors.Is(err, errResticRepositoryLocked) {
		t.Fatalf("a live lock must survive an attempted backup: ran=%v err=%v", ran, err)
	}
	runs, err := service.RecentRuns(ctx)
	if err != nil || len(runs) != 1 || runs[0].FailureCode != "restic_repository_locked" || runs[0].SnapshotID != "" {
		t.Fatalf("unexpected journal result for a live lock: %#v %v", runs, err)
	}

	check, err := service.QueueRepositoryCheck(ctx, PrimaryStorageID, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	service.executeVerification(ctx, check)
	if done := waitVerification(t, service, check.ID, terminalVerification); done.State != "failed" ||
		!strings.Contains(done.Detail, "заблокирован") {
		t.Fatalf("maintenance did not report the live lock: %+v", done)
	}
	if err = holder.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("live Restic lock holder was touched: %v", err)
	}
	waitForResticLock(t, service.repo)

	// An uncatchable termination leaves the real Restic lock on disk.
	if err = holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	waitForResticLock(t, service.repo)
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); !ran || !errors.Is(err, errResticRepositoryLocked) {
		t.Fatalf("an abandoned lock requires operator review: ran=%v err=%v", ran, err)
	}
	waitForResticLock(t, service.repo)
	// The fixture owns the only lock and has reaped its holder.
	if _, err := service.repo.run(ctx, nil, "unlock"); err != nil {
		t.Fatal(err)
	}
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); !ran || err != nil {
		t.Fatalf("the next backup must work after the lock is cleared: ran=%v err=%v", ran, err)
	}
	snapshots, err := service.ListSnapshots(ctx, source.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("expected one verified snapshot, got %#v %v", snapshots, err)
	}
}

func TestResticCancellationReleasesLock(t *testing.T) {
	requireIntegrationRestic(t)
	service := serviceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	check := service.repo.command(ctx, nil, "check", "--read-data")
	if err := check.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if check.ProcessState == nil {
			_ = check.Process.Kill()
			_ = check.Wait()
		}
	}()
	waitForResticLock(t, service.repo)
	cancel()
	if err := check.Wait(); err == nil {
		t.Fatal("expected Restic to be interrupted")
	}
	entries, err := os.ReadDir(filepath.Join(service.repo.path, "locks"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("Restic should release the lock on cancellation: %v %v", entries, err)
	}
}
