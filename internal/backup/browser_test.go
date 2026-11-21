package backup

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowseSnapshotPaginatesFoldersAndSearches(t *testing.T) {
	requireIntegrationRestic(t)
	svc := serviceFixture(t)
	dir := privateDir(t)
	nested := filepath.Join(dir, "folder", "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 132; i++ {
		name := fmt.Sprintf("document-%03d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"folder/inside.txt":        "a",
		"folder/nested/deep.txt":   "b",
		"folder/nested/запись.txt": "unicode contents",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	src, err := svc.Create(ctx, CreateSource{Name: "Browse test", Kind: "local", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, svc, ctx, src, "primary")
	if err = svc.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	ran, err := svc.scheduler.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("backup failed ran=%v err=%v", ran, err)
	}
	snaps, err := svc.ListSnapshots(ctx, src.ID)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("no snapshot %v %v", err, snaps)
	}
	id := snaps[0].ID
	root, err := svc.BrowseSnapshot(ctx, id, "", "", 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Files) != 30 || !root.HasMore || root.Directory != "." {
		t.Fatalf("root page %+v", root)
	}
	// Even with over 100 files at root, browsing a subdirectory must never
	// enumerate all descendants or return anything outside that folder.
	shallow, err := svc.BrowseSnapshot(ctx, id, "folder", "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if shallow.HasMore || len(shallow.Files) != 2 {
		t.Fatalf("not shallow %+v", shallow)
	}
	names := map[string]bool{}
	for _, entry := range shallow.Files {
		names[entry.Path] = true
		if strings.Contains(entry.Path, "deep.txt") {
			t.Fatal("included recursive descendant")
		}
	}
	if !names["folder/inside.txt"] || !names["folder/nested"] {
		t.Fatalf("wrong directory entries %+v", shallow.Files)
	}
	dirs, err := svc.BrowseFolders(ctx, id, "folder", 0, 20)
	if err != nil || len(dirs.Files) != 1 || dirs.Files[0].Path != "folder/nested" {
		t.Fatalf("folder-only navigation %+v %v", dirs, err)
	}
	inner, err := svc.BrowseSnapshot(ctx, id, "folder/nested", "", 0, 20)
	if err != nil || len(inner.Files) != 2 {
		t.Fatalf("nested folder %+v %v", inner, err)
	}
	download := httptest.NewRecorder()
	if err = svc.Download(ctx, id, []string{"folder/nested/запись.txt"}, true, download); err != nil {
		t.Fatal(err)
	}
	if download.Body.String() != "unicode contents" {
		t.Fatalf("wrong single file: %q", download.Body.String())
	}
	if got := download.Header().Get("Content-Disposition"); !strings.Contains(got, "filename*=") {
		t.Errorf("missing Unicode filename encoding: %q", got)
	}
	search, err := svc.BrowseSnapshot(ctx, id, ".", "deep", 0, 20)
	if err != nil || len(search.Files) != 1 || search.Files[0].Path != "folder/nested/deep.txt" {
		t.Fatalf("search %+v %v", search, err)
	}
	second, err := svc.BrowseSnapshot(ctx, id, ".", "", 30, 30)
	if err != nil || len(second.Files) != 30 {
		t.Fatalf("page 2 %+v %v", second, err)
	}
	seen := map[string]bool{}
	for _, entry := range root.Files {
		seen[entry.Path] = true
	}
	for _, entry := range second.Files {
		if seen[entry.Path] {
			t.Fatalf("duplicate across pages: %s", entry.Path)
		}
	}
	tail, err := svc.BrowseSnapshot(ctx, id, ".", "", 120, 30)
	if err != nil || len(tail.Files) != 13 || tail.HasMore {
		t.Fatalf("last page %+v %v", tail, err)
	}
	for _, bad := range []string{"../etc", "/etc", "folder/../nested", "folder//nested"} {
		if _, e := svc.BrowseSnapshot(ctx, id, bad, "", 0, 20); !errors.Is(e, ErrBrowseInvalidRequest) {
			t.Errorf("unsafe path %q: %v", bad, e)
		}
	}
	if _, err = svc.BrowseSnapshot(ctx, id, "folder/missing", "", 0, 20); !errors.Is(err, ErrBrowseDirectoryNotFound) {
		t.Fatalf("missing folder: %v", err)
	}
	if _, err = svc.BrowseSnapshot(ctx, id, ".", "x", 0, 20); !errors.Is(err, ErrBrowseInvalidRequest) {
		t.Fatalf("one-letter search: %v", err)
	}
	if _, err = svc.BrowseSnapshot(ctx, id, ".", "", 0, 201); !errors.Is(err, ErrBrowseInvalidRequest) {
		t.Fatalf("oversized page: %v", err)
	}
}
