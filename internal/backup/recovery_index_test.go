package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/vault"
)

func TestRecoveryReconcilesCommittedTreesOnCleanHost(t *testing.T) {
	requireIntegrationRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	disk := privateDir(t)
	originalState := privateDir(t)
	c, err := catalog.Open(originalState)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err = vault.Initialize(c); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	primaryPath := filepath.Join(disk, "protected")
	service, err := New(ctx, c, v, primaryPath, "")
	if err != nil {
		t.Fatal(err)
	}
	portable, err := service.registerStorage(ctx, "External", filepath.Join(disk, "passwordless"), "", NoPasswordMode)
	if err != nil {
		t.Fatal(err)
	}
	originalRoot := privateDir(t)
	if err = os.WriteFile(filepath.Join(originalRoot, "notes.txt"), []byte("historical version"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := service.Create(ctx, CreateSource{Name: "Original source", Kind: "local", Path: originalRoot})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, service, ctx, source, PrimaryStorageID)
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); err != nil || !ran {
		t.Fatalf("backup ran=%v: %v", ran, err)
	}
	kit := filepath.Join(privateDir(t), "offline-kit")
	if err = ExportRecoveryKit(ctx, c, kit); err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(filepath.Join(originalRoot, "notes.txt"), []byte("post-checkpoint version"), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption just after Restic publication and before the SQL transaction.
	record := recordUnindexedTree(t, ctx, service.repo, source, originalRoot, "post-checkpoint")
	if _, err = service.repo.treeBackup(ctx, originalRoot, source.ID); err != nil {
		t.Fatal(err)
	}

	newRoot := privateDir(t)
	if err = os.WriteFile(filepath.Join(newRoot, "notes.txt"), []byte("passwordless version"), 0600); err != nil {
		t.Fatal(err)
	}
	newSource, err := service.Create(ctx, CreateSource{Name: "New since checkpoint", Kind: "local", Path: newRoot})
	if err != nil {
		t.Fatal(err)
	}
	secondRepo, err := service.repositoryFor(ctx, portable.ID)
	if err != nil {
		t.Fatal(err)
	}
	newRecord := recordUnindexedTree(t, ctx, secondRepo, newSource, newRoot, "passwordless-version")
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(originalState, "catalog.db")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(originalRoot, "notes.txt")); err != nil {
		t.Fatal(err)
	}

	freshPath := filepath.Join(privateDir(t), "clean-instance")
	if err = RestoreRecoveryKit(ctx, kit, freshPath); err != nil {
		t.Fatal(err)
	}
	fresh, err := catalog.Open(freshPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Close() })
	for _, id := range []string{PrimaryStorageID, portable.ID} {
		if added, err := ReconcileRecoveryRepository(ctx, fresh, id); err != nil || added != 1 {
			t.Fatalf("repository %s imported %d: %v", id, added, err)
		}
		if added, err := ReconcileRecoveryRepository(ctx, fresh, id); err != nil || added != 0 {
			t.Fatalf("second import for %s imported %d: %v", id, added, err)
		}
	}
	freshVault, err := vault.Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := New(ctx, fresh, freshVault, "", "")
	if err != nil {
		t.Fatal(err)
	}
	history, err := recovered.ListSnapshots(ctx, "")
	if err != nil || len(history) != 3 {
		t.Fatalf("history after import: %v, %v", history, err)
	}
	for _, tc := range []struct {
		id, want string
	}{{record.ID, "post-checkpoint version"}, {newRecord.ID, "passwordless version"}} {
		dir, err := recovered.RestoreToNewDirectory(ctx, tc.id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || string(got) != tc.want {
			t.Fatalf("restored %s: %q, %v", tc.id, got, err)
		}
	}
	if _, err = recovered.CreateJob(ctx, CreateBackupJob{Name: "Recovered source", SourceID: newSource.ID, RepositoryID: PrimaryStorageID, Schedule: job.Schedule}); err == nil {
		t.Fatal("a recovered placeholder allowed scheduling a new backup")
	}

	// A marker referencing a missing snapshot must not yield a successful import.
	broken := record
	broken.ID = "broken-manifest"
	broken.ResticID = strings.Repeat("0", 64)
	if _, err = service.repo.commit(ctx, broken); err != nil {
		t.Fatal(err)
	}
	if added, err := ReconcileRecoveryRepository(ctx, fresh, PrimaryStorageID); err == nil || added != 0 {
		t.Fatalf("import accepted broken manifest, added=%d: %v", added, err)
	}
}

func recordUnindexedTree(t *testing.T, ctx context.Context, repo *repository, source Source, root, id string) portableRecord {
	t.Helper()
	native, err := repo.treeBackup(ctx, root, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.check(ctx, true); err != nil {
		t.Fatal(err)
	}
	record := portableRecord{
		ID: id, SourceID: source.ID, SourceName: source.Name, Kind: source.Kind, Root: root,
		ResticID: native, At: time.Now().UTC().Format(time.RFC3339Nano),
		DataVerified: true, Consistency: "best_effort", Fidelity: "basic_metadata",
	}
	if _, err = repo.commit(ctx, record); err != nil {
		t.Fatal(err)
	}
	return record
}
