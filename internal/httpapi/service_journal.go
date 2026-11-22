package httpapi

import "net/http"

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
	var tail journalTail
	if s.journal != nil {
		tail.Text, tail.Truncated = s.journal.Tail()
	}
	jsonResponse(w, http.StatusOK, tail)
}
