package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/mirivlad/BackupHub/internal/scheduler"
)

const workloadDays = 7
const workloadHistoryDays = 28
const workloadLimit = 50000

type WorkloadItem struct {
	JobID   string     `json:"job_id"`
	JobName string     `json:"job_name"`
	Start   time.Time  `json:"start"`
	End     *time.Time `json:"end,omitempty"`
}

type WorkloadSlot struct {
	Planned         int            `json:"planned"`
	Overlap         bool           `json:"overlap"`
	Unknown         bool           `json:"unknown"`
	Past            bool           `json:"past"`
	ObservedSeconds int64          `json:"observed_seconds"`
	BusySeconds     int64          `json:"busy_seconds"`
	Items           []WorkloadItem `json:"items,omitempty"`
}

type WorkloadDay struct {
	Date    string           `json:"date"`
	Weekday time.Weekday     `json:"weekday"`
	Hours   [24]WorkloadSlot `json:"hours"`
}

type WorkloadWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type WorkloadOverview struct {
	Timezone        string                    `json:"timezone"`
	Days            [workloadDays]WorkloadDay `json:"days"`
	PlannedRuns     int                       `json:"planned_runs"`
	OverlapHours    int                       `json:"overlap_hours"`
	UnmeasuredJobs  int                       `json:"unmeasured_jobs"`
	ActiveJob       bool                      `json:"active_job"`
	Truncated       bool                      `json:"truncated"`
	BusySeconds     int64                     `json:"busy_seconds"`
	ObservedSeconds int64                     `json:"observed_seconds"`
	HistoryDays     int                       `json:"history_days"`
	FreeWindow      *WorkloadWindow           `json:"free_window,omitempty"`
}

type timeSpan struct{ start, end time.Time }

type workloadRun struct {
	timeSpan
	job       string
	completed bool
}

func (s *Service) Workload(ctx context.Context, timezone string) (WorkloadOverview, error) {
	return s.workloadAt(ctx, timezone, time.Now())
}

func (s *Service) workloadAt(ctx context.Context, timezone string, now time.Time) (WorkloadOverview, error) {
	var result WorkloadOverview
	if timezone == "" || timezone == "Local" || len(timezone) > 100 {
		return result, errors.New("invalid workload timezone")
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return result, errors.New("invalid workload timezone")
	}
	result.Timezone, result.HistoryDays = timezone, workloadHistoryDays
	now = now.In(zone)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	end := today.AddDate(0, 0, workloadDays)
	dates := make(map[string]*WorkloadDay, workloadDays)
	for i := range result.Days {
		day := &result.Days[i]
		date := today.AddDate(0, 0, i)
		day.Date, day.Weekday = date.Format(time.DateOnly), date.Weekday()
		dates[day.Date] = day
	}
	runs, observedStart, err := s.workloadRuns(ctx, now.AddDate(0, 0, -workloadHistoryDays), now)
	if err != nil {
		return result, err
	}
	medians, observed, busy := summarizeRuns(runs, observedStart.In(zone), now)
	for i := range result.Days {
		day := &result.Days[i]
		for h := range day.Hours {
			slot := &day.Hours[h]
			slot.ObservedSeconds = observed[day.Weekday][h]
			slot.BusySeconds = busy[day.Weekday][h]
			result.ObservedSeconds += slot.ObservedSeconds
			result.BusySeconds += slot.BusySeconds
			slot.Past = true
		}
	}
	splitHours(now, end, func(local, _ time.Time) {
		dates[local.Format(time.DateOnly)].Hours[local.Hour()].Past = false
	})
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		return result, err
	}
	var spans []timeSpan
	for _, job := range jobs {
		result.ActiveJob = result.ActiveJob || job.Running || job.ManualQueued
		if !job.Enabled {
			continue
		}
		if _, measured := medians[job.ID]; !measured {
			result.UnmeasuredJobs++
		}
	}
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		duration, measured := medians[job.ID]
		cursor := now
		start, parseErr := time.Parse(time.RFC3339, job.NextRun)
		if parseErr != nil {
			return result, parseErr
		}
		if start.Before(now) {
			if job.Running {
				start, err = nextWorkloadRun(job, cursor)
			} else {
				start = now
			}
		}
		for err == nil && start.Before(end) {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if result.PlannedRuns >= workloadLimit {
				result.Truncated = true
				break
			}
			result.PlannedRuns++
			localStart := start.In(zone)
			if !measured {
				slot := &dates[localStart.Format(time.DateOnly)].Hours[localStart.Hour()]
				addWorkloadItem(slot, WorkloadItem{JobID: job.ID, JobName: job.Name, Start: start}, true)
			} else {
				finish := start.Add(duration)
				spans = append(spans, timeSpan{localStart, finish.In(zone)})
				item := WorkloadItem{JobID: job.ID, JobName: job.Name, Start: start, End: &finish}
				splitHours(localStart, minTime(finish.In(zone), end), func(local, _ time.Time) {
					slot := &dates[local.Format(time.DateOnly)].Hours[local.Hour()]
					addWorkloadItem(slot, item, false)
				})
			}
			cursor = start
			start, err = nextWorkloadRun(job, cursor)
		}
		if err != nil {
			return result, fmt.Errorf("calculate workload for %s: %w", job.Name, err)
		}
		if result.Truncated {
			break
		}
	}
	markWorkloadOverlaps(spans, dates, end, &result)
	if !result.Truncated && !result.ActiveJob && result.UnmeasuredJobs == 0 {
		result.FreeWindow = longestFreeWindow(spans, now, end)
	}
	return result, nil
}

