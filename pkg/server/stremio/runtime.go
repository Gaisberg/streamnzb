package stremio

import (
	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/usenet/validation"
)

// serverRuntime is the set of dependencies Reload replaces together when the
// configuration is saved.
//
// It exists because these twelve fields are not independent. A config that
// added an indexer arrives with the aggregator that can reach it; a config that
// switched AvailNZB off arrives with a nil client. Reading them one at a time —
// which is what 131 call sites used to do, none of them holding the lock —
// risks a request built from half of one configuration and half of the next: a
// new indexer list against the old aggregator, or a mode saying AvailNZB is on
// against the nil client that says it is off.
//
// So the accessor hands back all of them at once, under one read lock. Callers
// take a snapshot at the top and work from it, which makes "these move
// together" a property of the code rather than something to remember.
type serverRuntime struct {
	config               *config.Config
	baseURL              string
	indexer              indexer.Indexer
	queryCache           *indexer.QueryCache
	validator            *validation.Checker
	triageService        *triage.Service
	availClient          *availnzb.Client
	availReporter        *availnzb.Reporter
	availNZBIndexerHosts map[string]string
	tmdbClient           *tmdb.Client
	tvdbClient           *tvdb.Client
	streamManager        *auth.StreamManager
}

// runtime snapshots the reload set. Nil-safe on a Server no constructor built,
// because the package's tests build bare &Server{} literals.
func (s *Server) runtime() serverRuntime {
	if s == nil {
		return serverRuntime{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return serverRuntime{
		config:               s.config,
		baseURL:              s.baseURL,
		indexer:              s.indexer,
		queryCache:           s.queryCache,
		validator:            s.validator,
		triageService:        s.triageService,
		availClient:          s.availClient,
		availReporter:        s.availReporter,
		availNZBIndexerHosts: s.availNZBIndexerHosts,
		tmdbClient:           s.tmdbClient,
		tvdbClient:           s.tvdbClient,
		streamManager:        s.streamManager,
	}
}
