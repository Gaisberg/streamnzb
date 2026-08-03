package persistence

import (
	"testing"
	"time"
)

func TestDeleteMetricsByRange(t *testing.T) {
	mgr := newTestStateManager(t)

	t1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	providers := []ProviderMetric{
		{CollectedAt: t1, ProviderName: "Eweka", DownloadedMB: 100},
		{CollectedAt: t2, ProviderName: "Eweka", DownloadedMB: 200},
		{CollectedAt: t1, ProviderName: "UsenetExpress", DownloadedMB: 150},
	}
	indexers := []IndexerMetric{
		{CollectedAt: t1, IndexerName: "altHUB", SearchesCount: 5},
		{CollectedAt: t2, IndexerName: "altHUB", SearchesCount: 10},
		{CollectedAt: t1, IndexerName: "DrunkenSlug", SearchesCount: 8},
	}

	if err := mgr.RecordMetricsSnapshot(providers, indexers); err != nil {
		t.Fatalf("RecordMetricsSnapshot failed: %v", err)
	}

	// Delete Eweka metrics only for t1 range
	from1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to1 := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if err := mgr.DeleteProviderMetrics("Eweka", &from1, &to1); err != nil {
		t.Fatalf("DeleteProviderMetrics failed: %v", err)
	}

	summary, err := mgr.GetProviderMetricsSummary(nil, nil)
	if err != nil {
		t.Fatalf("GetProviderMetricsSummary failed: %v", err)
	}

	ewekaCount := 0
	for _, p := range summary {
		if p.ProviderName == "Eweka" {
			ewekaCount++
			if p.DownloadedMB != 200 {
				t.Errorf("Eweka downloadedMB = %f; want 200 (snapshot t1 deleted)", p.DownloadedMB)
			}
		}
	}
	if ewekaCount != 1 {
		t.Errorf("Eweka count = %d; want 1", ewekaCount)
	}

	// Delete altHUB indexer metrics for all time
	if err := mgr.DeleteIndexerMetrics("altHUB", nil, nil); err != nil {
		t.Fatalf("DeleteIndexerMetrics failed: %v", err)
	}

	idxSummary, err := mgr.GetIndexerMetricsSummary(nil, nil)
	if err != nil {
		t.Fatalf("GetIndexerMetricsSummary failed: %v", err)
	}

	for _, idx := range idxSummary {
		if idx.IndexerName == "altHUB" {
			t.Errorf("expected altHUB deleted, but found in summary")
		}
	}
}
