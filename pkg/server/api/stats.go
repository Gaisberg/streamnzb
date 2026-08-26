package api

import (
	"fmt"
	"sort"
	"time"

	"streamnzb/pkg/core/httpx"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp"
	usenetpool "streamnzb/pkg/usenet/pool"
)

type SystemStats struct {
	Timestamp         time.Time                   `json:"timestamp"`
	TotalSpeed        float64                     `json:"total_speed_mbps"`
	ActiveStreams     int                         `json:"active_streams"`
	TotalConnections  int                         `json:"total_connections"`
	ActiveConnections int                         `json:"active_connections"`
	TotalDownloadedMB float64                     `json:"total_downloaded_mb"`
	Providers         []ProviderStats             `json:"providers"`
	Indexers          []IndexerStats              `json:"indexers"`
	ActiveSessions    []session.ActiveSessionInfo `json:"active_sessions"`
	// StreamSpeeds is each stream's share of TotalSpeed, keyed by stream name and
	// metered the same way, so the dashboard can plot both on one axis. What is
	// missing from the sum is pool work no stream asked for: availability checks,
	// NZB downloads, probes and speed tests.
	StreamSpeeds map[string]float64 `json:"stream_speeds_mbps"`
}

type IndexerStats struct {
	Name                 string  `json:"name"`
	APIHitsLimit         int     `json:"api_hits_limit"`
	APIHitsUsed          int     `json:"api_hits_used"`
	APIHitsRemaining     int     `json:"api_hits_remaining"`
	AllTimeAPIHitsUsed   int     `json:"api_hits_used_all_time"`
	DownloadsLimit       int     `json:"downloads_limit"`
	DownloadsUsed        int     `json:"downloads_used"`
	DownloadsRemaining   int     `json:"downloads_remaining"`
	AllTimeDownloadsUsed int     `json:"downloads_used_all_time"`
	SearchesCount        int     `json:"searches_count"`
	UniqueHitsCount      int64   `json:"unique_hits_count"`
	AvgResponseMS        float64 `json:"avg_response_ms"`
	AvailAvailableCount  int64   `json:"avail_available_count"`
	AvailDiscardedCount  int64   `json:"avail_discarded_count"`
}

type ProviderStats struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	ActiveConns int    `json:"active_conns"`
	IdleConns   int    `json:"idle_conns"`
	// PendingConns is dials in flight: a slot is claimed but no connection is
	// established yet. Kept separate from ActiveConns so the dashboard reports
	// only connections the provider actually sees.
	PendingConns          int     `json:"pending_conns"`
	MaxConns              int     `json:"max_conns"`
	CurrentSpeed          float64 `json:"current_speed_mbps"`
	DownloadedMB          float64 `json:"downloaded_mb"`
	UsagePercent          float64 `json:"usage_percent"`
	ArticleAvailableCount int64   `json:"article_available_count"`
	ArticleMissingCount   int64   `json:"article_missing_count"`
}

