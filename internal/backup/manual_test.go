package backup

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/catalog"
)

func manualTestFixture(t *testing.T) (*Service, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO sources(id,kind,display_name,endpoint,settings_json,created_at)
  VALUES('local-1','local','Test','', '{"Host":"","Path":"/tmp","Share":"","Username":"","Database":""}',CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO datasets(id,source_id,display_name,locator)
   VALUES('local-1','local-1','Test','/tmp')`)
	if err != nil {
		t.Fatal(err)
	}
	planned := time.Now().Add(24 * time.Hour).Unix()
	_, err = c.DB.Exec(`INSERT INTO schedule_entries(id,dataset_id,kind,interval_seconds,next_run_unix,enabled,created_at)
  VALUES('job-1','local-1','backup',3600,?,1,CURRENT_TIMESTAMP)`, planned)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{catalog: c, wake: make(chan struct{}, 1)}, func() { _ = c.Close() }
}
func TestManualRunDoesNotOverwriteScheduleAndReportsQueued(t *testing.T) {
	svc, done := manualTestFixture(t)
	defer done()
	ctx := context.Background()
	var before int64
	if err := svc.catalog.DB.QueryRow("SELECT next_run_unix FROM schedule_entries WHERE id='job-1'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := svc.Manual(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	var after int64
	var queued int
	if err := svc.catalog.DB.QueryRow("SELECT next_run_unix,manual_pending FROM schedule_entries WHERE id='job-1'").Scan(&after, &queued); err != nil {
		t.Fatal(err)
	}
	if after != before || queued != 1 {
		t.Fatalf("manual run changed schedule or didn't queue: before=%d after=%d queued=%d", before, after, queued)
	}
	if err := svc.Manual(ctx, "job-1"); !errors.Is(err, ErrJobAlreadyRunning) {
		t.Fatalf("second manual run err=%v", err)
	}
	if _, err := svc.catalog.DB.Exec("UPDATE schedule_entries SET manual_pending=0,active_run_id='active' WHERE id='job-1'"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Manual(ctx, "job-1"); !errors.Is(err, ErrJobAlreadyRunning) {
		t.Fatalf("active run err=%v", err)
	}
	if _, err := svc.catalog.DB.Exec("UPDATE schedule_entries SET active_run_id=NULL,enabled=0 WHERE id='job-1'"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Manual(ctx, "job-1"); !errors.Is(err, ErrJobPaused) {
		t.Fatalf("paused err=%v", err)
	}
}
