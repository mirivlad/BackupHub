package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// BrowsePage deliberately contains just one page, never a full Restic tree.
// Directory listings use restic ls with a directory filter (not recursive).
// A search is explicitly recursive but its response is still bounded.
type BrowsePage struct {
	Files     []File `json:"files"`
	Directory string `json:"directory"`
	Search    string `json:"search,omitempty"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	HasMore   bool   `json:"has_more"`
}

var ErrBrowseDirectoryNotFound = errors.New("folder not found in selected snapshot")
var ErrBrowseInvalidRequest = errors.New("invalid snapshot browser request")

func (s *Service) BrowseSnapshot(ctx context.Context, snapshotID, directory, search string, offset, limit int) (BrowsePage, error) {
	return s.browseSnapshot(ctx, snapshotID, directory, search, offset, limit, false)
}
func (s *Service) BrowseFolders(ctx context.Context, snapshotID, directory string, offset, limit int) (BrowsePage, error) {
	return s.browseSnapshot(ctx, snapshotID, directory, "", offset, limit, true)
}
func (s *Service) browseSnapshot(ctx context.Context, snapshotID, directory, search string, offset, limit int, foldersOnly bool) (BrowsePage, error) {
	page := BrowsePage{Files: []File{}, Offset: offset, Limit: limit, Search: search}
	if limit < 1 || limit > 200 || offset < 0 || offset > 2000000 || len(directory) > 4096 || utf8.RuneCountInString(search) > 128 {
		return page, ErrBrowseInvalidRequest
	}
	dir, err := cleanRel(directory)
	if err != nil {
		return page, ErrBrowseInvalidRequest
	}
	page.Directory = dir
	search = strings.TrimSpace(search)
	if search != "" && utf8.RuneCountInString(search) < 2 {
		return page, ErrBrowseInvalidRequest
	}
	snap, err := s.Snapshot(ctx, snapshotID)
	if err != nil {
		return page, err
	}
	repo, err := s.repositoryFor(ctx, snap.RepositoryID)
	if err != nil {
		return page, err
	}
	// Only the server-generated absolute path can be passed to Restic.
	// Searching always starts at snapshot root.
	resticDir := dir
	if search != "" {
		resticDir = "."
	}
	absolute := filepath.ToSlash(filepath.Join(snap.Root, filepath.FromSlash(resticDir)))
	args := []string{"ls", "--json", "--sort", "name"}
	if search != "" {
		args = append(args, "--recursive")
	}
	args = append(args, snap.ResticID, absolute)
	cmd := repo.command(ctx, nil, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return page, err
	}
	var stderr boundedOutput
	stderr.max = 2048
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return page, err
	}
	decoded := json.NewDecoder(pipe)
	foundDir := dir == "." || search != ""
	matched := 0
	stoppedEarly := false
	needle := strings.ToLower(search)
	var parseErr error
	for {
		if ctx.Err() != nil {
			parseErr = ctx.Err()
			break
		}
		var raw struct {
			StructType string    `json:"struct_type"`
			Path       string    `json:"path"`
			Name       string    `json:"name"`
			Type       string    `json:"type"`
			Size       uint64    `json:"size"`
			MTime      time.Time `json:"mtime"`
		}
		if err = decoded.Decode(&raw); errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			parseErr = fmt.Errorf("restic directory stream: %w", err)
			break
		}
		if raw.StructType != "node" {
			continue
		}
		rel, err := relativeTo(snap.Root, raw.Path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if rel == dir && raw.Type == "dir" {
			foundDir = true
		}
		if rel == "." {
			continue
		}
		if search == "" {
			if filepath.ToSlash(filepath.Dir(rel)) != dir {
				continue
			}
		} else if !strings.Contains(strings.ToLower(rel), needle) {
			continue
		}
		kind := raw.Type
		if kind != "file" && kind != "dir" {
			kind = "other"
		}
		if foldersOnly && kind != "dir" {
			continue
		}
		if matched >= offset && len(page.Files) < limit {
			page.Files = append(page.Files, File{Path: rel, Name: raw.Name, Type: kind, Size: raw.Size, Modified: raw.MTime})
		}
		matched++
		if matched > offset+limit {
			page.HasMore = true
			stoppedEarly = true
			break
		}
	}
	if stoppedEarly || parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return page, parseErr
	}
	if !stoppedEarly && waitErr != nil {
		return page, fmt.Errorf("restic ls failed: %w: %s", waitErr, stderr.String())
	}
	if !foundDir {
		return page, ErrBrowseDirectoryNotFound
	}
	return page, nil
}

// resolveSnapshotFile checks a SINGLE immutable Restic tree entry, instead of
// scanning every path in the snapshot for a one-file download.
func (r *repository) resolveSnapshotFile(ctx context.Context, snapshotID, root, requested string) (string, error) {
	rel, err := cleanRel(requested)
	if err != nil || rel == "." {
		return "", errors.New("a single file is required")
	}
	if !validResticID(snapshotID) {
		return "", errors.New("invalid snapshot ID")
	}
	absolute := filepath.ToSlash(filepath.Join(root, filepath.FromSlash(rel)))
	cmd := r.command(ctx, nil, "ls", "--json", snapshotID, absolute)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr boundedOutput
	stderr.max = 2048
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return "", err
	}
	decoded := json.NewDecoder(pipe)
	found := ""
	var parseErr error
	for {
		var raw struct {
			StructType string `json:"struct_type"`
			Path       string `json:"path"`
			Type       string `json:"type"`
		}
		if err = decoded.Decode(&raw); errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			parseErr = err
			break
		}
		if raw.StructType != "node" || raw.Type != "file" {
			continue
		}
		candidate, relErr := relativeTo(root, raw.Path)
		if relErr == nil && candidate == rel {
			found = raw.Path
		}
	}
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return "", parseErr
	}
	if waitErr != nil {
		return "", fmt.Errorf("restic ls failed: %w: %s", waitErr, stderr.String())
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}
