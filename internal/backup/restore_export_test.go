package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreAndExportPreserveFilesNamedLikeMetadata(t *testing.T) {
	requireIntegrationRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	service := serviceFixture(t)
	root := privateDir(t)
	const readme = "BACKUPHUB-RESTORE-README.txt"
	const manifest = "backuphub-export-manifest.json"
	const otherFile = "backuphub-export-manifest-1.json/data.txt"
	const original = "original file contents"

	for _, name := range []string{readme, manifest, otherFile} {
		target := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := service.Create(ctx, CreateSource{Name: "Metadata collision", Kind: "local", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, service, ctx, source, PrimaryStorageID)
	if err := service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); err != nil || !ran {
		t.Fatalf("backup ran=%v: %v", ran, err)
	}
	snapshots, err := service.ListSnapshots(ctx, source.ID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots: %v, %v", snapshots, err)
	}
	id := snapshots[0].ID

	t.Run("restore", func(t *testing.T) {
		dir, err := service.RestoreToNewDirectory(ctx, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(dir, readme)); err != nil || string(got) != original {
			t.Fatalf("restored README = %q, %v", got, err)
		}
	})
	for _, tc := range []struct {
		name         string
		paths        []string
		metadataName string
	}{
		{"full snapshot", nil, "backuphub-export-manifest-2.json"},
		{"single file", []string{manifest}, "backuphub-export-manifest-1.json"},
		{"disjoint selection", []string{manifest, otherFile}, "backuphub-export-manifest-2.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if err := service.Download(ctx, id, tc.paths, false, response); err != nil {
				t.Fatal(err)
			}
			data := response.Body.Bytes()
			archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if archive.Comment != tc.metadataName {
				t.Fatalf("ZIP metadata pointer = %q", archive.Comment)
			}
			entries := map[string][]byte{}
			for _, file := range archive.File {
				if _, exists := entries[file.Name]; exists {
					t.Fatalf("duplicate ZIP entry %q", file.Name)
				}
				reader, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				content, err := io.ReadAll(reader)
				reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				entries[file.Name] = content
			}
			if got := string(entries[manifest]); got != original {
				t.Fatalf("archived user file = %q", got)
			}
			var metadata struct {
				SHA256 map[string]string
			}
			if err := json.Unmarshal(entries[tc.metadataName], &metadata); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(original))
			if metadata.SHA256[manifest] != hex.EncodeToString(sum[:]) {
				t.Fatal("archived user file missing from manifest")
			}
		})
	}
}
