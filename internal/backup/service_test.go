package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
	"github.com/mirivlad/BackupHub/internal/vault"
)

func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func serviceFixture(t *testing.T) *Service {
	t.Helper()
	state := privateDir(t)
	c, err := catalog.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err = vault.Initialize(c); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), c, v, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
func TestFileBackupVersionHistoryAndZipRestore(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		if os.Getenv("BACKUPHUB_RESTIC_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip("restic not installed")
	}
	svc := serviceFixture(t)
	src := privateDir(t)
	if err := os.Mkdir(filepath.Join(src, "Video"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Video", "clip.txt"), []byte("first version"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	entry, err := svc.Create(ctx, CreateSource{Name: "Home test", Kind: "local", Path: src, IntervalMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Manual(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	ran, err := svc.scheduler.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("first run %v ran=%v", err, ran)
	}
	snapshots, err := svc.ListSnapshots(ctx, entry.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots %v count=%d", err, len(snapshots))
	}
	original := snapshots[0]
	files, err := svc.Files(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no file inventory")
	}
	var zipBytes bytes.Buffer
	recorder := httptest.NewRecorder()
	if err = svc.Download(ctx, original.ID, []string{"Video"}, false, recorder); err != nil {
		t.Fatal(err)
	}
	zipBytes.Write(recorder.Body.Bytes())
	zr, err := zip.NewReader(bytes.NewReader(zipBytes.Bytes()), int64(zipBytes.Len()))
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, member := range zr.File {
		if member.Name == "Video/clip.txt" {
			reader, err := member.Open()
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(bytes) != "first version" {
				t.Fatalf("bad backup %q", bytes)
			}
			ok = true
		}
	}
	if !ok {
		t.Fatal("selected folder missing from ZIP")
	}
	if err = os.Remove(filepath.Join(src, "Video", "clip.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(src, "Video", "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = svc.Manual(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	ran, err = svc.scheduler.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("second run %v ran=%v", err, ran)
	}
	snapshots, err = svc.ListSnapshots(ctx, entry.ID)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("expected historical snapshots %v len=%d", err, len(snapshots))
	}
	destination, err := svc.RestoreToNewDirectory(ctx, original.ID, []string{"Video/clip.txt"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(destination, "Video", "clip.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "first version" {
		t.Fatalf("restore differs: %q", restored)
	}
	// There is NO in-place overwrite of the source.
	if _, err = os.Stat(filepath.Join(src, "Video", "clip.txt")); !os.IsNotExist(err) {
		t.Fatalf("source modified during restore: %v", err)
	}
}
func TestSourceValidationNoEscape(t *testing.T) {
	for _, val := range []string{"../etc", "/etc/passwd", "a/../b", ".\\x"} {
		if _, err := cleanRel(val); err == nil {
			t.Errorf("accepted invalid path %q", val)
		}
	}
	root := privateDir(t)
	if _, err := safeLocal(root, "../../oops"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := validateInput(CreateSource{Kind: "local", Path: root}, filepath.Join(root, "repo"), root, filepath.Join(root, "stage")); err == nil {
		t.Fatal("source includes state allowed")
	}
}
func TestRestoreRejectsNonexistent(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic required")
	}
	svc := serviceFixture(t)
	_, err := svc.RestoreToNewDirectory(context.Background(), "no-such-snapshot", []string{".."})
	if err == nil {
		t.Fatal("accepted invalid snapshot")
	}
}
func TestNoInsecureSourceConfig(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil { t.Skip("restic not installed") }
	svc := serviceFixture(t)
	source, err := svc.Create(context.Background(), CreateSource{Name: "Postgres", Kind: "postgres", Host: "db.example", Username: "readonly", Password: "test-secret", Database: "db", IntervalMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := svc.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != source.ID {
		t.Fatal("source not saved")
	}
	var config string
	if err = svc.catalog.DB.QueryRow("SELECT settings_json FROM sources WHERE id=?", source.ID).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, "test-secret") {
		t.Fatal("secret stored in settings")
	}
}
