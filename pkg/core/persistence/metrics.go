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

func computeCounterDeltaFloat(baseline float64, hasBaseline bool, values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	current := baseline
	if !hasBaseline {
		current = 0
	}
	for _, val := range values {
		if val >= current {
			total += (val - current)
		} else {
			total += val
		}
		current = val
	}
	return total
}

func computeCounterDeltaInt(baseline int64, hasBaseline bool, values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var total int64
	current := baseline
	if !hasBaseline {
		current = 0
	}
	for _, val := range values {
		if val >= current {
			total += (val - current)
		} else {
			total += val
		}
		current = val
	}
	return total
}

func (m *StateManager) GetProviderMetricsSummary(from, to *time.Time) ([]ProviderMetric, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var (
		query string
		args  []interface{}
	)
	if to != nil {
		query = `
			SELECT collected_at, provider_name, host, active_conns, idle_conns, max_conns, current_speed_mbps, downloaded_mb, usage_percent, article_available_count, article_missing_count
			FROM provider_metrics
			WHERE collected_at < ?
			ORDER BY collected_at ASC
		`
		args = append(args, to.Unix())
	} else {
		query = `
			SELECT collected_at, provider_name, host, active_conns, idle_conns, max_conns, current_speed_mbps, downloaded_mb, usage_percent, article_available_count, article_missing_count
			FROM provider_metrics
			ORDER BY collected_at ASC
		`
	}

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type providerSnapshot struct {
		CollectedAt      time.Time
		Host             string
		ActiveConns      int
		IdleConns        int
		MaxConns         int
		CurrentSpeedMbps float64
		DownloadedMB     float64
		ArticleAvailable int64
		ArticleMissing   int64
	}

	grouped := make(map[string][]providerSnapshot)
	for rows.Next() {
		var (
			collectedAt  int64
			providerName string
			s            providerSnapshot
		)
		if err := rows.Scan(
			&collectedAt,
			&providerName,
			&s.Host,
			&s.ActiveConns,
			&s.IdleConns,
			&s.MaxConns,
			&s.CurrentSpeedMbps,
			&s.DownloadedMB,
			new(float64),
			&s.ArticleAvailable,
			&s.ArticleMissing,
		); err != nil {
			return nil, err
		}
		s.CollectedAt = time.Unix(collectedAt, 0)
		key := strings.TrimSpace(providerName)
		if key != "" {
			grouped[key] = append(grouped[key], s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	agg := make(map[string]ProviderMetric)
	for name, snaps := range grouped {
		var (
			baseline    *providerSnapshot
			inRange     []providerSnapshot
			hasBaseline bool
		)
		for _, s := range snaps {
			if from != nil && s.CollectedAt.Before(*from) {
				b := s
				baseline = &b
				hasBaseline = true
			} else {
				inRange = append(inRange, s)
			}
		}

		if len(inRange) == 0 {
			continue
		}

		latest := inRange[len(inRange)-1]
		bDl := 0.0
		bAvail := int64(0)
		bMiss := int64(0)
		if hasBaseline && baseline != nil {
			bDl = baseline.DownloadedMB
			bAvail = baseline.ArticleAvailable
			bMiss = baseline.ArticleMissing
		}

		dlMBs := make([]float64, len(inRange))
		availCounts := make([]int64, len(inRange))
		missCounts := make([]int64, len(inRange))
		maxConns := 0
		maxSpeed := 0.0
		latestHost := latest.Host

		for i, s := range inRange {
			dlMBs[i] = s.DownloadedMB
			availCounts[i] = s.ArticleAvailable
			missCounts[i] = s.ArticleMissing
			if s.MaxConns > maxConns {
				maxConns = s.MaxConns
			}
			if s.CurrentSpeedMbps > maxSpeed {
				maxSpeed = s.CurrentSpeedMbps
			}
			if s.Host != "" {
				latestHost = s.Host
			}
		}

		agg[name] = ProviderMetric{
			CollectedAt:      latest.CollectedAt,
			ProviderName:     name,
			Host:             latestHost,
			ActiveConns:      latest.ActiveConns,
			IdleConns:        latest.IdleConns,
			MaxConns:         maxConns,
			CurrentSpeedMbps: maxSpeed,
			DownloadedMB:     computeCounterDeltaFloat(bDl, hasBaseline, dlMBs),
			ArticleAvailable: computeCounterDeltaInt(bAvail, hasBaseline, availCounts),
			ArticleMissing:   computeCounterDeltaInt(bMiss, hasBaseline, missCounts),
		}
	}

	var totalDownloadedMB float64
	for _, v := range agg {
		totalDownloadedMB += v.DownloadedMB
	}
	out := make([]ProviderMetric, 0, len(agg))
	for _, v := range agg {
		if totalDownloadedMB > 0 {
			v.UsagePercent = (v.DownloadedMB / totalDownloadedMB) * 100
		} else {
			v.UsagePercent = 0
		}
		out = append(out, v)
	}
	return out, nil
}

func (m *StateManager) GetIndexerMetricsSummary(from, to *time.Time) ([]IndexerMetric, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var (
		query string
		args  []interface{}
	)
	if to != nil {
		query = `
			SELECT collected_at, indexer_name, api_hits_used, api_hits_limit, downloads_used, downloads_limit, searches_count, unique_hits_count, avg_response_ms, avail_available_count, avail_discarded_count
			FROM indexer_metrics
			WHERE collected_at < ?
			ORDER BY collected_at ASC
		`
		args = append(args, to.Unix())
	} else {
		query = `
			SELECT collected_at, indexer_name, api_hits_used, api_hits_limit, downloads_used, downloads_limit, searches_count, unique_hits_count, avg_response_ms, avail_available_count, avail_discarded_count
			FROM indexer_metrics
			ORDER BY collected_at ASC
		`
	}

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type indexerSnapshot struct {
		CollectedAt         time.Time
		APIHitsUsed         int
		APIHitsLimit        int
		DownloadsUsed       int
		DownloadsLimit      int
		SearchesCount       int
		UniqueHitsCount     int64
		AvgResponseMS       float64
		AvailAvailableCount int64
		AvailDiscardedCount int64
	}

	grouped := make(map[string][]indexerSnapshot)
	for rows.Next() {
		var (
			collectedAt int64
			indexerName string
			s           indexerSnapshot
		)
		if err := rows.Scan(
			&collectedAt,
			&indexerName,
			&s.APIHitsUsed,
			&s.APIHitsLimit,
			&s.DownloadsUsed,
			&s.DownloadsLimit,
			&s.SearchesCount,
			&s.UniqueHitsCount,
			&s.AvgResponseMS,
			&s.AvailAvailableCount,
			&s.AvailDiscardedCount,
		); err != nil {
			return nil, err
		}
		s.CollectedAt = time.Unix(collectedAt, 0)
		key := strings.TrimSpace(indexerName)
		if key != "" {
			grouped[key] = append(grouped[key], s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	agg := make(map[string]IndexerMetric)
	for name, snaps := range grouped {
		var (
			baseline    *indexerSnapshot
			inRange     []indexerSnapshot
			hasBaseline bool
		)
		for _, s := range snaps {
			if from != nil && s.CollectedAt.Before(*from) {
				b := s
				baseline = &b
				hasBaseline = true
			} else {
				inRange = append(inRange, s)
			}
		}

		if len(inRange) == 0 {
			continue
		}

		latest := inRange[len(inRange)-1]
		bAPI := int64(0)
		bDl := int64(0)
		bSearch := int64(0)
		bUnique := int64(0)
		bAvail := int64(0)
		bDiscard := int64(0)
		if hasBaseline && baseline != nil {
			bAPI = int64(baseline.APIHitsUsed)
			bDl = int64(baseline.DownloadsUsed)
			bSearch = int64(baseline.SearchesCount)
			bUnique = baseline.UniqueHitsCount
			bAvail = baseline.AvailAvailableCount
			bDiscard = baseline.AvailDiscardedCount
		}

		apiHits := make([]int64, len(inRange))
		dlHits := make([]int64, len(inRange))
		searchHits := make([]int64, len(inRange))
		uniqueHits := make([]int64, len(inRange))
		availHits := make([]int64, len(inRange))
		discardHits := make([]int64, len(inRange))

		latestAvgResp := 0.0
		var sumResp float64
		var countResp int
		apiLimit := latest.APIHitsLimit
		dlLimit := latest.DownloadsLimit

		for i, s := range inRange {
			apiHits[i] = int64(s.APIHitsUsed)
			dlHits[i] = int64(s.DownloadsUsed)
			searchHits[i] = int64(s.SearchesCount)
			uniqueHits[i] = s.UniqueHitsCount
			availHits[i] = s.AvailAvailableCount
			discardHits[i] = s.AvailDiscardedCount
			if s.AvgResponseMS > 0 {
				sumResp += s.AvgResponseMS
				countResp++
			}
			if s.APIHitsLimit > apiLimit {
				apiLimit = s.APIHitsLimit
			}
			if s.DownloadsLimit > dlLimit {
				dlLimit = s.DownloadsLimit
			}
		}

		if countResp > 0 {
			latestAvgResp = sumResp / float64(countResp)
		} else {
			for i := len(snaps) - 1; i >= 0; i-- {
				if snaps[i].AvgResponseMS > 0 {
					latestAvgResp = snaps[i].AvgResponseMS
					break
				}
			}
		}

		agg[name] = IndexerMetric{
			CollectedAt:         latest.CollectedAt,
			IndexerName:         name,
			APIHitsUsed:         int(computeCounterDeltaInt(bAPI, hasBaseline, apiHits)),
			APIHitsLimit:        apiLimit,
			DownloadsUsed:       int(computeCounterDeltaInt(bDl, hasBaseline, dlHits)),
			DownloadsLimit:      dlLimit,
			SearchesCount:       int(computeCounterDeltaInt(bSearch, hasBaseline, searchHits)),
			UniqueHitsCount:     computeCounterDeltaInt(bUnique, hasBaseline, uniqueHits),
			AvgResponseMS:       latestAvgResp,
			AvailAvailableCount: computeCounterDeltaInt(bAvail, hasBaseline, availHits),
			AvailDiscardedCount: computeCounterDeltaInt(bDiscard, hasBaseline, discardHits),
		}
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

type PerformanceMetricRecord struct {
	CollectedAt time.Time `json:"collected_at"`
	MetricType  string    `json:"metric_type"`
	SampleCount int       `json:"sample_count"`
	MinMS       float64   `json:"min_ms"`
	MaxMS       float64   `json:"max_ms"`
	AvgMS       float64   `json:"avg_ms"`
	P50MS       float64   `json:"p50_ms"`
	P95MS       float64   `json:"p95_ms"`
	P99MS       float64   `json:"p99_ms"`
}

func (m *StateManager) RecordPerformanceMetrics(records []PerformanceMetricRecord) error {
	if len(records) == 0 {
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

		stmt, err := tx.Prepare(`
			INSERT INTO performance_metrics (
				collected_at, metric_type, sample_count, min_ms, max_ms, avg_ms, p50_ms, p95_ms, p99_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, rec := range records {
			collectedAt := rec.CollectedAt
			if collectedAt.IsZero() {
				collectedAt = time.Now()
			}
			if _, err := stmt.Exec(
				collectedAt.Unix(),
				rec.MetricType,
				rec.SampleCount,
				rec.MinMS,
				rec.MaxMS,
				rec.AvgMS,
				rec.P50MS,
				rec.P95MS,
				rec.P99MS,
			); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}

func (m *StateManager) GetPerformanceMetricsSummary(from, to *time.Time) ([]PerformanceMetricRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	query, args := buildTimeRangeSQL(`
		SELECT
			collected_at,
			metric_type,
			sample_count,
			min_ms,
			max_ms,
			avg_ms,
			p50_ms,
			p95_ms,
			p99_ms
		FROM performance_metrics
	`, from, to)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []PerformanceMetricRecord
	for rows.Next() {
		var (
			collectedAt int64
			rec         PerformanceMetricRecord
		)
		if err := rows.Scan(
			&collectedAt,
			&rec.MetricType,
			&rec.SampleCount,
			&rec.MinMS,
			&rec.MaxMS,
			&rec.AvgMS,
			&rec.P50MS,
			&rec.P95MS,
			&rec.P99MS,
		); err != nil {
			return nil, err
		}
		rec.CollectedAt = time.Unix(collectedAt, 0)
		results = append(results, rec)
	}
	return results, nil
}

type StreamAPISampleRecord struct {
	Timestamp          time.Time `json:"timestamp"`
	ContentType        string    `json:"content_type"`
	ID                 string    `json:"id"`
	TotalDurationMS    int64     `json:"total_duration_ms"`
	MetadataDurationMS int64     `json:"metadata_duration_ms"`
	SearchDurationMS   int64     `json:"search_duration_ms"`
	RankingDurationMS  int64     `json:"ranking_duration_ms"`
	AvailNZBDurationMS int64     `json:"avail_nzb_duration_ms"`
	CandidateCount     int       `json:"candidate_count"`
	ResultCount        int       `json:"result_count"`
}

type PlaybackTTFFSampleRecord struct {
	Timestamp             time.Time `json:"timestamp"`
	SessionID             string    `json:"session_id"`
	ProviderName          string    `json:"provider_name"`
	TTFFMS                int64     `json:"ttff_ms"`
	SessionResolutionMS   int64     `json:"session_resolution_ms"`
	NZBFetchDurationMS    int64     `json:"nzb_fetch_duration_ms"`
	NNTPConnectDurationMS int64     `json:"nntp_connect_duration_ms"`
	ProbeDurationMS       int64     `json:"probe_duration_ms"`
	FirstByteDurationMS   int64     `json:"first_byte_duration_ms"`
	IsCacheHit            bool      `json:"is_cache_hit"`
}

func (m *StateManager) RecordStreamAPISample(rec StreamAPISampleRecord) error {
	if m == nil || m.db == nil {
		return nil
	}
	return m.withWriteLock(func(db *sql.DB) error {
		ts := rec.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		_, err := db.Exec(`
			INSERT INTO stream_api_samples (
				timestamp, content_type, content_id, total_duration_ms,
				metadata_duration_ms, search_duration_ms, ranking_duration_ms, avail_nzb_duration_ms,
				candidate_count, result_count
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, ts.Unix(), rec.ContentType, rec.ID, rec.TotalDurationMS, rec.MetadataDurationMS, rec.SearchDurationMS, rec.RankingDurationMS, rec.AvailNZBDurationMS, rec.CandidateCount, rec.ResultCount)
		return err
	})
}

func (m *StateManager) RecordPlaybackTTFFSample(rec PlaybackTTFFSampleRecord) error {
	if m == nil || m.db == nil {
		return nil
	}
	return m.withWriteLock(func(db *sql.DB) error {
		ts := rec.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		isCacheHitInt := 0
		if rec.IsCacheHit {
			isCacheHitInt = 1
		}
		_, err := db.Exec(`
			INSERT INTO playback_ttff_samples (
				timestamp, session_id, provider_name, ttff_ms,
				session_resolution_ms, nzb_fetch_duration_ms, nntp_connect_duration_ms,
				probe_duration_ms, first_byte_duration_ms, is_cache_hit
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, ts.Unix(), rec.SessionID, rec.ProviderName, rec.TTFFMS, rec.SessionResolutionMS, rec.NZBFetchDurationMS, rec.NNTPConnectDurationMS, rec.ProbeDurationMS, rec.FirstByteDurationMS, isCacheHitInt)
		return err
	})
}

func (m *StateManager) GetRecentStreamAPISamples(limit int) ([]StreamAPISampleRecord, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 {
		limit = 1000
	}
	rows, err := m.db.Query(`
		SELECT timestamp, content_type, content_id, total_duration_ms, metadata_duration_ms, search_duration_ms, ranking_duration_ms, avail_nzb_duration_ms, candidate_count, result_count
		FROM stream_api_samples
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []StreamAPISampleRecord
	for rows.Next() {
		var ts int64
		var rec StreamAPISampleRecord
		if err := rows.Scan(&ts, &rec.ContentType, &rec.ID, &rec.TotalDurationMS, &rec.MetadataDurationMS, &rec.SearchDurationMS, &rec.RankingDurationMS, &rec.AvailNZBDurationMS, &rec.CandidateCount, &rec.ResultCount); err != nil {
			return nil, err
		}
		rec.Timestamp = time.Unix(ts, 0)
		results = append(results, rec)
	}
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results, nil
}

func (m *StateManager) GetRecentPlaybackTTFFSamples(limit int) ([]PlaybackTTFFSampleRecord, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 {
		limit = 1000
	}
	rows, err := m.db.Query(`
		SELECT timestamp, session_id, provider_name, ttff_ms, session_resolution_ms, nzb_fetch_duration_ms, nntp_connect_duration_ms, probe_duration_ms, first_byte_duration_ms, is_cache_hit
		FROM playback_ttff_samples
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []PlaybackTTFFSampleRecord
	for rows.Next() {
		var ts int64
		var isCacheHitInt int
		var rec PlaybackTTFFSampleRecord
		if err := rows.Scan(&ts, &rec.SessionID, &rec.ProviderName, &rec.TTFFMS, &rec.SessionResolutionMS, &rec.NZBFetchDurationMS, &rec.NNTPConnectDurationMS, &rec.ProbeDurationMS, &rec.FirstByteDurationMS, &isCacheHitInt); err != nil {
			return nil, err
		}
		rec.Timestamp = time.Unix(ts, 0)
		rec.IsCacheHit = isCacheHitInt != 0
		results = append(results, rec)
	}
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results, nil
}

func (m *StateManager) DeleteProviderMetrics(name string, from, to *time.Time) error {
	name = strings.TrimSpace(name)
	return m.withWriteLock(func(db *sql.DB) error {
		clauses := make([]string, 0, 3)
		args := make([]interface{}, 0, 3)
		if name != "" {
			clauses = append(clauses, "provider_name = ?")
			args = append(args, name)
		}
		if from != nil {
			clauses = append(clauses, "collected_at >= ?")
			args = append(args, from.Unix())
		}
		if to != nil {
			clauses = append(clauses, "collected_at < ?")
			args = append(args, to.Unix())
		}
		query := "DELETE FROM provider_metrics"
		if len(clauses) > 0 {
			query += " WHERE " + strings.Join(clauses, " AND ")
		}
		_, err := db.Exec(query, args...)
		return err
	})
}

func (m *StateManager) DeleteIndexerMetrics(name string, from, to *time.Time) error {
	name = strings.TrimSpace(name)
	return m.withWriteLock(func(db *sql.DB) error {
		clauses := make([]string, 0, 3)
		args := make([]interface{}, 0, 3)
		if name != "" {
			clauses = append(clauses, "indexer_name = ?")
			args = append(args, name)
		}
		if from != nil {
			clauses = append(clauses, "collected_at >= ?")
			args = append(args, from.Unix())
		}
		if to != nil {
			clauses = append(clauses, "collected_at < ?")
			args = append(args, to.Unix())
		}
		query := "DELETE FROM indexer_metrics"
		if len(clauses) > 0 {
			query += " WHERE " + strings.Join(clauses, " AND ")
		}
		_, err := db.Exec(query, args...)
		return err
	})
}
