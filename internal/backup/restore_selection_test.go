package backup

import (
	"strings"
	"testing"
)

func TestPlanRestoreCheckScopesAndLimits(t *testing.T) {
	tree := []treeEntry{
		{Path: "/root/alpha", Type: "dir"},
		{Path: "/root/alpha/f", Type: "file", Size: 2},
		{Path: "/root/alpha/nested/f", Type: "file", Size: 3},
		{Path: "/root/alpha/link", Type: "symlink"},
		{Path: "/root/alphabet/f", Type: "file", Size: 20 << 20},
		{Path: "/root/omega/f", Type: "file", Size: 300 << 20},
	}
	walk := func(visit func(treeEntry) error) error {
		for _, entry := range tree {
			if err := visit(entry); err != nil {
				return err
			}
		}
		return nil
	}
	for _, tc := range []struct {
		scope string
		files int
		bytes int64
	}{
		{"alpha/f", 1, 2}, {"alpha", 2, 5}, {"omega/f", 1, 300 << 20},
	} {
		if got, bytes, err := planRestoreCheck(walk, "/root", tc.scope); err != nil || len(got) != tc.files || bytes != tc.bytes {
			t.Fatalf("%q: %v %d %v", tc.scope, got, bytes, err)
		}
	}
	if got, _, err := planRestoreCheck(walk, "/root", ""); err != nil || len(got) < 2 || got[len(got)-1].Path != "/root/alphabet/f" {
		t.Fatalf("auto sampling did not span size bands: %v %v", got, err)
	}
	tree = []treeEntry{{Path: "/root/empty", Type: "file"}, {Path: "/root/large", Type: "file", Size: 3 << 30}}
	if _, _, err := planRestoreCheck(walk, "/root", ""); err == nil || !strings.Contains(err.Error(), "не выполнялась") {
		t.Fatalf("all-large snapshot presented as checked: %v", err)
	}
	if _, _, err := planRestoreCheck(walk, "/root", "large"); err == nil || !strings.Contains(err.Error(), "лимит") {
		t.Fatalf("explicit scope exceeded the cap: %v", err)
	}
}