func (s *Server) collectStats() SystemStats {
	logger.Trace("collectStats start")
	stats := SystemStats{
		Timestamp: time.Now(),
		Providers: make([]ProviderStats, 0),
	}

	var totalActive, totalMax int
	var totalDownloadedMB float64

	s.mu.RLock()
	pools := s.providerPools
	application := s.app
	liveIndexer := s.indexer
	attempts := s.attemptLister
	s.mu.RUnlock()
	articleByProvider := map[string]usenetpool.ProviderArticleStats{}
	articleByHost := map[string]usenetpool.ProviderArticleStats{}
	if application != nil {
		if comp := application.Components(); comp != nil && comp.UsenetPool != nil {
			stats.StreamSpeeds = comp.UsenetPool.StreamSpeeds()
			for _, providerStat := range comp.UsenetPool.ProviderArticleStats() {
				articleByProvider[providerStat.ProviderID] = providerStat
				if providerStat.Host != "" {
					articleByHost[providerStat.Host] = providerStat
				}
			}
		}
	}

	for name, pool := range pools {
		downloadedMB := pool.TotalMegabytes()

		// One snapshot per provider, so active, idle and max in a row agree
		// with each other instead of being read at three different instants.
		// Auxiliary pools — a running speed test, a probe, settings validation —
		// hold connections the provider counts against the same account, so
		// they are folded into the row rather than left invisible.
		conns := pool.ConnStats()
		aux := nntp.AuxConnStats(name)
		pStats := ProviderStats{
			Name:         name,
			Host:         pool.Host(),
			ActiveConns:  conns.Active + aux.Active,
			IdleConns:    conns.Idle + aux.Idle,
			PendingConns: conns.Pending + aux.Pending,
			MaxConns:     conns.Max,
			CurrentSpeed: pool.GetSpeed(),
			DownloadedMB: downloadedMB,
		}
		if articleStat, ok := articleByProvider[name]; ok {
			pStats.ArticleAvailableCount = articleStat.AvailableCount
			pStats.ArticleMissingCount = articleStat.UnavailableCount
		} else if articleStat, ok := articleByHost[pStats.Host]; ok {
			pStats.ArticleAvailableCount = articleStat.AvailableCount
			pStats.ArticleMissingCount = articleStat.UnavailableCount
		}

		totalActive += pStats.ActiveConns
		totalMax += pStats.MaxConns
		stats.TotalSpeed += pStats.CurrentSpeed
		totalDownloadedMB += downloadedMB

		stats.Providers = append(stats.Providers, pStats)
	}

	if totalDownloadedMB > 0 {
		for i := range stats.Providers {
			stats.Providers[i].UsagePercent = (stats.Providers[i].DownloadedMB / totalDownloadedMB) * 100
		}
		stats.TotalDownloadedMB = totalDownloadedMB
	}

	sort.Slice(stats.Providers, func(i, j int) bool {
		return stats.Providers[i].Name < stats.Providers[j].Name
	})

	if liveIndexer != nil {
		availIndexerStats := map[string]stremio.AvailIndexerStats{}
		uniqueIndexerHits := map[string]int64{}
		if s.strmServer != nil {
			availIndexerStats = s.strmServer.GetAvailIndexerStats()
			uniqueIndexerHits = s.strmServer.GetUniqueIndexerHits()
		}

		type indexerContainer interface {
			GetIndexers() []indexer.Indexer
		}

		var indexers []indexer.Indexer
		if container, ok := liveIndexer.(indexerContainer); ok {
			indexers = container.GetIndexers()
		} else {
			indexers = []indexer.Indexer{liveIndexer}
		}

		for _, idx := range indexers {
			usage := idx.GetUsage()
			stats.Indexers = append(stats.Indexers, IndexerStats{
				Name:                 idx.Name(),
				APIHitsLimit:         usage.APIHitsLimit,
				APIHitsUsed:          usage.APIHitsUsed,
				APIHitsRemaining:     usage.APIHitsRemaining,
				AllTimeAPIHitsUsed:   usage.AllTimeAPIHitsUsed,
				DownloadsLimit:       usage.DownloadsLimit,
				DownloadsUsed:        usage.DownloadsUsed,
				DownloadsRemaining:   usage.DownloadsRemaining,
				AllTimeDownloadsUsed: usage.AllTimeDownloadsUsed,
				SearchesCount:        usage.SearchesCount,
				UniqueHitsCount:      uniqueIndexerHits[idx.Name()],
				AvgResponseMS:        usage.AvgResponseMS,
				AvailAvailableCount:  availIndexerStats[idx.Name()].AvailableReturned,
				AvailDiscardedCount:  availIndexerStats[idx.Name()].Discarded,
			})
		}
	}

	stats.ActiveConnections = totalActive
	stats.TotalConnections = totalMax

	stats.ActiveSessions = s.sessionMgr.GetActiveSessions()

	s.mu.RLock()
	if s.proxyServer != nil {
		proxySessions := s.proxyServer.GetSessions()

		type proxyGroup struct {
			count int
			group string
			ip    string
		}
		groups := make(map[string]*proxyGroup)

		for _, ps := range proxySessions {

			ip := httpx.HostFromAddr(ps.RemoteAddr)

			if _, exists := groups[ip]; !exists {
				groups[ip] = &proxyGroup{ip: ip}
			}
			g := groups[ip]
			g.count++

			if ps.CurrentGroup != "" {
				g.group = ps.CurrentGroup
			}
		}

		// Iterate in sorted IP order so proxy entries keep a stable position in the dashboard list.
		ips := make([]string, 0, len(groups))
		for ip := range groups {
			ips = append(ips, ip)
		}
		sort.Strings(ips)

		for _, ip := range ips {
			g := groups[ip]
			title := fmt.Sprintf("Proxy Client (%d conns)", g.count)
			if g.group != "" {
				title = fmt.Sprintf("Proxy: %s (%d conns)", g.group, g.count)
			}

			stats.ActiveSessions = append(stats.ActiveSessions, session.ActiveSessionInfo{
				ID:      fmt.Sprintf("proxy-%s", ip),
				Title:   title,
				Clients: []string{ip},
			})
		}
	}
	s.mu.RUnlock()

	stats.ActiveStreams = len(stats.ActiveSessions)

	s.maybePersistMetrics(attempts, stats)

	logger.Trace("collectStats done", "providers", len(stats.Providers), "sessions", len(stats.ActiveSessions))
	return stats
}

