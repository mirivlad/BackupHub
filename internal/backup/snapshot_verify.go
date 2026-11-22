package backup

import (
	"archive/tar"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Restic authenticates data blobs as it reads them. Its TAR can omit a
// subtree after a read error, so verify the stream against the snapshot tree.
func (r *repository) verifySnapshot(ctx context.Context, id string) error {
	return r.withSnapshotIndex(ctx, func(index *snapshotIndex) error {
		if err := index.load(ctx, r, id); err != nil {
			return err
		}
		return r.readStream(ctx, func(reader io.Reader) error {
			return index.verifyArchive(ctx, r, id, reader)
		}, "dump", "--archive", "tar", id, "/")
	})
}

func (index *snapshotIndex) verifyArchive(ctx context.Context, repo *repository, id string, reader io.Reader) error {
	input := &tarInput{Reader: reader, tracking: true}
	archive := tar.NewReader(input)
	var padding int64
	for {
		input.bytes, input.lastNonzero, input.tracking = 0, 0, true
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			if index.remaining != 0 {
				return fmt.Errorf("restic snapshot archive incomplete: %d entries missing", index.remaining)
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
		archiveName := header.Name
		if header.Typeflag == tar.TypeDir {
			archiveName = strings.TrimSuffix(archiveName, "/")
		}
		name, err := tarSnapshotPath(archiveName)
		if err != nil {
			return err
		}
		entry, err := index.lookup(ctx, name)
		if errors.Is(err, sql.ErrNoRows) || entry.seen {
			return fmt.Errorf("unexpected or duplicate restic archive entry %q", name)
		}
		if err != nil {
			return err
		}
		if err := index.verifyEntry(ctx, repo, id, name, header, entry); err != nil {
			return err
		}
		if _, err := index.seen.ExecContext(ctx, []byte(name)); err != nil {
			return err
		}
		index.remaining--
		input.tracking = false
		if _, err := io.Copy(io.Discard, archive); err != nil {
			return fmt.Errorf("read restic archive entry %q: %w", name, err)
		}
		padding = (512 - header.Size%512) % 512
	}
}

func tarSnapshotPath(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name ||
		name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("invalid restic archive path %q", name)
	}
	return "/" + name, nil
}

func (i *snapshotIndex) verifyEntry(ctx context.Context, repo *repository, id, name string, header *tar.Header, entry snapshotArchiveEntry) error {
	valid := false
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		valid = entry.kind == "file" && header.Size == entry.size
	case tar.TypeDir:
		valid = entry.kind == "dir" && header.Size == 0
	case tar.TypeSymlink:
		if entry.kind == "symlink" && header.Size == 0 {
			exact, err := i.exactEntry(ctx, repo, id, name, entry)
			if err != nil {
				return err
			}
			valid = exact.target != "" && header.Linkname == exact.target
		}
	case tar.TypeLink:
		target, err := tarSnapshotPath(header.Linkname)
		if err != nil {
			return err
		}
		to, err := i.lookup(ctx, target)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if entry.kind == "file" && header.Size == 0 && to.seen && to.kind == "file" {
			entry, err = i.exactEntry(ctx, repo, id, name, entry)
			if err != nil {
				return err
			}
			to, err = i.exactEntry(ctx, repo, id, target, to)
			if err != nil {
				return err
			}
			valid = entry.inode != 0 && entry.inode == to.inode && entry.device == to.device && entry.size == to.size
		}
	}
	if !valid {
		return fmt.Errorf("restic archive entry %q does not match snapshot type, size or link", name)
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
	if waitErr != nil && (readErr == nil || stderr.String() != "") {
		return errors.Join(readErr, fmt.Errorf("restic %s: %w: %s", args[0], waitErr, stderr.String()))
	}
	return readErr
}
