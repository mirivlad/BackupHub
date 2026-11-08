package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Restic uses its native filesystem snapshot format for complete directory trees.
// A separate durable commit marker is written AFTER verifying all Restic data.
type repository struct{ binary, path, passwordFile string }

type boundedOutput struct {
	buf bytes.Buffer
	max int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.buf.Len() < b.max {
		_, _ = b.buf.Write(p[:min(n, b.max-b.buf.Len())])
	}
	return n, nil
}
func (b *boundedOutput) String() string { return b.buf.String() }

func initRepository(ctx context.Context, stateDir, repoPath string) (*repository, error) {
	if repoPath == "" {
		repoPath = filepath.Join(stateDir, "repository")
	}
	if !filepath.IsAbs(repoPath) || filepath.Clean(repoPath) == "/" {
		return nil, errors.New("repository requires an absolute non-root path")
	}
	binary, err := exec.LookPath("restic")
	if err != nil {
		return nil, fmt.Errorf("restic executable not installed: %w", err)
	}
	r := &repository{binary: binary, path: filepath.Clean(repoPath), passwordFile: filepath.Join(stateDir, "restic.key")}
	if err := os.MkdirAll(r.path, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(r.path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("repository path must be a real directory")
	}
	keyInfo, keyErr := os.Lstat(r.passwordFile)
	repoInfo, repoErr := os.Lstat(filepath.Join(r.path, "config"))
	switch {
	case keyErr == nil && repoErr == nil:
		if !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0077 != 0 {
			return nil, errors.New("restic key must be a private regular file")
		}
		if !repoInfo.Mode().IsRegular() {
			return nil, errors.New("repository config must be regular")
		}
		if _, err = r.run(ctx, nil, "snapshots", "--json"); err != nil {
			return nil, fmt.Errorf("repository cannot be opened: %w", err)
		}
		return r, nil
	case os.IsNotExist(keyErr) && os.IsNotExist(repoErr):
		// Fresh site only. Neither password nor repository config may pre-exist.
		entries, err := os.ReadDir(r.path)
		if err != nil {
			return nil, err
		}
		if len(entries) != 0 {
			return nil, errors.New("refusing to initialize a nonempty repository path")
		}
		key := make([]byte, 48)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(r.passwordFile, os.O_WRONLY|os.O_EXCL|os.O_CREATE, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.WriteString(hex.EncodeToString(key) + "\n")
		syncErr := f.Sync()
		closeErr := f.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			_ = os.Remove(r.passwordFile)
			return nil, errors.New("cannot persist repository key")
		}
		if _, err := r.run(ctx, nil, "init"); err != nil {
			return nil, fmt.Errorf("cannot initialize restic repository: %w", err)
		}
		return r, nil
	default:
		return nil, errors.New("repository/key mismatch: refusing to overwrite existing or lost restic credentials")
	}
}
func (r *repository) command(ctx context.Context, stdin io.Reader, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.binary, append([]string{"--repo", r.path, "--password-file", r.passwordFile, "--no-cache"}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	cmd.Stdin = stdin
	return cmd
}
func (r *repository) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := r.command(ctx, stdin, args...)
	var stdout, stderr boundedOutput
	stdout.max = 16 << 20
	stderr.max = 8192
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("restic %s: %w: %s", args[0], err, stderr.String())
	}
	return stdout.buf.Bytes(), nil
}

