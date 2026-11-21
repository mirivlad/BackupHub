package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/mirivlad/BackupHub/internal/scheduler"
)

func (s *Service) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT s.id,s.display_name,s.kind,s.settings_json,COALESCE(t.fingerprint,'') FROM sources s LEFT JOIN ssh_trusted_hosts t ON t.source_id=s.id ORDER BY s.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Source{}
	for rows.Next() {
		var out Source
		var encoded string
		if err = rows.Scan(&out.ID, &out.Name, &out.Kind, &encoded, &out.HostKeyFingerprint); err != nil {
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
		out.AuthMethod = settings.AuthMethod
		if out.Kind == "local" {
			out.AccessStatus, out.AccessHint = assessLocalAccess(out.Path)
		}
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

var ErrJobAlreadyRunning = errors.New("backup job is already running or queued")
var ErrJobPaused = errors.New("backup job is paused")

// Manual queues an independent execution without modifying next_run_unix.
func (s *Service) Manual(ctx context.Context, id string) error {
	if !safeID(id) {
		return errors.New("invalid job ID")
	}
	result, err := s.catalog.DB.ExecContext(ctx, `UPDATE schedule_entries SET manual_pending=1
   WHERE id=? AND enabled=1 AND active_run_id IS NULL AND manual_pending=0`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		var enabled, manual int
		var active sql.NullString
		err = s.catalog.DB.QueryRowContext(ctx, `SELECT enabled,manual_pending,active_run_id FROM schedule_entries WHERE id=?`, id).Scan(&enabled, &manual, &active)
		if err != nil {
			return err
		}
		if enabled == 0 {
			return ErrJobPaused
		}
		return ErrJobAlreadyRunning
	}
	s.notify()
	return nil
}
func (s *Service) Pause(ctx context.Context, id string, enabled bool) error {
	active := 0
	if enabled {
		active = 1
	}
	result, err := s.catalog.DB.ExecContext(ctx,
		"UPDATE schedule_entries SET enabled=?,manual_pending=CASE WHEN ?=0 THEN 0 ELSE manual_pending END WHERE id=?",
		active, active, id)
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
