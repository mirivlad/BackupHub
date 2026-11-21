package backup

import (
	"context"
	"database/sql"
	"errors"
	"github.com/mirivlad/BackupHub/internal/scheduler"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ScheduleProfile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Mode            string `json:"mode"`
	IntervalMinutes int    `json:"interval_minutes"`
	DaysOfWeek      string `json:"days_of_week"`
	Hour            int    `json:"hour"`
	Minute          int    `json:"minute"`
	Timezone        string `json:"timezone"`
}
type BackupJob struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SourceID     string `json:"source_id"`
	SourceName   string `json:"source_name"`
	ScheduleID   string `json:"schedule_id"`
	ScheduleName string `json:"schedule_name"`
	RepositoryID string `json:"repository_id"`
	Enabled      bool   `json:"enabled"`
	NextRun      string `json:"next_run"`
}
type CreateBackupJob struct {
	Name         string `json:"name"`
	SourceID     string `json:"source_id"`
	ScheduleID   string `json:"schedule_id"`
	RepositoryID string `json:"repository_id"`
}

func checkProfile(p ScheduleProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 120 || strings.ContainsAny(p.Name, "\x00\r\n") {
		return errors.New("invalid schedule name")
	}
	switch p.Mode {
	case "interval":
		if p.IntervalMinutes < 5 || p.IntervalMinutes > 525600 {
			return errors.New("interval must be 5..525600 minutes")
		}
	case "daily", "weekly":
		if p.Hour < 0 || p.Hour > 23 || p.Minute < 0 || p.Minute > 59 {
			return errors.New("invalid time")
		}
		if p.Mode == "weekly" {
			if p.DaysOfWeek == "" {
				return errors.New("choose at least one weekday")
			}
			seen := map[int]bool{}
			for _, entry := range strings.Split(p.DaysOfWeek, ",") {
				day, err := strconv.Atoi(entry)
				if err != nil || day < 0 || day > 6 || seen[day] {
					return errors.New("invalid weekdays")
				}
				seen[day] = true
			}
		}
	default:
		return errors.New("invalid schedule mode")
	}
	if p.Timezone == "" {
		return errors.New("timezone is required")
	}
	if len(p.Timezone) > 100 || strings.ContainsAny(p.Timezone, "\x00\r\n") {
		return errors.New("invalid timezone")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return errors.New("unknown timezone")
	}
	return nil
}
func NextScheduleTime(p ScheduleProfile, after time.Time) (time.Time, error) {
	if err := checkProfile(p); err != nil {
		return time.Time{}, err
	}
	return scheduler.NextOccurrence(p.Mode, p.IntervalMinutes, p.DaysOfWeek, p.Hour, p.Minute, p.Timezone, after)
}
func (s *Service) CreateSchedule(ctx context.Context, p ScheduleProfile) (ScheduleProfile, error) {
	if err := checkProfile(p); err != nil {
		return ScheduleProfile{}, err
	}
	id, err := randomID()
	if err != nil {
		return ScheduleProfile{}, err
	}
	p.ID = id
	_, err = s.catalog.DB.ExecContext(ctx, `INSERT INTO schedule_profiles(id,name,mode,interval_minutes,days_of_week,hour,minute,timezone,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		p.ID, strings.TrimSpace(p.Name), p.Mode, p.IntervalMinutes, p.DaysOfWeek, p.Hour, p.Minute, p.Timezone, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return ScheduleProfile{}, err
	}
	return p, nil
}
func (s *Service) ListSchedules(ctx context.Context) ([]ScheduleProfile, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT id,name,mode,interval_minutes,days_of_week,hour,minute,timezone FROM schedule_profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleProfile{}
	for rows.Next() {
		var p ScheduleProfile
		if err = rows.Scan(&p.ID, &p.Name, &p.Mode, &p.IntervalMinutes, &p.DaysOfWeek, &p.Hour, &p.Minute, &p.Timezone); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) DeleteSchedule(ctx context.Context, id string) error {
	if !safeID(id) {
		return errors.New("invalid schedule ID")
	}
	res, err := s.catalog.DB.ExecContext(ctx, `DELETE FROM schedule_profiles WHERE id=? AND NOT EXISTS (SELECT 1 FROM schedule_entries WHERE profile_id=?)`, id, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("schedule not found or used by a job")
	}
	return nil
}
func (s *Service) CreateJob(ctx context.Context, in CreateBackupJob) (BackupJob, error) {
	if !safeID(in.SourceID) || !safeID(in.ScheduleID) || !safeID(in.RepositoryID) {
		return BackupJob{}, errors.New("invalid source, schedule or storage")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return BackupJob{}, errors.New("invalid job name")
	}
	if _, err := s.repositoryFor(ctx, in.RepositoryID); err != nil {
		return BackupJob{}, errors.New("destination storage unavailable")
	}
	var sourceName string
	if err := s.catalog.DB.QueryRowContext(ctx, "SELECT display_name FROM sources WHERE id=?", in.SourceID).Scan(&sourceName); err != nil {
		return BackupJob{}, err
	}
	var p ScheduleProfile
	err := s.catalog.DB.QueryRowContext(ctx, `SELECT id,name,mode,interval_minutes,days_of_week,hour,minute,timezone FROM schedule_profiles WHERE id=?`, in.ScheduleID).Scan(
		&p.ID, &p.Name, &p.Mode, &p.IntervalMinutes, &p.DaysOfWeek, &p.Hour, &p.Minute, &p.Timezone)
	if err != nil {
		return BackupJob{}, err
	}
	next, err := NextScheduleTime(p, time.Now().UTC())
	if err != nil {
		return BackupJob{}, err
	}
	id, err := randomID()
	if err != nil {
		return BackupJob{}, err
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return BackupJob{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,dataset_id,schedule_json,created_at) VALUES(?,?,?,?)`, id, in.SourceID, "{}", now)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO schedule_entries(id,dataset_id,kind,interval_seconds,next_run_unix,enabled,created_at,profile_id,display_name)
  VALUES(?,?,?,?,?,?,?,?,?)`, id, in.SourceID, "backup", profileInterval(p), next.Unix(), 1, now, p.ID, name)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO schedule_repositories(schedule_id,repository_id) VALUES(?,?)", id, in.RepositoryID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return BackupJob{}, err
	}
	s.notify()
	return BackupJob{ID: id, Name: name, SourceID: in.SourceID, SourceName: sourceName, ScheduleID: p.ID, ScheduleName: p.Name, RepositoryID: in.RepositoryID, Enabled: true, NextRun: next.Format(time.RFC3339)}, nil
}
func profileInterval(p ScheduleProfile) int {
	switch p.Mode {
	case "daily":
		return 24 * 3600
	case "weekly":
		return 7 * 24 * 3600
	default:
		return p.IntervalMinutes * 60
	}
}
func (s *Service) ListJobs(ctx context.Context) ([]BackupJob, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT sch.id,COALESCE(NULLIF(sch.display_name,''),src.display_name),src.id,src.display_name,
  COALESCE(sch.profile_id,''),COALESCE(p.name,''),COALESCE(sr.repository_id,'primary'),sch.enabled,sch.next_run_unix
  FROM schedule_entries sch JOIN sources src ON src.id=sch.dataset_id
  LEFT JOIN schedule_profiles p ON p.id=sch.profile_id
  LEFT JOIN schedule_repositories sr ON sr.schedule_id=sch.id
  WHERE sch.kind='backup' ORDER BY sch.created_at DESC,sch.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupJob{}
	for rows.Next() {
		var j BackupJob
		var enabled int
		var next int64
		if err = rows.Scan(&j.ID, &j.Name, &j.SourceID, &j.SourceName, &j.ScheduleID, &j.ScheduleName, &j.RepositoryID, &enabled, &next); err != nil {
			return nil, err
		}
		j.Enabled = enabled != 0
		j.NextRun = time.Unix(next, 0).UTC().Format(time.RFC3339)
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Service) DeleteJob(ctx context.Context, id string) error {
	if !safeID(id) {
		return errors.New("invalid job ID")
	}
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_runs WHERE schedule_id=?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("job has execution history; pause it instead of deleting")
	}
	var active sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT active_run_id FROM schedule_entries WHERE id=?`, id).Scan(&active); err != nil {
		return err
	}
	if active.Valid {
		return errors.New("job is running")
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM schedule_repositories WHERE schedule_id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM schedule_entries WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM jobs WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
