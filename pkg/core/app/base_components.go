package app

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/easynews"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
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
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	var indexers []indexer.Indexer
	var availNzbHosts []string
	seenHost := make(map[string]bool)

	for _, idxCfg := range cfg.Indexers {
		if idxCfg.URL == "" {
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
			if strings.HasSuffix(downloadBase, "/") {
				downloadBase = downloadBase[:len(downloadBase)-1]
			}

			easynewsClient, err := easynews.NewClient(idxCfg.Username, idxCfg.Password, idxCfg.Name, downloadBase, idxCfg.APIHitsDay, idxCfg.DownloadsDay)
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
			client := newznab.NewClient(idxCfg)
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
		logger.Warn("!! No indexers configured. Add INDEXER_1_URL etc. to your .env file !!")
	}

	aggregator := indexer.NewAggregator(indexers...)

	providerPools := make(map[string]*nntp.ClientPool)
	var streamingPools []*nntp.ClientPool

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

		clientPool := nntp.NewClientPool(
			provider.Host,
			provider.Port,
			provider.UseSSL,
			provider.Username,
			provider.Password,
			provider.Connections,
		)

		if err := clientPool.Validate(); err != nil {
			logger.Error("Failed to initialize provider", "name", provider.Name, "host", provider.Host, "err", err)
			continue
		}

		poolName := provider.Name
		if poolName == "" {
			poolName = provider.Host
		}

		providerPools[poolName] = clientPool
		providerOrder = append(providerOrder, poolName)
		streamingPools = append(streamingPools, clientPool)
	}

	if len(providerPools) == 0 {
		logger.Warn("!! No valid NNTP providers initialized. Check your credentials in the web UI !!")
	}

	var usenetPool *pool.Pool
	var segmentCacheBudget *pool.SegmentCacheBudget
	const reservedMB = 150
	if cfg.MemoryLimitMB > reservedMB {
		segmentCacheMB := cfg.MemoryLimitMB - reservedMB
		segmentCacheBudget = pool.NewSegmentCacheBudget(segmentCacheMB)
		logger.Info("Segment cache set (memory limit minus reserved)", "segment_cache_mb", segmentCacheMB, "memory_limit_mb", cfg.MemoryLimitMB, "reserved_mb", reservedMB)
	}

	if len(providerOrder) > 0 {
		providerConfigs := make([]pool.ProviderConfig, 0, len(providerOrder))
		for i, name := range providerOrder {
			clientPool := providerPools[name]
			if clientPool == nil {
				continue
			}
			providerConfigs = append(providerConfigs, pool.ProviderConfig{
				ID:         name,
				Priority:   i,
				IsBackup:   false,
				ClientPool: clientPool,
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
	}, nil
}