func addWorkloadItem(slot *WorkloadSlot, item WorkloadItem, unknown bool) {
	slot.Planned++
	slot.Unknown = slot.Unknown || unknown
	if len(slot.Items) < 8 {
		slot.Items = append(slot.Items, item)
	}
}

func nextWorkloadRun(job BackupJob, cursor time.Time) (time.Time, error) {
	schedule := job.Schedule
	if schedule.FirstRun == "" {
		return scheduler.NextOccurrence(schedule.Mode, schedule.IntervalValue, schedule.DaysOfWeek,
			schedule.Hour, schedule.Minute, schedule.Timezone, cursor)
	}
	zone, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	anchor, err := time.ParseInLocation("2006-01-02T15:04", schedule.FirstRun, zone)
	if err != nil {
		return time.Time{}, err
	}
	return nextSchedule(schedule, anchor, cursor)
}

func (s *Service) workloadRuns(ctx context.Context, lower, now time.Time) ([]workloadRun, time.Time, error) {
	// A fresh installation has no earlier observations.
	var first sql.NullString
	if err := s.catalog.DB.QueryRowContext(ctx, `SELECT MIN(r.started_at) FROM schedule_runs r
	 JOIN schedule_entries e ON e.id=r.schedule_id WHERE e.kind='backup'`).Scan(&first); err != nil {
		return nil, time.Time{}, err
	}
	if !first.Valid {
		return nil, now, nil
	}
	observed, err := time.Parse(time.RFC3339Nano, first.String)
	if err != nil {
		return nil, time.Time{}, err
	}
	if observed.Before(lower) {
		observed = lower
	}
	rows, err := s.catalog.DB.QueryContext(ctx, `SELECT r.schedule_id,r.state,r.started_at,r.finished_at
	 FROM schedule_runs r JOIN schedule_entries e ON e.id=r.schedule_id WHERE e.kind='backup'
	  AND (r.finished_at IS NULL OR r.finished_at>=?)
	 ORDER BY r.started_at LIMIT ?`, lower.UTC().Format("2006-01-02T15:04:05"), workloadLimit+1)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var runs []workloadRun
	for rows.Next() {
		var r workloadRun
		var state, started string
		var finished sql.NullString
		if err = rows.Scan(&r.job, &state, &started, &finished); err != nil {
			return nil, time.Time{}, err
		}
		r.start, err = time.Parse(time.RFC3339Nano, started)
		r.end = now
		if err == nil && finished.Valid {
			r.end, err = time.Parse(time.RFC3339Nano, finished.String)
		}
		if err != nil {
			return nil, time.Time{}, err
		}
		r.completed = finished.Valid && state == "completed"
		runs = append(runs, r)
		if len(runs) > workloadLimit {
			return nil, time.Time{}, errors.New("too many backup runs for the workload window")
		}
	}
	return runs, observed, rows.Err()
}

