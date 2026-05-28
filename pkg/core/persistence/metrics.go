package persistence

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type ProviderMetric struct {
	CollectedAt      time.Time `json:"collected_at"`
	ProviderName     string    `json:"provider_name"`
	Host             string    `json:"host"`
	ActiveConns      int       `json:"active_conns"`
	IdleConns        int       `json:"idle_conns"`
	MaxConns         int       `json:"max_conns"`
	CurrentSpeedMbps float64   `json:"current_speed_mbps"`
	DownloadedMB     float64   `json:"downloaded_mb"`
	UsagePercent     float64   `json:"usage_percent"`
	ArticleAvailable int64     `json:"article_available_count"`
	ArticleMissing   int64     `json:"article_missing_count"`
}

type IndexerMetric struct {
	CollectedAt         time.Time `json:"collected_at"`
	IndexerName         string    `json:"indexer_name"`
	APIHitsUsed         int       `json:"api_hits_used"`
	APIHitsLimit        int       `json:"api_hits_limit"`
	DownloadsUsed       int       `json:"downloads_used"`
	DownloadsLimit      int       `json:"downloads_limit"`
	SearchesCount       int       `json:"searches_count"`
	UniqueHitsCount     int64     `json:"unique_hits_count"`
	AvgResponseMS       float64   `json:"avg_response_ms"`
	AvailAvailableCount int64     `json:"avail_available_count"`
	AvailDiscardedCount int64     `json:"avail_discarded_count"`
}

func buildTimeRangeSQL(base string, from, to *time.Time) (string, []interface{}) {
	clauses := make([]string, 0, 2)
	args := make([]interface{}, 0, 2)
	if from != nil {
		clauses = append(clauses, "collected_at >= ?")
		args = append(args, from.Unix())
	}
	if to != nil {
		clauses = append(clauses, "collected_at < ?")
		args = append(args, to.Unix())
	}
	if len(clauses) == 0 {
		return base, args
	}
	return base + " WHERE " + strings.Join(clauses, " AND "), args
}

func (m *StateManager) GetProviderMetricsSummary(from, to *time.Time) ([]ProviderMetric, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	query, args := buildTimeRangeSQL(`
		SELECT
			collected_at,
			provider_name,
			host,
			active_conns,
			idle_conns,
			max_conns,
			current_speed_mbps,
			downloaded_mb,
			usage_percent,
			article_available_count,
			article_missing_count
		FROM provider_metrics
	`, from, to)
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	agg := make(map[string]ProviderMetric)
	for rows.Next() {
		var (
			collectedAt int64
			mx          ProviderMetric
		)
		if err := rows.Scan(
			&collectedAt,
			&mx.ProviderName,
			&mx.Host,
			&mx.ActiveConns,
			&mx.IdleConns,
			&mx.MaxConns,
			&mx.CurrentSpeedMbps,
			&mx.DownloadedMB,
			&mx.UsagePercent,
			&mx.ArticleAvailable,
			&mx.ArticleMissing,
		); err != nil {
			return nil, err
		}
		mx.CollectedAt = time.Unix(collectedAt, 0)
		key := strings.TrimSpace(mx.ProviderName)
		if key == "" {
			continue
		}
		prev, ok := agg[key]
		if !ok {
			agg[key] = mx
			continue
		}
		if mx.CollectedAt.After(prev.CollectedAt) {
			prev.CollectedAt = mx.CollectedAt
			prev.Host = mx.Host
		}
		if mx.ActiveConns > prev.ActiveConns {
			prev.ActiveConns = mx.ActiveConns
		}
		if mx.IdleConns > prev.IdleConns {
			prev.IdleConns = mx.IdleConns
		}
		if mx.MaxConns > prev.MaxConns {
			prev.MaxConns = mx.MaxConns
		}
		if mx.CurrentSpeedMbps > prev.CurrentSpeedMbps {
			prev.CurrentSpeedMbps = mx.CurrentSpeedMbps
		}
		if mx.DownloadedMB > prev.DownloadedMB {
			prev.DownloadedMB = mx.DownloadedMB
		}
		if mx.UsagePercent > prev.UsagePercent {
			prev.UsagePercent = mx.UsagePercent
		}
		if mx.ArticleAvailable > prev.ArticleAvailable {
			prev.ArticleAvailable = mx.ArticleAvailable
		}
		if mx.ArticleMissing > prev.ArticleMissing {
			prev.ArticleMissing = mx.ArticleMissing
		}
		agg[key] = prev
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ProviderMetric, 0, len(agg))
	for _, v := range agg {
		out = append(out, v)
	}
	return out, nil
}

