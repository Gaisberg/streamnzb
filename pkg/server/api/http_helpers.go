package api

import (
	"encoding/json"
	"net/http"

	"streamnzb/pkg/auth"
)

// writeJSON sends v as a JSON response with the given status.
//
// The header must be set before WriteHeader — several handlers used to set it
// after, which silently dropped the content type. Routing every response
// through here makes that ordering impossible to get wrong.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError sends {"error": msg} with the given status.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// requireMethod rejects requests whose method is not in allowed, replying 405.
// Returns true when the request may proceed.
func requireMethod(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for _, m := range allowed {
		if r.Method == m {
			return true
		}
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	return false
}

// adminUsername reads the configured admin name under the read lock. Config is
// swapped wholesale on save (see applyConfigPatch), so an unlocked read races
// the reload goroutine.
func (s *Server) adminUsername() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.GetAdminUsername()
}

// adminPasswordHash and adminToken read the admin credentials under the same
// read lock and for the same reason as adminUsername.
func (s *Server) adminPasswordHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.AdminPasswordHash
}

func (s *Server) adminToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.AdminToken
}

// adminMustChangePassword reports whether the admin is still on the credential
// it was installed with. Locked for a second reason on top of the config swap:
// handleChangePassword clears this field in place, so it is one of the few that
// changes without the surrounding Config being replaced.
func (s *Server) adminMustChangePassword() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.AdminMustChangePassword
}

// isAdmin reports whether the request carries the admin stream.
func (s *Server) isAdmin(r *http.Request) bool {
	stream, _ := auth.StreamFromContext(r)
	return stream != nil && stream.Username == s.adminUsername()
}

// requireAdmin gates a handler on method and admin identity, replying 405 or
// 403 with forbidMsg. Returns true when the request may proceed. Passing no
// methods skips the method check.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request, forbidMsg string, allowed ...string) bool {
	if len(allowed) > 0 && !requireMethod(w, r, allowed...) {
		return false
	}
	if !s.isAdmin(r) {
		http.Error(w, forbidMsg, http.StatusForbidden)
		return false
	}
	return true
}
