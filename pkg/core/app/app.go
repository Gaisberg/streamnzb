package app

import (
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/metacache"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
	"streamnzb/pkg/usenet/validation"
)

type BuildOpts struct {
	AvailNZBURL        string
	AvailNZBAPIKey     string
	TMDBAPIKey         string
	TVDBAPIKey         string
	FallbackTMDBAPIKey string
	FallbackTVDBAPIKey string
	DataDir            string
	SessionTTL         time.Duration
}

type Components struct {
	Config               *config.Config
	Indexer              indexer.Indexer
	QueryCache           *indexer.QueryCache
	ProviderPools        map[string]*nntp.ClientPool
	ProviderOrder        []string
	StreamingPools       []*nntp.ClientPool
	UsenetPool           *pool.Pool
	AvailNZBIndexerHosts map[string]string
	IndexerCaps          map[string]*indexer.Caps
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

func resolveDataDir(override, loadedPath string) string {
	dataDir := override
	if dataDir == "" {
		dataDir = filepath.Dir(loadedPath)
	}
	if dataDir == "" || dataDir == "." {
		dataDir, _ = filepath.Abs(".")
	}
	return dataDir
}

func New() *App {
	return &App{}
}

func (a *App) effectiveTMDBKey() string {
	if k := strings.TrimSpace(a.opts.TMDBAPIKey); k != "" {
		return k
	}
	return strings.TrimSpace(a.opts.FallbackTMDBAPIKey)
}

func (a *App) effectiveTVDBKey() string {
	if k := strings.TrimSpace(a.opts.TVDBAPIKey); k != "" {
		return k
	}
	return strings.TrimSpace(a.opts.FallbackTVDBAPIKey)
}

func (a *App) EffectiveTMDBKey() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.effectiveTMDBKey()
}

func (a *App) EffectiveTVDBKey() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.effectiveTVDBKey()
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

func (a *App) SetAvailNZBAPIKey(apiKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	apiKey = strings.TrimSpace(apiKey)
	a.opts.AvailNZBAPIKey = apiKey
	if a.components != nil && a.components.AvailClient != nil {
		a.components.AvailClient.SetAPIKey(apiKey)
	}
}

const validationSampleSize = 5

func (a *App) buildFull(cfg *config.Config, opts BuildOpts) (*Components, error) {
	env.SetRuntimeHeaders(cfg.IndexerQueryHeader, cfg.IndexerGrabHeader, cfg.ProviderHeader)
	base, err := initialization.BuildComponents(cfg)
	if err != nil {
		return nil, err
	}

	validator := validation.NewChecker(base.UsenetPool, validationSampleSize, 6)
	triageSvc := triage.NewService()
	availClient := availnzb.NewClient(opts.AvailNZBURL, opts.AvailNZBAPIKey)
	go func(client *availnzb.Client) {
		if err := client.RefreshBackbones(); err != nil {
			logger.Debug("AvailNZB backbones refresh", "source", "app_build", "err", err)
		}
	}(availClient)
	dataDir := resolveDataDir(opts.DataDir, cfg.LoadedPath)
	// Display language is a per-call parameter on the shared clients, derived
	// from each stream's metadata profile — never client state.
	tmdbClient := tmdb.NewClientWithCache(a.effectiveTMDBKey(), metadataResponseCache(dataDir, "tmdb"))
	tvdbClient := tvdb.NewClientWithCache(a.effectiveTVDBKey(), dataDir, metadataResponseCache(dataDir, "tvdb"))

	return &Components{
		Config:               base.Config,
		Indexer:              base.Indexer,
		QueryCache:           base.QueryCache,
		ProviderPools:        base.ProviderPools,
		ProviderOrder:        base.ProviderOrder,
		StreamingPools:       base.StreamingPools,
		UsenetPool:           base.UsenetPool,
		AvailNZBIndexerHosts: base.AvailNZBIndexerHosts,
		IndexerCaps:          base.IndexerCaps,
		Validator:            validator,
		Triage:               triageSvc,
		AvailClient:          availClient,
		TMDBClient:           tmdbClient,
		TVDBClient:           tvdbClient,
		SegmentCacheBudget:   base.SegmentCacheBudget,
	}, nil
}

// ReloadScope describes which runtime subsystems a config change invalidates.
// Each flag is independent so a reload rebuilds only what actually changed.
type ReloadScope struct {
	Indexers  bool
	Providers bool
	Proxy     bool
	Database  bool
	AddonPort bool
}

func (s ReloadScope) Any() bool {
	return s.Indexers || s.Providers || s.Proxy || s.Database || s.AddonPort
}

