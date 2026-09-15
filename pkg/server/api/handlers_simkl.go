package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"streamnzb/pkg/services/metadata/simkl"
)

// simklClientFor reaches one stream's Simkl client through the app components;
// nil when the server was built without an app (tests), Simkl has no client
// id, or no stream was named.
func (s *Server) simklClientFor(stream string) *simkl.Client {
	if s.app == nil {
		return nil
	}
	comp := s.app.Components()
	if comp == nil {
		return nil
	}
	return comp.SimklClients.For(stream)
}

func (s *Server) simklEnabled() bool {
	if s.app == nil {
		return false
	}
	comp := s.app.Components()
	return comp != nil && comp.SimklClients.Enabled()
}

// accountStreamParam is the stream whose account a link request is about.
// Accounts are per stream, so every one of these endpoints needs to know which
// stream it is acting for; a request without one is a bug in the caller, not a
// default worth guessing.
func accountStreamParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("stream"))
}

type simklStatus struct {
	// Enabled reports whether a client id is available at all — without one
	// the PIN flow cannot start and the UI asks for a client id instead.
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	UserName  string `json:"user_name,omitempty"`
}

func (s *Server) currentSimklStatus(stream string) simklStatus {
	status := simklStatus{Enabled: s.simklEnabled()}
	client := s.simklClientFor(stream)
	if status.Enabled && client.Connected() {
		status.Connected = true
		status.UserName = client.UserName()
	}
	return status
}

// handleSimklStatus reports whether this stream has a Simkl account linked.
func (s *Server) handleSimklStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can view Simkl status", http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, s.currentSimklStatus(accountStreamParam(r)))
}

// handleSimklPin starts the PIN device flow for one stream and returns the
// code to display plus the polling contract for handleSimklPinCheck.
func (s *Server) handleSimklPin(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can link a Simkl account", http.MethodPost) {
		return
	}
	client := s.simklClientFor(accountStreamParam(r))
	if !s.simklEnabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "No Simkl client id is configured")
		return
	}
	if client == nil {
		writeJSONError(w, http.StatusBadRequest, "A stream is required to link a Simkl account")
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
	stream := accountStreamParam(r)
	client := s.simklClientFor(stream)
	if !s.simklEnabled() || client == nil {
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
		"status":    s.currentSimklStatus(stream),
	})
}

// handleSimklDisconnect unlinks this stream's account, leaving every other
// stream's alone.
func (s *Server) handleSimklDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can unlink a Simkl account", http.MethodPost) {
		return
	}
	stream := accountStreamParam(r)
	s.simklClientFor(stream).Disconnect()
	writeJSON(w, http.StatusOK, s.currentSimklStatus(stream))
}
