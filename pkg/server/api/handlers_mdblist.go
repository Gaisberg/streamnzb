package api

import (
	"net/http"

	"streamnzb/pkg/services/metadata/mdblist"
)

// mdblistClientFor reaches one stream's MDBList client through the app
// components; nil when the server was built without an app (tests), MDBList
// has no client id, or no stream was named.
func (s *Server) mdblistClientFor(stream string) *mdblist.Client {
	if s.app == nil {
		return nil
	}
	comp := s.app.Components()
	if comp == nil {
		return nil
	}
	return comp.MDBListClients.For(stream)
}

func (s *Server) mdblistEnabled() bool {
	if s.app == nil {
		return false
	}
	comp := s.app.Components()
	return comp != nil && comp.MDBListClients.Enabled()
}

type mdblistStatus struct {
	// Enabled reports whether a client id is available at all — without one
	// the device flow cannot start and the UI asks for a client id instead.
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	UserName  string `json:"user_name,omitempty"`
}

func (s *Server) currentMDBListStatus(stream string) mdblistStatus {
	status := mdblistStatus{Enabled: s.mdblistEnabled()}
	client := s.mdblistClientFor(stream)
	if status.Enabled && client.Connected() {
		status.Connected = true
		status.UserName = client.UserName()
	}
	return status
}

// handleMDBListStatus reports whether this stream has an MDBList account
// linked.
func (s *Server) handleMDBListStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can view MDBList status", http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, s.currentMDBListStatus(accountStreamParam(r)))
}

// handleMDBListDevice starts the device-code flow for one stream. The device
// code itself stays on the server — it is the bearer of the pending grant.
func (s *Server) handleMDBListDevice(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can link an MDBList account", http.MethodPost) {
		return
	}
	client := s.mdblistClientFor(accountStreamParam(r))
	if !s.mdblistEnabled() {
		writeJSONError(w, http.StatusServiceUnavailable, "No MDBList client id is configured")
		return
	}
	if client == nil {
		writeJSONError(w, http.StatusBadRequest, "A stream is required to link an MDBList account")
		return
	}
	device, err := client.StartDevice(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, device)
}

// handleMDBListDeviceCheck polls the started authorization. The frontend calls
// it at the interval MDBList dictated until connected turns true, the user
// denies it, or the code expires.
func (s *Server) handleMDBListDeviceCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can link an MDBList account", http.MethodPost) {
		return
	}
	stream := accountStreamParam(r)
	client := s.mdblistClientFor(stream)
	if !s.mdblistEnabled() || client == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "No MDBList client id is configured")
		return
	}
	connected, err := client.CheckDevice(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"connected": connected,
		"status":    s.currentMDBListStatus(stream),
	})
}

// handleMDBListDisconnect unlinks this stream's account.
func (s *Server) handleMDBListDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can unlink an MDBList account", http.MethodPost) {
		return
	}
	stream := accountStreamParam(r)
	s.mdblistClientFor(stream).Disconnect()
	writeJSON(w, http.StatusOK, s.currentMDBListStatus(stream))
}
