package restic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/domain"
)

func TestConfigRejectsLoosePassword(t *testing.T) {
	pwd := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pwd, []byte("test-only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Repository: filepath.Join(t.TempDir(), "repo"), PasswordFile: pwd}); err == nil {
		t.Fatal("accepted a non-private password file")
	}
	if err := os.Chmod(pwd, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Repository: "relative-repo", PasswordFile: pwd}); err == nil {
		t.Fatal("accepted a non-absolute repository path")
	}
}
func TestComponentNameValidation(t *testing.T) {
	for _, value := range []string{"", "..", "a/b", "a\\b", manifestFilename} {
		if validName(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"config", "large_file.bin", "source-2.tar"} {
		if !validName(value) {
			t.Fatalf("rejected %q", value)
		}
	}
}
func TestResticRoundTripAndRecoveryWithoutCatalog(t *testing.T) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		if os.Getenv("BACKUPHUB_RESTIC_REQUIRED") == "1" {
			t.Fatal("restic executable is required in CI")
		}
		t.Skip("restic binary not installed")
	}
	dir := t.TempDir()
	pwd := filepath.Join(dir, "repository-password")
	if err := os.WriteFile(pwd, []byte("temporary-integration-test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repository")
	e, err := New(Config{Binary: bin, Repository: repo, PasswordFile: pwd})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := e.Init(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := e.Begin(ctx, "dataset-1")
	if err != nil {
		t.Fatal(err)
	}
	dataA := []byte("Archive content for testing\n")
	a, err := tx.Put(ctx, "file-one", bytes.NewReader(dataA))
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != shaHex(dataA) || a.SizeBytes != int64(len(dataA)) {
		t.Fatal("component digest mismatch")
	}
	dataB := []byte("This is an app component")
	b, err := tx.Put(ctx, "file-two", bytes.NewReader(dataB))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	manifest := domain.Snapshot{
		SchemaVersion: domain.SnapshotSchemaVersion,
		ID:            "sample-001", SourceID: "source-1", DatasetID: "dataset-1", StartedAt: at, FinishedAt: at.Add(time.Second),
		State: domain.CaptureCommitted, Consistency: domain.ConsistencyBestEffort,
		MetadataFidelity: domain.MetadataContent, DataVerification: domain.VerificationNotRun,
		RestoreVerification: domain.VerificationNotRun, Components: []domain.Component{a},
	}
	if _, err := tx.Commit(ctx, manifest); err == nil {
		t.Fatal("published an incomplete app snapshot")
	}
	if _, err := e.ReadManifest(ctx, manifest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uncommitted data advertised: %v", err)
	}
	manifest.Components = append(manifest.Components, b)
	commitID, err := tx.Commit(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !objectPattern.MatchString(commitID) {
		t.Fatalf("bad commit id: %q", commitID)
	}
	if _, err := tx.Put(ctx, "late", strings.NewReader("oops")); err == nil {
		t.Fatal("wrote to closed transaction")
	}
	if err := e.Check(ctx, true); err != nil {
		t.Fatal(err)
	}
	// Construct an entirely new engine: it must reconstruct the manifest and
	// contents from Restic alone, without consulting SQLite or a prior process.
	recovered, err := New(Config{Binary: bin, Repository: repo, PasswordFile: pwd})
	if err != nil {
		t.Fatal(err)
	}
	list, err := recovered.ListCommitted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != manifest.ID {
		t.Fatalf("lost manifest after restart: %+v", list)
	}
	persisted, err := recovered.ReadManifest(ctx, manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Usable() {
		t.Fatal("recovered incomplete snapshot")
	}
	if err := recovered.Verify(ctx, manifest.ID); err != nil {
		t.Fatal(err)
	}
	reader, err := recovered.Read(ctx, manifest.ID, "file-one")
	if err != nil {
		t.Fatal(err)
	}
	result, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, dataA) {
		t.Fatal("restored data differs")
	}
	if _, err := recovered.Read(ctx, manifest.ID, "../secret"); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestAbortNeverPublishes(t *testing.T) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic unavailable")
	}
	dir := t.TempDir()
	pwd := filepath.Join(dir, "pw")
	if err = os.WriteFile(pwd, []byte("test-pw-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{Binary: bin, Repository: filepath.Join(dir, "repo"), PasswordFile: pwd})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = e.Init(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := e.Begin(ctx, "dataset-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Put(ctx, "sample", strings.NewReader("abandoned")); err != nil {
		t.Fatal(err)
	}
	if err = tx.Abort(ctx); err != nil {
		t.Fatal(err)
	}
	snapshots, err := e.ListCommitted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatal("aborted transaction published")
	}
}
func shaHex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
