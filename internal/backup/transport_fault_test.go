package backup

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The fixture is a TCP proxy to real disposable servers. Only the proxy's
// connections are cut or stalled, after actual server payload has arrived.
type captureProxy struct {
	address    string
	hit        chan struct{}
	resume     chan struct{}
	resumeOnce sync.Once
}

func newCaptureProxy(t *testing.T, upstream string, stall bool) *captureProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &captureProxy{address: listener.Addr().String(), hit: make(chan struct{}), resume: make(chan struct{})}
	var workers sync.WaitGroup
	var firstHit sync.Once
	workers.Go(func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer client.Close()
				server, err := net.DialTimeout("tcp", upstream, 5*time.Second)
				if err != nil {
					return
				}
				defer server.Close()
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = io.Copy(server, client)
					if tcp, ok := server.(*net.TCPConn); ok {
						_ = tcp.CloseWrite()
					}
				}()
				defer func() { _ = client.Close(); _ = server.Close(); <-done }()
				select {
				case <-p.resume:
					_, _ = io.Copy(client, server)
				default:
					if _, err := io.CopyN(client, server, 512<<10); err == nil {
						firstHit.Do(func() { close(p.hit) })
						if stall {
							<-p.resume
						}
					}
				}
			})
		}
	})
	t.Cleanup(func() {
		p.unblock()
		_ = listener.Close()
		workers.Wait()
	})
	return p
}

func (p *captureProxy) unblock() { p.resumeOnce.Do(func() { close(p.resume) }) }

