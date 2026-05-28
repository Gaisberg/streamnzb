package stremio

import (
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
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
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
	validator                 *validation.Checker
	sessionManager            *session.Manager
	triageService             *triage.Service
	availClient               *availnzb.Client
	availReporter             *availnzb.Reporter
	availNZBIndexerHosts      map[string]string
	tmdbClient                *tmdb.Client
	tvdbClient                *tvdb.Client
	streamManager             *auth.StreamManager
	playlistCache             sync.Map
	rawSearchCache            sync.Map
	recordedSuccessSessionIDs sync.Map // session ID -> struct{}; record actual playback success only once per stream
	recordedPreloadSessionIDs sync.Map // session ID -> struct{}; record preload only once per session lifetime
	recordedFailureSessionIDs sync.Map // session ID -> struct{}; record failure only once per session lifetime (prevents concurrent goroutines from inserting duplicate rows)
	loggedThresholdSkipIDs    sync.Map // session ID -> struct{}; keep threshold-below logs to a single line per session
	pendingAttemptResolutions sync.Map // session ID -> int64 token; delayed finalization for short plays/probes
	nextReleaseIndex          sync.Map // key: streamToken|key.CacheKey() → *nextReleaseCursor; tracks manual "next" progression
	webHandler                http.Handler
	apiHandler                http.Handler
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
		validator:            opts.Validator,
		sessionManager:       opts.SessionManager,
		triageService:        opts.TriageService,
		availClient:          resolvedAvailClient,
		availReporter:        availReporter,
		availNZBIndexerHosts: opts.AvailNZBIndexerHosts,
		tmdbClient:           opts.TMDBClient,
		tvdbClient:           opts.TVDBClient,
		streamManager:        opts.StreamManager,
		attemptRecorder:      opts.AttemptRecorder,
		availIndexerStats:    make(map[string]AvailIndexerStats),
		uniqueIndexerHits:    make(map[string]int64),
	}

	if err := s.CheckPort(opts.Port); err != nil {
		return nil, err
	}

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

func (s *Server) ClearSearchCaches() {
	s.playlistCache.Range(func(key, _ interface{}) bool {
		s.playlistCache.Delete(key)
		return true
	})
	s.rawSearchCache.Range(func(key, _ interface{}) bool {
		s.rawSearchCache.Delete(key)
		return true
	})
	s.nextReleaseIndex.Range(func(key, _ interface{}) bool {
		s.nextReleaseIndex.Delete(key)
		return true
	})
	logger.Info("Search caches cleared")
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
	s.validator = opts.Validator
	s.triageService = opts.TriageService
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
