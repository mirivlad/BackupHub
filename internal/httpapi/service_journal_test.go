package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirivlad/BackupHub/internal/auth"
)

func TestReadServiceJournalUsesFixedUnitAndBoundedOutput(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "journalctl")
	script := `#!/bin/sh
test "$*" = "--system --unit=backuphub.service --lines=100 --output=short-iso --reverse --no-pager --quiet" || exit 2
printf 'BackupHub started\n'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log, err := readServiceJournal(context.Background(), binary)
	if err != nil || log.Text != "BackupHub started" || log.Truncated {
		t.Fatalf("unexpected journal: %#v %v", log, err)
	}
	if err = os.WriteFile(binary, []byte(script+"exec yes 'journal line'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	log, err = readServiceJournal(context.Background(), binary)
	if err != nil || !log.Truncated || len(log.Text) > journalMaxBytes {
		t.Fatalf("journal size=%d, truncated=%v, err=%v", len(log.Text), log.Truncated, err)
	}
}

func TestServiceJournalRequiresAdministrator(t *testing.T) {
	h, db := testServer(t)
	defer db.Close()
	path := "/api/v1/system/journal"
	if code := request(t, h, http.MethodGet, path, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", code)
	}
	token, _, err := auth.New(db).IssueBootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	login := `{"username":"viewer","password":"journal fixture secret"}`
	payload, err := json.Marshal(map[string]string{
		"bootstrap_token": token, "username": "viewer", "password": "journal fixture secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res := request(t, h, http.MethodPost, "/api/v1/setup/admin", string(payload)); res.Code != http.StatusCreated {
		t.Fatalf("bootstrap: %d", res.Code)
	}
	if _, err = db.DB.Exec("UPDATE users SET role='viewer' WHERE username='viewer'"); err != nil {
		t.Fatal(err)
	}
	res := request(t, h, http.MethodPost, "/api/v1/auth/login", login)
	if res.Code != http.StatusOK {
		t.Fatalf("login: %d", res.Code)
	}
	if code := request(t, h, http.MethodGet, path, "", res.Result().Cookies()...).Code; code != http.StatusForbidden {
		t.Fatalf("viewer status: %d", code)
	}
}
