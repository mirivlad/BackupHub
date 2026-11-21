package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFastZIPOneResticProcessForFolderAndSelectedFiles(t *testing.T) {
	restic, err := exec.LookPath("restic")
	if err != nil {
		if os.Getenv("BACKUPHUB_RESTIC_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip("restic not installed")
	}
	svc := serviceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	directory := privateDir(t)
	folder := filepath.Join(directory, "Claude")
	if err = os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	requested := make([]string, 0, 64)
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("fragment-%03d.bin", i)
		if err = os.WriteFile(filepath.Join(folder, name), []byte(strings.Repeat(name, 50)), 0600); err != nil {
			t.Fatal(err)
		}
		requested = append(requested, "Claude/"+name)
	}
	_ = os.WriteFile(filepath.Join(folder, "unselected.txt"), []byte("not selected"), 0600)
	_ = os.Symlink("/etc/passwd", filepath.Join(folder, "symlink"))
	src, err := svc.Create(ctx, CreateSource{Name: "FastZIP source", Kind: "local", Path: directory})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, svc, ctx, src, "primary")
	if err = svc.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	ran, err := svc.scheduler.RunDue(ctx)
	if err != nil || !ran {
		t.Fatalf("backup run failed: %v %v", ran, err)
	}
	snaps, err := svc.ListSnapshots(ctx, src.ID)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("no snapshot %v, %+v", err, snaps)
	}
	repo, err := svc.repositoryFor(ctx, snaps[0].RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "restic-invocations.log")
	wrapper := filepath.Join(dir, "restic-wrapper")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", marker, restic)
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	repo.binary = wrapper

	run := func(label string, paths []string, wantFiles int, expectUnselected bool) {
		t.Helper()
		if err = os.WriteFile(marker, nil, 0600); err != nil {
			t.Fatal(err)
		}
		var buffer bytes.Buffer
		if err = repo.zipSelection(ctx, snaps[0].ResticID, snaps[0].Root, paths, &buffer); err != nil {
			t.Fatalf("%s zip: %v", label, err)
		}
		calls, e := os.ReadFile(marker)
		if e != nil {
			t.Fatal(e)
		}
		if got := strings.Count(string(calls), " dump "); got != 1 {
			t.Fatalf("%s started %d restic dump processes, calls=%q", label, got, calls)
		}
		zr, e := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
		if e != nil {
			t.Fatal(e)
		}
		found := map[string]bool{}
		for _, f := range zr.File {
			if f.Name == "backuphub-export-manifest.json" {
				reader, _ := f.Open()
				var manifest struct {
					SHA256 map[string]string `json:"sha256"`
				}
				if err = json.NewDecoder(reader).Decode(&manifest); err != nil {
					t.Fatal(err)
				}
				_ = reader.Close()
				if len(manifest.SHA256) != wantFiles {
					t.Fatalf("%s hash count %d != %d", label, len(manifest.SHA256), wantFiles)
				}
				continue
			}
			if strings.HasPrefix(f.Name, "/") || strings.HasPrefix(f.Name, "../") {
				t.Fatalf("unsafe zip path %q", f.Name)
			}
			reader, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			_, e = io.Copy(io.Discard, reader)
			_ = reader.Close()
			if e != nil {
				t.Fatal(e)
			}
			found[f.Name] = true
		}
		if len(found) != wantFiles {
			t.Fatalf("%s: got %d files, want %d", label, len(found), wantFiles)
		}
		if found["Claude/unselected.txt"] != expectUnselected {
			t.Errorf("%s: unselected file included=%v", label, found["Claude/unselected.txt"])
		}
		if found["Claude/symlink"] {
			t.Errorf("%s: followed a symbolic link", label)
		}
	}
	run("one selected file ZIP", []string{"Claude/fragment-000.bin"}, 1, false)
	run("selected 64 siblings", requested, 64, false)
	run("single whole folder", []string{"Claude"}, 65, true)
	run("whole snapshot", nil, 65, true)
}
