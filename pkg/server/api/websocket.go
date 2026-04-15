package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/validation"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {

	stream, ok := auth.StreamFromContext(r)
	if !ok {

		cookie, err := r.Cookie("auth_session")
		if err == nil && cookie != nil {
			stream, err = s.streamManager.AuthenticateToken(cookie.Value, s.config.GetAdminUsername(), s.config.AdminToken)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			ok = true
		}
	}

	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("WS upgrade error", "err", err)
		return
	}
	defer conn.Close()

	client := &Client{
		conn:   conn,
		send:   make(chan WSMessage, 256),
		stream: stream,
		user:   stream,
	}
	s.AddClient(client)

	defer func() {
		s.RemoveClient(client)
		conn.Close()
	}()

	logger.Debug("WS Client connected", "remote", r.RemoteAddr)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	go func() {
		stats := s.collectStats()
		payload, _ := json.Marshal(stats)
		trySendWS(client, WSMessage{Type: "stats", Payload: payload})
		s.sendLogHistory(client)
		var mustChangePassword bool
		if client.stream != nil && client.stream.Username == s.config.GetAdminUsername() {
			mustChangePassword = s.config.AdminMustChangePassword
		}
		authInfo := map[string]interface{}{
			"authenticated":        true,
			"username":             client.stream.Username,
			"must_change_password": mustChangePassword,
		}
		if s.strmServer != nil {
			authInfo["version"] = s.strmServer.Version()
		}
		authPayload, _ := json.Marshal(authInfo)
		trySendWS(client, WSMessage{Type: "auth_info", Payload: authPayload})
	}()

	go func() {
		defer func() {

		}()

		for {
			var msg WSMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logger.Warn("WS read error", "err", err)
				}

				conn.Close()
				return
			}

			_ = msg
		}
	}()

	for {
		select {
		case <-ticker.C:
			s.sendStats(client)
		case msg, ok := <-client.send:
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}

func trySendWS(client *Client, msg WSMessage) bool {
	select {
	case client.send <- msg:
		return true
	default:
		return false
	}
}

func (s *Server) sendStats(client *Client) {
	stats := s.collectStats()
	payload, _ := json.Marshal(stats)
	trySendWS(client, WSMessage{Type: "stats", Payload: payload})
}

func (s *Server) sendConfig(client *Client) {

	var cfg config.Config
	if client.stream != nil && client.stream.Username == s.config.GetAdminUsername() {
		cfg = configForAdminAPI(s.config)
	} else if client.stream != nil {
		cfg = redactedConfigForViewer(s.config)
	} else {
		cfg = redactedConfigForViewer(s.config)
	}

	var payload []byte
	if client.stream != nil && client.stream.Username == s.config.GetAdminUsername() {
		envKeys := config.GetEnvOverrideKeys()
		pl := configPayload{Config: cfg, EnvOverrides: envKeys}
		payload, _ = json.Marshal(pl)
	} else {
		payload, _ = json.Marshal(cfg)
	}
	trySendWS(client, WSMessage{Type: "config", Payload: payload})
}

func (s *Server) sendIndexerCaps(client *Client) {
	s.mu.RLock()
	caps := s.indexerCaps
	s.mu.RUnlock()
	if caps == nil {
		caps = make(map[string]*indexer.Caps)
	}
	payload, _ := json.Marshal(caps)
	trySendWS(client, WSMessage{Type: "indexer_caps", Payload: payload})
}

func (s *Server) sendLogHistory(client *Client) {

	history := logger.GetHistory()
	payload, _ := json.Marshal(history)

	trySendWS(client, WSMessage{Type: "log_history", Payload: payload})
}

func (s *Server) handleSaveConfigWS(conn *websocket.Conn, client *Client, payload json.RawMessage) {
	var newCfg config.Config
	if err := json.Unmarshal(payload, &newCfg); err != nil {
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid config data"}`)})
		return
	}

	if client.stream != nil && client.stream.Username == s.config.GetAdminUsername() {
		s.mu.RLock()
		currentCfg := s.config
		currentLoadedPath := s.config.LoadedPath
		s.mu.RUnlock()

		plan := validationPlanFromPatch(payload, currentCfg, &newCfg)
		fieldErrors := s.validateConfigWithPlan(&newCfg, plan)
		if len(fieldErrors) > 0 {
			errorPayload, _ := json.Marshal(map[string]interface{}{
				"status":  "error",
				"message": "Validation failed",
				"errors":  fieldErrors,
			})
			trySendWS(client, WSMessage{Type: "save_status", Payload: errorPayload})
			return
		}

		config.CopyEnvOverridesFrom(currentCfg, &newCfg)

		newCfg.AdminPasswordHash = currentCfg.AdminPasswordHash
		newCfg.AdminToken = currentCfg.AdminToken
		newCfg.AdminMustChangePassword = currentCfg.AdminMustChangePassword
		newCfg.Streams = currentCfg.Streams

		newCfg.ApplyProviderDefaults()

		if currentLoadedPath == "" {
			currentLoadedPath = filepath.Join(paths.GetDataDir(), "config.json")
		}
		newCfg.LoadedPath = currentLoadedPath

		s.mu.Lock()
		s.config = &newCfg
		s.mu.Unlock()

		if err := s.config.Save(); err != nil {
			trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage([]byte(fmt.Sprintf(`{"status":"error","message":"Failed to save config: %s"}`, err.Error())))})
			return
		}

		if s.strmServer != nil {
			s.strmServer.ClearSearchCaches()
		}
		s.reloadConfigAsync(&newCfg)

		s.sendConfig(client)
		s.sendIndexerCaps(client)
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"success","message":"Configuration saved and reloaded. Search cache cleared."}`)})
		return
	}

	trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Only admin can save global configuration"}`)})
}

