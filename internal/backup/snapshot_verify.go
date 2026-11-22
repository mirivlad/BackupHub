package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Restic authenticates data blobs as it reads them. Its TAR can omit a
// subtree after a read error, so verify the stream against the snapshot tree.
func (r *repository) verifySnapshot(ctx context.Context, id string) error {
	entries := make(map[string]snapshotArchiveEntry)
	if err := r.walkTree(ctx, id, func(entry treeEntry) error {
		switch entry.Type {
		case "file", "dir", "symlink":
			if entry.Path == "/" || !strings.HasPrefix(entry.Path, "/") || path.Clean(entry.Path) != entry.Path {
				return fmt.Errorf("invalid snapshot tree path %q", entry.Path)
			}
			if _, exists := entries[entry.Path]; exists {
				return fmt.Errorf("duplicate snapshot tree path %q", entry.Path)
			}
			entries[entry.Path] = snapshotArchiveEntry{treeEntry: entry}
		}
		// The Restic dumper omits sockets, devices and FIFOs.
		return nil
	}); err != nil {
		return err
	}

	return r.readStream(ctx, func(reader io.Reader) error {
		input := &tarInput{Reader: reader, tracking: true}
		archive := tar.NewReader(input)
		remaining := len(entries)
		var padding int64
		for {
			input.bytes, input.lastNonzero, input.tracking = 0, 0, true
			header, err := archive.Next()
			if errors.Is(err, io.EOF) {
				if remaining != 0 {
					return fmt.Errorf("restic snapshot archive incomplete: %d entries missing", remaining)
				}
				// archive/tar also accepts EOF on a block boundary without
				// the required two zero records. Restic writes both records.
				if input.bytes != padding+1024 || input.lastNonzero > padding {
					return errors.New("restic snapshot archive has a missing or invalid end marker")
				}
				input.bytes, input.lastNonzero = 0, 0
				_, err = io.Copy(io.Discard, input)
				if err == nil && input.lastNonzero != 0 {
					return errors.New("unexpected data after restic snapshot archive")
				}
				return err
			}
			if err != nil {
				return err
			}
			name, err := tarSnapshotPath(strings.TrimSuffix(header.Name, "/"))
			if err != nil {
				return err
			}
			entry, exists := entries[name]
			if !exists || entry.seen {
				return fmt.Errorf("unexpected or duplicate restic archive entry %q", name)
			}
			if err := verifyTarEntry(header, entry.treeEntry, entries); err != nil {
				return err
			}
			entry.seen = true
			entries[name] = entry
			remaining--
			input.tracking = false
			if _, err := io.Copy(io.Discard, archive); err != nil {
				return fmt.Errorf("read restic archive entry %q: %w", name, err)
			}
			padding = (512 - header.Size%512) % 512
		}
	}, "dump", "--archive", "tar", id, "/")
}

func tarSnapshotPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name ||
		name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("invalid restic archive path %q", name)
	}
	return "/" + name, nil
}

type snapshotArchiveEntry struct {
	treeEntry
	seen bool
}

func verifyTarEntry(header *tar.Header, entry treeEntry, entries map[string]snapshotArchiveEntry) error {
	valid := false
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		valid = entry.Type == "file" && uint64(header.Size) == entry.Size
	case tar.TypeDir:
		valid = entry.Type == "dir" && header.Size == 0
	case tar.TypeSymlink:
		valid = entry.Type == "symlink" && header.Size == 0 && header.Linkname != ""
	case tar.TypeLink:
		target, err := tarSnapshotPath(header.Linkname)
		to, exists := entries[target]
		valid = err == nil && exists && to.seen && to.Type == "file" &&
			entry.Type == "file" && entry.Inode != 0 && to.Inode == entry.Inode &&
			to.Size == entry.Size && header.Size == 0
	}
	if !valid {
		return fmt.Errorf("restic archive entry %q does not match snapshot type, size or link", entry.Path)
	}
	return nil
}

type tarInput struct {
	io.Reader
	bytes       int64
	lastNonzero int64
	tracking    bool
}

func (r *tarInput) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if !r.tracking {
		return n, err
	}
	for i := n - 1; i >= 0; i-- {
		if p[i] != 0 {
			r.lastNonzero = r.bytes + int64(i) + 1
			break
		}
	}
	r.bytes += int64(n)
	return n, err
}

func (r *repository) readStream(ctx context.Context, consume func(io.Reader) error, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := r.command(ctx, nil, args...)
	var stderr boundedOutput
	stderr.max = 8192
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	readErr := consume(pipe)
	if readErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return fmt.Errorf("restic %s: %w: %s", args[0], waitErr, stderr.String())
	}
	return nil
}
