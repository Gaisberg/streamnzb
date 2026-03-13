package app

import (
	"sync"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
	"streamnzb/pkg/usenet/validation"
)

type BuildOpts struct {
	DataStore  *config.DataStore
	SessionTTL time.Duration
}

type Components struct {
	Config               *config.Config
	Indexer              indexer.Indexer
	ProviderPools        map[string]*nntp.ClientPool
	ProviderOrder        []string
	StreamingPools       []*nntp.ClientPool
	UsenetPool           *pool.Pool
	AvailNZBIndexerHosts []string
	Validator            *validation.Checker
	Triage               *triage.Service
	AvailClient          *availnzb.Client
	TMDBClient           *tmdb.Client
	TVDBClient           *tvdb.Client
	SegmentCacheBudget   *pool.SegmentCacheBudget
}

type App struct {
	mu         sync.RWMutex
	components *Components
	opts       BuildOpts
}

func New() *App {
	return &App{}
}

func (a *App) Build(cfg *config.Config, opts BuildOpts) (*Components, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts = opts

	comp, err := a.buildFull(cfg, opts)
	if err != nil {
		return nil, err
	}
	a.components = comp
	return comp, nil
}

func (a *App) buildFull(cfg *config.Config, opts BuildOpts) (*Components, error) {
	base, err := BuildComponents(cfg)
	if err != nil {
		return nil, err
	}

	const validationSampleSize = 5
	validator := validation.NewChecker(base.UsenetPool, validationSampleSize, 6)
	defaultFilters := config.DefaultFilterConfig()
	defaultSorting := config.DefaultSortConfig()
	triageSvc := triage.NewService(&defaultFilters, defaultSorting)

	// Resolve AvailNZB API key: explicit .env key takes priority, then stored key, then auto-register.
	var availKeyStore availnzb.KeyStore
	if opts.DataStore != nil {
		availKeyStore = opts.DataStore
	}
	availAPIKey, err := availnzb.ResolveAPIKey(availKeyStore, cfg.AvailNZBURL, cfg.AvailNZBAPIKey, availnzb.DefaultAppName)
	if err != nil {
		logger.Warn("AvailNZB key resolution failed", "err", err)
	}
	availClient := availnzb.NewClient(cfg.AvailNZBURL, availAPIKey)
	go func(client *availnzb.Client) {
		if err := client.RefreshBackbones(); err != nil {
			logger.Debug("AvailNZB backbones refresh", "source", "app_build", "err", err)
		}
	}(availClient)
	tmdbClient := tmdb.NewClient(cfg.TMDBAPIKey)

	var tvdbStore tvdb.TokenStore
	if opts.DataStore != nil {
		tvdbStore = opts.DataStore
	}
	tvdbClient := tvdb.NewClient(cfg.TVDBAPIKey, tvdbStore)

	return &Components{
		Config:               base.Config,
		Indexer:              base.Indexer,
		ProviderPools:        base.ProviderPools,
		ProviderOrder:        base.ProviderOrder,
		StreamingPools:       base.StreamingPools,
		UsenetPool:           base.UsenetPool,
		AvailNZBIndexerHosts: base.AvailNZBIndexerHosts,
		Validator:            validator,
		Triage:               triageSvc,
		AvailClient:          availClient,
		TMDBClient:           tmdbClient,
		TVDBClient:           tvdbClient,
		SegmentCacheBudget:   base.SegmentCacheBudget,
	}, nil
}

func (a *App) Components() *Components {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.components
}
