package servicejournal

import (
	"fmt"
	"strings"
	"testing"
)

func TestJournalBoundsAndOrder(t *testing.T) {
	var journal Journal
	for i := 0; i <= maxLines; i++ {
		fmt.Fprintf(&journal, "event-%d\n", i)
	}
	tail, truncated := journal.Tail()
	lines := strings.Split(tail, "\n")
	if !truncated || len(lines) != maxLines || lines[0] != "event-100" || lines[maxLines-1] != "event-1" {
		t.Fatalf("unexpected journal tail: %d lines, truncated=%v", len(lines), truncated)
	}
	large := strings.Repeat("a", maxBytes*2)
	fmt.Fprint(&journal, large)
	tail, truncated = journal.Tail()
	if !truncated || len(tail) != maxBytes-1 || tail != large[:maxBytes-1] {
		t.Fatalf("unexpected journal byte cap: %d, truncated=%v", len(tail), truncated)
	}
}
