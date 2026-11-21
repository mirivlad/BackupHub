package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Create registers a source independently of backup jobs and schedules.
func (s *Service) Create(ctx context.Context, input CreateSource) (Source, error) {
	var empty Source
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 120 {
		return empty, errors.New("name must have 1-120 characters")
	}
	if strings.ContainsAny(input.Name, "\x00\r\n") {
		return empty, errors.New("invalid name")
	}
	switch input.Kind {
	case "local", "smb", "sftp", "postgres":
	default:
		return empty, errors.New("unsupported source type")
	}
	if input.Kind == "sftp" && input.AuthMethod == "" {
		input.AuthMethod = "password"
	}
	if err := validateInput(input, s.repo.path, s.catalog.DataDir, s.stageDir); err != nil {
		return empty, err
	}
	if input.Kind == "local" {
		var paths []string
		rows, err := s.catalog.DB.QueryContext(ctx, "SELECT path FROM storage_repositories")
		if err != nil {
			return empty, err
		}
		for rows.Next() {
			var p string
			if err = rows.Scan(&p); err != nil {
				_ = rows.Close()
				return empty, err
			}
			paths = append(paths, p)
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return empty, err
		}
		_ = rows.Close()
		for _, p := range paths {
			if pathOverlaps(input.Path, p) {
				return empty, errors.New("local source overlaps configured storage repository")
			}
		}
	}
	id, err := randomID()
	if err != nil {
		return empty, err
	}
	settings := Settings{Host: input.Host, Share: input.Share, Path: input.Path, Username: input.Username, Database: input.Database, AuthMethod: input.AuthMethod}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return empty, err
	}
	ref := ""
	secret, secretErr := sshStoredSecret(input)
	if secretErr != nil {
		return empty, secretErr
	}
	if secret != "" {
		ref = "source/" + id
		if err = s.vault.Put(ctx, ref, []byte(secret)); err != nil {
			return empty, err
		}
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		if ref != "" {
			_ = s.vault.Delete(ctx, ref)
		}
		return empty, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, "INSERT INTO sources(id,kind,display_name,endpoint,secret_ref,settings_json,created_at) VALUES(?,?,?,?,?,?,?)", id, input.Kind, input.Name, input.Host, nullable(ref), string(encoded), now)

	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO datasets(id,source_id,display_name,locator) VALUES(?,?,?,?)", id, id, input.Name, input.Path)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if ref != "" {
			_ = s.vault.Delete(ctx, ref)
		}
		return empty, err
	}
	return Source{ID: id, Name: input.Name, Kind: input.Kind, Host: input.Host, Share: input.Share, Path: input.Path, Username: input.Username, Database: input.Database, AuthMethod: input.AuthMethod}, nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
