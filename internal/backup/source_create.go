package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Create preserves the original source+job API for existing installations and callers.
func (s *Service) Create(ctx context.Context, input CreateSource) (Source, error) {
	return s.createSource(ctx, input, true)
}

// CreateUnscheduled registers a reusable source without implicitly creating a backup job.
func (s *Service) CreateUnscheduled(ctx context.Context, input CreateSource) (Source, error) {
	return s.createSource(ctx, input, false)
}
func (s *Service) createSource(ctx context.Context, input CreateSource, withJob bool) (Source, error) {
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
	if withJob && (input.IntervalMinutes < 5 || input.IntervalMinutes > 525600) {
		return empty, errors.New("interval must be 5-525600 minutes")
	}
	if input.RepositoryID == "" {
		input.RepositoryID = PrimaryStorageID
	}
	if !safeID(input.RepositoryID) {
		return empty, errors.New("invalid storage ID")
	}
	if withJob {
		if _, err := s.repositoryFor(ctx, input.RepositoryID); err != nil {
			return empty, fmt.Errorf("storage unavailable: %w", err)
		}
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
	settings := Settings{Host: input.Host, Share: input.Share, Path: input.Path, Username: input.Username, Database: input.Database}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return empty, err
	}
	ref := ""
	if input.Password != "" {
		ref = "source/" + id
		if err = s.vault.Put(ctx, ref, []byte(input.Password)); err != nil {
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
	if withJob && err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO jobs(id,dataset_id,schedule_json,created_at) VALUES(?,?,?,?)", id, id, fmt.Sprintf("{\"interval_minutes\":%d}", input.IntervalMinutes), now)
	}
	if withJob && err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO schedule_entries(id,dataset_id,kind,interval_seconds,next_run_unix,enabled,created_at) VALUES(?,?,?,?,?,?,?)", id, id, "backup", input.IntervalMinutes*60, time.Now().UTC().Add(time.Minute).Unix(), 1, now)
	}
	if withJob && err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO schedule_repositories(schedule_id,repository_id) VALUES(?,?)", id, input.RepositoryID)
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
	if withJob {
		s.notify()
	}
	return Source{ID: id, Name: input.Name, Kind: input.Kind, Host: input.Host, Share: input.Share, Path: input.Path, Username: input.Username, Database: input.Database, IntervalMinutes: input.IntervalMinutes, RepositoryID: input.RepositoryID, Enabled: withJob}, nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
