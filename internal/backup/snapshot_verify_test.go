package backup

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifySnapshotRejectsMissingDataPack(t *testing.T) {
	requireIntegrationRestic(t)
	r := serviceFixture(t).repo
	root := privateDir(t)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 256<<10)
	rand.Read(payload)
	file := filepath.Join(root, "payload.bin")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(root, "hard-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("payload.bin", filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := r.treeBackup(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.verifySnapshot(ctx, id); err != nil {
		t.Fatalf("valid snapshot failed verification: %v", err)
	}

	// Random file data produces a large data pack distinct from the metadata.
	var dataPack string
	err = filepath.Walk(filepath.Join(r.path, "data"), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && info.Size() >= int64(len(payload)) {
			dataPack = path
		}
		return err
	})
	if err != nil || dataPack == "" {
		t.Fatalf("no separate data pack in fixture: %v", err)
	}
	if err = os.Remove(dataPack); err != nil {
		t.Fatal(err)
	}
	if _, err = r.listTree(ctx, id); err != nil {
		t.Fatalf("metadata should still be readable: %v", err)
	}
	if err = r.verifySnapshot(ctx, id); err == nil {
		t.Fatal("verification succeeded with a missing data pack")
	}
}

func TestVerifySnapshotRejectsIncompleteDump(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "restic")
	script := `#!/bin/sh
case "$6" in
  ls) printf '%s\n' '{"struct_type":"snapshot"}' '{"struct_type":"node","type":"dir","path":"/some-dir"}' '{"struct_type":"node","type":"file","path":"/some-dir/missing"}' ;;
  dump) exec cat "$0.tar" ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(binary + ".tar")
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewWriter(output)
	if err = archive.WriteHeader(&tar.Header{Name: "some-dir", Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
		t.Fatal(err)
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}

	r := &repository{binary: binary}
	err = r.verifySnapshot(context.Background(), strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(), "snapshot archive incomplete") {
		t.Fatalf("successful but incomplete dump was accepted: %v", err)
	}
}
