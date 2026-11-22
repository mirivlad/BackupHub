package httpapi

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

const journalMaxBytes = 512 << 10

type journalTail struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func (s *Server) serviceJournal(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	user, _, _, ok := s.current(w, r)
	if !ok {
		return
	}
	if user.Role != "administrator" {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "administrator required"})
		return
	}
	if !s.limits.allow(r, "service_journal", 60) {
		jsonResponse(w, http.StatusTooManyRequests, map[string]string{"error": "Слишком частые запросы к журналу службы"})
		return
	}
	tail, err := readServiceJournal(r.Context(), "/usr/bin/journalctl")
	if err != nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "Журнал systemd для BackupHub недоступен"})
		return
	}
	jsonResponse(w, http.StatusOK, tail)
}

func readServiceJournal(ctx context.Context, binary string) (journalTail, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Use the daemon's existing read permissions. Keep the unit and every
	// argument fixed so callers can only see BackupHub's service journal.
	cmd := exec.CommandContext(ctx, binary, "--system", "--unit=backuphub.service",
		"--lines=100", "--output=short-iso", "--reverse", "--no-pager", "--quiet")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return journalTail{}, err
	}
	if err = cmd.Start(); err != nil {
		return journalTail{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, journalMaxBytes+1))
	if len(data) > journalMaxBytes || readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return journalTail{}, readErr
	}
	if len(data) > journalMaxBytes {
		return journalTail{Text: strings.TrimSpace(string(data[:journalMaxBytes])), Truncated: true}, nil
	}
	if waitErr != nil {
		return journalTail{}, waitErr
	}
	return journalTail{Text: strings.TrimSpace(string(data))}, nil
}
