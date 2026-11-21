package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// NextOccurrence computes calendar schedules in the named civil timezone. An
// occurrence in a nonexistent spring-DST hour is skipped, not shifted silently.
func NextOccurrence(mode string, intervalMinutes int, daysOfWeek string, hour, minute int, timezone string, after time.Time) (time.Time, error) {
	if mode == "interval" {
		if intervalMinutes < 5 || intervalMinutes > 525600 {
			return time.Time{}, errors.New("invalid interval")
		}
		return after.Add(time.Duration(intervalMinutes) * time.Minute), nil
	}
	if mode != "daily" && mode != "weekly" {
		return time.Time{}, errors.New("invalid calendar mode")
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	local := after.In(zone)
	for offset := 0; offset <= 8; offset++ {
		current := local.AddDate(0, 0, offset)
		candidate := time.Date(current.Year(), current.Month(), current.Day(), hour, minute, 0, 0, zone)
		if candidate.Hour() != hour || candidate.Minute() != minute {
			continue
		}
		if mode == "weekly" && !strings.Contains(","+daysOfWeek+",", ","+strconv.Itoa(int(candidate.Weekday()))+",") {
			continue
		}
		if candidate.After(after) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, errors.New("no upcoming occurrence")
}
func (s *Scheduler) nextRun(ctx context.Context, id string, completed time.Time, fallback time.Duration) (time.Time, error) {
	var mode, days, zone string
	var interval, hour, minute int
	err := s.db.QueryRowContext(ctx, `SELECT p.mode,p.interval_minutes,p.days_of_week,p.hour,p.minute,p.timezone
 FROM schedule_entries e JOIN schedule_profiles p ON p.id=e.profile_id WHERE e.id=?`, id).
		Scan(&mode, &interval, &days, &hour, &minute, &zone)
	if errors.Is(err, sql.ErrNoRows) {
		return completed.Add(fallback), nil
	} // legacy jobs
	if err != nil {
		return time.Time{}, err
	}
	return NextOccurrence(mode, interval, days, hour, minute, zone, completed)
}
