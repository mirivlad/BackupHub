package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func assessLocalAccess(path string) (string, string) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "denied", "Нет доступа к каталогу. Проверьте ACL и ограничения systemd ProtectHome."
		}
		if errors.Is(err, os.ErrNotExist) {
			return "missing", "Каталог не найден или скрыт ограничениями службы systemd."
		}
		return "error", "Не удалось проверить доступность каталога."
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "invalid", "Источник должен быть настоящим каталогом, не символьной ссылкой."
	}
	handle, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "denied", "Нет прав на чтение каталога. Проверьте ACL пользователя backuphub."
		}
		return "error", "Каталог не удалось открыть."
	}
	defer handle.Close()
	_, err = handle.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		if errors.Is(err, os.ErrPermission) {
			return "denied", "Недостаточно прав для просмотра содержимого каталога."
		}
		return "error", "Не удалось прочитать содержимое каталога."
	}
	return "ok", ""
}

// UpdateSource preserves source IDs, historical snapshots and secrets.
// An empty password means 'keep existing' only for the same remote source type.
func (s *Service) UpdateSource(ctx context.Context, id string, in CreateSource) (Source, error) {
	var empty Source
	if !safeID(id) {
		return empty, errors.New("invalid source ID")
	}
	old, _, oldRef, err := s.source(ctx, id)
	if err != nil {
		return empty, err
	}
	if strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 120 || strings.ContainsAny(in.Name, "\x00\r\n") {
		return empty, errors.New("invalid source name")
	}
	if in.Kind != "local" && in.Kind != "smb" && in.Kind != "sftp" && in.Kind != "postgres" {
		return empty, errors.New("unsupported source type")
	}
	if in.Kind == "sftp" && in.AuthMethod == "" {
		in.AuthMethod = "password"
	}
	var oldSettings Settings
	_, oldSettings, oldRef, err = s.source(ctx, id)
	if err != nil {
		return empty, err
	}
	sameCredentials := in.Kind == old.Kind && in.AuthMethod == oldSettings.AuthMethod || in.Kind == "sftp" && in.AuthMethod == "password" && old.Kind == "sftp" && oldSettings.AuthMethod == ""
	newCredentials := in.Password != "" || (in.Kind == "sftp" && in.AuthMethod == "key" && in.PrivateKey != "")
	checked := in
	if sameCredentials && !newCredentials && oldRef != "" {
		currentSecret, e := s.vault.Get(ctx, oldRef)
		if e != nil {
			return empty, e
		}
		if in.Kind == "sftp" && in.AuthMethod == "key" {
			var key sshKeySecret
			if e = json.Unmarshal(currentSecret, &key); e != nil {
				return empty, errors.New("stored SSH key is unreadable")
			}
			checked.PrivateKey = key.PrivateKey
			checked.KeyPassphrase = key.Passphrase
		} else {
			checked.Password = string(currentSecret)
		}
	}
	if err = validateInput(checked, s.repo.path, s.catalog.DataDir, s.stageDir); err != nil {
		return empty, err
	}
	if in.Kind == "local" {
		rows, qerr := s.catalog.DB.QueryContext(ctx, "SELECT path FROM storage_repositories")
		if qerr != nil {
			return empty, qerr
		}
		defer rows.Close()
		for rows.Next() {
			var path string
			if err = rows.Scan(&path); err != nil {
				return empty, err
			}
			if pathOverlaps(in.Path, path) {
				return empty, errors.New("local source overlaps configured storage repository")
			}
		}
		if err = rows.Err(); err != nil {
			return empty, err
		}
	}
	// Never overwrite an existing secret until the source transaction commits.
	newRef := ""
	if in.Kind != "local" && newCredentials {
		secret, e := sshStoredSecret(in)
		if e != nil {
			return empty, e
		}
		secretID, e := randomID()
		if e != nil {
			return empty, e
		}
		newRef = "source/" + id + "-" + secretID
		if err = s.vault.Put(ctx, newRef, []byte(secret)); err != nil {
			return empty, err
		}
	}
	selectedRef := oldRef
	if !sameCredentials {
		selectedRef = ""
	}
	if in.Kind == "local" {
		selectedRef = ""
	} else if newRef != "" {
		selectedRef = newRef
	}
	cleanup := func() {
		if newRef != "" {
			_ = s.vault.Delete(context.Background(), newRef)
		}
	}
	settings := Settings{Host: in.Host, Share: in.Share, Path: in.Path, Username: in.Username, Database: in.Database, AuthMethod: in.AuthMethod}
	encoded, err := json.Marshal(settings)
	if err != nil {
		cleanup()
		return empty, err
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		cleanup()
		return empty, err
	}
	defer tx.Rollback()
	var running int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schedule_entries WHERE dataset_id=? AND active_run_id IS NOT NULL", id).Scan(&running); err != nil {
		cleanup()
		return empty, err
	}
	if running > 0 {
		cleanup()
		return empty, errors.New("cannot edit a source while backup is running")
	}
	res, err := tx.ExecContext(ctx, "UPDATE sources SET kind=?,display_name=?,endpoint=?,settings_json=?,secret_ref=? WHERE id=?", in.Kind, strings.TrimSpace(in.Name), in.Host, string(encoded), nullable(selectedRef), id)
	if err != nil {
		cleanup()
		return empty, err
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		cleanup()
		return empty, fmt.Errorf("source not found or update failed: %w", sql.ErrNoRows)
	}
	if old.Kind == "sftp" && (in.Kind != "sftp" || in.Host != oldSettings.Host) {
		if _, err = tx.ExecContext(ctx, "DELETE FROM ssh_trusted_hosts WHERE source_id=?", id); err != nil {
			cleanup()
			return empty, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE datasets SET display_name=?,locator=? WHERE source_id=?", strings.TrimSpace(in.Name), in.Path, id); err != nil {
		cleanup()
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		cleanup()
		return empty, err
	}
	if oldRef != "" && oldRef != selectedRef {
		_ = s.vault.Delete(context.Background(), oldRef)
	}
	out := Source{ID: id, Name: strings.TrimSpace(in.Name), Kind: in.Kind, Host: in.Host, Share: in.Share, Path: in.Path, Username: in.Username, Database: in.Database, AuthMethod: in.AuthMethod}
	if in.Kind == "local" {
		out.AccessStatus, out.AccessHint = assessLocalAccess(in.Path)
	}
	s.notify()
	return out, nil
}

// Test whether the service user can actually access the local source.
func (s *Service) LocalSourceStatus(path string) (string, string) {
	return assessLocalAccess(filepath.Clean(path))
}
