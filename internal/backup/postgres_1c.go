package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	postgres1CKind   = "postgres_1c"
	postgres1CBundle = "backuphub.postgres-1c.v1"
)

var postgresSnapshotID = regexp.MustCompile(`^[0-9A-Fa-f]+-[0-9A-Fa-f]+-[0-9]+$`)

type postgres1CMetadata struct {
	ConfigSupported  bool              `json:"config_supported"`
	Version          int               `json:"server_version_num"`
	Distribution     string            `json:"server_version"`
	Database         string            `json:"database"`
	Encoding         string            `json:"encoding"`
	Collation        string            `json:"collation"`
	CType            string            `json:"ctype"`
	LocaleProvider   string            `json:"locale_provider"`
	ProviderLocale   string            `json:"provider_locale,omitempty"`
	CollationVersion string            `json:"collation_version,omitempty"`
	Extensions       map[string]string `json:"extensions"`
}

type postgres1CFile struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// The exporter holds one MVCC point across pg_dump and the binary COPY.
// The archive retains the table DDL and dependencies in their native sections.
func capturePostgres1C(ctx context.Context, settings Settings, password, target string) error {
	host, port := settings.Host, "5432"
	if h, p, err := net.SplitHostPort(host); err == nil {
		host, port = h, p
	}
	command := func(name string, args ...string) *exec.Cmd {
		cmd := operationCommand(ctx, nil, name, args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "PGHOST=" + host, "PGPORT=" + port,
			"PGUSER=" + settings.Username, "PGDATABASE=" + settings.Database, "PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=" + connectSeconds(ctx)}
		return cmd
	}
	exporter := command("psql", "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1")
	input, err := exporter.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := exporter.StdoutPipe()
	if err != nil {
		return err
	}
	var exporterError boundedOutput
	exporterError.max = 4096
	exporter.Stderr = &exporterError
	if err = exporter.Start(); err != nil {
		return fmt.Errorf("start PostgreSQL 1C snapshot session: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = exporter.Process.Kill()
			_ = exporter.Wait()
		}
	}()

	const start = `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL lock_timeout = '%ss';
LOCK TABLE public.config IN ACCESS SHARE MODE;
SELECT pg_export_snapshot(), json_build_object(
 'server_version_num', current_setting('server_version_num')::integer, 'server_version', version(),
 'database', current_database(), 'encoding', current_setting('server_encoding'),
 'collation', d.datcollate, 'ctype', d.datctype,
 'locale_provider', CASE to_jsonb(d)->>'datlocprovider'
   WHEN 'i' THEN 'icu' WHEN 'b' THEN 'builtin' ELSE 'libc' END,
 'provider_locale', COALESCE(to_jsonb(d)->>'datlocale',to_jsonb(d)->>'daticulocale'),
 'collation_version', to_jsonb(d)->>'datcollversion',
 'extensions', (SELECT json_object_agg(extname,extversion) FROM pg_extension),
 'config_supported', EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid
 JOIN pg_attribute a ON a.attrelid=c.oid
 WHERE n.nspname='public' AND c.relname='config' AND c.relkind='r' AND NOT c.relispartition
 AND a.attname='binarydata' AND NOT a.attisdropped AND a.atttypid='pg_catalog.bytea'::regtype
 AND NOT EXISTS (SELECT 1 FROM pg_inherits WHERE inhparent=c.oid)))
FROM pg_database d WHERE d.datname=current_database();`
	if _, err = io.WriteString(input, fmt.Sprintf(start, connectSeconds(ctx))+"\n"); err != nil {
		return fmt.Errorf("start PostgreSQL 1C transaction: %w", err)
	}
	row, err := bufio.NewReaderSize(progressReader{ctx, output}, 128<<10).ReadSlice('\n')
	if err != nil {
		_ = exporter.Process.Kill()
		waitErr := exporter.Wait()
		finished = true
		return fmt.Errorf("read PostgreSQL 1C snapshot: %w %s", errors.Join(err, waitErr), exporterError.String())
	}
	snapshot, raw, ok := strings.Cut(string(row), "|")
	var meta postgres1CMetadata
	if !ok || !postgresSnapshotID.MatchString(snapshot) || json.Unmarshal([]byte(raw), &meta) != nil ||
		meta.Version < 100000 {
		return errors.New("invalid PostgreSQL 1C snapshot metadata")
	}
	if !meta.ConfigSupported {
		return errors.New("PostgreSQL 1C requires a plain public.config table with a binarydata bytea column")
	}

	dumpPath := filepath.Join(target, "database.dump")
	stopDump := watchFileProgress(ctx, dumpPath)
	err = runPostgres1C(command("pg_dump", "-w", "--format=custom", "--no-owner", "--no-acl",
		"--lock-wait-timeout="+connectSeconds(ctx)+"s", "--exclude-table-data=public.config", "--snapshot="+snapshot, "--file="+dumpPath))
	stopDump()
	if err != nil {
		return err
	}
	var toc boundedOutput
	toc.max = 16 << 20
	list := command("pg_restore", "--list", dumpPath)
	list.Stdout = progressWriter{ctx, &toc}
	if err = runPostgres1C(list); err != nil {
		return err
	}
	if err = checkPostgres1CTOC(toc.String()); err != nil {
		return err
	}

	configPath := filepath.Join(target, "config.copy")
	config, err := os.OpenFile(configPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	copyCmd := command("psql", "-X", "-w", "-q", "-v", "ON_ERROR_STOP=1",
		"-c", "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY",
		"-c", "SET TRANSACTION SNAPSHOT '"+snapshot+"'",
		"-c", "SET LOCAL row_security=off",
		"-c", "COPY public.config TO STDOUT WITH (FORMAT binary)", "-c", "COMMIT")
	copyCmd.Stdout = progressWriter{ctx, config}
	if err = errors.Join(runPostgres1C(copyCmd), config.Close()); err != nil {
		return err
	}
	if err = checkPostgres1CBinary(configPath); err != nil {
		return err
	}
	if _, err = io.WriteString(input, "COMMIT;\n\\q\n"); err != nil {
		return err
	}
	err = exporter.Wait()
	finished = true
	if err != nil {
		return fmt.Errorf("close PostgreSQL 1C snapshot session: %w", err)
	}
	return writePostgres1CManifest(ctx, target, meta)
}

func runPostgres1C(cmd *exec.Cmd) error {
	var stderr boundedOutput
	stderr.max = 4096
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(cmd.Path), err, stderr.String())
	}
	return nil
}

