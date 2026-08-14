package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/server/stremio"
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

// streamToMap projects a stream onto the JSON shape the admin UI expects.
func streamToMap(d *auth.Stream) map[string]interface{} {
	return map[string]interface{}{
		"username":                    d.Username,
		"token":                       d.Token,
		"filter_sorting_mode":         d.FilterSortingMode,
		"indexer_mode":                d.IndexerMode,
		"use_availnzb":                d.UseAvailNZB,
		"filter_availnzb":             d.FilterAvailNZB,
		"combine_results":             d.CombineResults,
		"enable_failover":             d.EnableFailover,
		"results_mode":                d.ResultsMode,
		"auto_add_providers":          d.AutoAddProviders,
		"auto_add_indexers":           d.AutoAddIndexers,
		"indexer_overrides":           d.IndexerOverrides,
		"provider_selections":         d.ProviderSelections,
		"provider_connection_limits":  d.ProviderConnectionLimits,
		"disabled_providers":          d.DisabledProviders,
		"indexer_selections":          d.IndexerSelections,
		"movie_search_queries":        d.MovieSearchQueries,
		"series_search_queries":       d.SeriesSearchQueries,
		"filter_profile_name":         d.FilterProfileName,
		"filter_profile_by_type":      d.FilterProfileByType,
		"mute_error_video":            d.MuteErrorVideo,
		"result_name_template":        d.ResultNameTemplate,
		"result_description_template": d.ResultDescriptionTemplate,
		"addon_name":                  d.AddonName,
	}
}

func (s *Server) handleStreamsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can access streams list", http.MethodGet) {
		return
	}
	streams := s.streamManager.GetAllStreams()
	list := make([]map[string]interface{}, 0, len(streams))
	for _, d := range streams {
		list = append(list, streamToMap(&d))
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleStreamsCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can create streams", http.MethodPost) {
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	d, err := s.streamManager.CreateStream(req.Username, "", s.adminUsername())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
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
	if !s.requireAdmin(w, r, "Forbidden") {
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
		d, err := s.streamManager.GetStream(username, s.adminUsername())
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, streamToMap(d))
	case http.MethodDelete:
		if suffix != "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := s.streamManager.DeleteStream(username); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if s.strmServer != nil {
			s.strmServer.ClearSearchCaches()
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Stream %s deleted successfully", username),
		})
	case http.MethodPost:
		switch suffix {
		case "regenerate-token":
			token, err := s.streamManager.RegenerateToken(username)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "token": token})
		case "rename":
			s.handleStreamRename(w, r, username)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStreamRename renames a stream in place. The token is untouched, so an
// already-installed addon keeps working; what moves with the name is the
// stream's own history, which the history UI filters by name.
func (s *Server) handleStreamRename(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	newName := strings.TrimSpace(req.Username)
	if err := s.streamManager.RenameStream(username, newName, s.adminUsername()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	if mgr != nil {
		if updated, err := mgr.RenameStreamReferences(username, newName); err != nil {
			// The stream itself is renamed; stale history is worth a log, not a
			// failed request the admin cannot act on.
			logger.Error("Renaming stream history failed", "from", username, "to", newName, "err", err)
		} else if updated > 0 {
			logger.Info("Repointed stream history", "from", username, "to", newName, "rows", updated)
		}
	}

	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	s.broadcastConfig()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"username": newName,
		"message":  fmt.Sprintf("Stream renamed to %s", newName),
	})
}

func (s *Server) handlePutStreamConfigs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can save stream configurations", http.MethodPut) {
		return
	}
	var streamConfigs map[string]struct {
		FilterSortingMode         string                                `json:"filter_sorting_mode"`
		IndexerMode               string                                `json:"indexer_mode"`
		UseAvailNZB               *bool                                 `json:"use_availnzb"`
		FilterAvailNZB            *bool                                 `json:"filter_availnzb"`
		CombineResults            *bool                                 `json:"combine_results"`
		EnableFailover            *bool                                 `json:"enable_failover"`
		ResultsMode               string                                `json:"results_mode"`
		AutoAddProviders          *bool                                 `json:"auto_add_providers"`
		AutoAddIndexers           *bool                                 `json:"auto_add_indexers"`
		IndexerOverrides          map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
		ProviderSelections        []string                              `json:"provider_selections"`
		ProviderConnectionLimits  map[string]int                        `json:"provider_connection_limits"`
		DisabledProviders         []string                              `json:"disabled_providers"`
		IndexerSelections         []string                              `json:"indexer_selections"`
		MovieSearchQueries        []string                              `json:"movie_search_queries"`
		SeriesSearchQueries       []string                              `json:"series_search_queries"`
		FilterProfileName         string                                `json:"filter_profile_name"`
		FilterProfileByType       map[string]string                     `json:"filter_profile_by_type"`
		MuteErrorVideo            *bool                                 `json:"mute_error_video"`
		ResultNameTemplate        string                                `json:"result_name_template"`
		ResultDescriptionTemplate string                                `json:"result_description_template"`
		AddonName                 string                                `json:"addon_name"`
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
		if username == s.adminUsername() {
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
		// Sanitized after the sync so per-provider settings survive a provider
		// that sync just added back to the list.
		connectionLimits := sanitizeConnectionLimits(dc.ProviderConnectionLimits, providerSelections, s.config.Providers)
		disabledProviders := sanitizeDisabledProviders(dc.DisabledProviders, providerSelections)
		// An empty provider list means "every provider" downstream, so disabling
		// all of them would silently do the opposite of what was asked.
		if len(providerSelections) > 0 && len(disabledProviders) == len(providerSelections) {
			errors = append(errors, fmt.Sprintf("Stream %s must keep at least one provider enabled", username))
			continue
		}
		if err := stremio.ValidateResultTemplates(dc.ResultNameTemplate, dc.ResultDescriptionTemplate); err != nil {
			errors = append(errors, fmt.Sprintf("Invalid result format for %s: %v", username, err))
			continue
		}
		if err := s.streamManager.UpdateStreamConfig(username, &auth.Stream{
			FilterSortingMode:         dc.FilterSortingMode,
			IndexerMode:               dc.IndexerMode,
			UseAvailNZB:               dc.UseAvailNZB,
			FilterAvailNZB:            dc.FilterAvailNZB,
			CombineResults:            dc.CombineResults,
			EnableFailover:            dc.EnableFailover,
			ResultsMode:               dc.ResultsMode,
			AutoAddProviders:          dc.AutoAddProviders,
			AutoAddIndexers:           dc.AutoAddIndexers,
			IndexerOverrides:          indexerOverrides,
			ProviderSelections:        providerSelections,
			ProviderConnectionLimits:  connectionLimits,
			DisabledProviders:         disabledProviders,
			IndexerSelections:         indexerSelections,
			MovieSearchQueries:        dc.MovieSearchQueries,
			SeriesSearchQueries:       dc.SeriesSearchQueries,
			FilterProfileName:         dc.FilterProfileName,
			FilterProfileByType:       dc.FilterProfileByType,
			MuteErrorVideo:            dc.MuteErrorVideo,
			ResultNameTemplate:        dc.ResultNameTemplate,
			ResultDescriptionTemplate: dc.ResultDescriptionTemplate,
			AddonName:                 stremio.NormalizeAddonName(dc.AddonName),
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
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":  "error",
			"message": "Some stream configs failed to save",
			"errors":  errors,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"message": "Stream configurations saved successfully. Search cache cleared.",
	})
}
