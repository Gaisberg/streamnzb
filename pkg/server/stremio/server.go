package stremio

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/ffprobe"
	"streamnzb/pkg/playback"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/validation"
)

var (
	availTrue  = true
	availFalse = false
)

type Server struct {
	mu                        sync.RWMutex
	availStatsMu              sync.RWMutex
	manifest                  *Manifest
	version                   string
	baseURL                   string
	config                    *config.Config
	indexer                   indexer.Indexer
	queryCache                *indexer.QueryCache
	validator                 *validation.Checker
	sessionManager            *session.Manager
	triageService             *triage.Service
	rankingService            *ranking.Service
	availClient               *availnzb.Client
	availReporter             *availnzb.Reporter
	availNZBIndexerHosts      map[string]string
	tmdbClient                *tmdb.Client
	tvdbClient                *tvdb.Client
	kitsuClient               *kitsu.Client
	streamManager             *auth.StreamManager
	playlistCache             sync.Map
	rawSearchCache            sync.Map
	recordedSuccessSessionIDs sync.Map // session ID -> struct{}; record actual playback success only once per stream
	recordedPreloadSessionIDs sync.Map // session ID -> struct{}; record preload only once per session lifetime
	recordedFailureSessionIDs sync.Map // session ID -> struct{}; record failure only once per session lifetime (prevents concurrent goroutines from inserting duplicate rows)
	loggedThresholdSkipIDs    sync.Map // session ID -> struct{}; keep threshold-below logs to a single line per session
	pendingAttemptResolutions sync.Map // session ID -> int64 token; delayed finalization for short plays/probes
	nextReleaseIndex          sync.Map // key: streamToken|key.CacheKey() → *nextReleaseCursor; tracks manual "next" progression
	preProbeCancels           sync.Map // key: StreamSlotKey.CacheKey() → *preProbeCancelEntry; cancels the in-flight speculative pre-probe sweep when real playback starts
	pendingLibrarySavedIDs    sync.Map // session ID -> struct{}; the serve-path pending library save runs once per session, not once per HTTP range request
	webHandler                http.Handler
	apiHandler                http.Handler
	playback                  *playback.Service
	attemptRecorder           *persistence.StateManager
	onAttemptRecorded         func()
	availIndexerStats         map[string]AvailIndexerStats
	uniqueIndexerHits         map[string]int64
}

// AvailIndexerStats stores per-indexer availability outcomes aggregated from
// playlist processing: AvailableReturned counts releases returned as available,
// and Discarded counts releases discarded as unavailable.
type AvailIndexerStats struct {
	AvailableReturned int64
	Discarded         int64
}

const FailoverOrderPath = "/failover_order"

type ServerOptions struct {
	Config               *config.Config
	BaseURL              string
	Port                 int
	Indexer              indexer.Indexer
	QueryCache           *indexer.QueryCache
	Validator            *validation.Checker
	SessionManager       *session.Manager
	TriageService        *triage.Service
	AvailClient          *availnzb.Client
	AvailNZBIndexerHosts map[string]string
	TMDBClient           *tmdb.Client
	TVDBClient           *tvdb.Client
	StreamManager        *auth.StreamManager
	Version              string
	AttemptRecorder      *persistence.StateManager
}

