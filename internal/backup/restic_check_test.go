package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResticCheckIncludesStdoutAndStderrOnFailure(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "restic")
	const script = `#!/bin/sh
printf 'pack 123 failed its hash check\n'
printf 'Fatal: repository contains errors\n' >&2
exit 1
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	repo := repository{binary: binary}
	err := repo.check(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "pack 123 failed its hash check") ||
		!strings.Contains(err.Error(), "Fatal: repository contains errors") {
		t.Fatalf("missing verification diagnostics: %v", err)
	}
}