func (s *Server) reloadConfigAsync(newCfg *config.Config) {
	go func() {
		s.ensureAvailNZBReadyForReload(newCfg)

		if s.app != nil {
			comp, fullReload, err := s.app.Reload(newCfg)
			if err != nil {
				logger.Error("Reload: App.Reload failed", "err", err)
				return
			}
			s.ReloadFromComponents(comp, fullReload)
			logger.Info("Reload: configuration reloaded successfully", "full", fullReload)
			return
		}
		base, err := initialization.BuildComponents(newCfg)
		if err != nil {
			logger.Error("Reload: BuildComponents failed", "err", err)
			return
		}
		validator := validation.NewChecker(base.UsenetPool, 5, 6)
		triageService := triage.NewService()
		s.mu.RLock()
		availNZBURL := s.availNZBURL
		availNZBAPIKey := s.availNZBAPIKey
		tmdbAPIKey := s.tmdbAPIKey
		tvdbAPIKey := s.tvdbAPIKey
		s.mu.RUnlock()
		availClient := availnzb.NewClient(availNZBURL, availNZBAPIKey)
		tmdbClient := tmdb.NewClient(tmdbAPIKey)
		dataDir := filepath.Dir(base.Config.LoadedPath)
		if dataDir == "" {
			dataDir, _ = os.Getwd()
		}
		tvdbClient := tvdb.NewClient(tvdbAPIKey, dataDir)
		comp := &app.Components{
			Config:               base.Config,
			Indexer:              base.Indexer,
			ProviderPools:        base.ProviderPools,
			ProviderOrder:        base.ProviderOrder,
			StreamingPools:       base.StreamingPools,
			AvailNZBIndexerHosts: base.AvailNZBIndexerHosts,
			IndexerCaps:          base.IndexerCaps,
			Validator:            validator,
			Triage:               triageService,
			AvailClient:          availClient,
			TMDBClient:           tmdbClient,
			TVDBClient:           tvdbClient,
		}
		s.ReloadFromComponents(comp, true)
		logger.Info("Reload: configuration reloaded successfully")
	}()
}

func (s *Server) handleFetchCapsWS(client *Client) {
	s.mu.RLock()
	idx := s.indexer
	s.mu.RUnlock()

	caps := make(map[string]*indexer.Caps)
	if agg, ok := idx.(*indexer.Aggregator); ok {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, i := range agg.Indexers {
			if c, ok := i.(indexer.IndexerWithCaps); ok {
				wg.Add(1)
				go func(name string, fetcher indexer.IndexerWithCaps) {
					defer wg.Done()
					if result, err := fetcher.GetCaps(); err == nil {
						mu.Lock()
						caps[name] = result
						mu.Unlock()
					}
				}(i.Name(), c)
			}
		}
		wg.Wait()
	}

	s.mu.Lock()
	s.indexerCaps = caps
	s.mu.Unlock()

	s.sendIndexerCaps(client)
}

