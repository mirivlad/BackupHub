package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mirivlad/BackupHub/internal/scheduler"
)

// ScheduleProfile belongs to one backup job. It is never created separately.
// FirstRun is a wall-clock date/time in Timezone: YYYY-MM-DDTHH:mm.
type ScheduleProfile struct {
	Mode           string `json:"mode"`
	IntervalValue  int    `json:"interval_value"`
	IntervalUnit   string `json:"interval_unit"`
	DaysOfWeek     string `json:"days_of_week"`
	Timezone       string `json:"timezone"`
	FirstRun       string `json:"first_run"`
	CronExpression string `json:"cron_expression,omitempty"`
	Hour           int    `json:"hour"`
	Minute         int    `json:"minute"`
}
type BackupJob struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	SourceID             string          `json:"source_id"`
	SourceName           string          `json:"source_name"`
	RepositoryID         string          `json:"repository_id"`
	Schedule             ScheduleProfile `json:"schedule"`
	Enabled              bool            `json:"enabled"`
	Running              bool            `json:"running"`
	ManualQueued         bool            `json:"manual_queued"`
	CurrentState         string          `json:"current_state,omitempty"`
	NextRun              string          `json:"next_run"`
	RetryCount           int             `json:"retry_count"`
	RetryIntervalMinutes int             `json:"retry_interval_minutes"`
	OperationID          string          `json:"operation_id,omitempty"`
	Attempt              int             `json:"attempt,omitempty"`
	NextRetry            *time.Time      `json:"next_retry,omitempty"`
	WaitReason           string          `json:"wait_reason,omitempty"`
	Reconciling          bool            `json:"reconciling,omitempty"`
}
type CreateBackupJob struct {
	Name                 string          `json:"name"`
	SourceID             string          `json:"source_id"`
	RepositoryID         string          `json:"repository_id"`
	Schedule             ScheduleProfile `json:"schedule"`
	RetryCount           *int            `json:"retry_count,omitempty"`
	RetryIntervalMinutes *int            `json:"retry_interval_minutes,omitempty"`
}

func (in CreateBackupJob) retrySettings(count, minutes int) (int, int, error) {
	if in.RetryCount != nil {
		count = *in.RetryCount
	}
	if in.RetryIntervalMinutes != nil {
		minutes = *in.RetryIntervalMinutes
	}
	if count < 0 || count > 10 {
		return count, minutes, errors.New("retry count must be between 0 and 10")
	}
	if minutes < 1 || minutes > 525600 {
		return count, minutes, errors.New("retry interval must be between 1 and 525600 minutes")
	}
	return count, minutes, nil
}

