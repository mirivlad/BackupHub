package scheduler

import (
	"testing"
	"time"
)

func TestCronWeeklyInTimezone(t *testing.T) {
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	next, err := NextCron("15 8 * * 1-5", "Europe/Berlin", start, start)
	if err != nil {
		t.Fatal(err)
	}
	zone, _ := time.LoadLocation("Europe/Berlin")
	got := next.In(zone).Format("2006-01-02 15:04 Mon")
	if got != "2026-11-23 08:15 Mon" {
		t.Fatalf("unexpected cron next %s", got)
	}
}
func TestCronMultipleSelectors(t *testing.T) {
	start := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	next, err := NextCron("0,30 2,3 1,15 1,7 *", "UTC", start, start)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Format("2006-01-02 15:04"); got != "2027-01-01 02:00" {
		t.Fatalf("cron selector result %q", got)
	}
}
func TestCronDayOrWeekday(t *testing.T) {
	after := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	next, err := NextCron("0 2 1 * 1", "UTC", after, after)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Format("2006-01-02 15:04"); got != "2026-12-07 02:00" {
		t.Fatalf("cron OR rule got %q", got)
	}
}
func TestCronRejectsImpossibleAndInvalid(t *testing.T) {
	after := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	for _, expr := range []string{"0 0 31 2 *", "61 3 * * *", "* * *", "@reboot", "* * * * * *"} {
		if _, err := NextCron(expr, "UTC", after, after); err == nil {
			t.Errorf("accepted %q", expr)
		}
	}
}
func TestCronDSTAndPreview(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Berlin")
	after := time.Date(2026, 3, 28, 3, 0, 0, 0, zone)
	future, err := PreviewCron("30 2 * * *", "Europe/Berlin", after, after, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(future) != 3 {
		t.Fatalf("size=%d", len(future))
	}
	for i := 1; i < len(future); i++ {
		if !future[i].After(future[i-1]) {
			t.Fatalf("non increasing runs %v", future)
		}
	}
	if got := future[0].In(zone).Format("2006-01-02 15:04"); got != "2026-03-30 02:30" {
		t.Fatalf("DST cron got %q", got)
	}
}
