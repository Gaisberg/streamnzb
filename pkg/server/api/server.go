package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/health"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/nntp/proxy"
	"streamnzb/pkg/usenet/speedtest"
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

	// loginThrottle backs off repeated failed admin logins per client address.
	// Usable at its zero value, so it needs no wiring in the constructors.
	loginThrottle loginThrottle

	availNZBURL    string
	availNZBAPIKey string
	tmdbAPIKey     string
	tvdbAPIKey     string

	clients   map[*Client]bool
	clientsMu sync.Mutex
	// latestStats is the most recent snapshot the stats collector broadcast.
	// Connecting clients are handed this rather than collecting their own: the
	// speed meters are stateful, so an extra collection would steal part of the
	// window from the tick and leave the numbers in it disagreeing.
	latestStats     json.RawMessage
	latestStatsMu   sync.RWMutex
	logCh           chan string
	attemptLister   *persistence.StateManager
	addonRebind     func(int) error
	availNZBStore   availnzb.KeyStore
	speedTester     *speedtest.Tester
	metricsMu       sync.Mutex
	lastMetricsAt   time.Time
	metricsInFlight bool

	// Config reloads are serialized through one worker; rapid saves coalesce
	// so only the most recent pending config is applied.
	reloadMu      sync.Mutex
	pendingReload *config.Config
	reloadActive  bool

	// stopCh is closed by Shutdown to stop the stats and log-broadcast loops.
	// Only the constructors create it: tests build bare Servers, which have no
	// goroutines to stop. bgDone lets Shutdown wait for both to actually be
	// finished rather than merely told to stop — the stats loop writes metric
	// rows, and the caller closes the database next.
	stopCh   chan struct{}
	stopOnce sync.Once
	bgDone   sync.WaitGroup
}

type Client struct {
	conn   *websocket.Conn
	send   chan WSMessage
	stream *auth.Stream
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
		speedTester:    speedtest.NewTester(),
	}

	logger.SetBroadcast(s.logCh)
	s.stopCh = make(chan struct{})
	s.bgDone.Add(3)
	go s.broadcastLogs()
	go s.collectStatsLoop()
	go s.healthProbeLoop()
	health.Global().Subscribe(s.broadcastComponentHealth)

	return s
}

// Shutdown stops the background loops and waits for them to finish.
//
// The waiting is the substance. collectStatsLoop persists provider and indexer
// metrics every thirty seconds, so it has to be provably done before the caller
// closes the database — signalling it and moving on would leave a write racing
// the close. Safe on a Server no constructor built, and safe to call twice.
func (s *Server) Shutdown() {
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	s.bgDone.Wait()
}

// collectStatsLoop samples every meter once per second and hands the same
// snapshot to every client. Provider speed and per-stream speed are stateful
// meters — each sample closes the window the next one opens — so they must be
// read from one place, or clients see numbers measured over different slices of
// time that cannot be reconciled against each other.
func (s *Server) collectStatsLoop() {
	defer s.bgDone.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		s.publishStats()
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) publishStats() {
	payload, err := json.Marshal(s.collectStats())
	if err != nil {
		logger.Error("Failed to encode stats", "err", err)
		return
	}
	s.latestStatsMu.Lock()
	s.latestStats = payload
	s.latestStatsMu.Unlock()
	s.broadcast(WSMessage{Type: "stats", Payload: payload})
}

// snapshotStats returns the last published snapshot, nil before the first tick.
func (s *Server) snapshotStats() json.RawMessage {
	s.latestStatsMu.RLock()
	defer s.latestStatsMu.RUnlock()
	return s.latestStats
}

// broadcast queues msg to every connected client, dropping it for any client
// whose buffer is full rather than blocking the sender.
func (s *Server) broadcast(msg WSMessage) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		trySendWS(client, msg)
	}
}