// ReloadScopeFull marks every rebuildable subsystem changed — used when there
// is no prior config to diff against. Database and AddonPort stay out of it on
// purpose: reopening the database and rebinding the listener are disruptive
// rather than merely expensive, so they only ever run off an observed change.
func ReloadScopeFull() ReloadScope {
	return ReloadScope{Indexers: true, Providers: true, Proxy: true}
}

func ConfigChanged(old, new_ *config.Config) ReloadScope {
	if old == nil || new_ == nil {
		return ReloadScopeFull()
	}

	return ReloadScope{
		Indexers:  !reflect.DeepEqual(old.Indexers, new_.Indexers),
		Providers: !reflect.DeepEqual(old.Providers, new_.Providers),
		Proxy: old.ProxyHost != new_.ProxyHost ||
			old.ProxyPort != new_.ProxyPort ||
			old.ProxyEnabled != new_.ProxyEnabled ||
			old.ProxyAuthUser != new_.ProxyAuthUser ||
			old.ProxyAuthPass != new_.ProxyAuthPass,
		Database: old.DatabaseDriver != new_.DatabaseDriver ||
			old.DatabaseURL != new_.DatabaseURL,
		AddonPort: old.AddonPort != new_.AddonPort,
	}
}

// metadataResponseCache builds the persistent L2-backed response cache for one
// metadata source. The persistence manager is a process singleton already
// opened by BuildComponents; if it is somehow unavailable the cache degrades to
// in-memory-only via a nil store.
func metadataResponseCache(dataDir, source string) *metacache.Cache {
	sm, err := persistence.GetManager(dataDir)
	if err != nil {
		return metacache.New(nil, source)
	}
	return metacache.New(sm.MetadataCacheStore(), source)
}

// refreshLightComponents applies the cheap per-reload swaps shared by every
// non-full reload path: config pointer, triage state, and metadata clients.
// Rebuilt clients only lose their in-memory L1 — the persistent response cache
// carries across reloads (and restarts).
func (a *App) refreshLightComponents(comp *Components, newCfg *config.Config) {
	comp.Config = newCfg
	comp.Triage = triage.NewService()
	dataDir := resolveDataDir(a.opts.DataDir, newCfg.LoadedPath)
	comp.TMDBClient = tmdb.NewClientWithCache(a.effectiveTMDBKey(), metadataResponseCache(dataDir, "tmdb"))
	comp.TVDBClient = tvdb.NewClientWithCache(a.effectiveTVDBKey(), dataDir, metadataResponseCache(dataDir, "tvdb"))
}

func (a *App) Reload(newCfg *config.Config) (*Components, ReloadScope, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.opts.TMDBAPIKey = strings.TrimSpace(newCfg.TMDBAPIKey)
	a.opts.TVDBAPIKey = strings.TrimSpace(newCfg.TVDBAPIKey)
	env.SetRuntimeHeaders(newCfg.IndexerQueryHeader, newCfg.IndexerGrabHeader, newCfg.ProviderHeader)

	old := a.components
	scope := ConfigChanged(old.Config, newCfg)

	if scope.Indexers && scope.Providers {
		logger.Info("Reload: full rebuild (indexers and providers changed)")
		comp, err := a.buildFull(newCfg, a.opts)
		if err != nil {
			return nil, scope, err
		}
		a.components = comp
		return comp, scope, nil
	}

	comp := *old
	a.refreshLightComponents(&comp, newCfg)

	if scope.Indexers {
		logger.Info("Reload: rebuilding indexers - NNTP pools untouched")
		stack := initialization.BuildIndexerStack(newCfg)
		comp.Indexer = stack.Indexer
		comp.QueryCache = stack.QueryCache
		comp.IndexerCaps = stack.IndexerCaps
		comp.AvailNZBIndexerHosts = stack.AvailNZBIndexerHosts
	}
	if scope.Providers {
		logger.Info("Reload: rebuilding changed provider pools")
		pools := initialization.BuildProviderPools(newCfg, old.Config, old.ProviderPools)
		comp.ProviderPools = pools.Pools
		comp.ProviderOrder = pools.Order
		comp.StreamingPools = pools.StreamingPools
		comp.UsenetPool = pools.UsenetPool
		comp.SegmentCacheBudget = pools.SegmentCacheBudget
		comp.Validator = validation.NewChecker(pools.UsenetPool, validationSampleSize, 6)
	}
	if scope.Proxy {
		logger.Info("Reload: proxy config changed - restarting proxy listener only")
	}
	if !scope.Any() {
		logger.Info("Reload: config-only - no NNTP/indexer restart")
	}

	a.components = &comp
	return &comp, scope, nil
}

func (a *App) Components() *Components {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.components
}
