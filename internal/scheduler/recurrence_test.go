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
	after := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	next, err := NextOccurrence("weekly", 0, "1,3,5", 8, 45, "UTC", after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 23, 8, 45, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}
func TestEverySevenDaysAnchoredAcrossDST(t *testing.T) {
	zone, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	anchor := time.Date(2026, 3, 23, 2, 0, 0, 0, zone)
	for _, after := range []time.Time{
		anchor.Add(20 * time.Minute), // manual run on the start date
		time.Date(2026, 3, 24, 17, 15, 0, 0, zone),
		time.Date(2026, 3, 29, 19, 0, 0, 0, zone), // after DST change
	} {
		next, err := NextAnchoredOccurrence("interval", 7, "days", "", 0, 0, "Europe/Berlin", anchor, after)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Date(2026, 3, 30, 2, 0, 0, 0, zone)
		if !next.Equal(want) {
			t.Fatalf("next=%v expected %v after=%v", next, want, after)
		}
	}
	// Even if a manually launched backup took several hours, subsequent planned
	// starts remain on the 7-day grid, not 7 days from the last completion.
	next, err := NextAnchoredOccurrence("interval", 7, "days", "", 0, 0, "Europe/Berlin", anchor, time.Date(2026, 3, 30, 2, 10, 0, 0, zone))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 4, 6, 2, 0, 0, 0, zone)
	if !next.Equal(want) {
		t.Fatalf("schedule drifted: %v want %v", next, want)
	}
}
func TestFixedHourIntervalAnchored(t *testing.T) {
	anchor := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	after := anchor.Add(123 * time.Minute)
	next, err := NextAnchoredOccurrence("interval", 2, "hours", "", 0, 0, "UTC", anchor, after)
	if err != nil || !next.Equal(anchor.Add(4*time.Hour)) {
		t.Fatalf("next %v err %v", next, err)
	}
}
func TestWeeklyAnchoredNotBeforeStart(t *testing.T) {
	anchor := time.Date(2026, 11, 25, 8, 45, 0, 0, time.UTC) // Wednesday
	after := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	next, err := NextAnchoredOccurrence("weekly", 0, "", "1,5", 8, 45, "UTC", anchor, after)
	want := time.Date(2026, 11, 27, 8, 45, 0, 0, time.UTC)
	if err != nil || !next.Equal(want) {
		t.Fatalf("next %v want %v err %v", next, want, err)
	}
}
