package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/services/availnzb"
)

type availNZBStatusResponse struct {
	Status         *availnzb.MeResponse `json:"status,omitempty"`
	RecoverySecret string               `json:"recovery_secret,omitempty"`
	StatusError    string               `json:"status_error,omitempty"`
}

func (s *Server) handleAvailNZBStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Only admin can access AvailNZB key status", http.StatusForbidden)
		return
	}

	s.mu.RLock()
	availNZBURL := s.availNZBURL
	availNZBAPIKey := s.availNZBAPIKey
	s.mu.RUnlock()

	if availNZBURL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "AvailNZB URL is not configured"})
		return
	}
	if availNZBAPIKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "AvailNZB API key is not configured"})
		return
	}

	recoverySecret, err := availnzb.LoadStoredRecoverySecret(s.attemptLister)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Failed to load AvailNZB recovery secret: %v", err)})
		return
	}

	status, err := availnzb.NewClient(availNZBURL, availNZBAPIKey).GetMe()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(availNZBStatusResponse{
			RecoverySecret: recoverySecret,
			StatusError:    fmt.Sprintf("Failed to fetch AvailNZB key status: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(availNZBStatusResponse{
		Status:         status,
		RecoverySecret: recoverySecret,
	})
}
