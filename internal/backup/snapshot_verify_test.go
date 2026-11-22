package backup

import (
	"archive/tar"
	"bytes"
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
  ls) printf '{"struct_type":"snapshot","id":"%s"}\n' "$8"; printf '%s\n' '{"struct_type":"node","type":"dir","path":"/some-dir"}' '{"struct_type":"node","type":"file","path":"/some-dir/missing"}' ;;
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

func TestVerifySnapshotMatchesEntriesAndCompleteArchive(t *testing.T) {
	const script = `#!/bin/sh
case "$6" in
  ls)
    printf '{"struct_type":"snapshot","id":"%s"}\n' "$8"
    cat "$0.tree"
    ;;
  dump) exec cat "$0.tar" ;;
  *) exit 4 ;;
esac
`
	for _, tc := range []struct {
		name, file, symlink, hardlink string
		size, trim                    int
		extra                         bool
		pass                          bool
	}{
		{name: "complete", file: "d/f", size: 3, symlink: "d/s", hardlink: "d/f", pass: true},
		{name: "wrong-path", file: "d/unexpected", size: 3, symlink: "d/s", hardlink: "d/f"},
		{name: "wrong-size", file: "d/f", size: 4, symlink: "d/s", hardlink: "d/f"},
		{name: "wrong-type", file: "d/s", size: 3, symlink: "d/f", hardlink: "d/f"},
		{name: "duplicate", file: "d/f", size: 3, symlink: "d/f", hardlink: "d/f"},
		{name: "wrong-hardlink-target", file: "d/f", size: 3, symlink: "d/s", hardlink: "d"},
		{name: "no-trailer", file: "d/f", size: 3, symlink: "d/s", hardlink: "d/f", trim: 1024},
		{name: "short-trailer", file: "d/f", size: 3, symlink: "d/s", hardlink: "d/f", trim: 512},
		{name: "unexpected-trailing-content", file: "d/f", size: 3, symlink: "d/s", hardlink: "d/f", extra: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "restic")
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			const tree = `{"struct_type":"node","type":"dir","path":"/d"}
{"struct_type":"node","type":"dir","path":"/d/empty"}
{"struct_type":"node","type":"file","path":"/d/f","size":3,"inode":42}
{"struct_type":"node","type":"file","path":"/d/h","size":3,"inode":42}
{"struct_type":"node","type":"symlink","path":"/d/s"}`
			if err := os.WriteFile(binary+".tree", []byte(tree), 0600); err != nil {
				t.Fatal(err)
			}
			var raw bytes.Buffer
			archive := tar.NewWriter(&raw)
			for _, h := range []tar.Header{
				{Name: "d/", Typeflag: tar.TypeDir},
				{Name: "d/empty/", Typeflag: tar.TypeDir},
				{Name: tc.file, Typeflag: tar.TypeReg, Size: int64(tc.size)},
				{Name: "d/h", Typeflag: tar.TypeLink, Linkname: tc.hardlink},
				{Name: tc.symlink, Typeflag: tar.TypeSymlink, Linkname: "../elsewhere"},
			} {
				if err := archive.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
				if _, err := archive.Write(make([]byte, h.Size)); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			raw.Truncate(raw.Len() - tc.trim)
			if tc.extra {
				raw.WriteString("unexpected")
			}
			if err := os.WriteFile(binary+".tar", raw.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			repo := &repository{binary: binary}
			if err := repo.verifySnapshot(context.Background(), strings.Repeat("a", 64)); (err == nil) != tc.pass {
				t.Fatalf("pass=%v, error=%v", tc.pass, err)
			}
		})
	}
}

func TestIncompleteDumpCannotPublishSnapshot(t *testing.T) {
	requireIntegrationRestic(t)
	s := serviceFixture(t)
	ctx := context.Background()
	sourceDir := privateDir(t)
	if err := os.WriteFile(filepath.Join(sourceDir, "file"), []byte("real data"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := s.Create(ctx, CreateSource{Name: "Incomplete archive fixture", Kind: "local", Path: sourceDir})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, s, ctx, src, PrimaryStorageID)
	if err = s.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	bin := privateDir(t)
	script := `#!/bin/sh
if [ "$6" = "dump" ] && [ "$7" = "--archive" ]; then
    dd if=/dev/zero bs=1024 count=1 2>/dev/null
    exit 0
fi
exec RESTIC_BINARY "$@"
`
	script = strings.ReplaceAll(script, "RESTIC_BINARY", `"`+s.repo.binary+`"`)
	if err = os.WriteFile(filepath.Join(bin, "restic"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if ran, err := s.scheduler.RunDue(ctx); !ran || err == nil || !strings.Contains(err.Error(), "snapshot archive incomplete") {
		t.Fatalf("missing verification failure: ran=%v err=%v", ran, err)
	}
	snaps, err := s.ListSnapshots(ctx, src.ID)
	if err != nil || len(snaps) != 0 {
		t.Fatalf("incomplete snapshot was catalogued: %v %v", snaps, err)
	}
	native, err := s.repo.run(ctx, nil, "snapshots", "--json")
	if err != nil || !bytes.Contains(native, []byte("bh-pending")) || bytes.Contains(native, []byte("bh-commit-")) {
		t.Fatalf("expected only an unpublished Restic tree: %s %v", native, err)
	}
}
