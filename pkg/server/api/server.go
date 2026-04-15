package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/nntp/proxy"
	"streamnzb/pkg/usenet/validation"
)

type Server struct {
	mu             sync.RWMutex
	config         *config.Config
	providerPools  map[string]*nntp.ClientPool
	streamingPools []*nntp.ClientPool
	sessionMgr     *session.Manager
	strmServer     *stremio.Server
	proxyServer    *proxy.Server
	indexer        indexer.Indexer
	indexerCaps    map[string]*indexer.Caps
	streamManager  *auth.StreamManager
	app            *app.App

	availNZBURL    string
	availNZBAPIKey string
	tmdbAPIKey     string
	tvdbAPIKey     string

	clients       map[*Client]bool
	clientsMu     sync.Mutex
	logCh         chan string
	attemptLister *persistence.StateManager
	availNZBStore availnzb.KeyStore

	availNZBRegistrationInFlight bool
}

type Client struct {
	conn   *websocket.Conn
	send   chan WSMessage
	stream *auth.Stream

	user *auth.Stream
}

func NewServer(cfg *config.Config, pools map[string]*nntp.ClientPool, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer, streamManager *auth.StreamManager, availNZBURL, availNZBAPIKey, tmdbAPIKey, tvdbAPIKey string) *Server {
	return NewServerWithApp(cfg, pools, sessMgr, strmServer, indexer, streamManager, nil, availNZBURL, availNZBAPIKey, tmdbAPIKey, tvdbAPIKey)
}

func NewServerWithApp(cfg *config.Config, pools map[string]*nntp.ClientPool, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer, streamManager *auth.StreamManager, a *app.App, availNZBURL, availNZBAPIKey, tmdbAPIKey, tvdbAPIKey string) *Server {

	var list []*nntp.ClientPool
	for _, p := range pools {
		list = append(list, p)
	}

	s := &Server{
		config:         cfg,
		providerPools:  pools,
		streamingPools: list,
		sessionMgr:     sessMgr,
		strmServer:     strmServer,
		indexer:        indexer,
		streamManager:  streamManager,
		app:            a,
		availNZBURL:    availNZBURL,
		availNZBAPIKey: availNZBAPIKey,
		tmdbAPIKey:     tmdbAPIKey,
		tvdbAPIKey:     tvdbAPIKey,
		clients:        make(map[*Client]bool),
		logCh:          make(chan string, 100),
	}

	logger.SetBroadcast(s.logCh)
	go s.broadcastLogs()

	return s
}

func (s *Server) broadcastLogs() {
	for msgStr := range s.logCh {
		msg := WSMessage{Type: "log_entry", Payload: json.RawMessage(fmt.Sprintf("%q", msgStr))}

		s.clientsMu.Lock()
		for client := range s.clients {
			select {
			case client.send <- msg:
			default:

			}
		}
		s.clientsMu.Unlock()
	}
}

func (s *Server) BroadcastNZBAttemptsUpdate() {
	msg := WSMessage{Type: "nzb_attempts_updated", Payload: json.RawMessage("null")}
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		select {
		case client.send <- msg:
		default:
		}
	}
}

func (s *Server) AddClient(client *Client) {
	s.clientsMu.Lock()
	s.clients[client] = true
	s.clientsMu.Unlock()
}

func (s *Server) RemoveClient(client *Client) {
	s.clientsMu.Lock()
	delete(s.clients, client)
	s.clientsMu.Unlock()
	close(client.send)
}

func (s *Server) SetIndexerCaps(caps map[string]*indexer.Caps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexerCaps = caps
}

func (s *Server) SetAttemptLister(m *persistence.StateManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attemptLister = m
	s.availNZBStore = m
}

func (s *Server) SetProxyServer(p *proxy.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxyServer = p
}

func (s *Server) SetAvailNZBAPIKey(apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.availNZBAPIKey = strings.TrimSpace(apiKey)
}

func (s *Server) syncLiveAvailNZBAPIKey(apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if s.app != nil {
		s.app.SetAvailNZBAPIKey(apiKey)
	}
	s.SetAvailNZBAPIKey(apiKey)
}

func (s *Server) ensureAvailNZBReadyForReload(newCfg *config.Config) {
	if newCfg == nil || newCfg.AvailNZBMode == "disabled" {
		return
	}

	explicitKey := strings.TrimSpace(newCfg.AvailNZBAPIKey)
	if explicitKey != "" {
		s.syncLiveAvailNZBAPIKey(explicitKey)
		return
	}

	s.mu.RLock()
	currentKey := strings.TrimSpace(s.availNZBAPIKey)
	availNZBURL := strings.TrimSpace(s.availNZBURL)
	store := s.availNZBStore
	s.mu.RUnlock()

	if currentKey != "" {
		s.syncLiveAvailNZBAPIKey(currentKey)
		return
	}

	resolvedKey, err := availnzb.ResolveStartupAPIKey(store, availNZBURL, "")
	if err != nil {
		logger.Warn("AvailNZB key bootstrap during reload failed", "err", err)
		return
	}
	if resolvedKey != "" {
		s.syncLiveAvailNZBAPIKey(resolvedKey)
		return
	}

	s.startDeferredAvailNZBRegistration(newCfg.AvailNZBMode)
}

