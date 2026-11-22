package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSMB1RejectsAQuietlySkippedFile(t *testing.T) {
	if err := validateSMB1Summary("tar: dumped 2 files and 0 directories\nTotal bytes written: 37 (0.0 MiB/s)\n", 2, 37); err != nil {
		t.Fatalf("valid tar summary: %v", err)
	}
	for _, output := range []string{
		"NT_STATUS_ACCESS_DENIED opening remote file \\private.txt\ntar: dumped 2 files and 0 directories\nTotal bytes written: 37 (0.0 MiB/s)\n",
		"tar: dumped 2 files and 0 directories\nTotal bytes written: 50 (0.0 MiB/s)\n",
	} {
		if err := validateSMB1Summary(output, 2, 37); err == nil {
			t.Fatalf("accepted an incomplete archive: %s", output)
		}
	}
}

func TestSMB1CaptureAndRestore(t *testing.T) {
	host := os.Getenv("BACKUPHUB_TEST_SMB1_HOST")
	if host == "" {
		t.Skip("no SMB1 fixture configured")
	}
	requireIntegrationRestic(t)
	ctx := context.Background()
	service := serviceFixture(t)
	input := CreateSource{Name: "SMB1 fixture", Kind: "smb", Host: host, Share: "fixture",
		Path: "subfolder", Username: os.Getenv("BACKUPHUB_TEST_SMB1_USER"), Password: os.Getenv("BACKUPHUB_TEST_SMB1_PASSWORD")}
	source, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, service, ctx, source, PrimaryStorageID)
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); err != nil || !ran {
		t.Fatalf("SMB1 backup ran=%v: %v", ran, err)
	}
	history, err := service.ListSnapshots(ctx, source.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("SMB1 history = %v: %v", history, err)
	}
	dir, err := service.RestoreToNewDirectory(ctx, history[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "nested.txt")); err != nil || string(got) != "fixture in subfolder\n" {
		t.Fatalf("unexpected restored SMB1 data: %q %v", got, err)
	}
	input.Path = "missing"
	input.Kind = smb1Kind
	input.AuthMethod, input.Password = "password", ""
	if updated, err := service.UpdateSource(ctx, source.ID, input); err != nil || updated.Kind != "smb" {
		t.Fatalf("legacy API alias should preserve the unified SMB source: %#v %v", updated, err)
	}
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); err == nil || !ran {
		t.Fatalf("SMB1 missing directory was not rejected: %v %v", ran, err)
	}
	if history, err = service.ListSnapshots(ctx, source.ID); err != nil || len(history) != 1 {
		t.Fatalf("missing directory changed history: %v %v", history, err)
	}
	runs, err := service.RecentRuns(ctx)
	if err != nil || len(runs) != 2 || runs[0].State != "failed" || runs[0].FailureCode != "source_unavailable" || runs[0].SnapshotID != "" || runs[0].NextRetry == nil {
		t.Fatalf("failed capture must have a diagnostic and no snapshot: %#v %v", runs, err)
	}
}

func TestSMBPrefersModernDialect(t *testing.T) {
	host := os.Getenv("BACKUPHUB_TEST_SMB_DUAL_HOST")
	if host == "" {
		t.Skip("no dual-dialect SMB fixture configured")
	}
	// A server that offers both protocols must work without the SMB1 client.
	t.Setenv("PATH", t.TempDir())
	target := privateDir(t)
	cfg := Settings{Host: host, Share: "fixture", Path: "subfolder", Username: os.Getenv("BACKUPHUB_TEST_SMB1_USER")}
	if err := captureSMB(context.Background(), cfg, os.Getenv("BACKUPHUB_TEST_SMB1_PASSWORD"), target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "nested.txt"))
	if err != nil || string(data) != "fixture in subfolder\n" {
		t.Fatalf("unexpected data from modern SMB: %q %v", data, err)
	}
}
