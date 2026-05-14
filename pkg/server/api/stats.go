package api

import (
	"fmt"
	"net"
	"sort"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/session"
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
}

type IndexerStats struct {
	Name                 string `json:"name"`
	APIHitsLimit         int    `json:"api_hits_limit"`
	APIHitsUsed          int    `json:"api_hits_used"`
	APIHitsRemaining     int    `json:"api_hits_remaining"`
	AllTimeAPIHitsUsed   int    `json:"api_hits_used_all_time"`
	DownloadsLimit       int    `json:"downloads_limit"`
	DownloadsUsed        int    `json:"downloads_used"`
	DownloadsRemaining   int    `json:"downloads_remaining"`
	AllTimeDownloadsUsed int    `json:"downloads_used_all_time"`
	SearchesCount        int    `json:"searches_count"`
	AvgResponseMS        int64  `json:"avg_response_ms"`
	AvailAvailableCount  int64  `json:"avail_available_count"`
	AvailDiscardedCount  int64  `json:"avail_discarded_count"`
}

type ProviderStats struct {
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	ActiveConns  int     `json:"active_conns"`
	IdleConns    int     `json:"idle_conns"`
	MaxConns     int     `json:"max_conns"`
	CurrentSpeed float64 `json:"current_speed_mbps"`
	DownloadedMB float64 `json:"downloaded_mb"`
	UsagePercent float64 `json:"usage_percent"`
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
	s.mu.RUnlock()

	for name, pool := range pools {
		downloadedMB := pool.TotalMegabytes()

		pStats := ProviderStats{
			Name:         name,
			Host:         pool.Host(),
			ActiveConns:  pool.ActiveConnections(),
			IdleConns:    pool.IdleConnections(),
			MaxConns:     pool.MaxConn(),
			CurrentSpeed: pool.GetSpeed(),
			DownloadedMB: downloadedMB,
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

	if s.indexer != nil {
		availIndexerStats := map[string]stremio.AvailIndexerStats{}
		if s.strmServer != nil {
			availIndexerStats = s.strmServer.GetAvailIndexerStats()
		}

		type indexerContainer interface {
			GetIndexers() []indexer.Indexer
		}

		var indexers []indexer.Indexer
		if container, ok := s.indexer.(indexerContainer); ok {
			indexers = container.GetIndexers()
		} else {
			indexers = []indexer.Indexer{s.indexer}
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
				AvgResponseMS:        int64(usage.AvgResponseMS),
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

			ip := ps.RemoteAddr
			if host, _, err := net.SplitHostPort(ip); err == nil {
				ip = host
			}

			if _, exists := groups[ip]; !exists {
				groups[ip] = &proxyGroup{ip: ip}
			}
			g := groups[ip]
			g.count++

			if ps.CurrentGroup != "" {
				g.group = ps.CurrentGroup
			}
		}

		for ip, g := range groups {
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

	s.maybePersistMetrics(stats)

	logger.Trace("collectStats done", "providers", len(stats.Providers), "sessions", len(stats.ActiveSessions))
	return stats
}

func (s *Server) maybePersistMetrics(stats SystemStats) {
	mgr := s.attemptLister
	if mgr == nil {
		return
	}
	now := time.Now()
	const metricsInterval = 30 * time.Second

	s.metricsMu.Lock()
	if !s.lastMetricsAt.IsZero() && now.Sub(s.lastMetricsAt) < metricsInterval {
		s.metricsMu.Unlock()
		return
	}
	s.lastMetricsAt = now
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
			AvgResponseMS:       idx.AvgResponseMS,
			AvailAvailableCount: idx.AvailAvailableCount,
			AvailDiscardedCount: idx.AvailDiscardedCount,
		})
	}

	if err := mgr.RecordMetricsSnapshot(providers, indexers); err != nil {
		logger.Warn("Failed to persist metrics snapshot", "err", err)
	}
}