func summarizeRuns(runs []workloadRun, lower, upper time.Time) (map[string]time.Duration, [7][24]int64, [7][24]int64) {
	samples := map[string][]time.Duration{}
	var spans []timeSpan
	for _, r := range runs {
		if r.completed && r.end.After(r.start) && !r.end.Before(lower) {
			samples[r.job] = append(samples[r.job], r.end.Sub(r.start))
		}
		start := maxTime(r.start.In(lower.Location()), lower)
		end := minTime(r.end.In(lower.Location()), upper)
		if end.After(start) {
			spans = append(spans, timeSpan{start, end})
		}
	}
	medians := map[string]time.Duration{}
	for job, values := range samples {
		slices.Sort(values)
		mid := len(values) / 2
		medians[job] = values[mid]
		if len(values)%2 == 0 {
			medians[job] = values[mid-1] + (values[mid]-values[mid-1])/2
		}
	}
	var observed, busy [7][24]int64
	splitHours(lower, upper, func(start, end time.Time) {
		observed[start.Weekday()][start.Hour()] += int64(end.Sub(start) / time.Second)
	})
	sortSpans(spans)
	var cursor time.Time
	for _, span := range spans {
		splitHours(maxTime(span.start, cursor), span.end, func(start, end time.Time) {
			busy[start.Weekday()][start.Hour()] += int64(end.Sub(start) / time.Second)
		})
		cursor = maxTime(cursor, span.end)
	}
	return medians, observed, busy
}

func markWorkloadOverlaps(spans []timeSpan, dates map[string]*WorkloadDay, until time.Time, result *WorkloadOverview) {
	sortSpans(spans)
	var cursor time.Time
	for _, span := range spans {
		splitHours(span.start, minTime(minTime(span.end, cursor), until), func(local, _ time.Time) {
			slot := &dates[local.Format(time.DateOnly)].Hours[local.Hour()]
			if !slot.Overlap {
				slot.Overlap = true
				result.OverlapHours++
			}
		})
		cursor = maxTime(cursor, span.end)
	}
}

func longestFreeWindow(spans []timeSpan, from, end time.Time) *WorkloadWindow {
	var best *WorkloadWindow
	record := func(start, stop time.Time) {
		if stop.After(start) && (best == nil || stop.Sub(start) > best.End.Sub(best.Start)) {
			best = &WorkloadWindow{Start: start, End: stop}
		}
	}
	cursor := from
	for _, span := range spans {
		record(cursor, span.start)
		cursor = maxTime(cursor, span.end)
	}
	record(cursor, end)
	return best
}

func splitHours(from, until time.Time, add func(time.Time, time.Time)) {
	for from.Before(until) {
		elapsed := time.Duration(from.Minute())*time.Minute + time.Duration(from.Second())*time.Second + time.Duration(from.Nanosecond())
		next := minTime(from.Add(time.Hour-elapsed), until)
		_, zoneEnd := from.ZoneBounds()
		if zoneEnd.After(from) && zoneEnd.Before(next) {
			next = zoneEnd
		}
		add(from, next)
		from = next
	}
}

func sortSpans(spans []timeSpan) {
	slices.SortFunc(spans, func(a, b timeSpan) int { return a.start.Compare(b.start) })
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
