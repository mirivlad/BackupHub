package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mirivlad/BackupHub/internal/auth"
	"github.com/mirivlad/BackupHub/internal/backup"
	"github.com/mirivlad/BackupHub/internal/vault"
)

func TestRetryActionsRequireBackupPermissionAndCSRF(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil && os.Getenv("BACKUPHUB_RESTIC_REQUIRED") != "1" {
		t.Skip("Restic unavailable")
	}
	_, c := testServer(t)
	defer c.Close()
	ctx := context.Background()
	v, err := vault.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := backup.New(ctx, c, v, "", "")
	if err != nil {
		t.Fatal(err)
	}
	h := NewWithBackup(c, v, s)
	token, _, err := auth.New(c).IssueBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user, err := auth.New(c).CreateAdmin(ctx, token, "admin", "a-long-correct-test-password")
	if err != nil {
		t.Fatal(err)
	}
	login := request(t, h, "POST", "/api/v1/auth/login", `{"username":"admin","password":"a-long-correct-test-password"}`)
	if login.Code != 200 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	var csrf string
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == csrfCookie {
			csrf = cookie.Value
		}
	}
	src, err := s.Create(ctx, backup.CreateSource{Name: "fixture", Kind: "local", Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateJob(ctx, backup.CreateBackupJob{Name: "fixture", SourceID: src.ID, RepositoryID: "primary",
		Schedule: backup.ScheduleProfile{Mode: "daily", Timezone: "UTC", FirstRun: "2099-01-01T00:00"}})
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour).Unix()
	if _, err = c.DB.Exec(`INSERT INTO backup_operations(id,schedule_id,trigger,requested_unix,planned_unix,state,attempt,ready_unix)
		VALUES('pending-retry',?,'manual',1,1,'waiting',1,?)`, job.ID, later); err != nil {
		t.Fatal(err)
	}
	send := func(action, csrfHeader string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/backup/jobs/"+job.ID+"/"+action, strings.NewReader(`{"operation_id":"pending-retry"}`))
		for _, cookie := range login.Result().Cookies() {
			r.AddCookie(cookie)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-BackupHub-CSRF", csrfHeader)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", action, w.Code, want, w.Body.String())
		}
	}
	send("retry", "", http.StatusForbidden)
	if _, err = c.DB.Exec("UPDATE users SET role='viewer' WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	send("retry", csrf, http.StatusForbidden)
	send("cancel-retry", csrf, http.StatusForbidden)
	if _, err = c.DB.Exec("UPDATE users SET role='backup_operator' WHERE id=?", user.ID); err != nil {
		t.Fatal(err)
	}
	send("retry", csrf, http.StatusOK)
	var ready int64
	if err = c.DB.QueryRow("SELECT ready_unix FROM backup_operations WHERE id='pending-retry'").Scan(&ready); err != nil || ready >= later {
		t.Fatalf("retry now did not move eligibility: %d %v", ready, err)
	}
	send("cancel-retry", csrf, http.StatusOK)
	send("retry", csrf, http.StatusConflict)
}