func checkPostgres1CTOC(contents string) error {
	hasSchema := false
	for _, line := range strings.Split(contents, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || strings.HasPrefix(line, ";") {
			continue
		}
		if f[3] == "TABLE" && f[4] == "public" && f[5] == "config" {
			hasSchema = true
		}
		if len(f) >= 7 && f[3] == "TABLE" && f[4] == "DATA" && f[5] == "public" && f[6] == "config" {
			return errors.New("PostgreSQL 1C archive includes unexpected config data")
		}
	}
	if !hasSchema {
		return errors.New("PostgreSQL 1C archive is missing the public.config schema")
	}
	return nil
}

func checkPostgres1CBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	const signature = "PGCOPY\n\xff\r\n\x00"
	header, trailer := make([]byte, len(signature)), make([]byte, 2)
	if info.Size() < 21 {
		return errors.New("PostgreSQL 1C binary COPY is incomplete")
	}
	if _, err = io.ReadFull(f, header); err != nil {
		return err
	}
	if _, err = f.ReadAt(trailer, info.Size()-2); err != nil {
		return err
	}
	if string(header) != signature || trailer[0] != 0xff || trailer[1] != 0xff {
		return errors.New("PostgreSQL 1C binary COPY has invalid framing")
	}
	return nil
}

func writePostgres1CManifest(ctx context.Context, target string, meta postgres1CMetadata) error {
	files := make(map[string]postgres1CFile)
	for _, name := range []string{"database.dump", "config.copy"} {
		f, err := os.Open(filepath.Join(target, name))
		if err != nil {
			return err
		}
		digest := sha256.New()
		size, copyErr := io.Copy(digest, progressReader{ctx, f})
		if err = errors.Join(copyErr, f.Close()); err != nil {
			return err
		}
		files[name] = postgres1CFile{Bytes: size, SHA256: hex.EncodeToString(digest.Sum(nil))}
	}
	data, err := json.MarshalIndent(struct {
		Format       string                    `json:"format"`
		Source       postgres1CMetadata        `json:"source"`
		Files        map[string]postgres1CFile `json:"files"`
		RestoreOrder []string                  `json:"restore_order"`
	}{postgres1CBundle, meta, files, []string{"pre-data", "data", "config.copy", "post-data"}}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "backuphub-postgres-1c.json"), data, 0600)
}
