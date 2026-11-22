package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
)

type snapshotInventory struct {
	files, directories, symlinks int
}

// Restic authenticates and hashes blobs as dump reads them. Its tar dumper can
// end without an error when a subtree is unreadable, so also walk the tree
// with ls and check that the archive contains every expected entry.
func (r *repository) verifySnapshot(ctx context.Context, id string) error {
	var expected, actual snapshotInventory
	if err := r.walkTree(ctx, id, func(entry treeEntry) error {
		switch entry.Type {
		case "file":
			expected.files++
		case "dir":
			expected.directories++
		case "symlink":
			expected.symlinks++
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.readStream(ctx, func(reader io.Reader) error {
		archive := tar.NewReader(reader)
		for {
			header, err := archive.Next()
			if errors.Is(err, io.EOF) {
				// Drain padding so Restic can exit before we wait for it.
				_, err = io.Copy(io.Discard, reader)
				return err
			}
			if err != nil {
				return err
			}
			switch header.Typeflag {
			case tar.TypeReg, tar.TypeRegA, tar.TypeLink:
				actual.files++
			case tar.TypeDir:
				actual.directories++
			case tar.TypeSymlink:
				actual.symlinks++
			}
		}
	}, "dump", "--archive", "tar", id, "/"); err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("restic snapshot archive incomplete: read %+v, expected %+v", actual, expected)
	}
	return nil
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
