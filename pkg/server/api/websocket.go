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
	"streamnzb/pkg/indexer/easynews"
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
	if client.stream != nil && client.stream.Username == s.config.GetAdminUsername() {
		s.mu.RLock()
		currentCfg := s.config
		currentLoadedPath := s.config.LoadedPath
		s.mu.RUnlock()

		currentJSON, err := json.Marshal(currentCfg)
		if err != nil {
			trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Failed to prepare config update"}`)})
			return
		}

		var newCfg config.Config
		if err := json.Unmarshal(currentJSON, &newCfg); err != nil {
			trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Failed to prepare config update"}`)})
			return
		}
		if err := json.Unmarshal(payload, &newCfg); err != nil {
			trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(`{"status":"error","message":"Invalid config data"}`)})
			return
		}

		config.CopyEnvOverridesFrom(currentCfg, &newCfg)
		newCfg.AdminPasswordHash = currentCfg.AdminPasswordHash
		newCfg.AdminToken = currentCfg.AdminToken
		newCfg.AdminMustChangePassword = currentCfg.AdminMustChangePassword
		newCfg.Streams = cloneStreamEntries(currentCfg.Streams)
		providerRenames := renamedNamesByIndex(currentCfg.Providers, newCfg.Providers, func(provider config.Provider) string {
			return provider.Name
		})
		indexerRenames := renamedNamesByIndex(currentCfg.Indexers, newCfg.Indexers, func(indexer config.IndexerConfig) string {
			return indexer.Name
		})
		filterProfileRenames := renamedNamesByIndex(currentCfg.FilterProfiles, newCfg.FilterProfiles, func(fp config.FilterProfileConfig) string {
			return fp.Name
		})
		applyStreamNameRenames(newCfg.Streams, providerRenames, indexerRenames)
		applyFilterProfileRenames(newCfg.Streams, filterProfileRenames)
		newCfg.ApplyProviderDefaults()
		applyStreamAutoSelections(&newCfg)

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

		// When only filter profiles change, the raw indexer search results are still
		// valid — only the playlist (filtered/sorted) cache needs to be cleared so the
		// new filter/sort rules are applied on the next request. This avoids re-querying
		// indexers for what is effectively a cheap local re-filter/re-sort.
		onlyFilterProfilesChanged := plan.validateFilterProfiles &&
			!plan.validateProviders && !plan.validateIndexers &&
			!plan.validateMovieSearchQueries && !plan.validateSeriesSearchQueries &&
			!plan.validateIndexerProxyURL
		if s.strmServer != nil {
			if onlyFilterProfilesChanged {
				s.strmServer.ClearPlaylistCaches()
			} else {
				s.strmServer.ClearSearchCaches()
			}
		}
		s.reloadConfigAsync(&newCfg)

		s.sendConfig(client)
		s.sendIndexerCaps(client)
		cacheMessage := "Configuration saved and reloaded. Search cache cleared."
		if onlyFilterProfilesChanged {
			cacheMessage = "Configuration saved and reloaded. Playlist cache cleared."
		}
		trySendWS(client, WSMessage{Type: "save_status", Payload: json.RawMessage(fmt.Sprintf(`{"status":"success","message":%q}`, cacheMessage))})
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
		AutoAddProviders    *bool                                 `json:"auto_add_providers"`
		AutoAddIndexers     *bool                                 `json:"auto_add_indexers"`
		IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
		ProviderSelections  []string                              `json:"provider_selections"`
		IndexerSelections   []string                              `json:"indexer_selections"`
		MovieSearchQueries  []string                              `json:"movie_search_queries"`
		SeriesSearchQueries []string                              `json:"series_search_queries"`
		FilterProfileName   string                                `json:"filter_profile_name"`
		MuteErrorVideo      *bool                                 `json:"mute_error_video"`
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
		providerSelections := append([]string(nil), streamConfig.ProviderSelections...)
		indexerSelections := append([]string(nil), streamConfig.IndexerSelections...)
		indexerOverrides := cloneIndexerOverrides(streamConfig.IndexerOverrides)
		if streamConfig.AutoAddProviders != nil && *streamConfig.AutoAddProviders {
			providerSelections = syncOrderedSelections(providerSelections, enabledProviderNames(s.config.Providers))
		}
		if streamConfig.AutoAddIndexers != nil && *streamConfig.AutoAddIndexers {
			indexerSelections = syncOrderedSelections(indexerSelections, enabledIndexerNames(s.config.Indexers))
			indexerOverrides = filterIndexerOverrides(indexerOverrides, indexerSelections)
		}
		if err := s.streamManager.UpdateStreamConfig(username, &auth.Stream{
			FilterSortingMode:   streamConfig.FilterSortingMode,
			IndexerMode:         streamConfig.IndexerMode,
			UseAvailNZB:         streamConfig.UseAvailNZB,
			CombineResults:      streamConfig.CombineResults,
			EnableFailover:      streamConfig.EnableFailover,
			ResultsMode:         streamConfig.ResultsMode,
			AutoAddProviders:    streamConfig.AutoAddProviders,
			AutoAddIndexers:     streamConfig.AutoAddIndexers,
			IndexerOverrides:    indexerOverrides,
			ProviderSelections:  providerSelections,
			IndexerSelections:   indexerSelections,
			MovieSearchQueries:  streamConfig.MovieSearchQueries,
			SeriesSearchQueries: streamConfig.SeriesSearchQueries,
			FilterProfileName:   streamConfig.FilterProfileName,
			MuteErrorVideo:      streamConfig.MuteErrorVideo,
		}); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update stream config for %s: %v", username, err))
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
			"auto_add_providers":    stream.AutoAddProviders,
			"auto_add_indexers":     stream.AutoAddIndexers,
			"indexer_overrides":     stream.IndexerOverrides,
			"provider_selections":   stream.ProviderSelections,
			"indexer_selections":    stream.IndexerSelections,
			"movie_search_queries":  stream.MovieSearchQueries,
			"series_search_queries": stream.SeriesSearchQueries,
			"filter_profile_name":   stream.FilterProfileName,
			"mute_error_video":      stream.MuteErrorVideo,
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
		"auto_add_providers":    stream.AutoAddProviders,
		"auto_add_indexers":     stream.AutoAddIndexers,
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
	validateIndexerProxyURL        bool
	validateMovieSearchQueries     bool
	validateSeriesSearchQueries    bool
	validateDeviceAssignments      bool
	validateProviders              bool
	validateIndexers               bool
	providerDeletionOnly           bool
	indexerDeletionOnly            bool
	changedProviderIndexes         map[int]bool
	changedIndexerIndexes          map[int]bool
	validateTMDBAPIKey             bool
	validateTVDBAPIKey             bool
	validateSpeculativePreProbing  bool
	validateFilterProfiles         bool
}

