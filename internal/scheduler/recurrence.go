package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// NextOccurrence retains execution timing for schedules created before v0.2.1.
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

// NextAnchoredOccurrence calculates a stable recurrence independently of job
// duration and manual invocations. Day-based periods use civil dates in the
// configured timezone; minute/hour periods are fixed elapsed-time intervals.
func NextAnchoredOccurrence(mode string, value int, unit, days string, hour, minute int, zoneName string, anchor, after time.Time) (time.Time, error) {
	if anchor.IsZero() {
		return time.Time{}, errors.New("missing first run")
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return time.Time{}, err
	}
	anchor = anchor.In(zone)
	switch mode {
	case "interval":
		if value <= 0 {
			return time.Time{}, errors.New("invalid interval")
		}
		if !after.After(anchor) {
			return anchor.UTC(), nil
		}
		switch unit {
		case "minutes", "hours":
			duration := time.Duration(value) * time.Minute
			if unit == "hours" {
				duration = time.Duration(value) * time.Hour
			}
			elapsed := after.Sub(anchor)
			next := anchor.Add((elapsed/duration + 1) * duration)
			return next.UTC(), nil
		case "days":
			// Use calendar dates: seven days never slides by an hour across DST.
			local := after.In(zone)
			anchorDay := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 12, 0, 0, 0, time.UTC)
			afterDay := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, time.UTC)
			elapsedDays := int(afterDay.Sub(anchorDay).Hours() / 24)
			count := elapsedDays / value
			if count < 0 {
				count = 0
			}
			for steps := 0; steps < 4; steps++ {
				candidate := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), anchor.Hour(), anchor.Minute(), 0, 0, zone).AddDate(0, 0, count*value)
				if candidate.Hour() != anchor.Hour() || candidate.Minute() != anchor.Minute() {
					count++
					continue
				}
				if candidate.After(after) {
					return candidate.UTC(), nil
				}
				count++
			}
			return time.Time{}, errors.New("unable to find next day-based occurrence")
		default:
			return time.Time{}, errors.New("unsupported interval unit")
		}
	case "daily", "weekly":
		lower := after
		if lower.Before(anchor) {
			lower = anchor.Add(-time.Nanosecond)
		}
		return NextOccurrence(mode, 0, days, hour, minute, zoneName, lower)
	default:
		return time.Time{}, errors.New("unsupported schedule mode")
	}
}
func (s *Scheduler) nextRun(ctx context.Context, id string, completed time.Time, fallback time.Duration) (time.Time, error) {
	var mode, days, zone, unit, cronExpression string
	var interval, hour, minute, value int
	var anchor int64
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN p.cron_expression<>'' THEN 'cron' ELSE p.mode END,p.interval_minutes,p.days_of_week,p.hour,p.minute,p.timezone,p.start_at_unix,p.interval_unit,p.interval_value,p.cron_expression
 FROM schedule_entries e JOIN schedule_profiles p ON p.id=e.profile_id WHERE e.id=?`, id).
		Scan(&mode, &interval, &days, &hour, &minute, &zone, &anchor, &unit, &value, &cronExpression)
	if errors.Is(err, sql.ErrNoRows) {
		return completed.Add(fallback), nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if mode == "cron" {
		return NextCron(cronExpression, zone, time.Unix(anchor, 0), completed)
	}
	if anchor > 0 {
		return NextAnchoredOccurrence(mode, value, unit, days, hour, minute, zone, time.Unix(anchor, 0), completed)
	}
	return NextOccurrence(mode, interval, days, hour, minute, zone, completed)
}
