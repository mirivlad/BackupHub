package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RestoreToNewDirectory never overwrites an existing file or follows paths
// outside the dedicated server-side restore root. It is deliberately not an
// in-place restore onto a live production source.
func (s *Service) RestoreToNewDirectory(ctx context.Context, id string, selections []string) (string, error) {
	if len(selections) > 64 {
		return "", errors.New("too many selections")
	}
	snap, err := s.snapshot(ctx, id)
	if err != nil {
		return "", err
	}
	requested := map[string]bool{}
	for _, raw := range selections {
		p, err := cleanRel(raw)
		if err != nil {
			return "", err
		}
		requested[p] = true
	}
	if len(requested) == 0 {
		requested["."] = true
	}
	entries, err := s.repo.listTree(ctx, snap.ResticID)
	if err != nil {
		return "", err
	}
	root := filepath.Join(s.catalog.DataDir, "restores")
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	restoreID, err := randomID()
	if err != nil {
		return "", err
	}
	destination := filepath.Join(root, id+"-"+restoreID)
	if err = os.Mkdir(destination, 0700); err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(destination)
		}
	}()
	files := 0
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		rel, err := relativeTo(snap.Root, entry.Path)
		if err != nil || rel == "." {
			continue
		}
		include := requested["."]
		for selection := range requested {
			if rel == selection || strings.HasPrefix(rel, selection+"/") {
				include = true
				break
			}
		}
		if !include {
			continue
		}
		if entry.Type != "file" {
			continue
		}
		out, err := safeLocal(destination, rel)
		if err != nil {
			return "", err
		}
		if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
			return "", err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		dumpErr := s.repo.dumpFile(ctx, snap.ResticID, entry.Path, f)
		syncErr := f.Sync()
		closeErr := f.Close()
		if dumpErr != nil {
			return "", dumpErr
		}
		if syncErr != nil {
			return "", syncErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if entry.Mode&0111 != 0 {
			_ = os.Chmod(out, 0600|os.FileMode(entry.Mode&0111))
		}
		if !entry.MTime.IsZero() {
			_ = os.Chtimes(out, entry.MTime, entry.MTime)
		}
		files++
	}
	if files == 0 {
		return "", errors.New("no regular files matched restore selection")
	}
	note := fmt.Sprintf("BackupHub restore copy created %s UTC from snapshot %s.\nThis is not an in-place restore. Verify data before transferring it to the original source.\n", time.Now().UTC().Format(time.RFC3339), snap.ID)
	if err = os.WriteFile(filepath.Join(destination, "BACKUPHUB-RESTORE-README.txt"), []byte(note), 0600); err != nil {
		return "", err
	}
	success = true
	return destination, nil
}
