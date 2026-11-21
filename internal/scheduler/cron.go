package scheduler

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// NextCron calculates the next civil-time event using standard five-field cron
// semantics (minute hour day-of-month month day-of-week). Both restricted
// day-of-month and weekday fields follow conventional cron OR semantics.
// The timezone is selected separately; users never enter raw cron syntax.
func NextCron(expression, timezone string, start, after time.Time) (time.Time, error) {
	if len(expression) > 180 || len(strings.Fields(expression)) != 5 {
		return time.Time{}, errors.New("invalid cron rule: five fields required")
	}
	if len(timezone) == 0 || len(timezone) > 100 || strings.ContainsAny(timezone, " \t\r\n") {
		return time.Time{}, errors.New("invalid schedule timezone")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return time.Time{}, errors.New("unknown schedule timezone")
	}
	schedule, err := cron.ParseStandard("CRON_TZ=" + timezone + " " + expression)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron rule: %w", err)
	}
	cursor := after
	if !start.IsZero() && !after.After(start) {
		cursor = start.Add(-time.Nanosecond)
	}
	next := schedule.Next(cursor)
	if next.IsZero() {
		return time.Time{}, errors.New("cron rule has no future occurrences")
	}
	return next.UTC(), nil
}

func PreviewCron(expression, timezone string, start, after time.Time, count int) ([]time.Time, error) {
	if count < 1 || count > 20 {
		return nil, errors.New("invalid preview count")
	}
	out := make([]time.Time, 0, count)
	for range count {
		next, err := NextCron(expression, timezone, start, after)
		if err != nil {
			return nil, err
		}
		out = append(out, next)
		after = next
	}
	return out, nil
}