func TestRealTransportLossAndNoProgress(t *testing.T) {
	cases := []struct {
		name, hostEnv, userEnv, passwordEnv string
		source                              CreateSource
		stall                               bool
	}{
		{"SMB1 TCP drop", "BACKUPHUB_TEST_SMB1_HOST", "BACKUPHUB_TEST_SMB1_USER", "BACKUPHUB_TEST_SMB1_PASSWORD", CreateSource{Kind: "smb", Share: "fixture", Path: "subfolder"}, false},
		{"SMB2 TCP drop", "BACKUPHUB_TEST_SMB2_HOST", "BACKUPHUB_TEST_SMB1_USER", "BACKUPHUB_TEST_SMB1_PASSWORD", CreateSource{Kind: "smb", Share: "fixture", Path: "subfolder"}, false},
		{"SMB3 TCP drop", "BACKUPHUB_TEST_SMB_DUAL_HOST", "BACKUPHUB_TEST_SMB1_USER", "BACKUPHUB_TEST_SMB1_PASSWORD", CreateSource{Kind: "smb", Share: "fixture", Path: "subfolder"}, false},
		{"SMB3 blackhole", "BACKUPHUB_TEST_SMB_DUAL_HOST", "BACKUPHUB_TEST_SMB1_USER", "BACKUPHUB_TEST_SMB1_PASSWORD", CreateSource{Kind: "smb", Share: "fixture", Path: "subfolder"}, true},
		{"SFTP TCP drop", "BACKUPHUB_TEST_SFTP_HOST", "BACKUPHUB_TEST_SFTP_USER", "BACKUPHUB_TEST_SFTP_PASSWORD", CreateSource{Kind: "sftp", Path: "/home/bhfixture/capture"}, false},
		{"SFTP blackhole", "BACKUPHUB_TEST_SFTP_HOST", "BACKUPHUB_TEST_SFTP_USER", "BACKUPHUB_TEST_SFTP_PASSWORD", CreateSource{Kind: "sftp", Path: "/home/bhfixture/capture"}, true},
		{"PostgreSQL TCP drop", "BACKUPHUB_TEST_PG1C_HOST", "BACKUPHUB_TEST_PG1C_USER", "BACKUPHUB_TEST_PG1C_PASSWORD", CreateSource{Kind: "postgres", Database: "backuphub_faults"}, false},
		{"PostgreSQL blackhole", "BACKUPHUB_TEST_PG1C_HOST", "BACKUPHUB_TEST_PG1C_USER", "BACKUPHUB_TEST_PG1C_PASSWORD", CreateSource{Kind: "postgres", Database: "backuphub_faults"}, true},
		{"PostgreSQL 1C TCP drop", "BACKUPHUB_TEST_PG1C_HOST", "BACKUPHUB_TEST_PG1C_USER", "BACKUPHUB_TEST_PG1C_PASSWORD", CreateSource{Kind: postgres1CKind, Database: "backuphub_faults"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := os.Getenv(tc.hostEnv)
			if target == "" {
				t.Skip("needs the isolated transport fixture")
			}
			requireIntegrationRestic(t)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			s := serviceFixture(t)
			oldRoot := privateDir(t)
			if err := os.WriteFile(filepath.Join(oldRoot, "before.txt"), []byte("existing archive"), 0600); err != nil {
				t.Fatal(err)
			}
			oldSource, err := s.Create(ctx, CreateSource{Name: "pre-existing backup", Kind: "local", Path: oldRoot})
			if err != nil {
				t.Fatal(err)
			}
			oldJob := createTestBackupJob(t, s, ctx, oldSource, PrimaryStorageID)
			runBackup(t, s, oldJob.ID)
			prior, err := s.ListSnapshots(ctx, oldSource.ID)
			if err != nil || len(prior) != 1 {
				t.Fatalf("baseline snapshot: %+v %v", prior, err)
			}
			p := newCaptureProxy(t, target, tc.stall)
			tc.source.Name, tc.source.Host = tc.name, p.address
			tc.source.Username, tc.source.Password = os.Getenv(tc.userEnv), os.Getenv(tc.passwordEnv)
			source, err := s.Create(ctx, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			job := createTestBackupJob(t, s, ctx, source, PrimaryStorageID)
			s.timeouts = operationTimeouts{connect: 6 * time.Second, idle: 2 * time.Second}
			if err = s.Manual(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			ran, faultErr := s.scheduler.RunDue(ctx)
			if !ran || faultErr == nil {
				t.Fatalf("network fault did not interrupt backup: %v %v", ran, faultErr)
			}
			if time.Since(started) > 15*time.Second {
				t.Fatalf("faulted connection took %v to stop", time.Since(started))
			}
			select {
			case <-p.hit:
			default:
				t.Fatalf("fixture failed before the proxy received capture data: %v", faultErr)
			}
			runs, err := s.RecentRuns(ctx)
			if err != nil || len(runs) != 2 || runs[0].OperationState != "waiting" || runs[0].FailureStage != "capturing" || runs[0].SnapshotID != "" {
				t.Fatalf("wrong failed-attempt history: %+v %v (error: %v)", runs, err, faultErr)
			}
			want := "network_unavailable"
			if tc.stall {
				want = "operation_timeout"
			}
			if runs[0].FailureCode != want {
				t.Fatalf("fault classified as %q, want %q", runs[0].FailureCode, want)
			}
			p.unblock()
			s.timeouts.idle = defaultIdleTimeout
			if err = s.ChangeRetry(ctx, job.ID, runs[0].OperationID, false); err != nil {
				t.Fatal(err)
			}
			if ran, err := s.scheduler.RunDue(ctx); !ran || err != nil {
				t.Fatalf("retry did not recover: %v %v", ran, err)
			}
			snaps, err := s.ListSnapshots(ctx, source.ID)
			if err != nil || len(snaps) != 1 {
				t.Fatalf("duplicate or missing snapshot: %+v %v", snaps, err)
			}
			if _, err = s.repo.run(ctx, nil, "check", "--read-data"); err != nil {
				t.Fatal(err)
			}
			verifyNativeRestore(t, s.repo, prior[0], "before.txt", "existing archive")
			switch source.Kind {
			case "postgres", postgres1CKind:
				dir, err := s.RestoreToNewDirectory(ctx, snaps[0].ID, nil)
				if err != nil {
					t.Fatal(err)
				}
				toc, err := exec.CommandContext(ctx, "pg_restore", "--list", filepath.Join(dir, "database.dump")).CombinedOutput()
				if err != nil {
					t.Fatalf("restored dump unreadable: %v %s", err, toc)
				}
			default:
				verifyNativeRestore(t, s.repo, snaps[0], "nested.txt", "fixture in subfolder\n")
			}
		})
	}
}

func TestDiskFullResticRetry(t *testing.T) {
	volume := os.Getenv("BACKUPHUB_TEST_SMALL_VOLUME")
	if volume == "" {
		t.Skip("needs a disposable, capacity-limited filesystem")
	}
	requireIntegrationRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	parent, err := os.MkdirTemp(volume, "backuphub-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(parent)
	s := serviceFixture(t)
	store, err := s.RegisterStorage(ctx, "small disk", filepath.Join(parent, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	root := privateDir(t)
	if err = os.WriteFile(filepath.Join(root, "proof.txt"), []byte("old data"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := s.Create(ctx, CreateSource{Name: "full disk fixture", Kind: "local", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, source, store.ID)
	runBackup(t, s, job.ID)
	fill := filepath.Join(parent, "filler")
	for _, spec := range []struct {
		name   string
		blocks int
	}{{fill, 40}, {filepath.Join(root, "new-data"), 18}} {
		dd := exec.CommandContext(ctx, "dd", "if=/dev/urandom", "of="+spec.name, "bs=1M", fmt.Sprintf("count=%d", spec.blocks))
		if out, err := dd.CombinedOutput(); err != nil {
			t.Fatalf("prepare full disk test fixture: %v %s", err, out)
		}
	}
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); !ran || err == nil {
		t.Fatalf("disk full did not fail backup: %v %v", ran, err)
	}
	runs, err := s.RecentRuns(ctx)
	if err != nil || len(runs) != 2 || runs[0].FailureCode != "storage_full" || runs[0].OperationState != "waiting" || runs[0].SnapshotID != "" {
		t.Fatalf("disk full was not recorded: %+v %v", runs, err)
	}
	if err = os.Remove(fill); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeRetry(ctx, job.ID, runs[0].OperationID, false); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); !ran || err != nil {
		t.Fatalf("retry after freeing disk space: %v %v", ran, err)
	}
	snaps, err := s.ListSnapshots(ctx, source.ID)
	if err != nil || len(snaps) != 2 {
		t.Fatalf("expected old and new snapshots: %+v %v", snaps, err)
	}
	repo, err := s.repositoryFor(ctx, store.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.run(ctx, nil, "check", "--read-data"); err != nil {
		t.Fatal(err)
	}
	verifyNativeRestore(t, repo, snaps[1], "proof.txt", "old data")
	verifyNativeRestore(t, repo, snaps[0], "proof.txt", "old data")
}

func TestSIGTERMDuringSFTPTransferThenRepeatedFault(t *testing.T) {
	target := os.Getenv("BACKUPHUB_TEST_SFTP_HOST")
	if target == "" {
		t.Skip("needs isolated SFTP fixture")
	}
	requireIntegrationRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	s := serviceFixture(t)
	input := CreateSource{Name: "interrupt during SFTP data", Kind: "sftp", Host: target,
		Username: os.Getenv("BACKUPHUB_TEST_SFTP_USER"), Password: os.Getenv("BACKUPHUB_TEST_SFTP_PASSWORD"),
		Path: "/home/bhfixture/capture"}
	source, err := s.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, source, PrimaryStorageID)
	runBackup(t, s, job.ID)

	p := newCaptureProxy(t, target, true)
	input.Host, input.Password = p.address, ""
	if _, err = s.UpdateSource(ctx, source.ID, input); err != nil {
		t.Fatal(err)
	}
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestResilienceServiceChild$")
	child.Env = append(os.Environ(), "BACKUPHUB_TEST_CHILD_STATE="+s.catalog.DataDir)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-p.hit:
	case <-ctx.Done():
		t.Fatal("SFTP transfer did not reach the injected fault")
	case err := <-done:
		t.Fatalf("service exited before transfer: %v", err)
	}
	if err = child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("SIGTERM did not stop the service cleanly: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("service remained stuck after SIGTERM: %v", ctx.Err())
	}
	runs, err := s.RecentRuns(ctx)
	if err != nil || len(runs) != 2 || runs[0].State != "interrupted" || runs[0].FailureStage != "capturing" {
		t.Fatalf("SIGTERM was not recorded during capture: %+v %v", runs, err)
	}
	opID := runs[0].OperationID
	if err = s.ChangeRetry(ctx, job.ID, opID, false); err != nil {
		t.Fatal(err)
	}
	s.timeouts.idle = 2 * time.Second
	if ran, err := s.scheduler.RunDue(ctx); !ran || safeFailure(err) != "operation_timeout" {
		t.Fatalf("fault was not reproduced during retry: ran=%v err=%v", ran, err)
	}
	runs, err = s.RecentRuns(ctx)
	if err != nil || len(runs) != 3 || runs[0].Attempt != 2 || runs[0].OperationState != "waiting" || runs[0].OperationID != opID {
		t.Fatalf("repeat lost its original operation or budget: %+v %v", runs, err)
	}
	p.unblock()
	s.timeouts.idle = defaultIdleTimeout
	if err = s.ChangeRetry(ctx, job.ID, opID, false); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.scheduler.RunDue(ctx); !ran || err != nil {
		t.Fatalf("second retry failed: ran=%v err=%v", ran, err)
	}
	runs, err = s.RecentRuns(ctx)
	if err != nil || len(runs) != 4 || runs[0].Attempt != 3 || runs[0].OperationID != opID || runs[0].OperationState != "completed" {
		t.Fatalf("final attempt lost from journal: %+v %v", runs, err)
	}
	snaps, err := s.ListSnapshots(ctx, source.ID)
	if err != nil || len(snaps) != 2 {
		t.Fatalf("capture failures changed the snapshots: %+v %v", snaps, err)
	}
	if _, err = s.repo.run(ctx, nil, "check", "--read-data"); err != nil {
		t.Fatal(err)
	}
	for _, snap := range snaps {
		verifyNativeRestore(t, s.repo, snap, "nested.txt", "fixture in subfolder\n")
	}
}