func NewServer(opts *ServerOptions) (*Server, error) {
	if opts == nil {
		return nil, fmt.Errorf("ServerOptions is required")
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	availNZBMode := ""
	if opts.Config != nil {
		availNZBMode = config.NormalizeAvailNZBMode(opts.Config.AvailNZBMode)
	}
	var resolvedAvailClient *availnzb.Client
	if availNZBMode != "off" {
		resolvedAvailClient = opts.AvailClient
	}
	var availReporter *availnzb.Reporter
	if resolvedAvailClient != nil {
		availReporter = availnzb.NewReporter(resolvedAvailClient, opts.Validator)
	}
	s := &Server{
		manifest:             NewManifest(version),
		version:              version,
		baseURL:              opts.BaseURL,
		config:               opts.Config,
		indexer:              opts.Indexer,
		queryCache:           opts.QueryCache,
		validator:            opts.Validator,
		sessionManager:       opts.SessionManager,
		triageService:        opts.TriageService,
		rankingService:       newRankingService(opts.Config),
		availClient:          resolvedAvailClient,
		availReporter:        availReporter,
		availNZBIndexerHosts: opts.AvailNZBIndexerHosts,
		tmdbClient:           opts.TMDBClient,
		tvdbClient:           opts.TVDBClient,
		kitsuClient:          kitsu.NewClient(nil),
		streamManager:        opts.StreamManager,
		attemptRecorder:      opts.AttemptRecorder,
		availIndexerStats:    make(map[string]AvailIndexerStats),
		uniqueIndexerHits:    make(map[string]int64),
	}

	playbackSvc := &playback.Service{
		Sessions:                   opts.SessionManager,
		FFprobePath:                s.effectiveFFprobePath,
		StartupTimeout:             s.playbackStartupTimeout,
		AllowLargestDirectFallback: allowLargestDirectFallbackForSession,
		SaveToLibrary:              s.saveSessionToLibrary,
		NotePendingLibrarySave:     s.notePendingLibrarySave,
	}
	if opts.Validator != nil {
		playbackSvc.Validator = opts.Validator
	}
	s.playback = playbackSvc

	if err := s.CheckPort(opts.Port); err != nil {
		return nil, err
	}

	go func() {
		customPath := ""
		if opts.Config != nil {
			customPath = opts.Config.EffectiveFFprobePath()
		}
		if _, err := ffprobe.EnsureFFprobe(context.Background(), customPath); err != nil {
			logger.Debug("EnsureFFprobe background check", "err", err)
		}
	}()

	s.startLibraryFreshnessSweeper()

	return s, nil
}

func (s *Server) CheckPort(port int) error {
	address := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("addon port %d listen check failed: %w", port, err)
	}
	ln.Close()
	return nil
}

func (s *Server) SetWebHandler(h http.Handler) {
	s.webHandler = h
}

func (s *Server) SetAPIHandler(h http.Handler) {
	s.apiHandler = h
}

// clearSyncMap removes every entry from m.
func clearSyncMap(m *sync.Map) {
	m.Range(func(key, _ interface{}) bool {
		m.Delete(key)
		return true
	})
}

func (s *Server) ClearSearchCaches() {
	clearSyncMap(&s.playlistCache)
	clearSyncMap(&s.rawSearchCache)
	clearSyncMap(&s.nextReleaseIndex)
	if s.queryCache != nil {
		s.queryCache.Clear()
	}
	logger.Info("Search caches cleared")
}

// ClearPlaylistCaches clears only the playlist (filtered/sorted) cache while
// keeping the raw indexer search results cache intact. Use this when only
// filtering/sorting configuration changes (e.g. filter profiles, stream
// filter_sorting_mode) so subsequent requests reuse the cached indexer results
// and only re-run the cheap filter/sort step.
func (s *Server) ClearPlaylistCaches() {
	clearSyncMap(&s.playlistCache)
	clearSyncMap(&s.nextReleaseIndex)
	logger.Info("Playlist caches cleared (raw search cache preserved)")
}

// SetOnAttemptRecorded sets a callback invoked after each NZB attempt is recorded (e.g. to broadcast to WS clients).
func (s *Server) SetOnAttemptRecorded(f func()) {
	s.onAttemptRecorded = f
}

func (s *Server) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.version != "" {
		return s.version
	}
	return "dev"
}