var safeID = func(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// treeBackup rejects every nonzero restic exit status, including partial exit 3.
func (r *repository) treeBackup(ctx context.Context, root, sourceID string) (string, error) {
	if !safeID(sourceID) {
		return "", errors.New("invalid source id")
	}
	cmd := r.command(ctx, nil, "backup", "--json", "--tag", "bh-pending", "--tag", "bh-source-"+sourceID, "--host", "backuphub", root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr boundedOutput
	stderr.max = 8192
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return "", err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var snapshot string
	for scanner.Scan() {
		var line struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) == nil && line.MessageType == "summary" {
			snapshot = line.SnapshotID
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if scanErr != nil {
		return "", fmt.Errorf("restic stream: %w", scanErr)
	}
	if waitErr != nil {
		return "", fmt.Errorf("restic backup incomplete: %w: %s", waitErr, stderr.String())
	}
	if !validResticID(snapshot) {
		return "", errors.New("restic did not return a full snapshot ID")
	}
	return snapshot, nil
}
func validResticID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// This commit is independent of the SQLite catalog. Rebuilding the catalog can
// enumerate bh-commit-* markers and verify referenced native Restic snapshots.
type portableRecord struct {
	Format       string `json:"format"`
	ID           string `json:"id"`
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name"`
	Kind         string `json:"kind"`
	Root         string `json:"root"`
	ResticID     string `json:"restic_id"`
	At           string `json:"at"`
	DataVerified bool   `json:"data_verified"`
	Consistency  string `json:"consistency"`
	Fidelity     string `json:"fidelity"`
}

func (r *repository) commit(ctx context.Context, rec portableRecord) (string, error) {
	if !safeID(rec.ID) || !validResticID(rec.ResticID) {
		return "", errors.New("invalid committed identifiers")
	}
	rec.Format = "backuphub.restic.tree.v1"
	blob, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	output, err := r.run(ctx, bytes.NewReader(blob), "backup", "--stdin", "--stdin-filename", "backuphub-tree-manifest.json", "--tag", "bh-commit-"+rec.ID, "--json")
	if err != nil {
		return "", err
	}
	var id string
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		var event struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &event) == nil && event.MessageType == "summary" {
			id = event.SnapshotID
		}
	}
	if !validResticID(id) {
		return "", errors.New("restic commit marker missing")
	}
	return id, nil
}
func (r *repository) check(ctx context.Context, allData bool) error {
	args := []string{"check"}
	if allData {
		args = append(args, "--read-data")
	}
	_, err := r.run(ctx, nil, args...)
	return err
}

type treeEntry struct {
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Size  uint64    `json:"size"`
	Mode  uint32    `json:"mode"`
	MTime time.Time `json:"mtime"`
}

func (r *repository) listTree(ctx context.Context, id string) ([]treeEntry, error) {
	if !validResticID(id) {
		return nil, errors.New("invalid snapshot ID")
	}
	cmd := r.command(ctx, nil, "ls", "--json", id)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr boundedOutput
	stderr.max = 4096
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(pipe)
	entries := make([]treeEntry, 0, 256)
	var parseErr error
	for {
		var raw struct {
			StructType string    `json:"struct_type"`
			Path       string    `json:"path"`
			Name       string    `json:"name"`
			Type       string    `json:"type"`
			Size       uint64    `json:"size"`
			Mode       uint32    `json:"mode"`
			MTime      time.Time `json:"mtime"`
		}
		if err = decoder.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			parseErr = err
			break
		}
		if raw.StructType == "node" {
			entries = append(entries, treeEntry{Path: raw.Path, Name: raw.Name, Type: raw.Type, Size: raw.Size, Mode: raw.Mode, MTime: raw.MTime})
		}
		if len(entries) > 1500000 {
			parseErr = errors.New("snapshot file count exceeds safe browse limit")
			break
		}
	}
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return nil, parseErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("restic ls: %w: %s", waitErr, stderr.String())
	}
	return entries, nil
}
func cleanRel(value string) (string, error) {
	if value == "" || value == "." {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return "", errors.New("unsafe selection path")
	}
	val := path.Clean(value)
	if val == ".." || strings.HasPrefix(val, "../") || val != value {
		return "", errors.New("unsafe selection path")
	}
	return val, nil
}
func relativeTo(root, full string) (string, error) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(full))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("entry outside snapshot root")
	}
	return filepath.ToSlash(rel), nil
}

