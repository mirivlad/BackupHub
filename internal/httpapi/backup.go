package httpapi

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/mirivlad/BackupHub/internal/backup"
)

// backupAPI keeps all source/restore information behind session authentication.
// Credentials are accepted only on Create; no GET response returns them.
func (s *Server) backupAPI(w http.ResponseWriter, r *http.Request) {
	if s.backup == nil {
		jsonResponse(w, 503, map[string]string{"error": "backup engine unavailable"})
		return
	}
	user, _, csrf, ok := s.current(w, r)
	if !ok {
		return
	}
	isAdmin := user.Role == "administrator"
	canBackup := isAdmin || user.Role == "backup_operator"
	canRestore := isAdmin || user.Role == "restore_operator"
	isRead := canBackup || canRestore || user.Role == "viewer" || user.Role == "auditor"
	if !isRead {
		jsonResponse(w, 403, map[string]string{"error": "permission denied"})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !csrfValid(r, csrf) {
			jsonResponse(w, 403, map[string]string{"error": "invalid CSRF token"})
			return
		}
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/backup/")
	path := strings.Split(strings.Trim(suffix, "/"), "/")
	fail := func(err error) {
		if errors.Is(err, sql.ErrNoRows) {
			jsonResponse(w, 404, map[string]string{"error": "resource not found"})
			return
		}
		jsonResponse(w, 400, map[string]string{"error": publicError(err)})
	}
	if len(path) == 1 {
		switch path[0] {
		case "status":
			if !method(w, r, http.MethodGet) {
				return
			}
			jsonResponse(w, 200, map[string]any{"engine": "restic", "connected": true, "supported_sources": []string{"local", "smb", "sftp", "postgres"}, "production_ready": false})
			return
		case "sources":
			switch r.Method {
			case http.MethodGet:
				sources, err := s.backup.ListSources(r.Context())
				if err != nil {
					fail(err)
					return
				}
				jsonResponse(w, 200, map[string]any{"sources": sources})
			case http.MethodPost:
				if !isAdmin {
					jsonResponse(w, 403, map[string]string{"error": "admin required"})
					return
				}
				var input backup.CreateSource
				if !decode(w, r, &input) {
					return
				}
				output, err := s.backup.Create(r.Context(), input)
				if err != nil {
					fail(err)
					return
				}
				s.backup.Audit(r.Context(), user.ID, "source.created", output.ID)
				jsonResponse(w, 201, output)
			default:
				method(w, r, http.MethodGet)
			}
			return
		case "runs":
			if !method(w, r, http.MethodGet) {
				return
			}
			runs, err := s.backup.RecentRuns(r.Context())
			if err != nil {
				fail(err)
				return
			}
			jsonResponse(w, 200, map[string]any{"runs": runs})
			return
		case "snapshots":
			if !method(w, r, http.MethodGet) {
				return
			}
			snapshots, err := s.backup.ListSnapshots(r.Context(), r.URL.Query().Get("source_id"))
			if err != nil {
				fail(err)
				return
			}
			jsonResponse(w, 200, map[string]any{"snapshots": snapshots})
			return
		}
	}
	if len(path) == 3 && path[0] == "sources" {
		id, operation := path[1], path[2]
		if !canBackup || r.Method != http.MethodPost {
			jsonResponse(w, 403, map[string]string{"error": "backup permission and POST required"})
			return
		}
		switch operation {
		case "run":
			if err := s.backup.Manual(r.Context(), id); err != nil {
				fail(err)
				return
			}
			s.backup.Audit(r.Context(), user.ID, "backup.queued", id)
			jsonResponse(w, 202, map[string]any{"queued": true, "source_id": id})
		case "pause", "resume":
			if !isAdmin {
				jsonResponse(w, 403, map[string]string{"error": "admin required"})
				return
			}
			if err := s.backup.Pause(r.Context(), id, operation == "resume"); err != nil {
				fail(err)
				return
			}
			s.backup.Audit(r.Context(), user.ID, "schedule."+operation, id)
			jsonResponse(w, 200, map[string]any{"source_id": id, "enabled": operation == "resume"})
		default:
			jsonResponse(w, 404, map[string]string{"error": "unknown source operation"})
		}
		return
	}
	if len(path) == 3 && path[0] == "snapshots" {
		id, operation := path[1], path[2]
		switch operation {
		case "files":
			if !method(w, r, http.MethodGet) {
				return
			}
			files, err := s.backup.Files(r.Context(), id)
			if err != nil {
				fail(err)
				return
			}
			jsonResponse(w, 200, map[string]any{"files": files})
			return
		case "download":
			if !canRestore {
				jsonResponse(w, 403, map[string]string{"error": "restore permission required"})
				return
			}
			if !method(w, r, http.MethodGet) {
				return
			}
			selections := r.URL.Query()["path"]
			if len(selections) > 64 {
				jsonResponse(w, 400, map[string]string{"error": "too many paths"})
				return
			}
			single := r.URL.Query().Get("mode") == "single"
			s.backup.Audit(r.Context(), user.ID, "snapshot.export_requested", id)
			if err := s.backup.Download(r.Context(), id, selections, single, w); err != nil {
				// Streaming may have started. Don't write a second HTTP response to an
				// already-written ZIP or raw-file response.
				slog.Warn("snapshot download aborted", "snapshot_id", id, "error", publicError(err))
			}
			return
		case "restore":
			if !canRestore {
				jsonResponse(w, 403, map[string]string{"error": "restore permission required"})
				return
			}
			if !method(w, r, http.MethodPost) {
				return
			}
			var req struct {
				Paths []string `json:"paths"`
			}
			if !decode(w, r, &req) {
				return
			}
			destination, err := s.backup.RestoreToNewDirectory(r.Context(), id, req.Paths)
			if err != nil {
				fail(err)
				return
			}
			s.backup.Audit(r.Context(), user.ID, "snapshot.restore_to_new", id)
			jsonResponse(w, 201, map[string]any{"directory": destination, "in_place": false})
			return
		}
	}
	jsonResponse(w, 404, map[string]string{"error": "unknown backup endpoint"})
}
func publicError(err error) string {
	if err == nil {
		return ""
	}
	// Never copy raw errors from child command diagnostics into HTTP responses:
	// they may contain usernames, source paths or protocol details.
	if errors.Is(err, sql.ErrNoRows) {
		return "resource not found"
	}
	return "operation rejected or failed; consult server journal for details"
}