func (s *Server) startDeferredAvailNZBRegistration(mode string) {
	s.mu.Lock()
	if s.availNZBRegistrationInFlight {
		s.mu.Unlock()
		return
	}
	availNZBURL := strings.TrimSpace(s.availNZBURL)
	store := s.availNZBStore
	if availNZBURL == "" || store == nil {
		s.mu.Unlock()
		return
	}
	s.availNZBRegistrationInFlight = true
	s.mu.Unlock()

	logger.Info("AvailNZB API key registration deferred", "mode", mode)

	go func() {
		defer func() {
			s.mu.Lock()
			s.availNZBRegistrationInFlight = false
			s.mu.Unlock()
		}()

		registeredKey, err := availnzb.RegisterAndPersistAPIKey(store, availNZBURL, availnzb.DefaultAppName)
		if err != nil {
			if errors.Is(err, availnzb.ErrRegisterKeyIPAlreadyHasKey) {
				return
			}
			logger.Warn("AvailNZB background key registration failed", "err", err)
			return
		}

		s.syncLiveAvailNZBAPIKey(registeredKey)

		if s.app != nil {
			current := s.app.Components()
			if current != nil && current.AvailClient != nil {
				if err := current.AvailClient.RefreshBackbones(); err != nil {
					logger.Debug("AvailNZB backbones refresh", "source", "background_registration", "err", err)
				}
			}
		}

		logger.Info("AvailNZB background key registration completed")
	}()
}

func (s *Server) ReloadFromComponents(comp *app.Components, fullReload bool) {
	var oldProxy *proxy.Server
	var oldPools map[string]*nntp.ClientPool
	var newProxy *proxy.Server

	s.mu.Lock()
	if fullReload {
		oldProxy = s.proxyServer
		oldPools = s.providerPools

		s.providerPools = comp.ProviderPools
		s.indexer = comp.Indexer
		s.streamingPools = make([]*nntp.ClientPool, 0, len(comp.ProviderPools))
		for _, p := range comp.ProviderPools {
			s.streamingPools = append(s.streamingPools, p)
		}
		s.sessionMgr.UpdatePools(s.streamingPools)
		s.sessionMgr.UpdateUsenetPool(comp.UsenetPool)
	}

	s.config = comp.Config
	s.tmdbAPIKey = strings.TrimSpace(comp.Config.TMDBAPIKey)
	s.tvdbAPIKey = strings.TrimSpace(comp.Config.TVDBAPIKey)
	if s.app != nil {
		s.tmdbAPIKey = s.app.EffectiveTMDBKey()
		s.tvdbAPIKey = s.app.EffectiveTVDBKey()
	}
	if s.streamManager != nil {
		s.streamManager.SetConfig(comp.Config, nil)
	}
	if comp.IndexerCaps != nil {
		s.indexerCaps = comp.IndexerCaps
	}
	s.mu.Unlock()

	if fullReload {
		if oldProxy != nil {
			logger.Info("Stopping NNTP Proxy for reload...")
			if err := oldProxy.Stop(); err != nil {
				logger.Error("Failed to stop proxy", "err", err)
			}
		}
		for _, pool := range oldPools {
			pool.Shutdown()
		}

		if comp.Config.ProxyEnabled {
			logger.Info("Restarting NNTP Proxy...", "host", comp.Config.ProxyHost, "port", comp.Config.ProxyPort)
			var err error
			newProxy, err = proxy.NewServer(comp.Config.ProxyHost, comp.Config.ProxyPort, comp.UsenetPool, comp.Config.ProxyAuthUser, comp.Config.ProxyAuthPass)
			if err != nil {
				logger.Error("Failed to create new proxy during reload", "err", err)
			} else {
				s.mu.Lock()
				s.proxyServer = newProxy
				s.mu.Unlock()
				go func(p *proxy.Server) {
					if err := p.Start(); err != nil {
						logger.Error("Proxy server failed to start", "err", err)
					}
				}(newProxy)
			}
		} else {
			logger.Info("NNTP proxy disabled by config; not starting proxy server")
			s.mu.Lock()
			s.proxyServer = nil
			s.mu.Unlock()
		}
		s.cleanupIndexerUsageFromConfig(comp.Config)
		s.cleanupProviderUsageFromConfig(comp.Config)
	}

	logger.SetLevel(comp.Config.LogLevel)
	logger.SetVerboseNNTPLogging(comp.Config.VerboseNNTPLogging)
	if s.strmServer != nil {
		s.strmServer.Reload(&stremio.ServerOptions{
			Config:               comp.Config,
			BaseURL:              comp.Config.AddonBaseURL,
			Indexer:              comp.Indexer,
			Validator:            comp.Validator,
			TriageService:        comp.Triage,
			AvailClient:          comp.AvailClient,
			AvailNZBIndexerHosts: comp.AvailNZBIndexerHosts,
			TMDBClient:           comp.TMDBClient,
			TVDBClient:           comp.TVDBClient,
			StreamManager:        s.streamManager,
		})
	}
}