func (s *Server) handleSaveStreamConfigsWS(conn *websocket.Conn, client *Client, payload json.RawMessage) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Only admin can save stream configurations"}`)})
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
	if err := json.Unmarshal(payload, &streamConfigs); err != nil {
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid stream config data"}`)})
		return
	}

	var errors []string
	for username, streamConfig := range streamConfigs {
		if username == s.config.GetAdminUsername() {
			continue
		}
		if err := s.streamManager.UpdateStreamIndexerConfig(username, streamConfig.IndexerSelections, streamConfig.IndexerOverrides); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update indexer overrides for %s: %v", username, err))
		}
		if err := s.streamManager.UpdateStreamProviderSelections(username, streamConfig.ProviderSelections); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update provider selections for %s: %v", username, err))
		}
		if err := s.streamManager.UpdateStreamGeneralSettings(username, streamConfig.FilterSortingMode, streamConfig.IndexerMode, streamConfig.UseAvailNZB, streamConfig.CombineResults, streamConfig.EnableFailover, streamConfig.ResultsMode); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update general settings for %s: %v", username, err))
		}
		if err := s.streamManager.UpdateStreamSearchQueries(username, streamConfig.MovieSearchQueries, streamConfig.SeriesSearchQueries); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update search queries for %s: %v", username, err))
		}
	}

	if len(errors) > 0 {
		errorPayload, _ := json.Marshal(map[string]interface{}{
			"status":  "error",
			"message": "Some stream configs failed to save",
			"errors":  errors,
		})
		trySendWS(client, WSMessage{Type: "save_status", Payload: errorPayload})
		return
	}

	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"success","message":"Stream configurations saved successfully. Search cache cleared."}`)})
}

func (s *Server) handleGetStreamsWS(client *Client) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "users_response", Payload: json.RawMessage(`{"error":"Only admin can access streams list"}`)})
		return
	}

	streams := s.streamManager.GetAllStreams()

	streamList := make([]map[string]interface{}, 0, len(streams))
	for _, stream := range streams {
		streamList = append(streamList, map[string]interface{}{
			"username":              stream.Username,
			"token":                 stream.Token,
			"filter_sorting_mode":   stream.FilterSortingMode,
			"indexer_mode":          stream.IndexerMode,
			"use_availnzb":          stream.UseAvailNZB,
			"combine_results":       stream.CombineResults,
			"enable_failover":       stream.EnableFailover,
			"results_mode":          stream.ResultsMode,
			"indexer_overrides":     stream.IndexerOverrides,
			"provider_selections":   stream.ProviderSelections,
			"indexer_selections":    stream.IndexerSelections,
			"movie_search_queries":  stream.MovieSearchQueries,
			"series_search_queries": stream.SeriesSearchQueries,
		})
	}

	streamListPayload, _ := json.Marshal(streamList)
	trySendWS(client, WSMessage{Type: "users_response", Payload: streamListPayload})
}

func (s *Server) handleGetStreamWS(client *Client, payload json.RawMessage) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "user_response", Payload: json.RawMessage(`{"error":"Only admin can access user details"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	stream, err := s.streamManager.GetStream(req.Username, s.config.GetAdminUsername())
	if err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"username":              stream.Username,
		"token":                 stream.Token,
		"filter_sorting_mode":   stream.FilterSortingMode,
		"indexer_mode":          stream.IndexerMode,
		"use_availnzb":          stream.UseAvailNZB,
		"combine_results":       stream.CombineResults,
		"enable_failover":       stream.EnableFailover,
		"results_mode":          stream.ResultsMode,
		"indexer_overrides":     stream.IndexerOverrides,
		"provider_selections":   stream.ProviderSelections,
		"indexer_selections":    stream.IndexerSelections,
		"movie_search_queries":  stream.MovieSearchQueries,
		"series_search_queries": stream.SeriesSearchQueries,
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_response", Payload: respPayload})
}

