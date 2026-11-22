package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"github.com/mirivlad/BackupHub/internal/vault"
)

func TestResilienceServiceChild(t *testing.T) {
	state := os.Getenv("BACKUPHUB_TEST_CHILD_STATE")
	if state == "" {
		t.Skip("used only by the isolated process-fault fixture")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	c, err := catalog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	v, err := vault.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, c, v, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s.WaitStopped()
}

func TestResticProcessFaultsAndPublicationReconciliation(t *testing.T) {
	requireIntegrationRestic(t)
	for _, fault := range []struct {
		name        string
		args        string
		signal      syscall.Signal
		written     bool
		killCommand bool
	}{
		{name: "term_before_tree_write", args: `*" backup --json "*`, signal: syscall.SIGTERM},
		{name: "kill_after_tree_write", args: `*" backup --json "*`, signal: syscall.SIGKILL, written: true},
		{name: "kill_during_verification", args: `*" ls --json "*`, signal: syscall.SIGKILL},
		{name: "kill_after_manifest", args: `*" backup --stdin "*`, signal: syscall.SIGKILL, written: true},
		{name: "restic_exit_after_manifest", args: `*" backup --stdin "*`, signal: syscall.SIGTERM, written: true, killCommand: true},
	} {
		t.Run(fault.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
			defer cancel()
			s := serviceFixture(t)
			root := privateDir(t)
			file := filepath.Join(root, "proof.txt")
			if err := os.WriteFile(file, []byte("old content"), 0600); err != nil {
				t.Fatal(err)
			}
			source, err := s.Create(ctx, CreateSource{Name: "process fixture", Kind: "local", Path: root})
			if err != nil {
				t.Fatal(err)
			}
			job := createTestBackupJob(t, s, ctx, source, PrimaryStorageID)
			runBackup(t, s, job.ID)
			before, err := s.ListSnapshots(ctx, source.ID)
			if err != nil || len(before) != 1 {
				t.Fatalf("baseline: %v %v", before, err)
			}
			if err = os.WriteFile(file, []byte("new content"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = s.Manual(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			barrier := filepath.Join(privateDir(t), "entered")
			bin := privateDir(t)
			restic, err := exec.LookPath("restic")
			if err != nil {
				t.Fatal(err)
			}
			execute := ""
			if fault.written {
				execute = strconv.Quote(restic) + " \"$@\" || exit $?\n"
			}
			exitCommand := "exec /bin/sleep 180"
			if fault.killCommand {
				exitCommand = "kill -KILL $$"
			}
			wrapper := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in\n%s)\n%s : > %s\n%s;;\nesac\nexec %s \"$@\"\n",
				fault.args, execute, strconv.Quote(barrier), exitCommand, strconv.Quote(restic))
			if err = os.WriteFile(filepath.Join(bin, "restic"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestResilienceServiceChild$")
			child.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "BACKUPHUB_TEST_CHILD_STATE="+s.catalog.DataDir)
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- child.Wait() }()
			defer child.Process.Kill()
			waitUntil(t, ctx, func() bool {
				if _, err := os.Stat(barrier); err == nil {
					return true
				}
				select {
				case err := <-wait:
					t.Fatalf("child exited before fault: %v", err)
				default:
				}
				return false
			})
			if fault.killCommand {
				waitUntil(t, ctx, func() bool {
					var n int
					return s.catalog.DB.QueryRow("SELECT COUNT(*) FROM backup_operations WHERE state='completed'").Scan(&n) == nil && n == 2
				})
			}
			if err = child.Process.Signal(fault.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-wait:
				if (fault.signal == syscall.SIGTERM) != (err == nil) {
					t.Fatalf("unexpected child exit: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("child did not stop: %v", ctx.Err())
			}
			waitUntil(t, ctx, func() bool {
				lease, err := s.acquireServiceLease()
				if err != nil {
					return false
				}
				_ = lease.Close()
				return true
			})
			isManifest := fault.name == "kill_after_manifest" || fault.killCommand
			if !fault.killCommand {
				snaps, err := s.ListSnapshots(ctx, source.ID)
				if err != nil || len(snaps) != 1 {
					t.Fatalf("incomplete attempt visible: %+v %v", snaps, err)
				}
				if _, err = s.scheduler.RecoverInterrupted(ctx); err != nil {
					t.Fatal(err)
				}
				jobs, err := s.ListJobs(ctx)
				if err != nil || len(jobs) != 1 || jobs[0].OperationID == "" {
					t.Fatalf("logical operation lost: %+v %v", jobs, err)
				}
				if !isManifest {
					if err = s.ChangeRetry(ctx, job.ID, jobs[0].OperationID, false); err != nil {
						t.Fatal(err)
					}
				}
				if ran, err := s.scheduler.RunDue(ctx); !ran || err != nil {
					t.Fatalf("restart recovery ran=%v err=%v", ran, err)
				}
			}
			snaps, err := s.ListSnapshots(ctx, source.ID)
			if err != nil || len(snaps) != 2 {
				t.Fatalf("wanted exactly two committed snapshots: %+v %v", snaps, err)
			}
			runs, err := s.RecentRuns(ctx)
			if err != nil || runs[0].OperationState != "completed" || runs[0].SnapshotID == "" {
				t.Fatalf("recovered operation not linked: %+v %v", runs, err)
			}
			if isManifest {
				wantState, wantCode := "interrupted", "process_restart"
				if fault.killCommand {
					wantState, wantCode = "failed", "restic_interrupted"
				}
				if len(runs) != 2 || string(runs[0].State) != wantState || runs[0].FailureCode != wantCode || runs[0].Attempt != 1 {
					t.Fatalf("manifest recovery started another attempt: %+v", runs)
				}
				raw, err := s.repo.run(ctx, nil, "snapshots", "--json", "--tag", "bh-source-"+source.ID)
				if err != nil {
					t.Fatal(err)
				}
				var native []resticRecoverySnapshot
				if err := json.Unmarshal(raw, &native); err != nil || len(native) != 2 {
					t.Fatalf("publication replay wrote a duplicate native snapshot: %s %v", raw, err)
				}
			} else if len(runs) != 3 || runs[0].Attempt != 2 || runs[1].State != "interrupted" {
				t.Fatalf("interrupted attempt missing from history: %+v", runs)
			}
			if _, err = s.repo.run(ctx, nil, "check", "--read-data"); err != nil {
				t.Fatal(err)
			}
			verifyNativeRestore(t, s.repo, before[0], "proof.txt", "old content")
			verifyNativeRestore(t, s.repo, snaps[0], "proof.txt", "new content")
		})
	}
}

func runBackup(t *testing.T, s *Service, id string) {
	t.Helper()
	if err := s.Manual(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(context.Background()); err != nil || !ran {
		t.Fatalf("backup ran=%v err=%v", ran, err)
	}
}

func waitUntil(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for !check() {
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for test condition: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func verifyNativeRestore(t *testing.T, repo *repository, snap Snapshot, filename, expected string) {
	t.Helper()
	target := privateDir(t)
	if _, err := repo.run(context.Background(), nil, "restore", snap.ResticID, "--target", target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, snap.Root, filename))
	if err != nil || string(got) != expected {
		t.Fatalf("restored %q: %q %v", filename, got, err)
	}
}

func TestOwnedStagingRecoveryLeavesUnknownPaths(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx := context.Background()
	source, err := s.Create(ctx, CreateSource{Name: "fixture", Kind: "local", Path: privateDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, source, PrimaryStorageID)
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if run, err := s.scheduler.ClaimDue(ctx); run == nil || err != nil {
		t.Fatalf("claim: %v", err)
	}
	var runID string
	if err = s.catalog.DB.QueryRow("SELECT active_run_id FROM schedule_entries WHERE id=?", job.ID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	root, _, err := s.newStaging(ctx, s.stageDir, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "partial"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(s.stageDir, "capture-unknown")
	if err = os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if n, err := s.scheduler.RecoverInterrupted(ctx); err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	if err = s.cleanInterruptedStaging(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(root)); !os.IsNotExist(err) {
		t.Fatalf("owned interrupted staging remains: %v", err)
	}
	if _, err = os.Stat(unknown); err != nil {
		t.Fatalf("unknown staging was touched: %v", err)
	}
	var unused int
	err = s.catalog.DB.QueryRow("SELECT 1 FROM backup_staging WHERE run_id=?", runID).Scan(&unused)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ownership ledger was not cleared: %v", err)
	}
}

func TestAutomaticRetryAfterDestinationReturns(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := privateDir(t)
	if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("storage recovered"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := s.Create(ctx, CreateSource{Name: "fixture", Kind: "local", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, source, PrimaryStorageID)
	closed := s.repo.path + ".offline"
	if err = os.Rename(s.repo.path, closed); err != nil {
		t.Fatal(err)
	}
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); !ran || safeFailure(err) != "storage_unavailable" {
		t.Fatalf("storage outage: %v %v", ran, err)
	}
	if _, err = os.Stat(s.repo.path); !os.IsNotExist(err) {
		t.Fatalf("backup reinitialized a disconnected repository: %v", err)
	}
	if err = os.Rename(closed, s.repo.path); err != nil {
		t.Fatal(err)
	}
	// Keep the production event/timer path while shrinking elapsed test time.
	if _, err = s.catalog.DB.Exec("UPDATE backup_operations SET ready_unix=? WHERE schedule_id=?", time.Now().Unix()+1, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.WaitStopped() }()
	waitUntil(t, ctx, func() bool {
		var state string
		return s.catalog.DB.QueryRow("SELECT state FROM backup_operations WHERE schedule_id=?", job.ID).Scan(&state) == nil && state == "completed"
	})
	history, err := s.RecentRuns(ctx)
	if err != nil || len(history) != 2 || history[0].Attempt != 2 || history[1].FailureCode != "storage_unavailable" || history[0].OperationID != history[1].OperationID {
		t.Fatalf("retry ledger: %+v %v", history, err)
	}
	snaps, err := s.ListSnapshots(ctx, source.ID)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("duplicate or missing snapshot: %+v %v", snaps, err)
	}
	verifyNativeRestore(t, s.repo, snaps[0], "proof.txt", "storage recovered")
}

func TestDispatcherReleasesStalledRepositoryWithoutBlockingAnother(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	s.timeouts = operationTimeouts{connect: 6 * time.Second, idle: 10 * time.Second}
	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	tcpReady := make(chan struct{})
	released := make(chan struct{})
	defer close(released)
	go func() {
		c, err := socket.Accept()
		if err == nil {
			defer c.Close()
			close(tcpReady)
			<-released
		}
	}()
	stuckSource, err := s.Create(ctx, CreateSource{Name: "stalled login", Kind: "sftp", Host: socket.Addr().String(),
		Username: "fixture", Password: "fixture", Path: "/only-test"})
	if err != nil {
		t.Fatal(err)
	}
	stuck := createTestBackupJob(t, s, ctx, stuckSource, PrimaryStorageID)
	if err = s.Manual(ctx, stuck.ID); err != nil {
		t.Fatal(err)
	}
	local := privateDir(t)
	if err = os.WriteFile(filepath.Join(local, "proof.txt"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := s.Create(ctx, CreateSource{Name: "independent job", Kind: "local", Path: local})
	if err != nil {
		t.Fatal(err)
	}
	otherRepo, err := s.RegisterStorage(ctx, "independent destination", filepath.Join(privateDir(t), "repository"))
	if err != nil {
		t.Fatal(err)
	}
	other := createTestBackupJob(t, s, ctx, src, otherRepo.ID)
	same := createTestBackupJob(t, s, ctx, src, PrimaryStorageID)
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); s.WaitStopped() }()
	select {
	case <-tcpReady:
	case <-ctx.Done():
		t.Fatal("SFTP handshake never started")
	}
	for _, job := range []BackupJob{same, other} {
		if err = s.Manual(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, ctx, func() bool {
		var n int
		return s.catalog.DB.QueryRow("SELECT count(*) FROM schedule_runs").Scan(&n) == nil && n == 3
	})
	waitUntil(t, ctx, func() bool {
		var n int
		return s.catalog.DB.QueryRow("SELECT count(*) FROM backup_operations WHERE state='completed'").Scan(&n) == nil && n == 2
	})
	runs, err := s.RecentRuns(ctx)
	if err != nil || len(runs) != 3 {
		t.Fatalf("unexpected attempt ledger: %+v %v", runs, err)
	}
	byTask := map[string]scheduler.Run{}
	for _, run := range runs {
		byTask[run.TaskID] = run
	}
	a, b, c := byTask[stuck.ID], byTask[same.ID], byTask[other.ID]
	if a.FailureCode != "operation_timeout" || a.OperationState != "waiting" || a.FinishedAt == nil ||
		!c.StartedAt.Before(*a.FinishedAt) || b.StartedAt.Before(*a.FinishedAt) || b.StartedAt.Sub(*a.FinishedAt) > 2*time.Second {
		t.Fatalf("worker or repository was held incorrectly: stalled=%+v same=%+v other=%+v", a, b, c)
	}
}

func TestPublicationReconcilesAfterSQLiteTransactionFailureWithRetriesDisabled(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx := context.Background()
	root := privateDir(t)
	if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("verified before catalog commit"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := s.Create(ctx, CreateSource{Name: "SQLite fault fixture", Kind: "local", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, src, PrimaryStorageID)
	if _, err = s.catalog.DB.Exec("UPDATE schedule_entries SET retry_count=0 WHERE id=?", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.catalog.DB.Exec(`CREATE TRIGGER simulate_failed_catalog BEFORE INSERT ON file_snapshots
		BEGIN SELECT RAISE(FAIL, 'test transaction failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); !ran || safeFailure(err) != "snapshot_catalog_failed" {
		t.Fatalf("catalog fault: ran=%v err=%v", ran, err)
	}
	var n int
	if err = s.catalog.DB.QueryRow("SELECT COUNT(*) FROM snapshots").Scan(&n); err != nil || n != 0 {
		t.Fatalf("SQLite transaction partially published a snapshot: %d %v", n, err)
	}
	if _, err = s.catalog.DB.Exec("DROP TRIGGER simulate_failed_catalog"); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); !ran || err != nil {
		t.Fatalf("catalog reconciliation: ran=%v err=%v", ran, err)
	}
	runs, err := s.RecentRuns(ctx)
	if err != nil || len(runs) != 1 || runs[0].OperationState != "completed" || runs[0].FailureStage != "finalizing" || runs[0].SnapshotID == "" {
		t.Fatalf("failed attempt history changed or completed copy missing: %+v %v", runs, err)
	}
	snaps, err := s.ListSnapshots(ctx, src.ID)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("catalog has no unique copy: %v %v", snaps, err)
	}
	raw, err := s.repo.run(ctx, nil, "snapshots", "--json")
	var markers []resticRecoverySnapshot
	if err != nil || json.Unmarshal(raw, &markers) != nil || len(markers) != 2 {
		t.Fatalf("expected one tree and one portable marker: %s %v", raw, err)
	}
	verifyNativeRestore(t, s.repo, snaps[0], "proof.txt", "verified before catalog commit")
}

func TestProgressWatchAllowsLongActiveTransfer(t *testing.T) {
	options := operationTimeouts{connect: time.Second, idle: 250 * time.Millisecond}
	ctx, stop := watchProgress(context.WithValue(context.Background(), operationOptionsKey{}, options))
	defer stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < 22; i++ {
		select {
		case <-ctx.Done():
			t.Fatalf("operation stopped despite progress: %v", context.Cause(ctx))
		case <-ticker.C:
			noteProgress(ctx)
		}
	}
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatalf("wrong timeout cause: %v", context.Cause(ctx))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled operation did not time out")
	}
}

func TestLocalSourceMountFollowsParentAlias(t *testing.T) {
	realMount, err := localSourceMount("/dev/shm")
	if err != nil || realMount == "/" {
		t.Skip("needs a separate /dev/shm mount")
	}
	alias := filepath.Join(privateDir(t), "alias")
	if err = os.Symlink("/dev", alias); err != nil {
		t.Fatal(err)
	}
	got, err := localSourceMount(filepath.Join(alias, "shm"))
	if err != nil || got != realMount {
		t.Fatalf("source mount behind alias: %q %v, expected %q", got, err, realMount)
	}
}
