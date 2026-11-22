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
	"mime"
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
const (
	PasswordMode   = "password"
	NoPasswordMode = "no_password"
)

var errResticRepositoryLocked = errors.New("restic repository locked")

func validPasswordMode(mode string) bool { return mode == PasswordMode || mode == NoPasswordMode }

// Restic 0.17.0 introduced this global option. Reject unsupported versions
// before preparing a passwordless repository, never create a mixed-mode repo.
var ErrPasswordlessResticUnsupported = errors.New("passwordless storage requires restic 0.17.0 or newer")

func checkPasswordlessRestic(ctx context.Context, binary string) error {
	cmd := exec.CommandContext(ctx, binary, "--insecure-no-password", "version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrPasswordlessResticUnsupported, err)
	}
	return nil
}

type repository struct{ binary, path, passwordFile, passwordMode string }

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

// initRepository uses an on-disk initiation marker and creates Restic into
// a sibling temporary directory. A crash before publishing leaves the target
// repository empty, so an interrupted first start can finish using the same
// persistent password. Existing repositories never get silently recreated.
// Reject symlinked ancestors before writing or reading a storage path.
func validateRealParents(p string) error {
	if !filepath.IsAbs(p) {
		return errors.New("repository path must be absolute")
	}
	for current := filepath.Dir(filepath.Clean(p)); current != "/"; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("repository parent is not a real directory")
		}
	}
	return nil
}

func initRepository(ctx context.Context, stateDir, repoPath string) (*repository, error) {
	return initRepositoryNamed(ctx, stateDir, repoPath, filepath.Join(stateDir, "restic.key"), "restic.init-in-progress")
}

func initRepositoryNamed(ctx context.Context, stateDir, repoPath, keyPath, markerFilename string) (*repository, error) {
	return initRepositoryNamedMode(ctx, stateDir, repoPath, keyPath, markerFilename, PasswordMode)
}

