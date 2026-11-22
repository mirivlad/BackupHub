package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const snapshotIndexCacheKiB = 4096

type snapshotArchiveEntry struct {
	kind, target, subtree string
	size, inode, device   int64
	seen, exact           bool
}

type snapshotIndex struct {
	tx                        *sql.Tx
	insert, get, seen, enrich *sql.Stmt
	remaining                 int64
}

func snapshotIndexRoot(stateDir string) string {
	return filepath.Join(stateDir, "snapshot-checks")
}

func (s *Service) cleanSnapshotIndexes() error {
	root := snapshotIndexRoot(s.catalog.DataDir)
	if err := privateRestoreRoot(root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), "index-")
		if _, err := strconv.ParseUint(suffix, 10, 32); !ok || err != nil || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (r *repository) withSnapshotIndex(ctx context.Context, work func(*snapshotIndex) error) (err error) {
	var root string
	if r.passwordFile != "" {
		root = snapshotIndexRoot(filepath.Dir(r.passwordFile))
		if err := privateRestoreRoot(root); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp(root, "index-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "tree.db")}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA cache_size=-" + strconv.Itoa(snapshotIndexCacheKiB),
		"PRAGMA journal_mode=OFF", "PRAGMA synchronous=OFF", "PRAGMA temp_store=FILE",
		`CREATE TABLE entries(path BLOB PRIMARY KEY, kind TEXT NOT NULL, size INTEGER NOT NULL,
		 inode INTEGER NOT NULL, device INTEGER NOT NULL, target BLOB NOT NULL,
		 exact INTEGER NOT NULL, seen INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID`,
		`CREATE TABLE directories(path BLOB PRIMARY KEY, tree TEXT NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE tree_ids(path BLOB PRIMARY KEY, tree TEXT NOT NULL) WITHOUT ROWID`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	index := &snapshotIndex{tx: tx}
	for _, item := range []struct {
		statement **sql.Stmt
		query     string
	}{
		{&index.insert, `INSERT INTO entries(path,kind,size,inode,device,target,exact) VALUES(?,?,?,?,?,?,?)`},
		{&index.get, `SELECT kind,size,inode,device,target,exact,seen FROM entries WHERE path=?`},
		{&index.seen, `UPDATE entries SET seen=1 WHERE path=?`},
		{&index.enrich, `UPDATE entries SET inode=?,device=?,target=?,exact=1 WHERE path=? AND kind=? AND size=?`},
	} {
		*item.statement, err = tx.PrepareContext(ctx, item.query)
		if err != nil {
			return err
		}
	}
	return work(index)
}

func (i *snapshotIndex) add(ctx context.Context, name string, entry snapshotArchiveEntry) error {
	if !strings.HasPrefix(name, "/") || name == "/" || path.Clean(name) != name || strings.ContainsRune(name, 0) || entry.size < 0 {
		return fmt.Errorf("invalid snapshot tree entry %q", name)
	}
	if _, err := i.insert.ExecContext(ctx, []byte(name), entry.kind, entry.size, entry.inode, entry.device, []byte(entry.target), entry.exact); err != nil {
		return fmt.Errorf("index snapshot tree entry %q: %w", name, err)
	}
	i.remaining++
	return nil
}

func (i *snapshotIndex) lookup(ctx context.Context, name string) (snapshotArchiveEntry, error) {
	var entry snapshotArchiveEntry
	err := i.get.QueryRowContext(ctx, []byte(name)).Scan(&entry.kind, &entry.size, &entry.inode, &entry.device, &entry.target, &entry.exact, &entry.seen)
	return entry, err
}

var errLossyTreeNames = errors.New("Restic ls paths can be lossy")

func archiveNode(kind string) bool { return kind == "file" || kind == "dir" || kind == "symlink" }

func (i *snapshotIndex) load(ctx context.Context, r *repository, id string) error {
	err := r.walkTree(ctx, id, func(entry treeEntry) error {
		// ls encodes names as JSON strings, replacing invalid UTF-8 bytes.
		// A genuine U+FFFD is indistinguishable, so use raw tree objects.
		if strings.ContainsRune(entry.Path, '\uFFFD') {
			return errLossyTreeNames
		}
		if !archiveNode(entry.Type) {
			return nil
		}
		return i.add(ctx, entry.Path, snapshotArchiveEntry{kind: entry.Type, size: int64(entry.Size), inode: int64(entry.Inode)})
	})
	if !errors.Is(err, errLossyTreeNames) {
		return err
	}
	if _, err := i.tx.ExecContext(ctx, "DELETE FROM entries"); err != nil {
		return err
	}
	i.remaining = 0
	return i.loadRaw(ctx, r, id)
}

func (i *snapshotIndex) loadRaw(ctx context.Context, r *repository, id string) error {
	parent := []byte("/")
	tree, err := r.snapshotRootTree(ctx, id)
	if err != nil {
		return err
	}
	for {
		if err := r.readTreeObject(ctx, tree, func(name string, entry snapshotArchiveEntry) error {
			full := path.Join(string(parent), name)
			if !archiveNode(entry.kind) {
				return nil
			}
			if err := i.add(ctx, full, entry); err != nil {
				return err
			}
			if entry.kind == "dir" {
				if !validResticID(entry.subtree) {
					return fmt.Errorf("missing subtree ID for %q", full)
				}
				_, err := i.tx.ExecContext(ctx, "INSERT INTO directories VALUES(?,?)", []byte(full), entry.subtree)
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		err = i.tx.QueryRowContext(ctx, "SELECT path,tree FROM directories ORDER BY path LIMIT 1").Scan(&parent, &tree)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := i.tx.ExecContext(ctx, "DELETE FROM directories WHERE path=?", parent); err != nil {
			return err
		}
	}
}

func (i *snapshotIndex) exactEntry(ctx context.Context, r *repository, id, name string, existing snapshotArchiveEntry) (snapshotArchiveEntry, error) {
	if existing.exact {
		return existing, nil
	}
	parent := path.Dir(name)
	var missing []string
	var tree string
	known := parent
	for {
		err := i.tx.QueryRowContext(ctx, "SELECT tree FROM tree_ids WHERE path=?", []byte(known)).Scan(&tree)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return snapshotArchiveEntry{}, err
		}
		if known == "/" {
			tree, err = r.snapshotRootTree(ctx, id)
			if err != nil {
				return snapshotArchiveEntry{}, err
			}
			if err := i.rememberTree(ctx, known, tree); err != nil {
				return snapshotArchiveEntry{}, err
			}
			break
		}
		missing = append(missing, known)
		known = path.Dir(known)
	}
	for n := len(missing) - 1; n >= 0; n-- {
		if err := i.enrichDirectory(ctx, r, known, tree); err != nil {
			return snapshotArchiveEntry{}, err
		}
		known = missing[n]
		if err := i.tx.QueryRowContext(ctx, "SELECT tree FROM tree_ids WHERE path=?", []byte(known)).Scan(&tree); err != nil {
			return snapshotArchiveEntry{}, fmt.Errorf("missing Restic directory metadata %q: %w", known, err)
		}
	}
	if err := i.enrichDirectory(ctx, r, parent, tree); err != nil {
		return snapshotArchiveEntry{}, err
	}
	entry, err := i.lookup(ctx, name)
	if err == nil && !entry.exact {
		err = fmt.Errorf("Restic tree metadata did not match %q", name)
	}
	return entry, err
}

func (i *snapshotIndex) rememberTree(ctx context.Context, name, tree string) error {
	if !validResticID(tree) {
		return fmt.Errorf("invalid subtree ID for %q", name)
	}
	_, err := i.tx.ExecContext(ctx, "INSERT OR IGNORE INTO tree_ids VALUES(?,?)", []byte(name), tree)
	return err
}

func (i *snapshotIndex) enrichDirectory(ctx context.Context, repo *repository, parent, tree string) error {
	return repo.readTreeObject(ctx, tree, func(base string, node snapshotArchiveEntry) error {
		full := path.Join(parent, base)
		switch node.kind {
		case "dir":
			return i.rememberTree(ctx, full, node.subtree)
		case "file", "symlink":
			_, err := i.enrich.ExecContext(ctx, node.inode, node.device, []byte(node.target), []byte(full), node.kind, node.size)
			return err
		default:
			return nil
		}
	})
}

func (r *repository) snapshotRootTree(ctx context.Context, id string) (string, error) {
	raw, err := r.run(ctx, nil, "cat", "snapshot", id)
	if err != nil {
		return "", err
	}
	var snapshot struct {
		Tree string `json:"tree"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || !validResticID(snapshot.Tree) {
		return "", errors.New("invalid Restic snapshot root tree")
	}
	return snapshot.Tree, nil
}

// Restic quotes raw path-name bytes inside JSON and supplies a separate base64
// field for non-UTF-8 link targets. Decode one node at a time.
func (r *repository) readTreeObject(ctx context.Context, tree string, visit func(string, snapshotArchiveEntry) error) error {
	return r.readStream(ctx, func(reader io.Reader) error {
		dec := json.NewDecoder(reader)
		for _, want := range []json.Token{json.Delim('{'), "nodes"} {
			if token, err := dec.Token(); err != nil || token != want {
				return fmt.Errorf("invalid Restic tree object, expected %q: %v", want, err)
			}
		}
		nodelist, err := dec.Token()
		if err != nil || nodelist != nil && nodelist != json.Delim('[') {
			return errors.New("invalid Restic tree node array")
		}
		for nodelist != nil && dec.More() {
			var node struct {
				Name      string `json:"name"`
				Kind      string `json:"type"`
				Size      uint64 `json:"size"`
				Inode     uint64 `json:"inode"`
				Device    uint64 `json:"device_id"`
				Target    string `json:"linktarget"`
				TargetRaw []byte `json:"linktarget_raw"`
				Subtree   string `json:"subtree"`
			}
			if err := dec.Decode(&node); err != nil {
				return err
			}
			name, err := strconv.Unquote(`"` + node.Name + `"`)
			if err != nil || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
				return fmt.Errorf("invalid Restic node name %q", node.Name)
			}
			if node.TargetRaw != nil {
				node.Target = string(node.TargetRaw)
			}
			if err := visit(name, snapshotArchiveEntry{kind: node.Kind, size: int64(node.Size),
				inode: int64(node.Inode), device: int64(node.Device), target: node.Target, subtree: node.Subtree, exact: true}); err != nil {
				return err
			}
		}
		if nodelist != nil {
			if token, err := dec.Token(); err != nil || token != json.Delim(']') {
				return errors.New("truncated Restic tree node array")
			}
		}
		if token, err := dec.Token(); err != nil || token != json.Delim('}') {
			return errors.New("truncated Restic tree object")
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return errors.New("unexpected data after Restic tree object")
		}
		return nil
	}, "cat", "blob", tree)
}