func (s *Server) handleCreateStreamWS(client *Client, payload json.RawMessage) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can create users"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	stream, err := s.streamManager.CreateStream(req.Username, "", s.config.GetAdminUsername())
	if err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"user": map[string]interface{}{
			"username": stream.Username,
			"token":    stream.Token,
		},
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})

	s.broadcastStreamsList()
}

func (s *Server) handleDeleteStreamWS(client *Client, payload json.RawMessage) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can delete users"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	if err := s.streamManager.DeleteStream(req.Username); err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Stream %s deleted successfully", req.Username),
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})

	s.broadcastStreamsList()
}

func (s *Server) handleRegenerateTokenWS(client *Client, payload json.RawMessage) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can regenerate tokens"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	token, err := s.streamManager.RegenerateToken(req.Username)
	if err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"token":   token,
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})

	s.broadcastStreamsList()
}

func (s *Server) handleUpdatePasswordWS(client *Client, payload json.RawMessage) {

	if client.stream == nil || client.stream.Username != s.config.GetAdminUsername() {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Only admin can update password"}`)})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: json.RawMessage(`{"error":"Invalid request"}`)})
		return
	}

	newHash := auth.HashPassword(req.Password)
	s.mu.Lock()
	s.config.AdminPasswordHash = newHash
	s.config.AdminMustChangePassword = false
	s.mu.Unlock()
	if err := s.config.Save(); err != nil {
		errorPayload, _ := json.Marshal(map[string]string{"error": err.Error()})
		trySendWS(client, WSMessage{Type: "user_action_response", Payload: errorPayload})
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": "Password updated successfully",
	}

	respPayload, _ := json.Marshal(response)
	trySendWS(client, WSMessage{Type: "user_action_response", Payload: respPayload})
}

func (s *Server) broadcastStreamsList() {
	streams := s.streamManager.GetAllStreams()

	streamList := make([]map[string]interface{}, 0, len(streams))
	for _, stream := range streams {
		streamList = append(streamList, map[string]interface{}{
			"username":          stream.Username,
			"token":             stream.Token,
			"indexer_overrides": stream.IndexerOverrides,
		})
	}

	payload, _ := json.Marshal(streamList)

	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		if client.stream != nil && client.stream.Username == s.config.GetAdminUsername() {
			select {
			case client.send <- WSMessage{Type: "users_response", Payload: payload}:
			default:

			}
		}
	}
}

func (s *Server) validateConfig(cfg *config.Config) map[string]string {
	return s.validateConfigWithPlan(cfg, fullConfigValidationPlan())
}

type configValidationPlan struct {
	validateKeepLogFiles           bool
	validateNZBHistoryRetention    bool
	validatePlaybackStartupTimeout bool
	validateMovieSearchQueries     bool
	validateSeriesSearchQueries    bool
	validateDeviceAssignments      bool
	validateProviders              bool
	validateIndexers               bool
	providerDeletionOnly           bool
	indexerDeletionOnly            bool
	changedProviderIndexes         map[int]bool
	changedIndexerIndexes          map[int]bool
}

func fullConfigValidationPlan() configValidationPlan {
	return configValidationPlan{
		validateKeepLogFiles:           true,
		validateNZBHistoryRetention:    true,
		validatePlaybackStartupTimeout: true,
		validateMovieSearchQueries:     true,
		validateSeriesSearchQueries:    true,
		validateDeviceAssignments:      true,
		validateProviders:              true,
		validateIndexers:               true,
	}
}