// Mode is immutable for a repository. A passwordless repository still uses
// Restic encryption internally, but has no external recovery secret.
func initRepositoryNamedMode(ctx context.Context, stateDir, repoPath, keyPath, markerFilename, mode string) (*repository, error) {
	if !validPasswordMode(mode) {
		return nil, errors.New("invalid repository password mode")
	}
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
	if mode == NoPasswordMode {
		if err = checkPasswordlessRestic(ctx, binary); err != nil {
			return nil, err
		}
	}
	r := &repository{binary: binary, path: filepath.Clean(repoPath), passwordFile: keyPath, passwordMode: mode}
	if err = validateRealParents(r.path); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(r.path, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(r.path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("repository directory must be real and private")
	}
	marker := filepath.Join(stateDir, markerFilename)
	markerInfo, markerErr := os.Lstat(marker)
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return nil, markerErr
	}
	if markerErr == nil && (!markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm()&0077 != 0) {
		return nil, errors.New("repository init marker is unsafe")
	}
	keyInfo, keyErr := os.Lstat(r.passwordFile)
	if keyErr != nil && !os.IsNotExist(keyErr) {
		return nil, keyErr
	}
	repoInfo, repoErr := os.Lstat(filepath.Join(r.path, "config"))
	if repoErr != nil && !os.IsNotExist(repoErr) {
		return nil, repoErr
	}
	if mode == NoPasswordMode {
		// Never tolerate a stray key file: this would disguise a mode mismatch.
		if keyErr == nil {
			return nil, errors.New("passwordless repository unexpectedly has a key file")
		}
	} else if keyErr == nil {
		if !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0077 != 0 {
			return nil, errors.New("repository key is not private")
		}
	}
	if repoErr == nil {
		if (mode == PasswordMode && keyErr != nil) || !repoInfo.Mode().IsRegular() {
			return nil, errors.New("repository/key mismatch: refusing to overwrite existing or lost Restic credentials")
		}
		if _, err = r.run(ctx, nil, "cat", "config"); err != nil {
			return nil, fmt.Errorf("cannot open repository: %w", err)
		}
		if markerErr == nil {
			if err = os.Remove(marker); err != nil {
				return nil, err
			}
		}
		return r, nil
	}
	// Config is missing. Only an empty final repository may be initialized.
	entries, err := os.ReadDir(r.path)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("existing repository is incomplete or damaged; recovery required")
	}
	if mode == PasswordMode && keyErr == nil {
		// A key without a persisted initialization marker may indicate a deleted
		// existing repository. Never silently recreate it.
		if markerErr != nil {
			return nil, errors.New("repository config lost: refusing silent recreation")
		}
	} else if markerErr != nil {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if _, err = f.WriteString("initializing new repository\n"); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	if mode == PasswordMode && keyErr != nil {
		key := make([]byte, 48)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(r.passwordFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if _, err = f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err = f.Sync(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	// Sibling directory ensures atomic publication even on a separate mounted
	// storage volume. Only an empty directory can be replaced by Rename.
	candidate, err := os.MkdirTemp(filepath.Dir(r.path), filepath.Base(r.path)+".init-")
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(candidate)
		}
	}()
	temp := &repository{binary: r.binary, path: candidate, passwordFile: r.passwordFile, passwordMode: mode}
	if _, err = temp.run(ctx, nil, "init"); err != nil {
		return nil, fmt.Errorf("cannot initialize temporary repository: %w", err)
	}
	if _, err = temp.run(ctx, nil, "snapshots", "--json"); err != nil {
		return nil, fmt.Errorf("cannot verify temporary repository: %w", err)
	}
	// A directory with a 'config' and data cannot be renamed over an
	// existing directory on all supported filesystems. Remove only the empty
	// placeholder; os.Remove fails closed if files have appeared in it.
	entries, err = os.ReadDir(r.path)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, errors.New("repository target changed during initialization")
	}
	if err = os.Remove(r.path); err != nil {
		return nil, fmt.Errorf("cannot remove empty repository placeholder: %w", err)
	}
	if err = os.Rename(candidate, r.path); err != nil {
		return nil, fmt.Errorf("cannot publish repository: %w", err)
	}
	published = true
	if err = os.Remove(marker); err != nil {
		return nil, err
	}
	return r, nil
}

// openRepositoryNamed never creates directories or credentials: a disconnected
// mount must never silently become a brand-new repository on the system disk.
func openRepositoryNamed(ctx context.Context, stateDir, repoPath, keyPath string) (*repository, error) {
	return openRepositoryNamedMode(ctx, stateDir, repoPath, keyPath, PasswordMode)
}

func openRepositoryNamedMode(ctx context.Context, stateDir, repoPath, keyPath, mode string) (*repository, error) {
	r, err := existingRepository(repoPath, keyPath, mode)
	if err != nil {
		return nil, err
	}
	if _, err = r.run(ctx, nil, "cat", "config"); err != nil {
		return nil, fmt.Errorf("cannot open repository: %w", err)
	}
	return r, nil
}

// Look up paths and credentials without starting a Restic read operation.
// The requested command negotiates its own Restic lock.
func existingRepository(repoPath, keyPath, mode string) (*repository, error) {
	if !validPasswordMode(mode) {
		return nil, errors.New("invalid repository password mode")
	}
	if !filepath.IsAbs(repoPath) || !filepath.IsAbs(keyPath) {
		return nil, errors.New("repository path must be absolute")
	}
	binary, err := exec.LookPath("restic")
	if err != nil {
		return nil, err
	}
	r := &repository{binary: binary, path: filepath.Clean(repoPath), passwordFile: keyPath, passwordMode: mode}
	if err = validateRealParents(r.path); err != nil {
		return nil, err
	}
	for _, p := range []string{r.path, filepath.Dir(r.path)} {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("storage unavailable: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("unsafe repository path")
		}
	}
	if mode == PasswordMode {
		keyInfo, err := os.Lstat(keyPath)
		if err != nil {
			return nil, errors.Join(errStorageCredentials, err)
		}
		if !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("%w: repository key must be private", errStorageCredentials)
		}
	} else {
		if _, err := os.Lstat(keyPath); err == nil {
			return nil, errors.New("unexpected password file in passwordless mode")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	config, err := os.Lstat(filepath.Join(r.path, "config"))
	if err != nil {
		return nil, fmt.Errorf("repository unavailable: %w", err)
	}
	if !config.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: invalid Restic repository config", errRepositoryIntegrity)
	}
	return r, nil
}