func (m *StateManager) GetIndexerMetricsSummary(from, to *time.Time) ([]IndexerMetric, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	query, args := buildTimeRangeSQL(`
		SELECT
			collected_at,
			indexer_name,
			api_hits_used,
			api_hits_limit,
			downloads_used,
			downloads_limit,
			searches_count,
			unique_hits_count,
			avg_response_ms,
			avail_available_count,
			avail_discarded_count
		FROM indexer_metrics
	`, from, to)
	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	agg := make(map[string]IndexerMetric)
	for rows.Next() {
		var (
			collectedAt int64
			mx          IndexerMetric
		)
		if err := rows.Scan(
			&collectedAt,
			&mx.IndexerName,
			&mx.APIHitsUsed,
			&mx.APIHitsLimit,
			&mx.DownloadsUsed,
			&mx.DownloadsLimit,
			&mx.SearchesCount,
			&mx.UniqueHitsCount,
			&mx.AvgResponseMS,
			&mx.AvailAvailableCount,
			&mx.AvailDiscardedCount,
		); err != nil {
			return nil, err
		}
		mx.CollectedAt = time.Unix(collectedAt, 0)
		key := strings.TrimSpace(mx.IndexerName)
		if key == "" {
			continue
		}
		prev, ok := agg[key]
		if !ok {
			agg[key] = mx
			continue
		}
		isNewer := mx.CollectedAt.After(prev.CollectedAt)
		if isNewer {
			prev.CollectedAt = mx.CollectedAt
		}
		if mx.APIHitsUsed > prev.APIHitsUsed {
			prev.APIHitsUsed = mx.APIHitsUsed
		}
		if mx.APIHitsLimit > prev.APIHitsLimit {
			prev.APIHitsLimit = mx.APIHitsLimit
		}
		if mx.DownloadsUsed > prev.DownloadsUsed {
			prev.DownloadsUsed = mx.DownloadsUsed
		}
		if mx.DownloadsLimit > prev.DownloadsLimit {
			prev.DownloadsLimit = mx.DownloadsLimit
		}
		if mx.SearchesCount > prev.SearchesCount {
			prev.SearchesCount = mx.SearchesCount
		}
		if mx.UniqueHitsCount > prev.UniqueHitsCount {
			prev.UniqueHitsCount = mx.UniqueHitsCount
		}
		if isNewer {
			prev.AvgResponseMS = mx.AvgResponseMS
		}
		if mx.AvailAvailableCount > prev.AvailAvailableCount {
			prev.AvailAvailableCount = mx.AvailAvailableCount
		}
		if mx.AvailDiscardedCount > prev.AvailDiscardedCount {
			prev.AvailDiscardedCount = mx.AvailDiscardedCount
		}
		agg[key] = prev
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]IndexerMetric, 0, len(agg))
	for _, v := range agg {
		out = append(out, v)
	}
	return out, nil
}

func (m *StateManager) GetLatestProviderMetrics() ([]ProviderMetric, error) {
	return m.GetProviderMetricsSummary(nil, nil)
}

func (m *StateManager) GetLatestIndexerMetrics() ([]IndexerMetric, error) {
	return m.GetIndexerMetricsSummary(nil, nil)
}

func (m *StateManager) RecordMetricsSnapshot(providers []ProviderMetric, indexers []IndexerMetric) error {
	if len(providers) == 0 && len(indexers) == 0 {
		return nil
	}
	return m.withWriteLock(func(db *sql.DB) error {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		if len(providers) > 0 {
			stmt, err := tx.Prepare(`
				INSERT INTO provider_metrics (
					collected_at, provider_name, host, active_conns, idle_conns, max_conns, current_speed_mbps, downloaded_mb, usage_percent, article_available_count, article_missing_count
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, p := range providers {
				collectedAt := p.CollectedAt
				if collectedAt.IsZero() {
					collectedAt = time.Now()
				}
				if _, err := stmt.Exec(
					collectedAt.Unix(),
					p.ProviderName,
					p.Host,
					p.ActiveConns,
					p.IdleConns,
					p.MaxConns,
					p.CurrentSpeedMbps,
					p.DownloadedMB,
					p.UsagePercent,
					p.ArticleAvailable,
					p.ArticleMissing,
				); err != nil {
					return err
				}
			}
		}

		if len(indexers) > 0 {
			stmt, err := tx.Prepare(`
				INSERT INTO indexer_metrics (
					collected_at, indexer_name, api_hits_used, api_hits_limit, downloads_used, downloads_limit, searches_count, unique_hits_count, avg_response_ms, avail_available_count, avail_discarded_count
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, idx := range indexers {
				collectedAt := idx.CollectedAt
				if collectedAt.IsZero() {
					collectedAt = time.Now()
				}
				if _, err := stmt.Exec(
					collectedAt.Unix(),
					idx.IndexerName,
					idx.APIHitsUsed,
					idx.APIHitsLimit,
					idx.DownloadsUsed,
					idx.DownloadsLimit,
					idx.SearchesCount,
					idx.UniqueHitsCount,
					idx.AvgResponseMS,
					idx.AvailAvailableCount,
					idx.AvailDiscardedCount,
				); err != nil {
					return err
				}
			}
		}

		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}