func validateSchedule(p ScheduleProfile) (ScheduleProfile, time.Time, int, error) {
	if len(p.Timezone) == 0 || len(p.Timezone) > 100 || strings.ContainsAny(p.Timezone, "\x00\r\n") {
		return p, time.Time{}, 0, errors.New("invalid timezone")
	}
	zone, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return p, time.Time{}, 0, errors.New("unknown timezone")
	}
	if len(p.FirstRun) != 16 {
		return p, time.Time{}, 0, errors.New("first run date and time required (YYYY-MM-DDTHH:mm)")
	}
	anchor, err := time.ParseInLocation("2006-01-02T15:04", p.FirstRun, zone)
	if err != nil || anchor.Format("2006-01-02T15:04") != p.FirstRun {
		return p, time.Time{}, 0, errors.New("invalid first run date/time in selected timezone")
	}
	p.Hour = anchor.Hour()
	p.Minute = anchor.Minute()
	intervalMinutes := 0
	if p.IntervalValue < 0 || p.IntervalValue > 525600 {
		return p, time.Time{}, 0, errors.New("invalid interval")
	}
	switch p.Mode {
	case "interval":
		switch p.IntervalUnit {
		case "minutes":
			intervalMinutes = p.IntervalValue
		case "hours":
			intervalMinutes = p.IntervalValue * 60
		case "days":
			intervalMinutes = p.IntervalValue * 1440
		default:
			return p, time.Time{}, 0, errors.New("choose minutes, hours or days")
		}
		if p.IntervalValue < 1 || p.IntervalValue > 525600 || intervalMinutes < 5 || intervalMinutes > 525600 {
			return p, time.Time{}, 0, errors.New("invalid interval (5 minutes to 365 days)")
		}
		p.DaysOfWeek = ""
	case "cron":
		if _, e := scheduler.NextCron(p.CronExpression, p.Timezone, anchor, anchor.Add(-time.Nanosecond)); e != nil {
			return p, time.Time{}, 0, e
		}
		p.IntervalUnit = ""
		p.IntervalValue = 0
		p.DaysOfWeek = ""
	case "daily":
		p.IntervalUnit = ""
		p.IntervalValue = 0
		p.DaysOfWeek = ""
	case "weekly":
		p.IntervalUnit = ""
		p.IntervalValue = 0
		if p.DaysOfWeek == "" {
			return p, time.Time{}, 0, errors.New("choose weekdays")
		}
		seen := map[int]bool{}
		for _, x := range strings.Split(p.DaysOfWeek, ",") {
			n, e := strconv.Atoi(x)
			if e != nil || n < 0 || n > 6 || seen[n] {
				return p, time.Time{}, 0, errors.New("invalid weekdays")
			}
			seen[n] = true
		}
	default:
		return p, time.Time{}, 0, errors.New("invalid scheduling mode")
	}
	return p, anchor, intervalMinutes, nil
}
func nextSchedule(p ScheduleProfile, anchor, after time.Time) (time.Time, error) {
	if p.Mode == "cron" {
		return scheduler.NextCron(p.CronExpression, p.Timezone, anchor, after)
	}
	return scheduler.NextAnchoredOccurrence(p.Mode, p.IntervalValue, p.IntervalUnit, p.DaysOfWeek, p.Hour, p.Minute, p.Timezone, anchor, after)
}
func PreviewSchedule(p ScheduleProfile, count int) ([]time.Time, error) {
	normalized, anchor, _, err := validateSchedule(p)
	if err != nil {
		return nil, err
	}
	if count < 1 || count > 20 {
		return nil, errors.New("invalid preview count")
	}
	after := time.Now().UTC()
	out := make([]time.Time, 0, count)
	for i := 0; i < count; i++ {
		next, e := nextSchedule(normalized, anchor, after)
		if e != nil {
			return nil, e
		}
		out = append(out, next)
		after = next
	}
	return out, nil
}

