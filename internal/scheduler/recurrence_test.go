package scheduler

import (
	"testing"
	"time"
)

func TestCalendarScheduleDST(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	// March 29 2026 has no 02:30 in Berlin. Skip that day.
	after := time.Date(2026, 3, 28, 3, 0, 0, 0, zone)
	next, err := NextOccurrence("daily", 0, "", 2, 30, "Europe/Berlin", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 30, 2, 30, 0, 0, zone)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}
func TestWeeklyScheduleAndInterval(t *testing.T) {
	after := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) // Friday
	next, err := NextOccurrence("weekly", 0, "1,3,5", 8, 45, "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 23, 8, 45, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
	next, err = NextOccurrence("interval", 90, "", 0, 0, "UTC", after)
	if err != nil || !next.Equal(after.Add(90*time.Minute)) {
		t.Fatalf("interval=%v err=%v", next, err)
	}
}
