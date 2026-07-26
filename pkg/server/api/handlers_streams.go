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

func (s *Server) handleManagedStreams(w http.ResponseWriter, r *http.Request) {
	path := trimStreamAPIPath(r.URL.Path)
	if path == "configs" {
		s.handlePutStreamConfigs(w, r)
		return
	}
	if path == "" {
		if r.Method == http.MethodGet {
			s.handleStreamsList(w, r)
			return
		}
		if r.Method == http.MethodPost {
			s.handleStreamsCreate(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleStreamByUsername(w, r)
}

func (s *Server) handleStreamsList(w http.ResponseWriter, r *http.Request) {
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
			"auto_add_providers":    d.AutoAddProviders,
			"auto_add_indexers":     d.AutoAddIndexers,
			"indexer_overrides":     d.IndexerOverrides,
			"provider_selections":   d.ProviderSelections,
			"indexer_selections":    d.IndexerSelections,
			"movie_search_queries":  d.MovieSearchQueries,
			"series_search_queries": d.SeriesSearchQueries,
			"filter_profile_name":   d.FilterProfileName,
			"mute_error_video":      d.MuteErrorVideo,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (s *Server) handleStreamsCreate(w http.ResponseWriter, r *http.Request) {
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
	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"user":    map[string]interface{}{"username": d.Username, "token": d.Token},
	})
}

func (s *Server) handleStreamByUsername(w http.ResponseWriter, r *http.Request) {
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
			"auto_add_providers":    d.AutoAddProviders,
			"auto_add_indexers":     d.AutoAddIndexers,
			"indexer_overrides":     d.IndexerOverrides,
			"provider_selections":   d.ProviderSelections,
			"indexer_selections":    d.IndexerSelections,
			"movie_search_queries":  d.MovieSearchQueries,
			"series_search_queries": d.SeriesSearchQueries,
			"filter_profile_name":   d.FilterProfileName,
			"mute_error_video":      d.MuteErrorVideo,
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
		if s.strmServer != nil {
			s.strmServer.ClearSearchCaches()
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

func (s *Server) handlePutStreamConfigs(w http.ResponseWriter, r *http.Request) {
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
		AutoAddProviders    *bool                                 `json:"auto_add_providers"`
		AutoAddIndexers     *bool                                 `json:"auto_add_indexers"`
		IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
		ProviderSelections  []string                              `json:"provider_selections"`
		IndexerSelections   []string                              `json:"indexer_selections"`
		MovieSearchQueries  []string                              `json:"movie_search_queries"`
		SeriesSearchQueries []string                              `json:"series_search_queries"`
		FilterProfileName   string                                `json:"filter_profile_name"`
		FilterProfileByType map[string]string                     `json:"filter_profile_by_type"`
		MuteErrorVideo      *bool                                 `json:"mute_error_video"`
	}
	if err := json.NewDecoder(r.Body).Decode(&streamConfigs); err != nil {
		s.writeSaveStatus(w, "error", "Invalid stream config data", nil)
		return
	}
	var (
		errors  []string
		updated bool
	)
	for username, dc := range streamConfigs {
		if username == s.config.GetAdminUsername() {
			continue
		}
		providerSelections := append([]string(nil), dc.ProviderSelections...)
		indexerSelections := append([]string(nil), dc.IndexerSelections...)
		indexerOverrides := cloneIndexerOverrides(dc.IndexerOverrides)
		if dc.AutoAddProviders != nil && *dc.AutoAddProviders {
			providerSelections = syncOrderedSelections(providerSelections, enabledProviderNames(s.config.Providers))
		}
		if dc.AutoAddIndexers != nil && *dc.AutoAddIndexers {
			indexerSelections = syncOrderedSelections(indexerSelections, enabledIndexerNames(s.config.Indexers))
			indexerOverrides = filterIndexerOverrides(indexerOverrides, indexerSelections)
		}
		if err := s.streamManager.UpdateStreamConfig(username, &auth.Stream{
			FilterSortingMode:   dc.FilterSortingMode,
			IndexerMode:         dc.IndexerMode,
			UseAvailNZB:         dc.UseAvailNZB,
			CombineResults:      dc.CombineResults,
			EnableFailover:      dc.EnableFailover,
			ResultsMode:         dc.ResultsMode,
			AutoAddProviders:    dc.AutoAddProviders,
			AutoAddIndexers:     dc.AutoAddIndexers,
			IndexerOverrides:    indexerOverrides,
			ProviderSelections:  providerSelections,
			IndexerSelections:   indexerSelections,
			MovieSearchQueries:  dc.MovieSearchQueries,
			SeriesSearchQueries: dc.SeriesSearchQueries,
			FilterProfileName:   dc.FilterProfileName,
			FilterProfileByType: dc.FilterProfileByType,
			MuteErrorVideo:      dc.MuteErrorVideo,
		}); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update stream config for %s: %v", username, err))
			continue
		}
		updated = true
	}
	if updated {
		if s.strmServer != nil {
			s.strmServer.ClearSearchCaches()
		}
		// Stream settings live in the config, so open pages need the new copy.
		s.broadcastConfig()
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
		"message": "Stream configurations saved successfully. Search cache cleared.",
	})
}
