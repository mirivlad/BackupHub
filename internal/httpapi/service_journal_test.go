package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mirivlad/BackupHub/internal/auth"
)

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
