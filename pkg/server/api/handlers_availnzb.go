package api

import (
	"fmt"
	"net/http"

	"streamnzb/pkg/services/availnzb"
)

type availNZBStatusResponse struct {
	Status      *availnzb.MeResponse `json:"status,omitempty"`
	StatusError string               `json:"status_error,omitempty"`
	APIKey      string               `json:"api_key,omitempty"`
}

func (s *Server) handleAvailNZBStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	if !s.requireAdmin(w, r, "Only admin can access AvailNZB key status") {
		return
	}

	s.mu.RLock()
	availNZBURL := s.availNZBURL
	availNZBAPIKey := s.availNZBAPIKey
	s.mu.RUnlock()

	if availNZBURL == "" {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "AvailNZB URL is not configured"})
		return
	}
	if availNZBAPIKey == "" {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "AvailNZB API key is not configured"})
		return
	}

	status, err := availnzb.NewClient(availNZBURL, availNZBAPIKey).GetMe()
	if err != nil {
		writeJSON(w, http.StatusOK, availNZBStatusResponse{
			StatusError: fmt.Sprintf("Failed to fetch AvailNZB key status: %v", err),
			APIKey:      availNZBAPIKey,
		})
		return
	}

	writeJSON(w, http.StatusOK, availNZBStatusResponse{
		Status: status,
		APIKey: availNZBAPIKey,
	})
}