func (s *Server) addAvailIndexerStats(availableByIndexer, discardedByIndexer map[string]int) {
	if len(availableByIndexer) == 0 && len(discardedByIndexer) == 0 {
		return
	}
	s.availStatsMu.Lock()
	defer s.availStatsMu.Unlock()
	for name, n := range availableByIndexer {
		if strings.TrimSpace(name) == "" || n <= 0 {
			continue
		}
		curr := s.availIndexerStats[name]
		curr.AvailableReturned += int64(n)
		s.availIndexerStats[name] = curr
	}
	for name, n := range discardedByIndexer {
		if strings.TrimSpace(name) == "" || n <= 0 {
			continue
		}
		curr := s.availIndexerStats[name]
		curr.Discarded += int64(n)
		s.availIndexerStats[name] = curr
	}
}

func (s *Server) addUniqueIndexerHits(hitsByIndexer map[string]int) {
	if len(hitsByIndexer) == 0 {
		return
	}
	s.availStatsMu.Lock()
	defer s.availStatsMu.Unlock()
	for name, n := range hitsByIndexer {
		if strings.TrimSpace(name) == "" || n <= 0 {
			continue
		}
		s.uniqueIndexerHits[name] += int64(n)
	}
}

// GetAvailIndexerStats returns a snapshot copy of availIndexerStats keyed by
// indexer name. The copy is read under availStatsMu to avoid exposing internal
// mutable state to callers.
func (s *Server) GetAvailIndexerStats() map[string]AvailIndexerStats {
	s.availStatsMu.RLock()
	defer s.availStatsMu.RUnlock()
	out := make(map[string]AvailIndexerStats, len(s.availIndexerStats))
	for k, v := range s.availIndexerStats {
		out[k] = v
	}
	return out
}

// GetUniqueIndexerHits returns a snapshot copy of uniqueIndexerHits keyed by
// indexer name. The copy is read under availStatsMu to avoid exposing internal
// mutable state to callers.
func (s *Server) GetUniqueIndexerHits() map[string]int64 {
	s.availStatsMu.RLock()
	defer s.availStatsMu.RUnlock()
	out := make(map[string]int64, len(s.uniqueIndexerHits))
	for k, v := range s.uniqueIndexerHits {
		out[k] = v
	}
	return out
}

func (s *Server) Reload(opts *ServerOptions) {
	if opts == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config = opts.Config
	s.baseURL = opts.BaseURL
	s.indexer = opts.Indexer
	s.queryCache = opts.QueryCache
	s.validator = opts.Validator
	s.triageService = opts.TriageService
	reloadRankingService(s.rankingService, opts.Config)
	reloadMode := ""
	if opts.Config != nil {
		reloadMode = config.NormalizeAvailNZBMode(opts.Config.AvailNZBMode)
	}
	if reloadMode == "off" {
		s.availClient = nil
		s.availReporter = nil
	} else if opts.AvailClient != nil {
		s.availClient = opts.AvailClient
		s.availReporter = availnzb.NewReporter(opts.AvailClient, opts.Validator)
		go func(client *availnzb.Client) {
			if err := client.RefreshBackbones(); err != nil {
				logger.Debug("AvailNZB backbones refresh", "source", "stremio_reload", "err", err)
			}
		}(opts.AvailClient)
	} else {
		s.availClient = nil
		s.availReporter = nil
	}
	s.availNZBIndexerHosts = opts.AvailNZBIndexerHosts
	s.tmdbClient = opts.TMDBClient
	s.tvdbClient = opts.TVDBClient
	s.streamManager = opts.StreamManager
}

// reloadRankingService compiles the config's filter profiles. The service is
// reloaded in place rather than replaced, so playlist builds running
// concurrently keep reading a service that guards itself.
//
// A profile that fails to compile is logged and skipped, degrading to "no
// filtering" for streams bound to it rather than breaking startup.
func newRankingService(cfg *config.Config) *ranking.Service {
	svc := ranking.NewService()
	reloadRankingService(svc, cfg)
	return svc
}

func reloadRankingService(svc *ranking.Service, cfg *config.Config) {
	if svc == nil {
		return
	}
	for _, err := range svc.Reload(cfg) {
		logger.Warn("Filter profile failed to compile", "err", err)
	}
}
