package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tvdb"
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
			stream, err = s.streamManager.AuthenticateToken(cookie.Value, s.adminUsername(), s.config.AdminToken)
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
		if client.stream != nil && client.stream.Username == s.adminUsername() {
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
	if client.stream != nil && client.stream.Username == s.adminUsername() {
		cfg = configForAdminAPI(s.config)
	} else if client.stream != nil {
		cfg = redactedConfigForViewer(s.config)
	} else {
		cfg = redactedConfigForViewer(s.config)
	}

	var payload []byte
	if client.stream != nil && client.stream.Username == s.adminUsername() {
		envKeys := config.GetEnvOverrideKeys()
		pl := configPayload{Config: cfg, EnvOverrides: envKeys}
		payload, _ = json.Marshal(pl)
	} else {
		payload, _ = json.Marshal(cfg)
	}
	trySendWS(client, WSMessage{Type: "config", Payload: payload})
}

// broadcastConfig pushes the current config to every connected client, so a
// change made outside the websocket save path — saving a stream over REST, for
// one — does not leave open pages showing stale config until they reload.
func (s *Server) broadcastConfig() {
	s.clientsMu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.clientsMu.Unlock()

	for _, client := range clients {
		s.sendConfig(client)
	}
}

func (s *Server) sendLogHistory(client *Client) {

	history := logger.GetHistory()
	payload, _ := json.Marshal(history)

	trySendWS(client, WSMessage{Type: "log_history", Payload: payload})
}

// reloadConfigAsync schedules a reload of newCfg. Reloads are applied by a
// single worker goroutine; if saves arrive faster than reloads complete, the
// pending config is replaced so only the latest one is applied (latest-wins).
func (s *Server) reloadConfigAsync(newCfg *config.Config) {
	s.reloadMu.Lock()
	s.pendingReload = newCfg
	if s.reloadActive {
		s.reloadMu.Unlock()
		return
	}
	s.reloadActive = true
	s.reloadMu.Unlock()

	go func() {
		for {
			s.reloadMu.Lock()
			cfg := s.pendingReload
			s.pendingReload = nil
			if cfg == nil {
				s.reloadActive = false
				s.reloadMu.Unlock()
				return
			}
			s.reloadMu.Unlock()
			s.reloadConfig(cfg)
		}
	}()
}

func (s *Server) reloadConfig(newCfg *config.Config) {
	s.ensureAvailNZBReadyForReload(newCfg)

	if s.app != nil {
		comp, scope, err := s.app.Reload(newCfg)
		if err != nil {
			logger.Error("Reload: App.Reload failed", "err", err)
			return
		}
		s.ReloadFromComponents(comp, scope)
		// Same Debug demotion as the scope log above: config-only reloads run
		// on every debounced settings save; only reloads that restarted a
		// subsystem are worth an INFO line.
		reloadLog := logger.Debug
		if scope.Any() {
			reloadLog = logger.Info
		}
		reloadLog("Reload: configuration reloaded successfully",
			"indexers", scope.Indexers, "providers", scope.Providers, "proxy", scope.Proxy,
			"database", scope.Database, "addon_port", scope.AddonPort)
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
	tvdbCreds := s.tvdbCreds
	s.mu.RUnlock()
	availClient := availnzb.NewClient(availNZBURL, availNZBAPIKey)
	tmdbClient := s.cachedTMDBClient(tmdbAPIKey)
	dataDir := filepath.Dir(base.Config.LoadedPath)
	if dataDir == "" {
		dataDir, _ = os.Getwd()
	}
	tvdbClient := tvdb.NewClientWithCache(tvdbCreds, dataDir, s.metadataCache("tvdb"))
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
	s.ReloadFromComponents(comp, app.ReloadScopeFull())
	logger.Info("Reload: configuration reloaded successfully")
}

// cacheClearScopeForPatch decides how much cached search state a config patch
// invalidates. Unknown keys (and unparseable or full-config saves) clear the
// search caches — the safe default for fields added later.
func cacheClearScopeForPatch(body []byte) cacheClearScope {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || len(raw) == 0 {
		return cacheClearSearch
	}
	scope := cacheClearNone
	for key := range raw {
		switch {
		case patchKeysNoCacheImpact[key]:
		case patchKeysPlaylistOnly[key]:
			scope = max(scope, cacheClearPlaylist)
		default:
			return cacheClearSearch
		}
	}
	return scope
}

// clearCachesForScope applies the computed scope and returns the suffix for
// the save-status message ("" when nothing was cleared).
func (s *Server) clearCachesForScope(scope cacheClearScope) string {
	if s.strmServer == nil {
		return ""
	}
	switch scope {
	case cacheClearSearch:
		s.strmServer.ClearSearchCaches()
		return " Search cache cleared."
	case cacheClearPlaylist:
		s.strmServer.ClearPlaylistCaches()
		return " Playlist cache cleared."
	default:
		return ""
	}
}

// clearPatchedFilterProfiles empties the profile lists a save is about to
// replace, so the patch lands on a clean slate.
//
// A save is applied by seeding the config from the current one and unmarshalling
// the patch over it. encoding/json merges rather than replaces: decoding into a
// non-nil map keeps the entries the patch does not mention. A profile's
// attribute overrides are such a map, so resetting a trait back to its default —
// which drops its key — left the old override in place and the reset never
// stuck. Nil-ing the slice first makes the patch authoritative.
//
// Metadata profiles get the same treatment, but only when the patch carries
// the key: nil MetadataProfiles is the "never migrated" marker, and clearing
// it on an unrelated save would re-run the legacy migration on next load.
func clearPatchedFilterProfiles(body []byte, cfg *config.Config) {
	if len(body) == 0 || cfg == nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return
	}
	if _, ok := raw["filter_profiles"]; ok {
		cfg.FilterProfiles = nil
	}
	if _, ok := raw["metadata_profiles"]; ok {
		cfg.MetadataProfiles = nil
	}
	if _, ok := raw["format_profiles"]; ok {
		cfg.FormatProfiles = nil
	}
}