func validationPlanFromPatch(body []byte, currentCfg, nextCfg *config.Config) configValidationPlan {
	plan := fullConfigValidationPlan()
	if len(body) == 0 || currentCfg == nil || nextCfg == nil {
		return plan
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || len(raw) == 0 {
		return plan
	}

	plan = configValidationPlan{}

	if _, ok := raw["keep_log_files"]; ok {
		plan.validateKeepLogFiles = true
	}
	if _, ok := raw["nzb_history_retention_days"]; ok {
		plan.validateNZBHistoryRetention = true
	}
	if _, ok := raw["playback_startup_timeout_seconds"]; ok {
		plan.validatePlaybackStartupTimeout = true
	}
	if _, ok := raw["movie_search_queries"]; ok {
		plan.validateMovieSearchQueries = true
		plan.validateDeviceAssignments = true
	}
	if _, ok := raw["series_search_queries"]; ok {
		plan.validateSeriesSearchQueries = true
		plan.validateDeviceAssignments = true
	}
	if _, ok := raw["providers"]; ok {
		plan.validateProviders = true
		if len(nextCfg.Providers) < len(currentCfg.Providers) {
			plan.providerDeletionOnly = true
		} else {
			plan.changedProviderIndexes = changedIndexes(currentCfg.Providers, nextCfg.Providers)
		}
	}
	if _, ok := raw["indexers"]; ok {
		plan.validateIndexers = true
		if len(nextCfg.Indexers) < len(currentCfg.Indexers) {
			plan.indexerDeletionOnly = true
		} else {
			plan.changedIndexerIndexes = changedIndexes(currentCfg.Indexers, nextCfg.Indexers)
		}
	}

	return plan
}

func changedIndexes[T any](current, next []T) map[int]bool {
	changed := make(map[int]bool)
	for i := range next {
		if i >= len(current) || !reflect.DeepEqual(current[i], next[i]) {
			changed[i] = true
		}
	}
	return changed
}

func (s *Server) validateConfigWithPlan(cfg *config.Config, plan configValidationPlan) map[string]string {
	errors := make(map[string]string)
	if plan.validateKeepLogFiles && (cfg.KeepLogFiles < 1 || cfg.KeepLogFiles > 50) {
		errors["keep_log_files"] = "Must be between 1 and 50"
	}
	if plan.validateNZBHistoryRetention && (cfg.NZBHistoryRetentionDays < 0 || cfg.NZBHistoryRetentionDays > 3650) {
		errors["nzb_history_retention_days"] = "Must be between 0 and 3650 days"
	}
	if plan.validatePlaybackStartupTimeout && (cfg.PlaybackStartupTimeoutSeconds < 1 || cfg.PlaybackStartupTimeoutSeconds > config.MaxPlaybackStartupTimeoutSeconds) {
		errors["playback_startup_timeout_seconds"] = "Must be between 1 and 60 seconds"
	}
	validateSearchQueries := func(prefix string, queries []config.SearchQueryConfig) {
		seen := make(map[string]bool)
		for i, query := range queries {
			name := strings.TrimSpace(query.Name)
			if name == "" {
				errors[fmt.Sprintf("%s.%d.name", prefix, i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seen[key] {
					errors[fmt.Sprintf("%s.%d.name", prefix, i)] = "Name must be unique"
				}
				seen[key] = true
			}
			mode := strings.ToLower(strings.TrimSpace(query.SearchMode))
			if mode != "id" && mode != "text" {
				errors[fmt.Sprintf("%s.%d.search_mode", prefix, i)] = "Search mode must be id or text"
			}
		}
	}

	if plan.validateMovieSearchQueries {
		validateSearchQueries("movie_search_queries", cfg.MovieSearchQueries)
	}
	if plan.validateSeriesSearchQueries {
		validateSearchQueries("series_search_queries", cfg.SeriesSearchQueries)
	}

	if plan.validateDeviceAssignments {
		movieQueryNames := make(map[string]bool, len(cfg.MovieSearchQueries))
		for _, query := range cfg.MovieSearchQueries {
			if name := strings.ToLower(strings.TrimSpace(query.Name)); name != "" {
				movieQueryNames[name] = true
			}
		}
		seriesQueryNames := make(map[string]bool, len(cfg.SeriesSearchQueries))
		for _, query := range cfg.SeriesSearchQueries {
			if name := strings.ToLower(strings.TrimSpace(query.Name)); name != "" {
				seriesQueryNames[name] = true
			}
		}
		for username, stream := range cfg.Streams {
			if stream == nil {
				continue
			}
			for i, name := range stream.MovieSearchQueries {
				if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" && !movieQueryNames[normalized] {
					errors[fmt.Sprintf("streams.%s.movie_search_queries.%d", username, i)] = "Assigned movie search query does not exist"
				}
			}
			for i, name := range stream.SeriesSearchQueries {
				if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" && !seriesQueryNames[normalized] {
					errors[fmt.Sprintf("streams.%s.series_search_queries.%d", username, i)] = "Assigned show search query does not exist"
				}
			}
		}
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	if plan.validateProviders && !plan.providerDeletionOnly {
		for i, p := range cfg.Providers {
			if len(plan.changedProviderIndexes) > 0 && !plan.changedProviderIndexes[i] {
				continue
			}
			wg.Add(1)
			go func(idx int, provider config.Provider) {
				defer wg.Done()
				if provider.Enabled != nil && !*provider.Enabled {
					return
				}
				if provider.Host == "" {
					mu.Lock()
					errors[fmt.Sprintf("providers.%d.host", idx)] = "Host is required"
					mu.Unlock()
					return
				}
				pool := nntp.NewClientPool(provider.Host, provider.Port, provider.UseSSL, provider.Username, provider.Password, 1)
				if err := pool.Validate(); err != nil {
					mu.Lock()
					errors[fmt.Sprintf("providers.%d.host", idx)] = err.Error()
					mu.Unlock()
				}
			}(i, p)
		}
	}

	if plan.validateIndexers && !plan.indexerDeletionOnly {
		for i, idx := range cfg.Indexers {
			if len(plan.changedIndexerIndexes) > 0 && !plan.changedIndexerIndexes[i] {
				continue
			}
			wg.Add(1)
			go func(index int, indexerCfg config.IndexerConfig) {
				defer wg.Done()
				if indexerCfg.Enabled != nil && !*indexerCfg.Enabled {
					return
				}
				if strings.EqualFold(indexerCfg.Type, "easynews") {
					if indexerCfg.Username == "" {
						mu.Lock()
						errors[fmt.Sprintf("indexers.%d.username", index)] = "Username is required"
						mu.Unlock()
					}
					if indexerCfg.Password == "" {
						mu.Lock()
						errors[fmt.Sprintf("indexers.%d.password", index)] = "Password is required"
						mu.Unlock()
					}
					return
				}
				if indexerCfg.URL == "" {
					mu.Lock()
					errors[fmt.Sprintf("indexers.%d.url", index)] = "URL is required"
					mu.Unlock()
					return
				}
				if strings.Contains(indexerCfg.APIPath, "{indexer_id}") {
					mu.Lock()
					errors[fmt.Sprintf("indexers.%d.api_path", index)] = "Replace {indexer_id} with the Prowlarr indexer ID (for example 1/api)"
					mu.Unlock()
					return
				}
				indexerPingTimeout := indexerCfg.EffectiveTimeout()
				client := newznab.NewClient(indexerCfg, nil)
				errCh := make(chan error, 1)
				go func() { errCh <- client.Ping() }()
				var err error
				select {
				case err = <-errCh:
				case <-time.After(indexerPingTimeout):
					err = fmt.Errorf("connection timeout after %v", indexerPingTimeout)
				}
				if err != nil {
					mu.Lock()
					errors[fmt.Sprintf("indexers.%d.url", index)] = err.Error()
					mu.Unlock()
				}
			}(i, idx)
		}
	}

	wg.Wait()
	return errors
}

func (s *Server) handleStreamSearchWS(client *Client, payload json.RawMessage) {
	var req struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil || req.Type == "" || req.ID == "" {
		return
	}
	contentType := req.Type
	if contentType != "movie" && contentType != "series" {
		return
	}
	const streamResultCap = 12
	var sent int
	sink := func(stream stremio.Stream) bool {
		if sent >= streamResultCap {
			return false
		}
		payload, err := json.Marshal(stream)
		if err != nil {
			return true
		}
		trySendWS(client, WSMessage{Type: "stream_result", Payload: payload})
		sent++
		return sent < streamResultCap
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctx = stremio.WithStreamSink(ctx, sink)
	go func() {
		defer func() {
			donePayload, _ := json.Marshal(map[string]int{"count": sent})
			trySendWS(client, WSMessage{Type: "stream_search_done", Payload: donePayload})
		}()
		_, _ = s.strmServer.GetStreams(ctx, contentType, req.ID, client.stream)
	}()
}

func (s *Server) handleCloseSessionWS(payload json.RawMessage) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}
	logger.Debug("WS closing session", "id", req.ID)
	s.sessionMgr.DeleteSession(req.ID)
}

func (s *Server) handleRestartWS(conn *websocket.Conn) {
	go func() {
		time.Sleep(500 * time.Millisecond)
		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Start()
		os.Exit(0)
	}()
}
