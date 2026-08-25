package initialization

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/health"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/metrics"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/easynews"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
	"strings"
	"sync"
	"time"
)

type InitializedComponents struct {
	Config               *config.Config
	Indexer              indexer.Indexer
	QueryCache           *indexer.QueryCache
	ProviderPools        map[string]*nntp.ClientPool
	ProviderOrder        []string
	StreamingPools       []*nntp.ClientPool
	UsenetPool           *pool.Pool
	SegmentCacheBudget   *pool.SegmentCacheBudget
	AvailNZBIndexerHosts map[string]string
	IndexerCaps          map[string]*indexer.Caps
}

func WaitForInputAndExit(err error) {
	logger.Error("CRITICAL ERROR", "err", err)
	fmt.Println("\nPress Enter to exit...")
	var input string
	fmt.Scanln(&input)
	os.Exit(1)
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

	dataDir := paths.GetDataDir()
	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		logger.Error("Failed to initialize state manager", "err", err)
	}
	if stateMgr != nil && cfg.ResetLegacyStreamState {
		if err := stateMgr.Delete("devices"); err != nil {
			logger.Warn("Failed to clear legacy devices state during stream-model upgrade", "err", err)
		}
		if err := stateMgr.Delete("users"); err != nil {
			logger.Warn("Failed to clear legacy users state during stream-model upgrade", "err", err)
		}
		logger.Info("Cleared legacy persisted device state for stream-model upgrade")
	}
	if stateMgr != nil && cfg.NZBHistoryRetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -cfg.NZBHistoryRetentionDays)
		deleted, err := stateMgr.DeleteAttemptsBefore(cutoff)
		if err != nil {
			logger.Warn("Failed to prune NZB attempt history", "retention_days", cfg.NZBHistoryRetentionDays, "err", err)
		} else if deleted > 0 {
			logger.Info("Pruned NZB attempt history", "retention_days", cfg.NZBHistoryRetentionDays, "deleted", deleted)
		}
	}

	if stateMgr != nil {
		// Restored before any pool dials or any search runs, so a verdict
		// recorded before the last restart is honored instead of rediscovered
		// by hammering a provider that already said no.
		if _, err := health.Init(stateMgr); err != nil {
			logger.Warn("Failed to restore component health", "err", err)
		}
		HydrateMetricsFromState(stateMgr)
		stateRef := stateMgr
		metrics.Default().SetOnStreamAPISample(func(s metrics.StreamAPISample) {
			_ = stateRef.RecordStreamAPISample(persistence.StreamAPISampleRecord{
				Timestamp:          s.Timestamp,
				ContentType:        s.ContentType,
				ID:                 s.ID,
				TotalDurationMS:    s.TotalDuration.Milliseconds(),
				MetadataDurationMS: s.MetadataDuration.Milliseconds(),
				SearchDurationMS:   s.SearchDuration.Milliseconds(),
				RankingDurationMS:  s.RankingDuration.Milliseconds(),
				AvailNZBDurationMS: s.AvailNZBDuration.Milliseconds(),
				CandidateCount:     s.CandidateCount,
				ResultCount:        s.ResultCount,
			})
		})
		metrics.Default().SetOnPlaybackTTFFSample(func(s metrics.PlaybackTTFFSample) {
			_ = stateRef.RecordPlaybackTTFFSample(persistence.PlaybackTTFFSampleRecord{
				Timestamp:             s.Timestamp,
				SessionID:             s.SessionID,
				ProviderName:          s.ProviderName,
				TTFFMS:                s.TTFF.Milliseconds(),
				SessionResolutionMS:   s.SessionResolution.Milliseconds(),
				NZBFetchDurationMS:    s.NZBFetchDuration.Milliseconds(),
				NNTPConnectDurationMS: s.NNTPConnectDuration.Milliseconds(),
				ProbeDurationMS:       s.ProbeDuration.Milliseconds(),
				FirstByteDurationMS:   s.FirstByteDuration.Milliseconds(),
				IsCacheHit:            s.IsCacheHit,
			})
		})
	}

	stack := buildIndexerStack(cfg, stateMgr)
	pools := buildProviderPools(cfg, stateMgr, nil, nil)

	return &InitializedComponents{
		Config:               cfg,
		Indexer:              stack.Indexer,
		QueryCache:           stack.QueryCache,
		ProviderPools:        pools.Pools,
		ProviderOrder:        pools.Order,
		StreamingPools:       pools.StreamingPools,
		UsenetPool:           pools.UsenetPool,
		SegmentCacheBudget:   pools.SegmentCacheBudget,
		AvailNZBIndexerHosts: stack.AvailNZBIndexerHosts,
		IndexerCaps:          stack.IndexerCaps,
	}, nil
}

