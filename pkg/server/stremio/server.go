package stremio

import (
	"fmt"
	"net"
	"net/http"
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
	nextReleaseIndex          sync.Map // key: streamToken|key.CacheKey() → *nextReleaseCursor; tracks manual "next" progression
	webHandler                http.Handler
	apiHandler                http.Handler
	attemptRecorder           *persistence.StateManager
	onAttemptRecorded         func()
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
		availNZBMode = opts.Config.AvailNZBMode
	}
	var resolvedAvailClient *availnzb.Client
	if availNZBMode != "disabled" {
		resolvedAvailClient = opts.AvailClient
	}
	var availReporter *availnzb.Reporter
	if resolvedAvailClient != nil {
		availReporter = availnzb.NewReporter(resolvedAvailClient, opts.Validator)
		if availNZBMode == "status_only" {
			availReporter.Disabled = true
		}
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
		reloadMode = opts.Config.AvailNZBMode
	}
	if reloadMode == "disabled" {
		s.availClient = nil
		s.availReporter = nil
	} else if opts.AvailClient != nil {
		s.availClient = opts.AvailClient
		s.availReporter = availnzb.NewReporter(opts.AvailClient, opts.Validator)
		if reloadMode == "status_only" {
			s.availReporter.Disabled = true
		}
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
