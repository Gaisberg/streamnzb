package initialization

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/easynews"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
	"strings"
	"sync"
)

type InitializedComponents struct {
	Config               *config.Config
	Indexer              indexer.Indexer
	ProviderPools        map[string]*nntp.ClientPool
	ProviderOrder        []string
	StreamingPools       []*nntp.ClientPool
	UsenetPool           *pool.Pool
	SegmentCacheBudget   *pool.SegmentCacheBudget
	AvailNZBIndexerHosts []string
	IndexerCaps          map[string]*indexer.Caps
}

func WaitForInputAndExit(err error) {
	logger.Error("CRITICAL ERROR", "err", err)
	fmt.Println("\nPress Enter to exit...")
	var input string
	fmt.Scanln(&input)
	os.Exit(1)
}

func Bootstrap() (*InitializedComponents, error) {

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}

	return BuildComponents(cfg)
}

func hostFromIndexerURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return strings.TrimPrefix(h, "api.")
}

func BuildComponents(cfg *config.Config) (*InitializedComponents, error) {

	var indexers []indexer.Indexer
	var availNzbHosts []string
	seenHost := make(map[string]bool)

	dataDir := paths.GetDataDir()
	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		logger.Error("Failed to initialize state manager", "err", err)
	}
	if stateMgr != nil && cfg.ResetLegacyDeviceState {
		if err := stateMgr.Delete("devices"); err != nil {
			logger.Warn("Failed to clear legacy devices state during stream-model upgrade", "err", err)
		}
		if err := stateMgr.Delete("users"); err != nil {
			logger.Warn("Failed to clear legacy users state during stream-model upgrade", "err", err)
		}
		logger.Info("Cleared legacy persisted device state for stream-model upgrade")
	}

	usageMgr, err := indexer.GetUsageManager(stateMgr)
	if err != nil {
		logger.Error("Failed to initialize usage manager", "err", err)
	}

	for _, idxCfg := range cfg.Indexers {
		if idxCfg.URL == "" && !strings.EqualFold(idxCfg.Type, "easynews") {
			continue
		}
		if idxCfg.Enabled != nil && !*idxCfg.Enabled {
			continue
		}

		indexerType := idxCfg.Type
		if indexerType == "" {
			indexerType = "newznab"
		}

		isAggregator := config.IsAggregatorIndexerType(indexerType)
		if indexerType == "aggregator" {
			indexerType = "newznab"
		}

		switch indexerType {
		case "easynews":

			downloadBase := cfg.AddonBaseURL
			if downloadBase == "" {
				downloadBase = "http://127.0.0.1:7000"
			}

			if len(downloadBase) > 0 && downloadBase[len(downloadBase)-1] == '/' {
				downloadBase = downloadBase[:len(downloadBase)-1]
			}

			easynewsClient, err := easynews.NewClient(idxCfg.Username, idxCfg.Password, idxCfg.Name, downloadBase, idxCfg.APIHitsDay, idxCfg.DownloadsDay, usageMgr)
			if err != nil {
				logger.Error("Failed to initialize Easynews from indexer list", "name", idxCfg.Name, "err", err)
			} else {
				indexers = append(indexers, easynewsClient)
				logger.Info("Initialized Easynews indexer", "name", idxCfg.Name)
			}
			if h := "members.easynews.com"; !seenHost[h] {
				seenHost[h] = true
				availNzbHosts = append(availNzbHosts, h)
			}
		default:
			client := newznab.NewClient(idxCfg, usageMgr)
			indexers = append(indexers, client)
			logger.Info("Initialized Newznab indexer", "name", idxCfg.Name, "url", idxCfg.URL)
			if h := hostFromIndexerURL(idxCfg.URL); h != "" && !seenHost[h] {
				seenHost[h] = true
				if !isAggregator {
					availNzbHosts = append(availNzbHosts, h)
				}
			}
		}
	}

	if len(indexers) == 0 {
		logger.Warn("!! No indexers configured. Add some via the web UI or config.json !!")
	}

	aggregator := indexer.NewAggregator(indexers...)

	indexerCaps := make(map[string]*indexer.Caps)
	var capsMu sync.Mutex
	var capsWg sync.WaitGroup
	for _, idx := range indexers {
		if c, ok := idx.(indexer.IndexerWithCaps); ok {
			capsWg.Add(1)
			go func(name string, capsFetcher indexer.IndexerWithCaps) {
				defer capsWg.Done()
				caps, err := capsFetcher.GetCaps()
				if err != nil {
					logger.Warn("Failed to fetch caps", "indexer", name, "err", err)
					return
				}
				capsMu.Lock()
				indexerCaps[name] = caps
				capsMu.Unlock()
			}(idx.Name(), c)
		}
	}
	capsWg.Wait()
	if len(indexerCaps) > 0 {
		logger.Info("Fetched indexer capabilities", "count", len(indexerCaps))
	}

	providerPools := make(map[string]*nntp.ClientPool)
	var streamingPools []*nntp.ClientPool

	var providerUsageMgr *nntp.ProviderUsageManager
	if stateMgr != nil {
		if mgr, err := nntp.GetProviderUsageManager(stateMgr); err != nil {
			logger.Error("Failed to initialize provider usage manager", "err", err)
		} else {
			providerUsageMgr = mgr
		}
	}

	providers := make([]config.Provider, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {

		if p.Enabled != nil && *p.Enabled {
			providers = append(providers, p)
		}
	}

	sort.Slice(providers, func(i, j int) bool {
		priI := 999
		priJ := 999
		if providers[i].Priority != nil {
			priI = *providers[i].Priority
		}
		if providers[j].Priority != nil {
			priJ = *providers[j].Priority
		}
		return priI < priJ
	})

	providerOrder := make([]string, 0, len(providers))
	for _, provider := range providers {
		logger.Info("Initializing NNTP pool", "provider", provider.Name, "host", provider.Host, "conns", provider.Connections)

		pool := nntp.NewClientPool(
			provider.Host,
			provider.Port,
			provider.UseSSL,
			provider.Username,
			provider.Password,
			provider.Connections,
		)

		if err := pool.Validate(); err != nil {
			logger.Error("Failed to initialize provider", "name", provider.Name, "host", provider.Host, "err", err)
			continue
		}

		poolName := provider.Name
		if poolName == "" {
			poolName = provider.Host
		}

		if providerUsageMgr != nil {
			if usage := providerUsageMgr.GetUsage(poolName); usage != nil {
				pool.RestoreTotalBytes(usage.TotalBytes)
			}
			pool.SetUsageManager(poolName, providerUsageMgr)
		}

		providerPools[poolName] = pool
		providerOrder = append(providerOrder, poolName)
		streamingPools = append(streamingPools, pool)
	}

	if len(providerPools) == 0 {
		logger.Warn("!! No valid NNTP providers initialized. Check your credentials in the web UI !!")
	}

	var usenetPool *pool.Pool
	var segmentCacheBudget *pool.SegmentCacheBudget
	// Reserve headroom for non-cache memory (session + 100+ loader Files, NZB, RAR blueprint, runtime, stacks).
	// Otherwise segment cache uses 80% of limit and the remaining 20% is too small, so we exceed the limit.
	const reservedMB = 150
	if cfg.MemoryLimitMB > reservedMB {
		segmentCacheMB := cfg.MemoryLimitMB - reservedMB
		segmentCacheBudget = pool.NewSegmentCacheBudget(segmentCacheMB)
		logger.Info("Segment cache set (memory limit minus reserved)", "segment_cache_mb", segmentCacheMB, "memory_limit_mb", cfg.MemoryLimitMB, "reserved_mb", reservedMB)
	}

	if len(providerOrder) > 0 {
		providerConfigs := make([]pool.ProviderConfig, 0, len(providerOrder))
		for i, name := range providerOrder {
			cp := providerPools[name]
			if cp == nil {
				continue
			}
			providerConfigs = append(providerConfigs, pool.ProviderConfig{
				ID:         name,
				Priority:   i,
				IsBackup:   false,
				ClientPool: cp,
			})
		}
		if len(providerConfigs) > 0 {
			var err error
			usenetPool, err = pool.NewPool(&pool.Config{
				Providers:    providerConfigs,
				SegmentCache: pool.NewMemorySegmentCacheWithBudget(segmentCacheBudget),
			})
			if err != nil {
				logger.Error("Failed to build usenet pool", "err", err)
			} else {
				logger.Info("Usenet pool initialized", "providers", len(providerConfigs))
			}
		}
	}

	return &InitializedComponents{
		Config:               cfg,
		Indexer:              aggregator,
		ProviderPools:        providerPools,
		ProviderOrder:        providerOrder,
		StreamingPools:       streamingPools,
		UsenetPool:           usenetPool,
		SegmentCacheBudget:   segmentCacheBudget,
		AvailNZBIndexerHosts: availNzbHosts,
		IndexerCaps:          indexerCaps,
	}, nil
}