// IndexerStack is the searchable indexer surface built from config: the
// aggregator, its shared query cache, fetched caps, and AvailNZB host mapping.
type IndexerStack struct {
	Indexer              indexer.Indexer
	QueryCache           *indexer.QueryCache
	IndexerCaps          map[string]*indexer.Caps
	AvailNZBIndexerHosts map[string]string
}

// BuildIndexerStack rebuilds only the indexer components, leaving NNTP pools
// untouched. Used for reloads where just the indexer configuration changed.
func BuildIndexerStack(cfg *config.Config) *IndexerStack {
	stateMgr, err := persistence.GetManager(paths.GetDataDir())
	if err != nil {
		logger.Error("Failed to initialize state manager", "err", err)
	}
	return buildIndexerStack(cfg, stateMgr)
}

func buildIndexerStack(cfg *config.Config, stateMgr *persistence.StateManager) *IndexerStack {
	var indexers []indexer.Indexer
	availNzbHosts := make(map[string]string)

	usageMgr, err := indexer.GetUsageManager(stateMgr)
	if err != nil {
		logger.Error("Failed to initialize usage manager", "err", err)
	}

	queryCache := indexer.NewQueryCache()

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
		effectiveProxyURL := strings.TrimSpace(idxCfg.ProxyURL)
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
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

			easynewsClient, err := easynews.NewClient(idxCfg.Username, idxCfg.Password, idxCfg.Name, downloadBase, idxCfg.APIHitsDay, idxCfg.DownloadsDay, idxCfg.RateLimitRPS, idxCfg.EffectiveTimeoutSeconds(), effectiveProxyURL, idxCfg.QueryHeader, idxCfg.GrabHeader, usageMgr)
			if err != nil {
				logger.Error("Failed to initialize Easynews from indexer list", "name", idxCfg.Name, "err", err)
			} else {
				cachedClient := indexer.NewCachedIndexer(easynewsClient, queryCache, 10*time.Minute)
				indexers = append(indexers, cachedClient)
				logger.Info("Initialized Easynews indexer", "name", idxCfg.Name)
			}
			if h := "members.easynews.com"; h != "" {
				availNzbHosts[idxCfg.Name] = h
			}
		default:
			effectiveCfg := idxCfg
			effectiveCfg.ProxyURL = effectiveProxyURL
			client := newznab.NewClient(effectiveCfg, usageMgr)
			cachedClient := indexer.NewCachedIndexer(client, queryCache, 10*time.Minute)
			indexers = append(indexers, cachedClient)
			logger.Info("Initialized Newznab indexer", "name", idxCfg.Name, "url", idxCfg.URL)
			if h := hostFromIndexerURL(idxCfg.URL); h != "" {
				if !isAggregator {
					availNzbHosts[idxCfg.Name] = h
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
				// A caps failure is worth reporting — the fetch runs right
				// after a key is saved, so a rejection lands immediately. A
				// caps SUCCESS proves nothing and must not clear a verdict:
				// many indexers serve caps publicly, so the request succeeds
				// with any key at all. Only a real authenticated request
				// (search or ping) may mark the indexer healthy.
				if err != nil {
					indexer.ReportHealth(name, err)
				}
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

	return &IndexerStack{
		Indexer:              aggregator,
		QueryCache:           queryCache,
		IndexerCaps:          indexerCaps,
		AvailNZBIndexerHosts: availNzbHosts,
	}
}

// ProviderPoolSet is the NNTP side of the runtime built from config: one
// client pool per enabled provider plus the priority-ordered usenet pool
// wrapping them.
type ProviderPoolSet struct {
	Pools              map[string]*nntp.ClientPool
	Order              []string
	StreamingPools     []*nntp.ClientPool
	UsenetPool         *pool.Pool
	SegmentCacheBudget *pool.SegmentCacheBudget
}

// BuildProviderPools rebuilds provider pools, reusing pools from prevPools
// whose connection settings are unchanged between prevCfg and cfg so their
// established NNTP connections survive the reload. Pools left out of the
// result are NOT shut down here — the caller owns teardown of dropped pools.
func BuildProviderPools(cfg, prevCfg *config.Config, prevPools map[string]*nntp.ClientPool) *ProviderPoolSet {
	stateMgr, err := persistence.GetManager(paths.GetDataDir())
	if err != nil {
		logger.Error("Failed to initialize state manager", "err", err)
	}
	return buildProviderPools(cfg, stateMgr, prevCfg, prevPools)
}

// providerConnEqual reports whether two provider entries dial the same
// upstream identically. Priority and Enabled are excluded: they affect
// ordering and membership, not the established connections.
// countBackups reports how many of the built provider configs are held back for
// failover.
func countBackups(providers []pool.ProviderConfig) int {
	n := 0
	for i := range providers {
		if providers[i].IsBackup {
			n++
		}
	}
	return n
}

func providerConnEqual(a, b config.Provider) bool {
	return a.Host == b.Host &&
		a.Port == b.Port &&
		a.UseSSL == b.UseSSL &&
		a.Username == b.Username &&
		a.Password == b.Password &&
		a.Connections == b.Connections
}

func buildProviderPools(cfg *config.Config, stateMgr *persistence.StateManager, prevCfg *config.Config, prevPools map[string]*nntp.ClientPool) *ProviderPoolSet {
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

	prevByName := make(map[string]config.Provider)
	if prevCfg != nil {
		for _, p := range prevCfg.Providers {
			name := p.Name
			if name == "" {
				name = p.Host
			}
			prevByName[name] = p
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
	// Pipeline depth follows the round-trip time to one particular server, so it
	// is carried per provider rather than folded into the shared pool default.
	providerDepths := make(map[string]int, len(providers))
	// Backup providers are held back for failover, so the pool needs to know
	// which ones they are before it hands out a single connection.
	providerBackups := make(map[string]bool, len(providers))
	for _, provider := range providers {
		poolName := provider.Name
		if poolName == "" {
			poolName = provider.Host
		}
		if provider.PipelineDepth != nil {
			providerDepths[poolName] = *provider.PipelineDepth
		}
		if provider.Backup != nil && *provider.Backup {
			providerBackups[poolName] = true
		}

		if prev, ok := prevByName[poolName]; ok && providerConnEqual(prev, provider) {
			if reused := prevPools[poolName]; reused != nil {
				logger.Info("Reusing NNTP pool", "provider", poolName, "host", provider.Host)
				providerPools[poolName] = reused
				providerOrder = append(providerOrder, poolName)
				streamingPools = append(streamingPools, reused)
				continue
			}
		}

		logger.Info("Initializing NNTP pool", "provider", provider.Name, "host", provider.Host, "conns", provider.Connections)

		pool := nntp.NewClientPool(
			provider.Host,
			provider.Port,
			provider.UseSSL,
			provider.Username,
			provider.Password,
			provider.Connections,
		)

		pool.SetProviderName(poolName)

		if err := pool.Validate(); err != nil {
			logger.Error("Failed to initialize provider", "name", provider.Name, "host", provider.Host, "err", err)
			continue
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
				ID:            name,
				Priority:      i,
				IsBackup:      providerBackups[name],
				ClientPool:    cp,
				PipelineDepth: providerDepths[name],
			})
		}
		if backups := countBackups(providerConfigs); backups > 0 && backups == len(providerConfigs) {
			// The pool drops the flag in this case; say so once here rather
			// than leaving the operator to wonder why nothing is held back.
			logger.Warn("Every enabled provider is marked backup — treating them all as primaries", "providers", backups)
		}
		if len(providerConfigs) > 0 {
			var err error
			usenetPool, err = pool.NewPool(&pool.Config{
				Providers:     providerConfigs,
				SegmentCache:  pool.NewMemorySegmentCacheWithBudget(segmentCacheBudget),
				PipelineDepth: env.NNTPPipelineDepth(),
			})
			if err != nil {
				logger.Error("Failed to build usenet pool", "err", err)
			} else {
				logger.Info("Usenet pool initialized", "providers", len(providerConfigs))
			}
		}
	}

	return &ProviderPoolSet{
		Pools:              providerPools,
		Order:              providerOrder,
		StreamingPools:     streamingPools,
		UsenetPool:         usenetPool,
		SegmentCacheBudget: segmentCacheBudget,
	}
}

// HydrateMetricsFromState fills the in-memory metrics ring from persisted
// samples. Called at startup, and again after a database swap — the ring would
// otherwise keep showing samples read from the database that was left behind.
func HydrateMetricsFromState(stateMgr *persistence.StateManager) {
	if stateMgr == nil {
		return
	}
	streamRecords, err := stateMgr.GetRecentStreamAPISamples(1000)
	if err != nil {
		logger.Warn("Failed to load recent stream API samples", "err", err)
	}
	ttffRecords, err := stateMgr.GetRecentPlaybackTTFFSamples(1000)
	if err != nil {
		logger.Warn("Failed to load recent TTFF samples", "err", err)
	}

	streamSamples := make([]metrics.StreamAPISample, 0, len(streamRecords))
	for _, r := range streamRecords {
		streamSamples = append(streamSamples, metrics.StreamAPISample{
			Timestamp:        r.Timestamp,
			ContentType:      r.ContentType,
			ID:               r.ID,
			TotalDuration:    time.Duration(r.TotalDurationMS) * time.Millisecond,
			MetadataDuration: time.Duration(r.MetadataDurationMS) * time.Millisecond,
			SearchDuration:   time.Duration(r.SearchDurationMS) * time.Millisecond,
			RankingDuration:  time.Duration(r.RankingDurationMS) * time.Millisecond,
			AvailNZBDuration: time.Duration(r.AvailNZBDurationMS) * time.Millisecond,
			CandidateCount:   r.CandidateCount,
			ResultCount:      r.ResultCount,
		})
	}
	ttffSamples := make([]metrics.PlaybackTTFFSample, 0, len(ttffRecords))
	for _, r := range ttffRecords {
		ttffSamples = append(ttffSamples, metrics.PlaybackTTFFSample{
			Timestamp:           r.Timestamp,
			SessionID:           r.SessionID,
			ProviderName:        r.ProviderName,
			TTFF:                time.Duration(r.TTFFMS) * time.Millisecond,
			SessionResolution:   time.Duration(r.SessionResolutionMS) * time.Millisecond,
			NZBFetchDuration:    time.Duration(r.NZBFetchDurationMS) * time.Millisecond,
			NNTPConnectDuration: time.Duration(r.NNTPConnectDurationMS) * time.Millisecond,
			ProbeDuration:       time.Duration(r.ProbeDurationMS) * time.Millisecond,
			FirstByteDuration:   time.Duration(r.FirstByteDurationMS) * time.Millisecond,
			IsCacheHit:          r.IsCacheHit,
		})
	}
	metrics.Default().Hydrate(streamSamples, ttffSamples)
}
