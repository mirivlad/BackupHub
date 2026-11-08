// Package restic implements the BackupHub repository contract using Restic's
// documented CLI, without invoking a shell or relying on user backup scripts.
// This first adapter supports a local filesystem repository only. A successful
// Restic snapshot is not itself a completed BackupHub application backup:
// a separate portable commit manifest is the only publishable checkpoint.
package restic

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/mirivlad/BackupHub/internal/domain"
	"github.com/mirivlad/BackupHub/internal/storage"
)

const portableFormat = "backuphub.restic.objects.v1"
const manifestFilename = "backuphub-manifest.json"
const maxManifestSize = 4 << 20

var ErrNotFound = errors.New("committed BackupHub snapshot not found")
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var objectPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Config struct {
	Binary       string
	Repository   string
	PasswordFile string
}

type Engine struct{ binary, repo, passwordFile string }

var _ storage.Engine = (*Engine)(nil)

// New only accepts a local repository for now. Passwords are not passed on
// argv or environment variables, and a symlinked/loose password file is refused.
func New(cfg Config) (*Engine, error) {
	if cfg.Binary == "" {
		cfg.Binary = "restic"
	}
	binary, err := exec.LookPath(cfg.Binary)
	if err != nil {
		return nil, fmt.Errorf("restic binary: %w", err)
	}
	if !filepath.IsAbs(cfg.Repository) || !filepath.IsAbs(cfg.PasswordFile) {
		return nil, errors.New("repository and password file must use absolute local paths")
	}
	repo := filepath.Clean(cfg.Repository)
	if repo == string(filepath.Separator) {
		return nil, errors.New("repository cannot be filesystem root")
	}
	info, err := os.Lstat(cfg.PasswordFile)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("password file must be a regular, private (0600) file")
	}
	return &Engine{binary: binary, repo: repo, passwordFile: cfg.PasswordFile}, nil
}

func (e *Engine) command(ctx context.Context, stdin io.Reader, args ...string) *exec.Cmd {
	fixed := []string{"--repo", e.repo, "--password-file", e.passwordFile, "--no-cache"}
	cmd := exec.CommandContext(ctx, e.binary, append(fixed, args...)...)
	cmd.Stdin = stdin
	// The local adapter intentionally does not inherit RESTIC_* or AWS_*
	// variables. An untrusted parent process must not override the repo.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C"}
	return cmd
}

type limitedWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.buf.Len() < w.max {
		_, _ = w.buf.Write(p[:min(n, w.max-w.buf.Len())])
	}
	return n, nil
}
func (w *limitedWriter) String() string { return w.buf.String() }
func (e *Engine) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := e.command(ctx, stdin, args...)
	var stdout limitedWriter
	stdout.max = 8 << 20
	var stderr limitedWriter
	stderr.max = 4 << 10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Restic exit code 3 is a PARTIAL backup and must always fail.
		return nil, fmt.Errorf("restic %s failed: %w: %s", args[0], err, stderr.String())
	}
	return stdout.buf.Bytes(), nil
}

// Init is explicit and never overwrites an existing repository.
func (e *Engine) Init(ctx context.Context) error {
	_, err := e.run(ctx, nil, "init")
	return err
}
func (e *Engine) Check(ctx context.Context, readData bool) error {
	args := []string{"check"}
	if readData {
		args = append(args, "--read-data")
	}
	_, err := e.run(ctx, nil, args...)
	return err
}

