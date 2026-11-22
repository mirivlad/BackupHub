package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
)

func waitVerification(t *testing.T, s *Service, id string, isState func(string) bool) VerificationRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for {
		run, err := scanVerification(s.catalog.DB.QueryRowContext(ctx,
			"SELECT "+verificationColumns+" FROM verification_runs WHERE id=?", id))
		if err != nil {
			t.Fatal(err)
		}
		if isState(run.State) {
			return run
		}
		select {
		case <-ctx.Done():
			t.Fatalf("verification did not reach the expected state: %+v", run)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func terminalVerification(state string) bool {
	return state == "passed" || state == "failed" || state == "interrupted"
}

func backupVerificationFixture(t *testing.T, s *Service) (Snapshot, string, []byte) {
	t.Helper()
	ctx := context.Background()
	sourceDir := privateDir(t)
	file := filepath.Join(sourceDir, "report.bin")
	payload := make([]byte, 256<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := s.Create(ctx, CreateSource{Name: "Verification source", Kind: "local", Path: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, source, PrimaryStorageID)
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); err != nil || !ran {
		t.Fatalf("backup ran=%v, error=%v", ran, err)
	}
	snaps, err := s.ListSnapshots(ctx, source.ID)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("backup was not published: %v %v", snaps, err)
	}
	return snaps[0], file, payload
}

func TestVerificationChecksAndRestoreWithRealRestic(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	snap, file, payload := backupVerificationFixture(t, s)
	if snap.Verification != "data_sha256_verified" || snap.RestoreCheck.State != "unknown" {
		t.Fatalf("unexpected fresh snapshot: %+v", snap)
	}
	// Trust the immutable snapshot, even after its live source changes.
	if err := os.WriteFile(file, []byte("new live contents"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := s.QueueRestoreCheck(ctx, snap.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueRepositoryCheck(ctx, PrimaryStorageID, "metadata"); !errors.Is(err, ErrVerificationBusy) {
		t.Fatalf("duplicate repository work was accepted: %v", err)
	}
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.WaitStopped() }()
	done := waitVerification(t, s, run.ID, terminalVerification)
	if done.State != "passed" || done.Files == nil || *done.Files != 1 || done.Bytes == nil ||
		*done.Bytes != int64(len(payload)) || done.DurationMS == nil || done.SnapshotID != snap.ID {
		t.Fatalf("unexpected restore test: %+v", done)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "new live contents" {
		t.Fatalf("restore check changed source: %q %v", got, err)
	}
	if entries, err := os.ReadDir(s.restoreCheckRoot()); err != nil || len(entries) != 0 {
		t.Fatalf("restore workspace was not cleared: %v %v", entries, err)
	}
	for _, mode := range []string{"metadata", "data"} {
		run, err := s.QueueRepositoryCheck(ctx, PrimaryStorageID, mode)
		if err != nil {
			t.Fatal(err)
		}
		if done := waitVerification(t, s, run.ID, terminalVerification); done.State != "passed" {
			t.Fatalf("real repository %s check: %+v", mode, done)
		}
	}
	storages, err := s.ListStorages(ctx)
	if err != nil || len(storages) != 1 || storages[0].MetadataCheck.State != "passed" || storages[0].DataCheck.State != "passed" {
		t.Fatalf("wrong storage integrity states: %+v %v", storages, err)
	}

	chosen, err := s.QueueRestoreCheck(ctx, snap.ID, "report.bin")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitVerification(t, s, chosen.ID, terminalVerification); done.State != "passed" ||
		done.Selection != "report.bin" || done.Bytes == nil || *done.Bytes != int64(len(payload)) {
		t.Fatalf("selected file check failed: %+v", done)
	}
	if _, err := s.QueueRestoreCheck(ctx, snap.ID, "../outside"); !errors.Is(err, ErrRestoreCheckPath) {
		t.Fatalf("unsafe selection accepted: %v", err)
	}

	// Corrupt only a disposable repository's data pack. Its inventory
	// remains available, but neither restore verification nor data check can pass.
	var pack string
	if err := filepath.Walk(s.repo.path+"/data", func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && info.Size() >= int64(len(payload)) {
			pack = path
		}
		return err
	}); err != nil || pack == "" {
		t.Fatalf("missing data-pack fixture: %s %v", pack, err)
	}
	if err := os.Chmod(pack, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(pack, 9); err != nil {
		t.Fatal(err)
	}
	failed, err := s.QueueRestoreCheck(ctx, snap.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitVerification(t, s, failed.ID, terminalVerification); done.State != "failed" || done.Detail == "" {
		t.Fatalf("corrupt pack accepted by restore check: %+v", done)
	}
	check, err := s.QueueRepositoryCheck(ctx, PrimaryStorageID, "data")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitVerification(t, s, check.ID, terminalVerification); done.State != "failed" || done.Detail == "" {
		t.Fatalf("corrupt pack accepted by repository check: %+v", done)
	}
	history, err := s.RestoreChecks(ctx, snap.ID)
	if err != nil || len(history) != 3 || history[0].State != "failed" || history[1].State != "passed" || history[2].State != "passed" {
		t.Fatalf("restore history lost evidence: %+v %v", history, err)
	}
	snapAfter, err := s.Snapshot(ctx, snap.ID)
	if err != nil || snapAfter.RestoreCheck.State != "failed" || snapAfter.Verification != snap.Verification {
		t.Fatalf("wrong snapshot states after failure: %+v %v", snapAfter, err)
	}
	if entries, err := os.ReadDir(s.restoreCheckRoot()); err != nil || len(entries) != 0 {
		t.Fatalf("failed restore left temporary files: %v %v", entries, err)
	}
}

func TestVerificationScheduleAndIndependentUnavailableStorage(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second, err := s.RegisterStorage(ctx, "Second disk", filepath.Join(privateDir(t), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetCheckSchedule(ctx, second.ID, "metadata", 24); err != nil {
		t.Fatal(err)
	}
	if _, err = s.catalog.DB.ExecContext(ctx, "UPDATE repository_check_schedules SET next_run=1 WHERE repository_id=?", second.ID); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(s.repo.path, s.repo.path+".detached"); err != nil {
		t.Fatal(err)
	}
	// Simulate opening an established catalog when its primary volume is already absent.
	s, err = New(ctx, s.catalog, s.vault, "", "")
	if err != nil {
		t.Fatal(err)
	}
	primary, err := s.QueueRepositoryCheck(ctx, PrimaryStorageID, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.WaitStopped() }()
	if done := waitVerification(t, s, primary.ID, terminalVerification); done.State != "failed" || !strings.Contains(done.Detail, "недоступно") {
		t.Fatalf("wrong unavailable volume result: %+v", done)
	}
	secondRuns, err := s.StorageChecks(ctx, second.ID)
	if err != nil || len(secondRuns.Runs) != 1 || secondRuns.Schedule.NextRun == nil || !secondRuns.Schedule.NextRun.After(time.Now()) {
		t.Fatalf("independent schedule was not advanced: %+v %v", secondRuns, err)
	}
	if done := waitVerification(t, s, secondRuns.Runs[0].ID, terminalVerification); done.State != "passed" {
		t.Fatalf("independent available repository was blocked: %+v", done)
	}
	if _, err = os.Stat(s.repo.path); !os.IsNotExist(err) {
		t.Fatalf("offline primary repository was recreated: %v", err)
	}
}

func TestVerificationRestartAndV12Migration(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	snap, _, _ := backupVerificationFixture(t, s)
	state := s.catalog.DataDir
	instance := s.catalog.InstanceID
	keyBefore, err := os.ReadFile(s.repo.passwordFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"DROP TABLE verification_runs", "DROP TABLE repository_check_schedules", "PRAGMA user_version=12",
	} {
		if _, err := s.catalog.DB.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.catalog.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := catalog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err = New(ctx, migrated, s.vault, "", "")
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Snapshot(ctx, snap.ID)
	keyAfter, keyErr := os.ReadFile(s.repo.passwordFile)
	if err != nil || keyErr != nil || string(keyBefore) != string(keyAfter) || migrated.InstanceID != instance ||
		after.ResticID != snap.ResticID || after.Verification != snap.Verification || after.RestoreCheck.State != "unknown" {
		t.Fatalf("legacy snapshot or key changed during migration: %+v %v %v", after, err, keyErr)
	}
	run, err := s.QueueRestoreCheck(ctx, after.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = migrated.DB.Exec("UPDATE verification_runs SET state='running',started_at=? WHERE id=?", time.Now().UnixMilli(), run.ID); err != nil {
		t.Fatal(err)
	}
	tempDir := filepath.Join(s.restoreCheckRoot(), run.ID)
	if err = os.MkdirAll(tempDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(tempDir, "partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.WaitStopped() }()
	if done := waitVerification(t, s, run.ID, terminalVerification); done.State != "interrupted" || done.Detail == "" {
		t.Fatalf("incomplete check was not recovered: %+v", done)
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("temporary files left after restart: %v", err)
	}
	fresh, err := s.QueueRestoreCheck(ctx, after.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if done := waitVerification(t, s, fresh.ID, terminalVerification); done.State != "passed" {
		t.Fatalf("existing snapshot cannot be tested after migration: %+v", done)
	}
}

func TestRestoreCheckDetectsInsufficientSpaceAndHashMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := requireRestoreSpace(dir, 1<<60); err == nil || !strings.Contains(err.Error(), "недостаточно") {
		t.Fatalf("insufficient space was not reported: %v", err)
	}
	file := filepath.Join(dir, "file")
	want := sha256.Sum256([]byte("one"))
	if err := os.WriteFile(file, []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoredFile(context.Background(), file, 3, want[:]); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("same-size corruption accepted: %v", err)
	}
}

func TestVerificationServiceCancellationAndRepositoryLease(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	realRestic := s.repo.binary
	bin := privateDir(t)
	// Deliberately wait in the CLI so the concurrent daemon and cancellation
	// tests do not depend on the speed of a real repository check.
	script := `#!/bin/sh
if [ "$6" = check ]; then
    trap 'exit 130' INT
    printf '[0:06] 40.00%% 2 / 5 packs\n'
    while :; do sleep 0.05; done
fi
exec "$BACKUPHUB_FIXTURE_RESTIC" "$@"
`
	// The system under test intentionally supplies a minimal command environment.
	wrapper := strings.ReplaceAll(script, `"$BACKUPHUB_FIXTURE_RESTIC"`, `"`+realRestic+`"`)
	if err := os.WriteFile(filepath.Join(bin, "restic"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := s.QueueRepositoryCheck(ctx, PrimaryStorageID, "data")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.WaitStopped()
	defer cancel()
	waitVerification(t, s, run.ID, func(state string) bool { return state == "running" })

	another, err := New(ctx, s.catalog, s.vault, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = another.Start(ctx); err == nil {
		t.Fatal("a second service acquired the same catalog")
	}
	if release, ok := s.tryRepositoryWork(PrimaryStorageID); ok {
		release()
		t.Fatal("backup could overlap with maintenance")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		running, err := scanVerification(s.catalog.DB.QueryRow("SELECT "+verificationColumns+" FROM verification_runs WHERE id=?", run.ID))
		if err != nil {
			t.Fatal(err)
		}
		if running.Progress != nil && *running.Progress == 0.4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Restic progress was not recorded: %+v", running)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	s.WaitStopped()
	if done := waitVerification(t, s, run.ID, terminalVerification); done.State != "interrupted" || done.DurationMS == nil {
		t.Fatalf("canceled operation did not become interrupted: %+v", done)
	}
}

func TestRepositoryProgressWithRealRestic(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	snap, _, _ := backupVerificationFixture(t, s)
	var reports int
	var last float64
	if err := s.repo.checkWithProgress(context.Background(), true, func(fraction float64) {
		reports++
		last = fraction
	}); err != nil {
		t.Fatal(err)
	}
	if reports == 0 || last != 1 {
		t.Fatalf("no final report from real Restic pack verification: reports=%d last=%f", reports, last)
	}
	t.Logf("Restic reported %d progress lines for the repository containing snapshot %s, final %.0f%%", reports, snap.ID, 100*last)
}

func TestRecoveredApplicationSnapshotCannotOfferFileCheck(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	snap, _, _ := backupVerificationFixture(t, s)
	ctx := context.Background()
	if _, err := s.catalog.DB.Exec("UPDATE sources SET kind=? WHERE id=?", recoveredSourceKind, snap.SourceID); err != nil {
		t.Fatal(err)
	}
	file, err := s.Snapshot(ctx, snap.ID)
	if err != nil || !file.CanTestRestore {
		t.Fatalf("a recovered file snapshot should be eligible: %+v %v", file, err)
	}
	if _, err := s.catalog.DB.Exec("UPDATE file_snapshots SET consistency='application_consistent',fidelity='logical_dump' WHERE snapshot_id=?", snap.ID); err != nil {
		t.Fatal(err)
	}
	app, err := s.Snapshot(ctx, snap.ID)
	if err != nil || app.CanTestRestore {
		t.Fatalf("recovered application snapshot incorrectly eligible: %+v %v", app, err)
	}
	if _, err = s.QueueRestoreCheck(ctx, snap.ID, "report.bin"); !errors.Is(err, ErrRestoreCheckUnsupported) {
		t.Fatalf("application snapshot accepted a file-check request: %v", err)
	}
}
