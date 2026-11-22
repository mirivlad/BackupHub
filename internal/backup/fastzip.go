package backup

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// optimizedZipSelection streams a single Restic tar export into an uncompressed
// ZIP (ZIP Store) for a whole snapshot, one directory, or multiple files that
// share a non-root parent. This avoids reloading Restic's repository index for
// each file, the largest source of latency when exporting browser caches.
func (r *repository) optimizedZipSelection(ctx context.Context, snapshotID, root string, requested map[string]bool, output io.Writer) (bool, error) {
	if !validResticID(snapshotID) || !filepath.IsAbs(root) {
		return true, errors.New("invalid snapshot")
	}
	if requested["."] {
		return true, r.zipFromResticTar(ctx, snapshotID, root, ".", requested, output)
	}
	if len(requested) == 1 {
		for name := range requested {
			full, err := r.resolveSnapshotFile(ctx, snapshotID, root, name)
			if err == nil {
				return true, r.zipSingleFile(ctx, snapshotID, name, full, output)
			}
			if !errors.Is(err, os.ErrNotExist) {
				return true, err
			}
			// An exact single directory is the common case for folder download.
			return true, r.zipFromResticTar(ctx, snapshotID, root, name, requested, output)
		}
	}
	// Exporting a selection from one folder via one process is much faster than
	// one Restic process per file. Disparate selections spanning the snapshot
	// root use the existing targeted-file path so an enormous unrelated
	// repository is never scanned just to export two files.
	common := ""
	for name := range requested {
		parent := path.Dir(name)
		if common == "" {
			common = parent
			continue
		}
		for common != "." && parent != common && !strings.HasPrefix(parent, common+"/") {
			common = path.Dir(common)
		}
	}
	if common == "." || common == "" {
		return false, nil
	}
	return true, r.zipFromResticTar(ctx, snapshotID, root, common, requested, output)
}

func archiveIncludes(requested map[string]bool, rel string) bool {
	if requested["."] {
		return true
	}
	for target := range requested {
		if rel == target || strings.HasPrefix(rel, target+"/") {
			return true
		}
	}
	return false
}

func (r *repository) zipFromResticTar(ctx context.Context, snapshotID, root, directory string, requested map[string]bool, output io.Writer) error {
	directory, err := cleanRel(directory)
	if err != nil {
		return err
	}
	absolute := filepath.ToSlash(filepath.Join(root, filepath.FromSlash(directory)))
	if !filepath.IsAbs(absolute) {
		return errors.New("invalid archive directory")
	}
	cmd := r.command(ctx, nil, "dump", "--archive", "tar", snapshotID, absolute)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr boundedOutput
	stderr.max = 4096
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	tw := tar.NewReader(pipe)
	zw := zip.NewWriter(output)
	hashes := map[string]string{}
	var total uint64
	count := 0
	buffer := make([]byte, 128*1024)
	var readErr error
	for {
		if ctx.Err() != nil {
			readErr = ctx.Err()
			break
		}
		header, e := tw.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			readErr = fmt.Errorf("restic tar stream: %w", e)
			break
		}
		// Restic dumps tar names without the initial / even for absolute paths.
		// Resolve against the snapshot root, not the local filesystem.
		archiveName := strings.TrimPrefix(header.Name, "/")
		absoluteName := path.Clean("/" + archiveName)
		relative, e := relativeTo(root, filepath.FromSlash(absoluteName))
		if e != nil || relative == "." {
			continue
		}
		if _, e = cleanRel(relative); e != nil {
			readErr = errors.New("unsafe archive entry name")
			break
		}
		if !archiveIncludes(requested, relative) {
			continue
		}
		// As in the original ZIP exporter, never follow links and do not export
		// device nodes. Directory nodes are implicit in the ZIP member paths.
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if header.Size < 0 {
			readErr = errors.New("invalid negative file size")
			break
		}
		size := uint64(header.Size)
		if total > 1<<43 || size > 1<<43-total {
			readErr = errors.New("export exceeds configured 8 TiB safety limit")
			break
		}
		total += size
		count++
		if count > 500000 {
			readErr = errors.New("too many files for one export")
			break
		}
		fh := &zip.FileHeader{Name: relative, Method: zip.Store}
		fh.SetModTime(header.ModTime)
		writer, e := zw.CreateHeader(fh)
		if e != nil {
			readErr = e
			break
		}
		digest := sha256.New()
		n, e := io.CopyBuffer(io.MultiWriter(writer, digest), tw, buffer)
		if e != nil {
			readErr = e
			break
		}
		if n != header.Size {
			readErr = io.ErrUnexpectedEOF
			break
		}
		hashes[relative] = hex.EncodeToString(digest.Sum(nil))
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return fmt.Errorf("restic directory dump failed: %w: %s", waitErr, stderr.String())
	}
	if count == 0 {
		return errors.New("selection not found or contains no regular files")
	}
	return finishZIP(zw, snapshotID, requested, hashes)
}

func (r *repository) zipSingleFile(ctx context.Context, snapshotID, rel, absolute string, output io.Writer) error {
	zw := zip.NewWriter(output)
	writer, err := zw.CreateHeader(&zip.FileHeader{Name: rel, Method: zip.Store})
	if err != nil {
		return err
	}
	digest := sha256.New()
	if err = r.dumpFile(ctx, snapshotID, absolute, io.MultiWriter(writer, digest)); err != nil {
		return err
	}
	return finishZIP(zw, snapshotID, map[string]bool{rel: true}, map[string]string{rel: hex.EncodeToString(digest.Sum(nil))})
}

func finishZIP(zw *zip.Writer, snapshotID string, requested map[string]bool, hashes map[string]string) error {
	targets := make([]string, 0, len(requested))
	for target := range requested {
		if target != "." {
			targets = append(targets, target)
		}
	}
	sort.Strings(targets)
	manifest, err := json.MarshalIndent(struct {
		Snapshot string            `json:"snapshot"`
		Included []string          `json:"included"`
		SHA256   map[string]string `json:"sha256"`
	}{snapshotID, targets, hashes}, "", "  ")
	if err != nil {
		return err
	}
	// A data file or its implied directory can occupy the usual manifest name.
	const stem = "backuphub-export-manifest"
	roots := map[string]bool{}
	for name := range hashes {
		root, _, _ := strings.Cut(name, "/")
		if strings.HasPrefix(root, stem) {
			roots[root] = true
		}
	}
	name := stem + ".json"
	for suffix := 1; roots[name]; suffix++ {
		name = fmt.Sprintf("%s-%d.json", stem, suffix)
	}
	if err := zw.SetComment(name); err != nil {
		return err
	}
	writer, err := zw.Create(name)
	if err != nil {
		return err
	}
	if _, err = writer.Write(manifest); err != nil {
		return err
	}
	return zw.Close()
}
