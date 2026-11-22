package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/vault"
)

func TestRecoveryKitRoundTripWithLiveWAL(t *testing.T) {
	requireIntegrationRestic(t)
	ctx := context.Background()
	service := serviceFixture(t)
	source, err := service.Create(ctx, CreateSource{Name: "Documents", Kind: "local", Path: privateDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, service, ctx, source, PrimaryStorageID)
	if err := service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	protected, err := service.RegisterStorage(ctx, "Protected", filepath.Join(privateDir(t), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	passwordless, err := service.registerStorage(ctx, "Passwordless", filepath.Join(privateDir(t), "repo"), "", NoPasswordMode)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.vault.Put(ctx, "recovery-fixture", []byte("before export")); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetCheckSchedule(ctx, protected.ID, "metadata", 24); err != nil {
		t.Fatal(err)
	}
	check, err := service.QueueRepositoryCheck(ctx, protected.ID, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	root := privateDir(t)
	kit := filepath.Join(root, "kit")
	if err = ExportRecoveryKit(ctx, service.catalog, kit); err != nil {
		t.Fatal(err)
	}
	if err = ExportRecoveryKit(ctx, service.catalog, kit); err == nil {
		t.Fatal("export reused an existing directory")
	}
	for _, name := range []string{"catalog.db", "master.key", "restic.key", "restic-" + protected.ID + ".key", "manifest.json"} {
		info, err := os.Stat(filepath.Join(kit, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("kit member %s: %v %v", name, info, err)
		}
	}
	for _, name := range []string{"catalog.db-wal", "catalog.db-shm", "restic-" + passwordless.ID + ".key"} {
		if _, err = os.Lstat(filepath.Join(kit, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected kit member %s: %v", name, err)
		}
	}
	if err = service.vault.Put(ctx, "recovery-fixture", []byte("after export")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fresh-state")
	if err = RestoreRecoveryKit(ctx, kit, target); err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if c.InstanceID != service.catalog.InstanceID {
		t.Fatal("instance identity changed")
	}
	restored, err := vault.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restored.Get(ctx, "recovery-fixture"); err != nil || string(got) != "before export" {
		t.Fatalf("restored secret did not match the catalog checkpoint: %v", err)
	}
	var enabled, pending int
	if err = c.DB.QueryRowContext(ctx, "SELECT enabled, manual_pending FROM schedule_entries WHERE id=?", job.ID).Scan(&enabled, &pending); err != nil || enabled != 0 || pending != 0 {
		t.Fatalf("restored schedule enabled=%d, pending=%d: %v", enabled, pending, err)
	}
	var state string
	if err = c.DB.QueryRowContext(ctx, "SELECT state FROM verification_runs WHERE id=?", check.ID).Scan(&state); err != nil || state != "interrupted" {
		t.Fatalf("restored queued check has state %q: %v", state, err)
	}
	var paused bool
	if err = c.DB.QueryRowContext(ctx, "SELECT next_run IS NULL FROM repository_check_schedules WHERE repository_id=?", protected.ID).
		Scan(&paused); err != nil || !paused {
		t.Fatalf("restored storage schedule paused=%v: %v", paused, err)
	}
	if err = RestoreRecoveryKit(ctx, kit, target); err == nil {
		t.Fatal("restored over an existing state directory")
	}

	if err = os.WriteFile(filepath.Join(kit, "restic.key"), []byte("damaged key"), 0600); err != nil {
		t.Fatal(err)
	}
	damaged := filepath.Join(root, "damaged-state")
	if err = RestoreRecoveryKit(ctx, kit, damaged); err == nil {
		t.Fatal("restored a damaged kit")
	}
	if _, err = os.Stat(damaged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("damaged restore published partial state: %v", err)
	}
}
