package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
)

func newStatsTestStateManager(t *testing.T) *persistence.StateManager {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "api-stats-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	mgr, err := persistence.GetManager(tempDir)
	if err != nil {
		t.Fatalf("GetManager: %v", err)
	}
	return mgr
}

func TestHandlePersistedStatsIncludesUniqueHitsCount(t *testing.T) {
	mgr := newStatsTestStateManager(t)
	err := mgr.RecordMetricsSnapshot(nil, []persistence.IndexerMetric{
		{
			CollectedAt:         time.Now(),
			IndexerName:         "IndexerA",
			APIHitsUsed:         10,
			SearchesCount:       8,
			UniqueHitsCount:     3,
			AvgResponseMS:       120,
			AvailAvailableCount: 2,
			AvailDiscardedCount: 1,
		},
	})
	if err != nil {
		t.Fatalf("RecordMetricsSnapshot: %v", err)
	}

	srv := &Server{attemptLister: mgr}
	req := httptest.NewRequest(http.MethodGet, "/api/stats/persisted", nil)
	rr := httptest.NewRecorder()

	srv.handlePersistedStats(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	indexers, _ := payload["indexers"].([]any)
	if len(indexers) != 1 {
		t.Fatalf("indexers len = %d, want 1", len(indexers))
	}
	row, _ := indexers[0].(map[string]any)
	if got := row["unique_hits_count"]; got != float64(3) {
		t.Fatalf("unique_hits_count = %v, want 3", got)
	}
}