// dumpFile never lets request paths change the Restic repository or snapshot.
func (r *repository) dumpFile(ctx context.Context, id, fullPath string, writer io.Writer) error {
	if !validResticID(id) || !filepath.IsAbs(fullPath) {
		return errors.New("invalid restic dump path")
	}
	cmd := r.command(ctx, nil, "dump", id, filepath.ToSlash(fullPath))
	var stderr boundedOutput
	stderr.max = 4096
	cmd.Stderr = &stderr
	cmd.Stdout = writer
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic dump: %w: %s", err, stderr.String())
	}
	return nil
}

// zipSelection composes a ZIP64-compatible archive directly on the HTTP stream
// from the selected immutable snapshot. It never needs a second local copy.
func (r *repository) zipSelection(ctx context.Context, snapshotID, root string, selections []string, w io.Writer) error {
	if len(selections) > 64 {
		return errors.New("too many selected paths")
	}
	requested := map[string]bool{}
	for _, v := range selections {
		safe, err := cleanRel(v)
		if err != nil {
			return err
		}
		requested[safe] = true
	}
	if len(requested) == 0 {
		requested["."] = true
	}
	entries, err := r.listTree(ctx, snapshotID)
	if err != nil {
		return err
	}
	type item struct {
		full, relative string
		size           uint64
		mtime          time.Time
	}
	picked := []item{}
	var total uint64
	for _, e := range entries {
		rel, err := relativeTo(root, e.Path)
		if err != nil {
			continue
		}
		if rel == "." {
			continue
		}
		include := requested["."]
		for p := range requested {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				include = true
				break
			}
		}
		if !include {
			continue
		}
		if e.Type == "dir" {
			continue
		}
		if e.Type != "file" {
			continue
		} // never dereference symlinks during export
		if strings.ContainsRune(rel, '\x00') || strings.HasPrefix(rel, "../") {
			return errors.New("unsafe path from repository")
		}
		total += e.Size
		if total > 1<<43 {
			return errors.New("export exceeds configured 8 TiB safety limit")
		}
		picked = append(picked, item{full: e.Path, relative: rel, size: e.Size, mtime: e.MTime})
	}
	if len(picked) == 0 {
		return errors.New("selection not found or contains no regular files")
	}
	if len(picked) > 500000 {
		return errors.New("too many files for one export")
	}
	zw := zip.NewWriter(w)
	hashes := map[string]string{}
	for _, f := range picked {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := &zip.FileHeader{Name: f.relative, Method: zip.Deflate}
		h.SetModTime(f.mtime)
		target, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		digest := sha256.New()
		if err = r.dumpFile(ctx, snapshotID, f.full, io.MultiWriter(target, digest)); err != nil {
			return err
		}
		hashes[f.relative] = hex.EncodeToString(digest.Sum(nil))
	}
	content, err := json.MarshalIndent(struct {
		Snapshot string            `json:"snapshot"`
		Included []string          `json:"included"`
		SHA256   map[string]string `json:"sha256"`
	}{snapshotID, selections, hashes}, "", "  ")
	if err != nil {
		return err
	}
	mf, err := zw.Create("backuphub-export-manifest.json")
	if err != nil {
		return err
	}
	if _, err = mf.Write(content); err != nil {
		return err
	}
	return zw.Close()
}
func (r *repository) downloadSingle(ctx context.Context, id, root, requested string, w http.ResponseWriter) error {
	rel, err := cleanRel(requested)
	if err != nil || rel == "." {
		return errors.New("a single file is required")
	}
	entries, err := r.listTree(ctx, id)
	if err != nil {
		return err
	}
	for _, e := range entries {
		candidate, err := relativeTo(root, e.Path)
		if err == nil && candidate == rel && e.Type == "file" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(rel)))
			return r.dumpFile(ctx, id, e.Path, w)
		}
	}
	return errors.New("file not present in the selected snapshot")
}
