package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The designated lab connection needs permission to create disposable databases.
func TestPostgres1CBackupAndRestore(t *testing.T) {
	host := os.Getenv("BACKUPHUB_TEST_PG1C_HOST")
	if host == "" {
		t.Skip("no disposable PostgreSQL 1C integration server configured")
	}
	requireIntegrationRestic(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	user := os.Getenv("BACKUPHUB_TEST_PG1C_USER")
	password := os.Getenv("BACKUPHUB_TEST_PG1C_PASSWORD")
	hostName, port := host, "5432"
	if h, p, err := net.SplitHostPort(host); err == nil {
		hostName, port = h, p
	}
	env := append(os.Environ(), "PGUSER="+user, "PGPASSWORD="+password, "PGHOST="+hostName, "PGPORT="+port)
	run := func(stdin io.Reader, name string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env, cmd.Stdin = env, stdin
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	psql := func(database, sql string) string {
		return run(nil, "psql", "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1", "-d", database, "-c", sql)
	}
	suffix, err := randomID()
	if err != nil {
		t.Fatal(err)
	}
	sourceDB, restoredDB := "bh_src_"+suffix, "bh_restore_"+suffix
	for _, db := range []string{sourceDB, restoredDB} {
		psql("postgres", "CREATE DATABASE "+db)
		t.Cleanup(func() {
			cmd := exec.Command("psql", "-X", "-w", "-qAt", "-d", "postgres", "-c", "DROP DATABASE "+db)
			cmd.Env = env
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("clean disposable test database: %v %s", err, output)
			}
		})
	}
	psql(sourceDB, `CREATE TABLE public.config (filename text PRIMARY KEY, binarydata bytea NOT NULL);
		INSERT INTO public.config VALUES ('fixture', decode(repeat('abcd1269',524288),'hex'));
		CREATE TABLE public.app_data (value text REFERENCES public.config(filename));
		INSERT INTO public.app_data VALUES ('fixture');`)
	want := psql(sourceDB, `SELECT filename,octet_length(binarydata),md5(binarydata) FROM public.config`)

	service := serviceFixture(t)
	source, err := service.Create(ctx, CreateSource{Name: "1C fixture", Kind: postgres1CKind, Host: host, Username: user,
		Database: sourceDB, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	job := createTestBackupJob(t, service, ctx, source, PrimaryStorageID)
	if err = service.Manual(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if ran, err := service.scheduler.RunDue(ctx); err != nil || !ran {
		t.Fatalf("1C backup ran=%v: %v", ran, err)
	}
	history, err := service.ListSnapshots(ctx, source.ID)
	if err != nil || len(history) != 1 || history[0].Fidelity != "logical_dump_split_config" {
		t.Fatalf("1C history = %v: %v", history, err)
	}
	dir, err := service.RestoreToNewDirectory(ctx, history[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "backuphub-postgres-1c.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Format string                    `json:"format"`
		Files  map[string]postgres1CFile `json:"files"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.Format != postgres1CBundle || len(manifest.Files) != 2 {
		t.Fatalf("invalid 1C bundle manifest: %v", err)
	}
	for name, file := range manifest.Files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		hash := sha256.Sum256(data)
		if err != nil || int64(len(data)) != file.Bytes || hex.EncodeToString(hash[:]) != file.SHA256 {
			t.Fatalf("1C bundle checksum %s: %v", name, err)
		}
	}
	dump := filepath.Join(dir, "database.dump")
	for _, section := range []string{"pre-data", "data"} {
		run(nil, "pg_restore", "-w", "--exit-on-error", "--no-owner", "--no-acl", "--section="+section, "-d", restoredDB, dump)
	}
	if n := psql(restoredDB, "SELECT count(*) FROM public.config"); n != "0" {
		t.Fatalf("config was duplicated in the main dump: %s", n)
	}
	config, err := os.Open(filepath.Join(dir, "config.copy"))
	if err != nil {
		t.Fatal(err)
	}
	defer config.Close()
	run(config, "psql", "-X", "-w", "-q", "-v", "ON_ERROR_STOP=1", "-d", restoredDB,
		"-c", "COPY public.config FROM STDIN WITH (FORMAT binary)")
	run(nil, "pg_restore", "-w", "--exit-on-error", "--no-owner", "--no-acl", "--section=post-data", "-d", restoredDB, dump)
	if got := psql(restoredDB, "SELECT filename,octet_length(binarydata),md5(binarydata) FROM public.config"); got != want {
		t.Fatalf("configuration changed: got %q want %q", got, want)
	}
	if got := psql(restoredDB, `SELECT count(*) FROM public.app_data a JOIN public.config c ON a.value=c.filename`); got != "1" {
		t.Fatalf("application data did not match config: %s", got)
	}
}