// Keep v7 CHECK constraints intact; a nonempty cron_expression distinguishes cron mode.
func dbScheduleMode(p ScheduleProfile) string {
	if p.Mode == "cron" {
		return "interval"
	}
	return p.Mode
}
func profileInterval(p ScheduleProfile, minutes int) int {
	if p.Mode == "cron" {
		return 300
	}
	if p.Mode == "daily" {
		return 24 * 3600
	}
	if p.Mode == "weekly" {
		return 7 * 24 * 3600
	}
	return minutes * 60
}
func (s *Service) CreateJob(ctx context.Context, in CreateBackupJob) (BackupJob, error) {
	var empty BackupJob
	if !safeID(in.SourceID) || !safeID(in.RepositoryID) {
		return empty, errors.New("invalid source or storage")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		return empty, errors.New("invalid job name")
	}
	retries, step, err := in.retrySettings(3, 30)
	if err != nil {
		return empty, err
	}
	p, anchor, minutes, err := validateSchedule(in.Schedule)
	if err != nil {
		return empty, err
	}
	if _, err = s.repositoryFor(ctx, in.RepositoryID); err != nil {
		return empty, errors.New("destination storage unavailable")
	}
	var sourceName string
	if err = s.catalog.DB.QueryRowContext(ctx, "SELECT display_name FROM sources WHERE id=? AND kind<>?", in.SourceID, recoveredSourceKind).Scan(&sourceName); err != nil {
		return empty, errors.New("source not found or available only for restore")
	}
	now := time.Now().UTC()
	next, err := nextSchedule(p, anchor, now)
	if err != nil {
		return empty, err
	}
	id, err := randomID()
	if err != nil {
		return empty, err
	}
	nextUnix := next.Unix()
	tx, err := s.catalog.DB.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	created := now.Format(time.RFC3339Nano)
	// One profile per job, generated within the same transaction.
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_profiles(id,name,mode,interval_minutes,days_of_week,hour,minute,timezone,created_at,start_at_unix,interval_unit,interval_value,cron_expression)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "job-"+id, dbScheduleMode(p), minutes, p.DaysOfWeek, p.Hour, p.Minute, p.Timezone, created, anchor.Unix(), p.IntervalUnit, p.IntervalValue, p.CronExpression)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,dataset_id,schedule_json,created_at) VALUES(?,?,?,?)`, id, in.SourceID, "{}", created)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO schedule_entries(id,dataset_id,kind,interval_seconds,next_run_unix,enabled,created_at,profile_id,display_name,retry_count,retry_interval_minutes)
 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, in.SourceID, "backup", profileInterval(p, minutes), nextUnix, 1, created, id, name, retries, step)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO schedule_repositories(schedule_id,repository_id) VALUES(?,?)`, id, in.RepositoryID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return empty, fmt.Errorf("create job: %w", err)
	}
	s.notify()
	return BackupJob{ID: id, Name: name, SourceID: in.SourceID, SourceName: sourceName, RepositoryID: in.RepositoryID, Schedule: p, Enabled: true, RetryCount: retries, RetryIntervalMinutes: step, NextRun: time.Unix(nextUnix, 0).UTC().Format(time.RFC3339)}, nil
}
func (s *Service) ListJobs(ctx context.Context) ([]BackupJob, error) {
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT e.id,COALESCE(NULLIF(e.display_name,''),src.display_name),
 src.id,src.display_name,COALESCE(sr.repository_id,'primary'),e.enabled,e.next_run_unix,
 CASE WHEN COALESCE(p.cron_expression,'')<>'' THEN 'cron' ELSE COALESCE(p.mode,'interval') END,COALESCE(p.interval_minutes,e.interval_seconds/60),
 COALESCE(p.days_of_week,''),COALESCE(p.hour,0),COALESCE(p.minute,0),
 COALESCE(p.timezone,'UTC'),COALESCE(p.start_at_unix,0),
 COALESCE(p.interval_unit,'minutes'),COALESCE(p.interval_value,e.interval_seconds/60),COALESCE(p.cron_expression,''),
 COALESCE(e.active_run_id,''),e.manual_pending,COALESCE(active.state,''),
 e.retry_count,e.retry_interval_minutes,COALESCE(o.id,''),COALESCE(o.attempt,0),o.ready_unix,COALESCE(o.failure_code,''),o.recover_run_id IS NOT NULL
 FROM schedule_entries e JOIN sources src ON src.id=e.dataset_id
 LEFT JOIN schedule_runs active ON active.id=e.active_run_id
 LEFT JOIN backup_operations o ON o.schedule_id=e.id AND o.state IN ('running','waiting')
 LEFT JOIN schedule_profiles p ON p.id=e.profile_id
 LEFT JOIN schedule_repositories sr ON sr.schedule_id=e.id
 WHERE e.kind='backup' ORDER BY e.created_at DESC,e.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupJob{}
	for rows.Next() {
		var j BackupJob
		var enabled, minutes int
		var next, anchor int64
		var activeID, activeState string
		var pending int
		var ready sql.NullInt64
		var p ScheduleProfile
		if err = rows.Scan(&j.ID, &j.Name, &j.SourceID, &j.SourceName, &j.RepositoryID, &enabled, &next,
			&p.Mode, &minutes, &p.DaysOfWeek, &p.Hour, &p.Minute, &p.Timezone, &anchor, &p.IntervalUnit, &p.IntervalValue, &p.CronExpression, &activeID, &pending, &activeState,
			&j.RetryCount, &j.RetryIntervalMinutes, &j.OperationID, &j.Attempt, &ready, &j.WaitReason, &j.Reconciling); err != nil {
			return nil, err
		}
		if anchor > 0 {
			zone, loadErr := time.LoadLocation(p.Timezone)
			if loadErr != nil {
				return nil, loadErr
			}
			p.FirstRun = time.Unix(anchor, 0).In(zone).Format("2006-01-02T15:04")
		} else if p.Mode == "interval" && p.IntervalValue == 0 {
			p.IntervalValue = minutes
		}
		j.Schedule = p
		j.Enabled = enabled != 0
		j.Running = activeID != ""
		j.CurrentState = activeState
		if j.Reconciling {
			j.CurrentState = "reconciling"
		} else if ready.Valid {
			nextRetry := time.Unix(ready.Int64, 0).UTC()
			j.NextRetry = &nextRetry
		}
		j.ManualQueued = pending != 0
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
	var profile sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT active_run_id,profile_id FROM schedule_entries WHERE id=?`, id).Scan(&active, &profile); err != nil {
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
	if profile.Valid {
		if _, err = tx.ExecContext(ctx, `DELETE FROM schedule_profiles WHERE id=? AND NOT EXISTS(SELECT 1 FROM schedule_entries WHERE profile_id=?)`, profile.String, profile.String); err != nil {
			return err
		}
	}
	return tx.Commit()
}