func (s *Server) broadcastLogs() {
	defer s.bgDone.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case msgStr := <-s.logCh:
			s.broadcast(WSMessage{Type: "log_entry", Payload: json.RawMessage(fmt.Sprintf("%q", msgStr))})
		}
	}
}

func (s *Server) BroadcastNZBAttemptsUpdate() {
	s.broadcast(WSMessage{Type: "nzb_attempts_updated", Payload: json.RawMessage("null")})
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

// Indexer, IndexerCaps and Config expose the live indexer stack and
// configuration to handlers mounted outside this package (the Newznab
// endpoint). They are read through the lock on every call because a config
// reload swaps all three out from under a caller that held on to them.
func (s *Server) Indexer() indexer.Indexer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexer
}

func (s *Server) IndexerCaps() map[string]*indexer.Caps {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexerCaps
}

func (s *Server) Config() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *Server) SetIndexerCaps(caps map[string]*indexer.Caps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexerCaps = caps
}

// SetAddonRebinder installs the composition root's port-rebind hook, used when
// a config reload changes the addon port.
func (s *Server) SetAddonRebinder(rebind func(int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addonRebind = rebind
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

// ProxyServer returns the NNTP proxy currently installed, or nil when it is
// disabled. A config reload can replace it, so shutdown has to ask for the live
// one rather than holding the instance it started with.
func (s *Server) ProxyServer() *proxy.Server {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyServer
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
	if newCfg == nil || config.NormalizeAvailNZBMode(newCfg.AvailNZBMode) == "off" {
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

	resolvedKey, err := availnzb.ResolveAPIKey(store, availNZBURL, "", availnzb.DefaultAppName)
	if err != nil {
		logger.Warn("AvailNZB key bootstrap during reload failed", "err", err)
		return
	}
	if resolvedKey != "" {
		s.syncLiveAvailNZBAPIKey(resolvedKey)
	}
}

func (s *Server) ReloadFromComponents(comp *app.Components, scope app.ReloadScope) {
	var oldProxy *proxy.Server
	var oldPools map[string]*nntp.ClientPool
	var newProxy *proxy.Server
	// Providers feed the usenet pool the proxy serves from, so a pool swap
	// forces a proxy restart even when the listener config itself is unchanged.
	restartProxy := scope.Proxy || scope.Providers

	s.mu.Lock()
	if scope.Providers {
		oldPools = s.providerPools

		s.providerPools = comp.ProviderPools
		s.streamingPools = make([]*nntp.ClientPool, 0, len(comp.ProviderPools))
		for _, p := range comp.ProviderPools {
			s.streamingPools = append(s.streamingPools, p)
		}
		s.sessionMgr.UpdateUsenetPool(comp.UsenetPool)
	}
	if restartProxy {
		oldProxy = s.proxyServer
	}

	// comp carries the previous pointers for unchanged subsystems, so these
	// assignments are no-ops unless the scope rebuilt them.
	s.indexer = comp.Indexer
	s.config = comp.Config
	if s.sessionMgr != nil {
		s.sessionMgr.SetTTL(time.Duration(comp.Config.EffectiveSessionTTLSeconds()) * time.Second)
		s.sessionMgr.SetPostPlaybackEvictTTL(time.Duration(comp.Config.EffectiveSessionPostPlaybackTTLSeconds()) * time.Second)
	}
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
	stateMgr := s.attemptLister
	rebindAddon := s.addonRebind
	s.mu.Unlock()

	if scope.Database {
		if err := initialization.ReloadDatabase(stateMgr, comp.Config); err != nil {
			// The old database is still attached and working — the swap is
			// all-or-nothing — so this degrades to "setting did not apply".
			logger.Error("Failed to switch database; staying on the current one", "err", err)
		}
	}
	if scope.AddonPort && rebindAddon != nil {
		if err := rebindAddon(comp.Config.AddonPort); err != nil {
			logger.Error("Failed to rebind addon port; still serving on the previous one", "err", err)
		}
	}

	if restartProxy && oldProxy != nil {
		logger.Info("Stopping NNTP Proxy for reload...")
		if err := oldProxy.Stop(); err != nil {
			logger.Error("Failed to stop proxy", "err", err)
		}
	}
	if scope.Providers {
		// Only tear down pools that were not carried over into the new set.
		for name, pool := range oldPools {
			if comp.ProviderPools[name] != pool {
				pool.Shutdown()
			}
		}
		s.cleanupProviderUsageFromConfig(comp.Config)
	}
	if restartProxy {
		if comp.Config.ProxyEnabled {
			logger.Info("Restarting NNTP Proxy...", "host", comp.Config.ProxyHost, "port", comp.Config.ProxyPort)
			newProxy = proxy.NewServer(comp.Config.ProxyHost, comp.Config.ProxyPort, comp.UsenetPool, comp.Config.ProxyAuthUser, comp.Config.ProxyAuthPass)
			// Installed even when the bind below fails, so the settings page can
			// read the reason off it instead of showing a healthy-looking proxy.
			s.mu.Lock()
			s.proxyServer = newProxy
			s.mu.Unlock()

			// Binding here rather than inside the goroutine keeps the failure
			// visible to the save that caused it: the settings page refetches
			// the config as soon as this reload returns.
			if err := newProxy.Listen(); err != nil {
				logger.Warn("NNTP proxy is not listening", "err", err)
			} else {
				go func(p *proxy.Server) {
					if err := p.Serve(); err != nil {
						logger.Error("NNTP proxy stopped serving", "err", err)
					}
				}(newProxy)
			}
		} else {
			logger.Info("NNTP proxy disabled by config; not starting proxy server")
			s.mu.Lock()
			s.proxyServer = nil
			s.mu.Unlock()
		}
	}
	if scope.Indexers {
		s.cleanupIndexerUsageFromConfig(comp.Config)
	}

	logger.SetLevel(comp.Config.LogLevel)
	logger.SetVerboseNNTPLogging(comp.Config.VerboseNNTPLogging)
	if s.strmServer != nil {
		s.strmServer.Reload(&stremio.ServerOptions{
			Config:               comp.Config,
			BaseURL:              comp.Config.AddonBaseURL,
			Indexer:              comp.Indexer,
			QueryCache:           comp.QueryCache,
			Validator:            comp.Validator,
			TriageService:        comp.Triage,
			AvailClient:          comp.AvailClient,
			AvailNZBIndexerHosts: comp.AvailNZBIndexerHosts,
			TMDBClient:           comp.TMDBClient,
			TVDBClient:           comp.TVDBClient,
			SimklClient:          comp.SimklClient,
			StreamManager:        s.streamManager,
		})
	}
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

	authMiddleware := auth.StreamAuthMiddleware(s.streamManager, func() string { return s.adminUsername() }, func() string { return s.adminToken() })
	mux.Handle("/api/ws", authMiddleware(http.HandlerFunc(s.handleWebSocket)))
	mux.Handle("/api/config", authMiddleware(http.HandlerFunc(s.handleConfig)))
	mux.Handle("/api/cache/clear", authMiddleware(http.HandlerFunc(s.handleClearCache)))
	mux.Handle("/api/devices", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/devices/", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/streams", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/streams/", authMiddleware(http.HandlerFunc(s.handleManagedStreams)))
	mux.Handle("/api/providers/test", authMiddleware(http.HandlerFunc(s.handleProviderTest)))
	mux.Handle("/api/providers/speedtest", authMiddleware(http.HandlerFunc(s.handleProviderSpeedTest)))
	mux.Handle("/api/indexer/caps", authMiddleware(http.HandlerFunc(s.handleGetIndexerCaps)))
	mux.Handle("/api/indexer/caps/refresh", authMiddleware(http.HandlerFunc(s.handleRefreshIndexerCaps)))
	mux.Handle("/api/useragents/latest", authMiddleware(http.HandlerFunc(s.handleLatestUserAgents)))
	mux.Handle("/api/stats/persisted", authMiddleware(http.HandlerFunc(s.handlePersistedStats)))
	mux.Handle("/api/stats/history", authMiddleware(http.HandlerFunc(s.handleStatsHistory)))
	mux.Handle("/api/stats/performance", authMiddleware(http.HandlerFunc(s.handlePerformanceStats)))
	mux.Handle("/api/availnzb/status", authMiddleware(http.HandlerFunc(s.handleAvailNZBStatus)))
	mux.Handle("/api/sessions/close", authMiddleware(http.HandlerFunc(s.handleCloseSession)))
	mux.Handle("/api/restart", authMiddleware(http.HandlerFunc(s.handleRestart)))
	mux.Handle("/api/auth/change-password", authMiddleware(http.HandlerFunc(s.handleChangePassword)))
	mux.Handle("/api/tmdb/search", authMiddleware(http.HandlerFunc(s.handleTMDBSearch)))
	mux.Handle("/api/tmdb/tv/", authMiddleware(http.HandlerFunc(s.handleTMDBTV)))
	mux.Handle("/api/metadata/catalogs", authMiddleware(http.HandlerFunc(s.handleMetadataCatalogs)))
	mux.Handle("/api/metadata/certifications", authMiddleware(http.HandlerFunc(s.handleMetadataCertifications)))
	mux.Handle("/api/simkl/status", authMiddleware(http.HandlerFunc(s.handleSimklStatus)))
	mux.Handle("/api/simkl/pin", authMiddleware(http.HandlerFunc(s.handleSimklPin)))
	mux.Handle("/api/simkl/pin/check", authMiddleware(http.HandlerFunc(s.handleSimklPinCheck)))
	mux.Handle("/api/simkl/disconnect", authMiddleware(http.HandlerFunc(s.handleSimklDisconnect)))
	mux.Handle("/api/search/streams", authMiddleware(http.HandlerFunc(s.handleStreams)))
	mux.Handle("/api/search/releases", authMiddleware(http.HandlerFunc(s.handleSearchReleases)))
	mux.Handle("/api/play/nzb", authMiddleware(http.HandlerFunc(s.handleDirectPlayNZB)))
	mux.Handle("/api/ranking/explain", authMiddleware(http.HandlerFunc(s.handleRankingExplain)))
	mux.Handle("/api/format/preview", authMiddleware(http.HandlerFunc(s.handleFormatPreview)))
	mux.Handle("/api/format/convert", authMiddleware(http.HandlerFunc(s.handleFormatConvert)))

	mux.Handle("/api/logs/download", authMiddleware(http.HandlerFunc(s.handleDownloadLogs)))
	mux.Handle("/api/nzb-attempts", authMiddleware(http.HandlerFunc(s.handleNZBAttempts)))
	mux.Handle("/api/nzb-attempts/clear", authMiddleware(http.HandlerFunc(s.handleClearNZBAttempts)))
	mux.Handle("/api/search-diagnostics", authMiddleware(http.HandlerFunc(s.handleSearchDiagnostics)))

	mux.Handle("/api/library", authMiddleware(http.HandlerFunc(s.handleGetLibrary)))
	mux.Handle("/api/library/pin", authMiddleware(http.HandlerFunc(s.handlePinLibrary)))
	mux.Handle("/api/library/delete", authMiddleware(http.HandlerFunc(s.handleDeleteLibrary)))
	mux.Handle("/api/library/clear", authMiddleware(http.HandlerFunc(s.handleClearLibrary)))
	mux.Handle("/api/library/stats", authMiddleware(http.HandlerFunc(s.handleLibraryStats)))
	mux.Handle("/api/health/components", authMiddleware(http.HandlerFunc(s.handleComponentHealth)))
	mux.Handle("/api/health/components/retry", authMiddleware(http.HandlerFunc(s.handleComponentHealthRetry)))

	return mux
}
