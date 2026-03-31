package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
)

const (
	apiDevicesPrefix = "/api/devices"
	apiStreamsPrefix = "/api/streams"
)

func trimStreamAPIPath(path string) string {
	path = strings.TrimPrefix(path, apiStreamsPrefix)
	path = strings.TrimPrefix(path, apiDevicesPrefix)
	return strings.Trim(path, "/")
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	path := trimStreamAPIPath(r.URL.Path)
	if path == "configs" {
		s.handlePutDeviceConfigs(w, r)
		return
	}
	if path == "" {
		if r.Method == http.MethodGet {
			s.handleDevicesList(w, r)
			return
		}
		if r.Method == http.MethodPost {
			s.handleDevicesCreate(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleDeviceByUsername(w, r)
}

func (s *Server) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Only admin can access streams list", http.StatusForbidden)
		return
	}
	streams := s.streamManager.GetAllStreams()
	list := make([]map[string]interface{}, 0, len(streams))
	for _, d := range streams {
		list = append(list, map[string]interface{}{
			"username":              d.Username,
			"token":                 d.Token,
			"filter_sorting_mode":   d.FilterSortingMode,
			"indexer_mode":          d.IndexerMode,
			"use_availnzb":          d.UseAvailNZB,
			"combine_results":       d.CombineResults,
			"enable_failover":       d.EnableFailover,
			"results_mode":          d.ResultsMode,
			"indexer_overrides":     d.IndexerOverrides,
			"provider_selections":   d.ProviderSelections,
			"indexer_selections":    d.IndexerSelections,
			"movie_search_queries":  d.MovieSearchQueries,
			"series_search_queries": d.SeriesSearchQueries,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (s *Server) handleDevicesCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Only admin can create streams", http.StatusForbidden)
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	d, err := s.streamManager.CreateStream(req.Username, "", s.config.GetAdminUsername())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"user":    map[string]interface{}{"username": d.Username, "token": d.Token},
	})
}

func (s *Server) handleDeviceByUsername(w http.ResponseWriter, r *http.Request) {
	path := trimStreamAPIPath(r.URL.Path)
	parts := strings.SplitN(path, "/", 2)
	username := parts[0]
	if username == "" {
		http.Error(w, "username required", http.StatusBadRequest)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	suffix := ""
	if len(parts) > 1 {
		suffix = parts[1]
	}
	switch r.Method {
	case http.MethodGet:
		if suffix != "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		d, err := s.streamManager.GetStream(username, s.config.GetAdminUsername())
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"username":              d.Username,
			"token":                 d.Token,
			"filter_sorting_mode":   d.FilterSortingMode,
			"indexer_mode":          d.IndexerMode,
			"use_availnzb":          d.UseAvailNZB,
			"combine_results":       d.CombineResults,
			"enable_failover":       d.EnableFailover,
			"results_mode":          d.ResultsMode,
			"indexer_overrides":     d.IndexerOverrides,
			"provider_selections":   d.ProviderSelections,
			"indexer_selections":    d.IndexerSelections,
			"movie_search_queries":  d.MovieSearchQueries,
			"series_search_queries": d.SeriesSearchQueries,
		})
	case http.MethodDelete:
		if suffix != "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := s.streamManager.DeleteStream(username); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Stream %s deleted successfully", username),
		})
	case http.MethodPost:
		if suffix != "regenerate-token" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		token, err := s.streamManager.RegenerateToken(username)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "token": token})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePutDeviceConfigs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Only admin can save stream configurations", http.StatusForbidden)
		return
	}
	var streamConfigs map[string]struct {
		FilterSortingMode   string                                `json:"filter_sorting_mode"`
		IndexerMode         string                                `json:"indexer_mode"`
		UseAvailNZB         *bool                                 `json:"use_availnzb"`
		CombineResults      *bool                                 `json:"combine_results"`
		EnableFailover      *bool                                 `json:"enable_failover"`
		ResultsMode         string                                `json:"results_mode"`
		IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
		ProviderSelections  []string                              `json:"provider_selections"`
		IndexerSelections   []string                              `json:"indexer_selections"`
		MovieSearchQueries  []string                              `json:"movie_search_queries"`
		SeriesSearchQueries []string                              `json:"series_search_queries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&streamConfigs); err != nil {
		s.writeSaveStatus(w, "error", "Invalid stream config data", nil)
		return
	}
	var errors []string
	for username, dc := range streamConfigs {
		if username == s.config.GetAdminUsername() {
			continue
		}
		if err := s.streamManager.UpdateStreamConfig(username, &auth.Device{
			FilterSortingMode:   dc.FilterSortingMode,
			IndexerMode:         dc.IndexerMode,
			UseAvailNZB:         dc.UseAvailNZB,
			CombineResults:      dc.CombineResults,
			EnableFailover:      dc.EnableFailover,
			ResultsMode:         dc.ResultsMode,
			IndexerOverrides:    dc.IndexerOverrides,
			ProviderSelections:  dc.ProviderSelections,
			IndexerSelections:   dc.IndexerSelections,
			MovieSearchQueries:  dc.MovieSearchQueries,
			SeriesSearchQueries: dc.SeriesSearchQueries,
		}); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update stream config for %s: %v", username, err))
		}
	}
	if len(errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "error",
			"message": "Some stream configs failed to save",
			"errors":  errors,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Stream configurations saved successfully",
	})
}