func (r *repository) command(ctx context.Context, stdin io.Reader, args ...string) *exec.Cmd {
	base := []string{"--repo", r.path, "--no-cache"}
	if r.passwordMode == NoPasswordMode {
		base = append(base, "--insecure-no-password")
	} else {
		base = append(base, "--password-file", r.passwordFile)
	}
	cmd := operationCommand(ctx, nil, r.binary, append(base, args...)...)
	// Let Restic release its repository lock before forcing termination.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	cmd.Stdin = stdin
	return cmd
}
func (r *repository) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := r.command(ctx, stdin, args...)
	var stdout, stderr boundedOutput
	stdout.max = 16 << 20
	stderr.max = 8192
	cmd.Stdout = progressWriter{ctx, &stdout}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("restic %s: %w: %s", args[0], err, stderr.String())
	}
	noteProgress(ctx)
	return stdout.buf.Bytes(), nil
}

func isResticLockConflict(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && (exit.ExitCode() == 11 ||
		(exit.ExitCode() == 1 && strings.Contains(err.Error(), "repository is already locked")))
}

// Stock Restic unlock can remove a live lock based solely on its age.
func resticLockError(err error) error {
	if isResticLockConflict(err) {
		return errors.Join(errResticRepositoryLocked, err)
	}
	return err
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
	cmd.Env = append(cmd.Env, "RESTIC_PROGRESS_FPS=4")
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
	var files, bytes, discoveredFiles, discoveredBytes uint64
	for scanner.Scan() {
		var line struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
			FilesDone   uint64 `json:"files_done"`
			BytesDone   uint64 `json:"bytes_done"`
			TotalFiles  uint64 `json:"total_files"`
			TotalBytes  uint64 `json:"total_bytes"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.MessageType == "summary" {
			snapshot = line.SnapshotID
		}
		if line.FilesDone > files || line.BytesDone > bytes || line.TotalFiles > discoveredFiles || line.TotalBytes > discoveredBytes {
			noteProgress(ctx)
			files, bytes = max(files, line.FilesDone), max(bytes, line.BytesDone)
			discoveredFiles, discoveredBytes = max(discoveredFiles, line.TotalFiles), max(discoveredBytes, line.TotalBytes)
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
	return r.fullSnapshotID(ctx, snapshot)
}

// Older Restic versions report an eight-character ID in the backup summary.
// Resolve only that exact prefix, with ambiguity treated as a failed commit.
func (r *repository) fullSnapshotID(ctx context.Context, id string) (string, error) {
	if validResticID(id) {
		return id, nil
	}
	if len(id) != 8 {
		return "", errors.New("restic did not return a snapshot ID")
	}
	if _, err := hex.DecodeString(id); err != nil || strings.ToLower(id) != id {
		return "", errors.New("restic returned an invalid snapshot ID")
	}
	raw, err := r.run(ctx, nil, "snapshots", "--json", id)
	if err != nil {
		return "", err
	}
	var found []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &found); err != nil || len(found) != 1 ||
		!validResticID(found[0].ID) || !strings.HasPrefix(found[0].ID, id) {
		return "", errors.New("restic snapshot prefix did not resolve to one full ID")
	}
	return found[0].ID, nil
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
const (
	portableTreeFormat = "backuphub.restic.tree.v1"
	portableTreeFile   = "backuphub-tree-manifest.json"
)

type portableRecord struct {
	Format            string `json:"format"`
	ID                string `json:"id"`
	AttemptID         string `json:"attempt_id,omitempty"`
	SourceID          string `json:"source_id"`
	SourceName        string `json:"source_name"`
	Kind              string `json:"kind"`
	Root              string `json:"root"`
	ResticID          string `json:"restic_id"`
	At                string `json:"at"`
	DataVerified      bool   `json:"data_verified"`
	Consistency       string `json:"consistency"`
	Fidelity          string `json:"fidelity"`
	CaptureDurationMS *int64 `json:"capture_duration_ms,omitempty"`
}

func (r *repository) commit(ctx context.Context, rec portableRecord) (string, error) {
	if !safeID(rec.ID) || !validResticID(rec.ResticID) {
		return "", errors.New("invalid committed identifiers")
	}
	rec.Format = portableTreeFormat
	blob, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	output, err := r.run(ctx, bytes.NewReader(blob), "backup", "--stdin", "--stdin-filename", portableTreeFile, "--tag", "bh-commit-"+rec.ID, "--json")
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
	return r.fullSnapshotID(ctx, id)
}

type treeEntry struct {
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Size  uint64    `json:"size"`
	Mode  uint32    `json:"mode"`
	Inode uint64    `json:"inode"`
	MTime time.Time `json:"mtime"`
}

func (r *repository) listTree(ctx context.Context, id string) ([]treeEntry, error) {
	entries := make([]treeEntry, 0, 256)
	err := r.walkTree(ctx, id, func(entry treeEntry) error {
		if len(entries) >= 1500000 {
			return errors.New("snapshot file count exceeds safe browse limit")
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *repository) walkTree(ctx context.Context, id string, visit func(treeEntry) error) error {
	if !validResticID(id) {
		return errors.New("invalid snapshot ID")
	}
	return r.readStream(ctx, func(reader io.Reader) error {
		decoder := json.NewDecoder(reader)
		snapshotSeen := false
		for {
			var raw struct {
				StructType string    `json:"struct_type"`
				ID         string    `json:"id"`
				Path       string    `json:"path"`
				Name       string    `json:"name"`
				Type       string    `json:"type"`
				Size       uint64    `json:"size"`
				Mode       uint32    `json:"mode"`
				Inode      uint64    `json:"inode"`
				MTime      time.Time `json:"mtime"`
			}
			if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
				if !snapshotSeen {
					return errors.New("Restic did not return snapshot tree metadata")
				}
				return nil
			} else if err != nil {
				return err
			}
			switch raw.StructType {
			case "snapshot":
				if snapshotSeen || raw.ID != id {
					return errors.New("Restic returned unexpected snapshot tree metadata")
				}
				snapshotSeen = true
			case "node":
				if !snapshotSeen {
					return errors.New("Restic returned a tree node before snapshot metadata")
				}
				entry := treeEntry{Path: raw.Path, Name: raw.Name, Type: raw.Type, Size: raw.Size, Mode: raw.Mode, Inode: raw.Inode, MTime: raw.MTime}
				if err := visit(entry); err != nil {
					return err
				}
			}
		}
	}, "ls", "--json", id)
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
	handled, err := r.optimizedZipSelection(ctx, snapshotID, root, requested, w)
	if handled {
		return err
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
		h := &zip.FileHeader{Name: f.relative, Method: zip.Store}
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
	return finishZIP(zw, snapshotID, requested, hashes)
}
func (r *repository) downloadSingle(ctx context.Context, id, root, requested string, w http.ResponseWriter) error {
	rel, err := cleanRel(requested)
	if err != nil || rel == "." {
		return errors.New("a single file is required")
	}
	full, err := r.resolveSnapshotFile(ctx, id, root, rel)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	// FormatMediaType handles quoted names and non-ASCII filenames (RFC 5987).
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(rel)}))
	return r.dumpFile(ctx, id, full, w)
}
