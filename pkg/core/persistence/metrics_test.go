package persistence

import (
	"testing"
	"time"
)

func TestRecordMetricsSnapshotPersistsProviderArticleCounters(t *testing.T) {
	mgr := newTestStateManager(t)
	start := time.Now().Add(-2 * time.Minute)

	err := mgr.RecordMetricsSnapshot(
		[]ProviderMetric{
			{
				CollectedAt:      start,
				ProviderName:     "provider-a",
				Host:             "news-a.example",
				ActiveConns:      1,
				IdleConns:        1,
				MaxConns:         20,
				CurrentSpeedMbps: 12.5,
				DownloadedMB:     200.0,
				UsagePercent:     33.3,
				ArticleAvailable: 120,
				ArticleMissing:   7,
			},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordMetricsSnapshot first: %v", err)
	}

	err = mgr.RecordMetricsSnapshot(
		[]ProviderMetric{
			{
				CollectedAt:      start.Add(1 * time.Minute),
				ProviderName:     "provider-a",
				Host:             "news-a.example",
				ActiveConns:      2,
				IdleConns:        0,
				MaxConns:         20,
				CurrentSpeedMbps: 14.0,
				DownloadedMB:     280.0,
				UsagePercent:     40.0,
				ArticleAvailable: 190,
				ArticleMissing:   11,
			},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordMetricsSnapshot second: %v", err)
	}

	summary, err := mgr.GetProviderMetricsSummary(nil, nil)
	if err != nil {
		t.Fatalf("GetProviderMetricsSummary: %v", err)
	}
	if len(summary) != 1 {
		t.Fatalf("summary len = %d, want 1", len(summary))
	}
	got := summary[0]
	if got.ProviderName != "provider-a" {
		t.Fatalf("ProviderName = %q, want provider-a", got.ProviderName)
	}
	if got.ArticleAvailable != 190 {
		t.Fatalf("ArticleAvailable = %d, want 190", got.ArticleAvailable)
	}
	if got.ArticleMissing != 11 {
		t.Fatalf("ArticleMissing = %d, want 11", got.ArticleMissing)
	}
}

func TestRecordMetricsSnapshotPersistsIndexerUniqueHitsCount(t *testing.T) {
	mgr := newTestStateManager(t)
	start := time.Now().Add(-2 * time.Minute)

	err := mgr.RecordMetricsSnapshot(
		nil,
		[]IndexerMetric{
			{
				CollectedAt:         start,
				IndexerName:         "IndexerA",
				APIHitsUsed:         5,
				APIHitsLimit:        100,
				DownloadsUsed:       1,
				DownloadsLimit:      10,
				SearchesCount:       5,
				UniqueHitsCount:     2,
				AvgResponseMS:       200,
				AvailAvailableCount: 3,
				AvailDiscardedCount: 1,
			},
		},
	)
	if err != nil {
		t.Fatalf("RecordMetricsSnapshot first: %v", err)
	}

	err = mgr.RecordMetricsSnapshot(
		nil,
		[]IndexerMetric{
			{
				CollectedAt:         start.Add(1 * time.Minute),
				IndexerName:         "IndexerA",
				APIHitsUsed:         7,
				APIHitsLimit:        100,
				DownloadsUsed:       2,
				DownloadsLimit:      10,
				SearchesCount:       7,
				UniqueHitsCount:     4,
				AvgResponseMS:       180,
				AvailAvailableCount: 5,
				AvailDiscardedCount: 1,
			},
		},
	)
	if err != nil {
		t.Fatalf("RecordMetricsSnapshot second: %v", err)
	}

	summary, err := mgr.GetIndexerMetricsSummary(nil, nil)
	if err != nil {
		t.Fatalf("GetIndexerMetricsSummary: %v", err)
	}
	if len(summary) != 1 {
		t.Fatalf("summary len = %d, want 1", len(summary))
	}
	got := summary[0]
	if got.IndexerName != "IndexerA" {
		t.Fatalf("IndexerName = %q, want IndexerA", got.IndexerName)
	}
	if got.UniqueHitsCount != 4 {
		t.Fatalf("UniqueHitsCount = %d, want 4", got.UniqueHitsCount)
	}
}

func TestRecordPerformanceSamples(t *testing.T) {
	mgr := newTestStateManager(t)
	now := time.Now()

	err := mgr.RecordStreamAPISample(StreamAPISampleRecord{
		Timestamp:          now,
		ContentType:        "movie",
		ID:                 "tt1234567",
		TotalDurationMS:    150,
		MetadataDurationMS: 20,
		SearchDurationMS:   100,
		RankingDurationMS:  10,
		AvailNZBDurationMS: 20,
		CandidateCount:     15,
		ResultCount:        5,
	})
	if err != nil {
		t.Fatalf("RecordStreamAPISample failed: %v", err)
	}

	err = mgr.RecordPlaybackTTFFSample(PlaybackTTFFSampleRecord{
		Timestamp:             now,
		SessionID:             "sess-1",
		ProviderName:          "provider-x",
		TTFFMS:                450,
		SessionResolutionMS:   50,
		NZBFetchDurationMS:    100,
		NNTPConnectDurationMS: 100,
		ProbeDurationMS:       150,
		FirstByteDurationMS:   50,
		IsCacheHit:            true,
	})
	if err != nil {
		t.Fatalf("RecordPlaybackTTFFSample failed: %v", err)
	}

	streamSamples, err := mgr.GetRecentStreamAPISamples(10)
	if err != nil {
		t.Fatalf("GetRecentStreamAPISamples failed: %v", err)
	}
	if len(streamSamples) != 1 {
		t.Fatalf("streamSamples len = %d, want 1", len(streamSamples))
	}
	if streamSamples[0].ID != "tt1234567" || streamSamples[0].TotalDurationMS != 150 {
		t.Fatalf("unexpected streamSample: %+v", streamSamples[0])
	}

	ttffSamples, err := mgr.GetRecentPlaybackTTFFSamples(10)
	if err != nil {
		t.Fatalf("GetRecentPlaybackTTFFSamples failed: %v", err)
	}
	if len(ttffSamples) != 1 {
		t.Fatalf("ttffSamples len = %d, want 1", len(ttffSamples))
	}
	if ttffSamples[0].SessionID != "sess-1" || ttffSamples[0].TTFFMS != 450 || !ttffSamples[0].IsCacheHit {
		t.Fatalf("unexpected ttffSample: %+v", ttffSamples[0])
	}
}