// Each Put creates a provisional Restic object snapshot. Abort never deletes
// these objects: recovery and protected retention must decide safe cleanup.
type transaction struct {
	mu      sync.Mutex
	engine  *Engine
	dataset string
	objects map[string]object
	closed  bool
}
type object struct {
	SnapshotID string `json:"snapshot_id"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}
type portableManifest struct {
	Format   string            `json:"format"`
	Snapshot domain.Snapshot   `json:"snapshot"`
	Objects  map[string]object `json:"objects"`
}

func (e *Engine) Begin(ctx context.Context, datasetID string) (storage.Transaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !idPattern.MatchString(datasetID) {
		return nil, errors.New("invalid dataset ID")
	}
	return &transaction{engine: e, dataset: datasetID, objects: make(map[string]object)}, nil
}
func validName(name string) bool {
	if len(name) < 1 || len(name) > 180 || name == manifestFilename || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	return true
}
func (t *transaction) Put(ctx context.Context, name string, data io.Reader) (domain.Component, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return domain.Component{}, errors.New("transaction already closed")
	}
	if !validName(name) || data == nil {
		return domain.Component{}, errors.New("invalid component")
	}
	if _, exists := t.objects[name]; exists {
		return domain.Component{}, errors.New("duplicate component")
	}
	hash := sha256.New()
	counter := &byteCounter{}
	tee := io.TeeReader(data, io.MultiWriter(hash, counter))
	resticID, err := t.engine.backup(ctx, name, tee, "bh-pending")
	if err != nil {
		return domain.Component{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	t.objects[name] = object{SnapshotID: resticID, Size: counter.total, SHA256: digest}
	return domain.Component{Name: name, Required: true, Captured: true, SizeBytes: counter.total, SHA256: digest}, nil
}

type byteCounter struct{ total int64 }

func (w *byteCounter) Write(p []byte) (int, error) { w.total += int64(len(p)); return len(p), nil }
func (e *Engine) backup(ctx context.Context, name string, data io.Reader, tag string) (string, error) {
	output, err := e.run(ctx, data, "backup", "--stdin", "--stdin-filename", name, "--tag", tag, "--json")
	if err != nil {
		return "", err
	}
	var snapshotID string
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return "", fmt.Errorf("invalid restic JSON: %w", err)
		}
		if event.MessageType == "summary" {
			snapshotID = event.SnapshotID
		}
	}
	if !objectPattern.MatchString(snapshotID) {
		return "", errors.New("restic did not report a full snapshot ID")
	}
	return snapshotID, nil
}
func (t *transaction) Commit(ctx context.Context, manifest domain.Snapshot) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("transaction already closed")
	}
	if manifest.DatasetID != t.dataset || !idPattern.MatchString(manifest.ID) {
		return "", errors.New("dataset or snapshot identity mismatch")
	}
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	if !manifest.Usable() {
		return "", errors.New("uncommitted snapshot cannot publish")
	}
	if len(manifest.Components) != len(t.objects) {
		return "", errors.New("manifest does not account for all stored objects")
	}
	for _, part := range manifest.Components {
		obj, exists := t.objects[part.Name]
		if !exists || !part.Captured || part.SHA256 != obj.SHA256 || part.SizeBytes != obj.Size {
			return "", fmt.Errorf("component mismatch: %s", part.Name)
		}
	}
	_, err := t.engine.readPortable(ctx, manifest.ID)
	if err == nil {
		return "", errors.New("snapshot ID already committed")
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	portable := portableManifest{Format: portableFormat, Snapshot: manifest, Objects: t.objects}
	data, err := json.Marshal(portable)
	if err != nil {
		return "", err
	}
	if len(data) > maxManifestSize {
		return "", errors.New("portable manifest exceeds 4 MiB limit")
	}
	// A manifest snapshot, written last, is the only commit marker. Orphaned
	// provisional parts are never advertised as a successful BackupHub backup.
	id, err := t.engine.backup(ctx, manifestFilename, bytes.NewReader(data), "bh-commit-"+manifest.ID)
	if err != nil {
		return "", err
	}
	t.closed = true
	return id, nil
}
func (t *transaction) Abort(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	// Intentionally leave orphaned provisional Restic snapshots; the eventual
	// privileged GC must preserve any objects referenced by committed manifests.
	return nil
}
func (e *Engine) readPortable(ctx context.Context, id string) (portableManifest, error) {
	var empty portableManifest
	if !idPattern.MatchString(id) {
		return empty, errors.New("invalid snapshot ID")
	}
	data, err := e.run(ctx, nil, "snapshots", "--tag", "bh-commit-"+id, "--json")
	if err != nil {
		return empty, err
	}
	var matching []struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}
	if err = json.Unmarshal(data, &matching); err != nil {
		return empty, fmt.Errorf("restic snapshot list: %w", err)
	}
	ids := make([]string, 0, 1)
	for _, item := range matching {
		for _, tag := range item.Tags {
			if tag == "bh-commit-"+id {
				ids = append(ids, item.ID)
				break
			}
		}
	}
	if len(ids) == 0 {
		return empty, ErrNotFound
	}
	if len(ids) != 1 {
		return empty, errors.New("ambiguous duplicate committed manifest")
	}
	if !objectPattern.MatchString(ids[0]) {
		return empty, errors.New("invalid repository snapshot ID")
	}
	raw, err := e.dumpLimited(ctx, ids[0], manifestFilename, maxManifestSize+1)
	if err != nil {
		return empty, err
	}
	if len(raw) > maxManifestSize {
		return empty, errors.New("oversized manifest")
	}
	if err = json.Unmarshal(raw, &empty); err != nil {
		return portableManifest{}, fmt.Errorf("portable manifest JSON: %w", err)
	}
	if empty.Format != portableFormat || empty.Snapshot.ID != id || !empty.Snapshot.Usable() {
		return portableManifest{}, errors.New("invalid committed manifest")
	}
	if len(empty.Objects) != len(empty.Snapshot.Components) {
		return portableManifest{}, errors.New("manifest object count mismatch")
	}
	for _, c := range empty.Snapshot.Components {
		obj, ok := empty.Objects[c.Name]
		if !ok || !objectPattern.MatchString(obj.SnapshotID) || obj.SHA256 != c.SHA256 || obj.Size != c.SizeBytes {
			return portableManifest{}, errors.New("manifest component reference mismatch")
		}
	}
	return empty, nil
}
func (e *Engine) ReadManifest(ctx context.Context, id string) (domain.Snapshot, error) {
	m, err := e.readPortable(ctx, id)
	if err != nil {
		return domain.Snapshot{}, err
	}
	return m.Snapshot, nil
}
func (e *Engine) dumpLimited(ctx context.Context, snapshotID, name string, maxSize int64) ([]byte, error) {
	cmd := e.command(ctx, nil, "dump", snapshotID, "/"+name)
	var stderr limitedWriter
	stderr.max = 4096
	cmd.Stderr = &stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(output, maxSize))
	if readErr != nil || int64(len(data)) == maxSize {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, fmt.Errorf("restic dump: %w: %s", waitErr, stderr.String())
	}
	return data, nil
}

// Streaming dumps never materialize full datasets in memory.
type readProcess struct {
	stdout   io.ReadCloser
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	stderr   *limitedWriter
	once     sync.Once
	waitErr  error
	finished bool
}

func (p *readProcess) finish() {
	p.once.Do(func() { p.waitErr = p.cmd.Wait(); p.finished = true; p.cancel() })
}
func (p *readProcess) Read(dst []byte) (int, error) {
	n, err := p.stdout.Read(dst)
	if err == io.EOF {
		p.finish()
		if p.waitErr != nil {
			return n, fmt.Errorf("restic dump: %w: %s", p.waitErr, p.stderr.String())
		}
	}
	return n, err
}
func (p *readProcess) Close() error {
	if !p.finished {
		p.cancel()
		_ = p.stdout.Close()
	}
	p.finish()
	if p.finished && p.waitErr != nil && !errors.Is(p.waitErr, context.Canceled) {
		// If Close is called before EOF the caller deliberately aborted the stream.
		return nil
	}
	return nil
}
func (e *Engine) Read(ctx context.Context, snapshotID, component string) (io.ReadCloser, error) {
	manifest, err := e.readPortable(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	obj, ok := manifest.Objects[component]
	if !ok || !validName(component) {
		return nil, ErrNotFound
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := e.command(processCtx, nil, "dump", obj.SnapshotID, "/"+component)
	var stderr limitedWriter
	stderr.max = 4096
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	return &readProcess{stdout: stdout, cmd: cmd, cancel: cancel, stderr: &stderr}, nil
}
func (e *Engine) Verify(ctx context.Context, id string) error {
	manifest, err := e.readPortable(ctx, id)
	if err != nil {
		return err
	}
	for _, component := range manifest.Snapshot.Components {
		source, err := e.Read(ctx, id, component.Name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if size != component.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != component.SHA256 {
			return fmt.Errorf("component digest mismatch: %s", component.Name)
		}
	}
	return nil
}

// ListCommitted discovers recoverable manifests without SQLite; no special
// catalog indexes or old BackupHub daemon are required.
func (e *Engine) ListCommitted(ctx context.Context) ([]domain.Snapshot, error) {
	raw, err := e.run(ctx, nil, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var all []struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	}
	if err = json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	found := map[string]bool{}
	result := []domain.Snapshot{}
	for _, item := range all {
		for _, tag := range item.Tags {
			if !strings.HasPrefix(tag, "bh-commit-") {
				continue
			}
			id := strings.TrimPrefix(tag, "bh-commit-")
			if found[id] {
				break
			}
			found[id] = true
			m, err := e.ReadManifest(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("broken committed snapshot %s: %w", id, err)
			}
			result = append(result, m)
			break
		}
	}
	return result, nil
}

// A random ID for test/CLI plans, not a repository pointer.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
