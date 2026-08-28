package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"streamnzb/pkg/services/metadata/simkl"
)

// simklClient reaches the shared Simkl client through the app components; nil
// when the server was built without an app (tests) or Simkl has no client id.
func (s *Server) simklClient() *simkl.Client {
	if s.app == nil {
		return nil
	}
	comp := s.app.Components()
	if comp == nil {
		return nil
	}
	return comp.SimklClient
}

type simklStatus struct {
	// Enabled reports whether a client id is available at all — without one
	// the PIN flow cannot start and the UI asks for a client id instead.
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	UserName  string `json:"user_name,omitempty"`
}

func (s *Server) currentSimklStatus() simklStatus {
	client := s.simklClient()
	status := simklStatus{Enabled: client.Enabled()}
	if status.Enabled && client.Connected() {
		status.Connected = true
		status.UserName = client.UserName()
	}
	return status
}

// handleSimklStatus reports whether a Simkl account is linked, for the
// Metadata page's account card.
func (s *Server) handleSimklStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can view Simkl status", http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, s.currentSimklStatus())
}

// handleSimklPin starts the PIN device flow and returns the code to display
// plus the polling contract for handleSimklPinCheck.
func (s *Server) handleSimklPin(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can link a Simkl account", http.MethodPost) {
		return
	}
	client := s.simklClient()
	if !client.Enabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "No Simkl client id is configured")
		return
	}
	pin, err := client.StartPIN(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pin)
}

// handleSimklPinCheck polls one started PIN authorization. The frontend calls
// it at the interval Simkl dictated until connected turns true or the code
// expires.
func (s *Server) handleSimklPinCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can link a Simkl account", http.MethodPost) {
		return
	}
	var body struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.UserCode) == "" {
		writeJSONError(w, http.StatusBadRequest, "user_code is required")
		return
	}
	client := s.simklClient()
	if !client.Enabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "No Simkl client id is configured")
		return
	}
	connected, err := client.CheckPIN(r.Context(), body.UserCode)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"connected": connected,
		"status":    s.currentSimklStatus(),
	})
}

// handleSimklDisconnect unlinks the account.
func (s *Server) handleSimklDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can unlink a Simkl account", http.MethodPost) {
		return
	}
	s.simklClient().Disconnect()
	writeJSON(w, http.StatusOK, s.currentSimklStatus())
}