func fullConfigValidationPlan() configValidationPlan {
	return configValidationPlan{
		validateKeepLogFiles:           true,
		validateNZBHistoryRetention:    true,
		validatePlaybackStartupTimeout: true,
		validateIndexerProxyURL:        true,
		validateMovieSearchQueries:     true,
		validateSeriesSearchQueries:    true,
		validateDeviceAssignments:      true,
		validateProviders:              true,
		validateIndexers:               true,
		validateTMDBAPIKey:             true,
		validateTVDBAPIKey:             true,
		validateSpeculativePreProbing:  true,
		validateFilterProfiles:         true,
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
	if _, ok := raw["speculative_pre_probing_count"]; ok {
		plan.validateSpeculativePreProbing = true
	}
	if _, ok := raw["indexer_proxy_url"]; ok {
		plan.validateIndexerProxyURL = true
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
	if _, ok := raw["tmdb_api_key"]; ok {
		plan.validateTMDBAPIKey = true
	}
	if _, ok := raw["tvdb_api_key"]; ok {
		plan.validateTVDBAPIKey = true
	}
	if _, ok := raw["filter_profiles"]; ok {
		plan.validateFilterProfiles = true
		plan.validateDeviceAssignments = true
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

func pingIndexerWithTimeout(indexerCfg config.IndexerConfig) error {
	pingTimeout := indexerCfg.EffectiveTimeout()
	ping := func() error {
		if strings.EqualFold(indexerCfg.Type, "easynews") {
			client, err := easynews.NewClient(
				indexerCfg.Username,
				indexerCfg.Password,
				indexerCfg.Name,
				"",
				indexerCfg.APIHitsDay,
				indexerCfg.DownloadsDay,
				indexerCfg.RateLimitRPS,
				indexerCfg.EffectiveTimeoutSeconds(),
				indexerCfg.ProxyURL,
				indexerCfg.GrabHeader,
				nil,
			)
			if err != nil {
				return err
			}
			return client.Ping()
		}
		return newznab.NewClient(indexerCfg, nil).Ping()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- ping() }()
	select {
	case err := <-errCh:
		return err
	case <-time.After(pingTimeout):
		return fmt.Errorf("connection timeout after %v", pingTimeout)
	}
}

func verifyGlobalIndexerProxy(cfg *config.Config) error {
	if cfg == nil || strings.TrimSpace(cfg.IndexerProxyURL) == "" {
		return nil
	}

	type probeResult struct {
		name string
		err  error
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempted int
	samples := make([]string, 0, 3)
	resultCh := make(chan probeResult, len(cfg.Indexers))
	var wg sync.WaitGroup

	for _, idx := range cfg.Indexers {
		if idx.Enabled != nil && !*idx.Enabled {
			continue
		}
		if strings.EqualFold(idx.Type, "easynews") {
			if strings.TrimSpace(idx.Username) == "" || strings.TrimSpace(idx.Password) == "" {
				continue
			}
		} else {
			if strings.TrimSpace(idx.URL) == "" || strings.Contains(idx.APIPath, "{indexer_id}") {
				continue
			}
		}

		testCfg := idx
		testCfg.ProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
		attempted++
		wg.Add(1)
		go func(testCfg config.IndexerConfig) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			default:
			}

			err := pingIndexerWithTimeout(testCfg)
			name := strings.TrimSpace(testCfg.Name)
			if name == "" {
				if strings.EqualFold(testCfg.Type, "easynews") {
					name = "easynews"
				} else {
					name = testCfg.URL
				}
			}
			select {
			case resultCh <- probeResult{name: name, err: err}:
			case <-ctx.Done():
			}
		}(testCfg)
	}

	if attempted == 0 {
		return nil
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for result := range resultCh {
		if result.err == nil {
			cancel()
			wg.Wait()
			return nil
		}
		if len(samples) < 3 {
			samples = append(samples, fmt.Sprintf("%s: %v", result.name, result.err))
		}
	}
	return fmt.Errorf("global proxy could not reach any enabled indexer (%s)", strings.Join(samples, " | "))
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
	if plan.validateSpeculativePreProbing && (cfg.SpeculativePreProbingCount < 0 || cfg.SpeculativePreProbingCount > 5) {
		errors["speculative_pre_probing_count"] = "Must be between 0 and 5"
	}
	if plan.validateIndexerProxyURL {
		if err := config.ValidateIndexerProxyURL(cfg.IndexerProxyURL); err != nil {
			errors["indexer_proxy_url"] = err.Error()
		} else if err := verifyGlobalIndexerProxy(cfg); err != nil {
			errors["indexer_proxy_url"] = err.Error()
		}
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
			titleLanguages := config.NormalizeSearchTitleLanguages(query.SearchTitleLanguages)
			if mode == "id" && len(titleLanguages) == 0 {
				if rawSingle := strings.TrimSpace(query.SearchTitleLanguage); rawSingle != "" {
					titleLanguages = []string{config.NormalizeSearchTitleLanguage(rawSingle)}
				}
			}
			if mode == "text" && len(titleLanguages) > 1 {
				errors[fmt.Sprintf("%s.%d.search_title_languages", prefix, i)] = "Text search can use only one title language"
			}
			if mode == "id" && len(titleLanguages) == 0 {
				errors[fmt.Sprintf("%s.%d.search_title_languages", prefix, i)] = "Add at least one title language"
			}
			if prefix == "series_search_queries" {
				rawScope := strings.ToLower(strings.TrimSpace(query.SeriesSearchScope))
				if rawScope != "" {
					normalizedScope := config.NormalizeSeriesSearchScope(rawScope)
					switch rawScope {
					case normalizedScope,
						"episode_param",
						"episode_query",
						"season_param",
						"season_query":
					default:
						errors[fmt.Sprintf("%s.%d.series_search_scope", prefix, i)] = "TV scope must be season_episode, season, or none"
					}
				}
			}
		}
	}

	if plan.validateMovieSearchQueries {
		validateSearchQueries("movie_search_queries", cfg.MovieSearchQueries)
	}
	if plan.validateSeriesSearchQueries {
		validateSearchQueries("series_search_queries", cfg.SeriesSearchQueries)
	}

	if plan.validateFilterProfiles {
		seen := make(map[string]bool)
		for i, fp := range cfg.FilterProfiles {
			name := strings.TrimSpace(fp.Name)
			if name == "" {
				errors[fmt.Sprintf("filter_profiles.%d.name", i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seen[key] {
					errors[fmt.Sprintf("filter_profiles.%d.name", i)] = "Name must be unique"
				}
				seen[key] = true
			}
		}
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
		filterProfileNames := make(map[string]bool, len(cfg.FilterProfiles))
		for _, fp := range cfg.FilterProfiles {
			if name := strings.ToLower(strings.TrimSpace(fp.Name)); name != "" {
				filterProfileNames[name] = true
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
			if fpName := strings.ToLower(strings.TrimSpace(stream.FilterProfileName)); fpName != "" && !filterProfileNames[fpName] {
				errors[fmt.Sprintf("streams.%s.filter_profile_name", username)] = "Assigned filter profile does not exist"
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
					if err := config.ValidateIndexerProxyURL(indexerCfg.ProxyURL); err != nil {
						mu.Lock()
						errors[fmt.Sprintf("indexers.%d.proxy_url", index)] = err.Error()
						mu.Unlock()
						return
					}
					testCfg := indexerCfg
					effectiveProxyURL := strings.TrimSpace(indexerCfg.ProxyURL)
					if effectiveProxyURL == "" && plan.validateIndexerProxyURL {
						effectiveProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
					}
					testCfg.ProxyURL = effectiveProxyURL
					if err := pingIndexerWithTimeout(testCfg); err != nil {
						mu.Lock()
						errors[fmt.Sprintf("indexers.%d.username", index)] = err.Error()
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
				if err := config.ValidateIndexerProxyURL(indexerCfg.ProxyURL); err != nil {
					mu.Lock()
					errors[fmt.Sprintf("indexers.%d.proxy_url", index)] = err.Error()
					mu.Unlock()
					return
				}
				testCfg := indexerCfg
				effectiveProxyURL := strings.TrimSpace(indexerCfg.ProxyURL)
				if effectiveProxyURL == "" && plan.validateIndexerProxyURL {
					effectiveProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
				}
				testCfg.ProxyURL = effectiveProxyURL
				err := pingIndexerWithTimeout(testCfg)
				if err != nil {
					mu.Lock()
					errors[fmt.Sprintf("indexers.%d.url", index)] = err.Error()
					mu.Unlock()
				}
			}(i, idx)
		}
	}

	if plan.validateTMDBAPIKey && strings.TrimSpace(cfg.TMDBAPIKey) != "" {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			client := tmdb.NewClient(key)
			if err := client.Ping(); err != nil {
				mu.Lock()
				errors["tmdb_api_key"] = err.Error()
				mu.Unlock()
			}
		}(strings.TrimSpace(cfg.TMDBAPIKey))
	}

	if plan.validateTVDBAPIKey && strings.TrimSpace(cfg.TVDBAPIKey) != "" {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			client := tvdb.NewClient(key, paths.GetDataDir())
			if err := client.Ping(); err != nil {
				mu.Lock()
				errors["tvdb_api_key"] = err.Error()
				mu.Unlock()
			}
		}(strings.TrimSpace(cfg.TVDBAPIKey))
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