func (s *Server) Reload(cfg *config.Config, pools map[string]*nntp.ClientPool, indexers indexer.Indexer,
	validator *validation.Checker, triage *triage.Service, avail *availnzb.Client, availNZBIndexerHosts map[string]string,
	tmdbClient *tmdb.Client, tvdbClient *tvdb.Client) {
	comp := &app.Components{
		Config:               cfg,
		Indexer:              indexers,
		ProviderPools:        pools,
		StreamingPools:       nil,
		AvailNZBIndexerHosts: availNZBIndexerHosts,
		Validator:            validator,
		Triage:               triage,
		AvailClient:          avail,
		TMDBClient:           tmdbClient,
		TVDBClient:           tvdbClient,
	}
	var streamingPools []*nntp.ClientPool
	for _, p := range pools {
		streamingPools = append(streamingPools, p)
	}
	comp.StreamingPools = streamingPools
	comp.ProviderOrder = make([]string, 0, len(pools))
	for name := range pools {
		comp.ProviderOrder = append(comp.ProviderOrder, name)
	}
	s.ReloadFromComponents(comp, true)
}

func (s *Server) cleanupIndexerUsageFromConfig(cfg *config.Config) {
	usageMgr, err := indexer.GetUsageManager(nil)
	if err != nil || usageMgr == nil {
		return
	}
	var configuredNames []string
	if cfg != nil {
		for _, idx := range cfg.Indexers {
			if idx.URL != "" && idx.Name != "" {
				configuredNames = append(configuredNames, idx.Name)
			}
		}
	}
	usageMgr.SyncUsage(configuredNames)
}

func (s *Server) cleanupProviderUsageFromConfig(cfg *config.Config) {
	usageMgr, err := nntp.GetProviderUsageManager(nil)
	if err != nil || usageMgr == nil {
		return
	}
	var configuredNames []string
	if cfg != nil {
		for _, p := range cfg.Providers {
			if p.Name != "" {
				configuredNames = append(configuredNames, p.Name)
			}
		}
	}
	usageMgr.SyncUsage(configuredNames)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/auth/check", s.handleAuthCheck)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/info", s.handleInfo)

	authMiddleware := auth.StreamAuthMiddleware(s.streamManager, func() string { return s.config.GetAdminUsername() }, func() string { return s.config.AdminToken })
	mux.Handle("/api/ws", authMiddleware(http.HandlerFunc(s.handleWebSocket)))
	mux.Handle("/api/config", authMiddleware(http.HandlerFunc(s.handleConfig)))
	mux.Handle("/api/cache/clear", authMiddleware(http.HandlerFunc(s.handleClearCache)))
	mux.Handle("/api/devices", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/devices/", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/streams", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/streams/", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/indexer/caps", authMiddleware(http.HandlerFunc(s.handleGetIndexerCaps)))
	mux.Handle("/api/indexer/caps/refresh", authMiddleware(http.HandlerFunc(s.handleRefreshIndexerCaps)))
	mux.Handle("/api/availnzb/status", authMiddleware(http.HandlerFunc(s.handleAvailNZBStatus)))
	mux.Handle("/api/sessions/close", authMiddleware(http.HandlerFunc(s.handleCloseSession)))
	mux.Handle("/api/restart", authMiddleware(http.HandlerFunc(s.handleRestart)))
	mux.Handle("/api/auth/change-password", authMiddleware(http.HandlerFunc(s.handleChangePassword)))
	mux.Handle("/api/tmdb/search", authMiddleware(http.HandlerFunc(s.handleTMDBSearch)))
	mux.Handle("/api/tmdb/tv/", authMiddleware(http.HandlerFunc(s.handleTMDBTV)))
	mux.Handle("/api/search/streams", authMiddleware(http.HandlerFunc(s.handleStreams)))
	mux.Handle("/api/search/releases", authMiddleware(http.HandlerFunc(s.handleSearchReleases)))

	mux.Handle("/api/logs/download", authMiddleware(http.HandlerFunc(s.handleDownloadLogs)))
	mux.Handle("/api/nzb-attempts", authMiddleware(http.HandlerFunc(s.handleNZBAttempts)))

	return mux
}