func (s *Server) maybePersistMetrics(mgr *persistence.StateManager, stats SystemStats) {
	if mgr == nil {
		return
	}
	now := time.Now()
	const metricsInterval = 30 * time.Second

	s.metricsMu.Lock()
	if s.metricsInFlight {
		s.metricsMu.Unlock()
		return
	}
	if !s.lastMetricsAt.IsZero() && now.Sub(s.lastMetricsAt) < metricsInterval {
		s.metricsMu.Unlock()
		return
	}
	s.metricsInFlight = true
	s.metricsMu.Unlock()

	providers := make([]persistence.ProviderMetric, 0, len(stats.Providers))
	for _, p := range stats.Providers {
		providers = append(providers, persistence.ProviderMetric{
			CollectedAt:      now,
			ProviderName:     p.Name,
			Host:             p.Host,
			ActiveConns:      p.ActiveConns,
			IdleConns:        p.IdleConns,
			MaxConns:         p.MaxConns,
			CurrentSpeedMbps: p.CurrentSpeed,
			DownloadedMB:     p.DownloadedMB,
			UsagePercent:     p.UsagePercent,
			ArticleAvailable: p.ArticleAvailableCount,
			ArticleMissing:   p.ArticleMissingCount,
		})
	}

	indexers := make([]persistence.IndexerMetric, 0, len(stats.Indexers))
	for _, idx := range stats.Indexers {
		indexers = append(indexers, persistence.IndexerMetric{
			CollectedAt:         now,
			IndexerName:         idx.Name,
			APIHitsUsed:         idx.APIHitsUsed,
			APIHitsLimit:        idx.APIHitsLimit,
			DownloadsUsed:       idx.DownloadsUsed,
			DownloadsLimit:      idx.DownloadsLimit,
			SearchesCount:       idx.SearchesCount,
			UniqueHitsCount:     idx.UniqueHitsCount,
			AvgResponseMS:       idx.AvgResponseMS,
			AvailAvailableCount: idx.AvailAvailableCount,
			AvailDiscardedCount: idx.AvailDiscardedCount,
		})
	}

	if err := mgr.RecordMetricsSnapshot(providers, indexers); err != nil {
		s.metricsMu.Lock()
		s.metricsInFlight = false
		s.metricsMu.Unlock()
		logger.Warn("Failed to persist metrics snapshot", "err", err)
		return
	}
	s.metricsMu.Lock()
	s.lastMetricsAt = now
	s.metricsInFlight = false
	s.metricsMu.Unlock()
}
