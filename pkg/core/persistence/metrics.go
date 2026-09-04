package persistence

import (
	"context"
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
	GrabSuccessCount    int64     `json:"grab_success_count"`
	GrabFailureCount    int64     `json:"grab_failure_count"`
	UniqueSuccessCount  int64     `json:"unique_success_count"`
	AvgResponseMS       float64   `json:"avg_response_ms"`
	AvgGrabMS           float64   `json:"avg_grab_ms"`
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

// deleteMetricRows removes rows from a metrics table, optionally narrowed by
// name and a collected_at range.
// counterDelta sums the growth of a monotonic counter across samples,
// treating any decrease as a reset (the provider restarted, so the new value
// is the whole delta). baseline is the last value before the window;
// hasBaseline false means the window starts from zero.
func counterDelta[T int64 | float64](baseline T, hasBaseline bool, values []T) T {
	if len(values) == 0 {
		return 0
	}
	var total T
	current := baseline
	if !hasBaseline {
		current = 0
	}
	for _, val := range values {
		if val >= current {
			total += val - current
		} else {
			total += val
		}
		current = val
	}
	return total
}

// foldAverage folds one "average" column across a range of snapshots. A
// snapshot reporting 0 has nothing to say — the counters these averages come
// from live in memory and a restart clears them — so only non-zero samples are
// averaged, and a range holding none falls back to the newest non-zero value
// ever recorded for that row rather than reporting a misleading zero.
func foldAverage[T any](inRange, all []T, get func(T) float64) float64 {
	var sum float64
	var count int
	for _, s := range inRange {
		if v := get(s); v > 0 {
			sum += v
			count++
		}
	}
	if count > 0 {
		return sum / float64(count)
	}
	for i := len(all) - 1; i >= 0; i-- {
		if v := get(all[i]); v > 0 {
			return v
		}
	}
	return 0
}

func (m *StateManager) deleteMetricRows(table, nameCol, name string, from, to *time.Time) error {
	name = strings.TrimSpace(name)
	return m.withWriteLock(func(db *connRef) error {
		clauses := make([]string, 0, 3)
		args := make([]interface{}, 0, 3)
		if name != "" {
			clauses = append(clauses, nameCol+" = ?")
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
		query := "DELETE FROM " + table
		if len(clauses) > 0 {
			query += " WHERE " + strings.Join(clauses, " AND ")
		}
		_, err := db.Exec(query, args...)
		return err
	})
}

// baselineFrom finds the last sample before `from` so counter deltas have a
// starting point, falling back to `from` itself when there is none.
func (m *StateManager) baselineFrom(table string, from *time.Time) *time.Time {
	if from == nil {
		return nil
	}
	var baselineTs int64
	err := m.db.QueryRow(`SELECT MAX(collected_at) FROM `+table+` WHERE collected_at < ?`, from.Unix()).Scan(&baselineTs)
	if err == nil && baselineTs > 0 {
		t := time.Unix(baselineTs, 0)
		return &t
	}
	return from
}

// orNow substitutes the current time for a zero timestamp.
func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// reverse flips a slice in place; the sample queries read newest-first and
// callers want oldest-first.
func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// withTx runs fn inside a transaction, rolling back unless fn commits by
// returning nil.
func (m *StateManager) withTx(c *connRef, fn func(*txn) error) error {
	tx, err := c.BeginTx(context.Background())
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (m *StateManager) GetProviderMetricsSummary(from, to *time.Time) ([]ProviderMetric, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var (
		query string
		args  []interface{}
	)
	fetchFrom := m.baselineFrom("provider_metrics", from)

	query, args = buildTimeRangeSQL("SELECT collected_at, provider_name, host, active_conns, idle_conns, max_conns, current_speed_mbps, downloaded_mb, usage_percent, article_available_count, article_missing_count FROM provider_metrics", fetchFrom, to)
	query += " ORDER BY collected_at ASC"

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
			DownloadedMB:     counterDelta(bDl, hasBaseline, dlMBs),
			ArticleAvailable: counterDelta(bAvail, hasBaseline, availCounts),
			ArticleMissing:   counterDelta(bMiss, hasBaseline, missCounts),
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
	fetchFrom := m.baselineFrom("indexer_metrics", from)

	query, args = buildTimeRangeSQL("SELECT collected_at, indexer_name, api_hits_used, api_hits_limit, downloads_used, downloads_limit, searches_count, unique_hits_count, grab_success_count, grab_failure_count, unique_success_count, avg_response_ms, avg_grab_ms, avail_available_count, avail_discarded_count FROM indexer_metrics", fetchFrom, to)
	query += " ORDER BY collected_at ASC"

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
		GrabSuccessCount    int64
		GrabFailureCount    int64
		UniqueSuccessCount  int64
		AvgResponseMS       float64
		AvgGrabMS           float64
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
			&s.GrabSuccessCount,
			&s.GrabFailureCount,
			&s.UniqueSuccessCount,
			&s.AvgResponseMS,
			&s.AvgGrabMS,
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
		bGrabOK := int64(0)
		bGrabFail := int64(0)
		bUniqueOK := int64(0)
		bAvail := int64(0)
		bDiscard := int64(0)
		if hasBaseline && baseline != nil {
			bAPI = int64(baseline.APIHitsUsed)
			bDl = int64(baseline.DownloadsUsed)
			bSearch = int64(baseline.SearchesCount)
			bUnique = baseline.UniqueHitsCount
			bGrabOK = baseline.GrabSuccessCount
			bGrabFail = baseline.GrabFailureCount
			bUniqueOK = baseline.UniqueSuccessCount
			bAvail = baseline.AvailAvailableCount
			bDiscard = baseline.AvailDiscardedCount
		}

		apiHits := make([]int64, len(inRange))
		dlHits := make([]int64, len(inRange))
		searchHits := make([]int64, len(inRange))
		uniqueHits := make([]int64, len(inRange))
		grabOKHits := make([]int64, len(inRange))
		grabFailHits := make([]int64, len(inRange))
		uniqueOKHits := make([]int64, len(inRange))
		availHits := make([]int64, len(inRange))
		discardHits := make([]int64, len(inRange))

		apiLimit := latest.APIHitsLimit
		dlLimit := latest.DownloadsLimit

		for i, s := range inRange {
			apiHits[i] = int64(s.APIHitsUsed)
			dlHits[i] = int64(s.DownloadsUsed)
			searchHits[i] = int64(s.SearchesCount)
			uniqueHits[i] = s.UniqueHitsCount
			grabOKHits[i] = s.GrabSuccessCount
			grabFailHits[i] = s.GrabFailureCount
			uniqueOKHits[i] = s.UniqueSuccessCount
			availHits[i] = s.AvailAvailableCount
			discardHits[i] = s.AvailDiscardedCount
			if s.APIHitsLimit > apiLimit {
				apiLimit = s.APIHitsLimit
			}
			if s.DownloadsLimit > dlLimit {
				dlLimit = s.DownloadsLimit
			}
		}

		avgResp := foldAverage(inRange, snaps, func(s indexerSnapshot) float64 { return s.AvgResponseMS })
		avgGrab := foldAverage(inRange, snaps, func(s indexerSnapshot) float64 { return s.AvgGrabMS })

		agg[name] = IndexerMetric{
			CollectedAt:         latest.CollectedAt,
			IndexerName:         name,
			APIHitsUsed:         int(counterDelta(bAPI, hasBaseline, apiHits)),
			APIHitsLimit:        apiLimit,
			DownloadsUsed:       int(counterDelta(bDl, hasBaseline, dlHits)),
			DownloadsLimit:      dlLimit,
			SearchesCount:       int(counterDelta(bSearch, hasBaseline, searchHits)),
			UniqueHitsCount:     counterDelta(bUnique, hasBaseline, uniqueHits),
			GrabSuccessCount:    counterDelta(bGrabOK, hasBaseline, grabOKHits),
			GrabFailureCount:    counterDelta(bGrabFail, hasBaseline, grabFailHits),
			UniqueSuccessCount:  counterDelta(bUniqueOK, hasBaseline, uniqueOKHits),
			AvgResponseMS:       avgResp,
			AvgGrabMS:           avgGrab,
			AvailAvailableCount: counterDelta(bAvail, hasBaseline, availHits),
			AvailDiscardedCount: counterDelta(bDiscard, hasBaseline, discardHits),
		}
	}

	out := make([]IndexerMetric, 0, len(agg))
	for _, v := range agg {
		out = append(out, v)
	}
	return out, nil
}

func (m *StateManager) GetLatestProviderMetrics() ([]ProviderMetric, error) {
	since := time.Now().Add(-24 * time.Hour)
	metrics, err := m.GetProviderMetricsSummary(&since, nil)
	if err == nil && len(metrics) > 0 {
		return metrics, nil
	}
	return m.GetProviderMetricsSummary(nil, nil)
}

func (m *StateManager) GetLatestIndexerMetrics() ([]IndexerMetric, error) {
	since := time.Now().Add(-24 * time.Hour)
	metrics, err := m.GetIndexerMetricsSummary(&since, nil)
	if err == nil && len(metrics) > 0 {
		return metrics, nil
	}
	return m.GetIndexerMetricsSummary(nil, nil)
}

func (m *StateManager) RecordMetricsSnapshot(providers []ProviderMetric, indexers []IndexerMetric) error {
	if len(providers) == 0 && len(indexers) == 0 {
		return nil
	}
	return m.withWriteLock(func(db *connRef) error {
		return m.withTx(db, func(tx *txn) error {

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
					collectedAt := orNow(p.CollectedAt)
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
						collected_at, indexer_name, api_hits_used, api_hits_limit, downloads_used, downloads_limit, searches_count, unique_hits_count, grab_success_count, grab_failure_count, unique_success_count, avg_response_ms, avg_grab_ms, avail_available_count, avail_discarded_count
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				`)
				if err != nil {
					return err
				}
				defer stmt.Close()
				for _, idx := range indexers {
					collectedAt := orNow(idx.CollectedAt)
					if _, err := stmt.Exec(
						collectedAt.Unix(),
						idx.IndexerName,
						idx.APIHitsUsed,
						idx.APIHitsLimit,
						idx.DownloadsUsed,
						idx.DownloadsLimit,
						idx.SearchesCount,
						idx.UniqueHitsCount,
						idx.GrabSuccessCount,
						idx.GrabFailureCount,
						idx.UniqueSuccessCount,
						idx.AvgResponseMS,
						idx.AvgGrabMS,
						idx.AvailAvailableCount,
						idx.AvailDiscardedCount,
					); err != nil {
						return err
					}
				}
			}
			return nil
		})
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
	return m.withWriteLock(func(db *connRef) error {
		return m.withTx(db, func(tx *txn) error {

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
				collectedAt := orNow(rec.CollectedAt)
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
			return nil
		})
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
	return m.withWriteLock(func(db *connRef) error {
		ts := orNow(rec.Timestamp)
		_, err := db.Exec(`
			INSERT INTO stream_api_samples (
				"timestamp", content_type, content_id, total_duration_ms,
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
	return m.withWriteLock(func(db *connRef) error {
		ts := orNow(rec.Timestamp)
		isCacheHitInt := boolToInt(rec.IsCacheHit)
		_, err := db.Exec(`
			INSERT INTO playback_ttff_samples (
				"timestamp", session_id, provider_name, ttff_ms,
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
		SELECT "timestamp", content_type, content_id, total_duration_ms, metadata_duration_ms, search_duration_ms, ranking_duration_ms, avail_nzb_duration_ms, candidate_count, result_count
		FROM stream_api_samples
		ORDER BY "timestamp" DESC
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
	reverse(results)
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
		SELECT "timestamp", session_id, provider_name, ttff_ms, session_resolution_ms, nzb_fetch_duration_ms, nntp_connect_duration_ms, probe_duration_ms, first_byte_duration_ms, is_cache_hit
		FROM playback_ttff_samples
		ORDER BY "timestamp" DESC
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
	reverse(results)
	return results, nil
}

func (m *StateManager) DeleteProviderMetrics(name string, from, to *time.Time) error {
	return m.deleteMetricRows("provider_metrics", "provider_name", name, from, to)
}

func (m *StateManager) DeleteIndexerMetrics(name string, from, to *time.Time) error {
	return m.deleteMetricRows("indexer_metrics", "indexer_name", name, from, to)
}
