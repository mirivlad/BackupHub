package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"time"
)

func (s *Service) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT src.id,src.display_name,src.kind,src.settings_json,sch.interval_seconds,sch.next_run_unix,sch.enabled,COALESCE(sr.repository_id,'primary') FROM sources src JOIN schedule_entries sch ON sch.id=src.id LEFT JOIN source_repositories sr ON sr.source_id=src.id ORDER BY src.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Source{}
	for rows.Next() {
		var out Source
		var encoded string
		var seconds, next int64
		var enabled int
		if err = rows.Scan(&out.ID, &out.Name, &out.Kind, &encoded, &seconds, &next, &enabled,&out.RepositoryID); err != nil {
			return nil, err
		}
		var settings Settings
		if err = json.Unmarshal([]byte(encoded), &settings); err != nil {
			return nil, err
		}
		out.Host = settings.Host
		out.Path = settings.Path
		out.Share = settings.Share
		out.Username = settings.Username
		out.Database = settings.Database
		out.IntervalMinutes = int(seconds / 60)
		out.NextRun = time.Unix(next, 0).UTC().Format(time.RFC3339)
		out.Enabled = enabled != 0
		result = append(result, out)
	}
	return result, rows.Err()
}
func (s *Service) source(ctx context.Context, id string) (Source, Settings, string, error) {
	var out Source
	var settings Settings
	var encoded string
	var secret sql.NullString
	err := s.catalog.DB.QueryRowContext(ctx, `SELECT id,display_name,kind,settings_json,secret_ref FROM sources WHERE id=?`, id).Scan(&out.ID, &out.Name, &out.Kind, &encoded, &secret)
	if err != nil {
		return out, settings, "", err
	}
	if err = json.Unmarshal([]byte(encoded), &settings); err != nil {
		return out, settings, "", err
	}
	return out, settings, secret.String, nil
}
func (s *Service) Manual(ctx context.Context, id string) error {
	if !safeID(id) {
		return errors.New("invalid source ID")
	}
	result, err := s.catalog.DB.ExecContext(ctx, `UPDATE schedule_entries SET next_run_unix=? WHERE id=? AND active_run_id IS NULL`, time.Now().UTC().Unix()-1, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("source not found or already running")
	}
	s.notify()
	return nil
}
func (s *Service) Pause(ctx context.Context, id string, enabled bool) error {
	active := 0
	if enabled {
		active = 1
	}
	result, err := s.catalog.DB.ExecContext(ctx, "UPDATE schedule_entries SET enabled=? WHERE id=?", active, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if enabled {
		s.notify()
	}
	return nil
}
func (s *Service) RecentRuns(ctx context.Context) ([]scheduler.Run, error) {
	return s.scheduler.RecentRuns(ctx, 100)
}

// Execute obeys the scheduler's typed transition ledger. A job can only be
// marked successful once the final native Restic commit marker is durable.
