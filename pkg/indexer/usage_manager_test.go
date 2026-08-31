package indexer

import (
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

var (
	usageManagerTestStateOnce sync.Once
	usageManagerTestStateMgr  *persistence.StateManager
	usageManagerTestStateErr  error
)

func testUsageManagerState(t *testing.T) *persistence.StateManager {
	t.Helper()

	usageManagerTestStateOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "streamnzb-indexer-usage-")
		if err != nil {
			usageManagerTestStateErr = err
			return
		}
		usageManagerTestStateMgr, usageManagerTestStateErr = persistence.GetManager(tempDir)
	})
	if usageManagerTestStateErr != nil {
		t.Fatalf("GetManager: %v", usageManagerTestStateErr)
	}
	return usageManagerTestStateMgr
}

func newTestUsageManager(t *testing.T) *UsageManager {
	t.Helper()
	return &UsageManager{
		state: testUsageManagerState(t),
		data:  make(map[string]*UsageData),
	}
}

func TestCountsDropHitsOutsideTrailingWindow(t *testing.T) {
	um := newTestUsageManager(t)
	name := "usage-trailing-window"
	now := time.Now()

	um.RecordHits(name, 5, 2, now.Add(-25*time.Hour))
	um.RecordHits(name, 2, 1, now)

	got := um.Counts(name, now)
	if got.APIHits != 2 || got.Downloads != 1 {
		t.Fatalf("expected only hits inside the window, got hits=%d downloads=%d", got.APIHits, got.Downloads)
	}
	if got.AllTimeAPIHits != 7 || got.AllTimeDownloads != 3 {
		t.Fatalf("expected all-time to keep every hit, got hits=%d downloads=%d", got.AllTimeAPIHits, got.AllTimeDownloads)
	}
}

func TestCountsUseFreshHeaderSyncPlusLaterHits(t *testing.T) {
	um := newTestUsageManager(t)
	name := "usage-header-sync"
	now := time.Now()

	um.RecordHits(name, 4, 0, now.Add(-time.Minute))
	nine := 9
	um.SetHeaderCounts(name, &nine, nil, now)
	um.RecordHits(name, 1, 1, now.Add(2*time.Second))

	got := um.Counts(name, now.Add(3*time.Second))
	if got.APIHits != 10 {
		t.Fatalf("APIHits = %d, want the synced 9 plus the 1 hit after the sync", got.APIHits)
	}
	// No download sync was stored, so downloads stay on the trailing count.
	if got.Downloads != 1 {
		t.Fatalf("Downloads = %d, want the trailing count 1", got.Downloads)
	}
}

func TestCountsIgnoreHeaderSyncOlderThanWindow(t *testing.T) {
	um := newTestUsageManager(t)
	name := "usage-header-stale"
	now := time.Now()

	nine := 9
	um.SetHeaderCounts(name, &nine, nil, now.Add(-25*time.Hour))
	um.RecordHits(name, 3, 0, now)

	if got := um.Counts(name, now); got.APIHits != 3 {
		t.Fatalf("APIHits = %d, want the trailing count 3 once the sync went stale", got.APIHits)
	}
}

func TestMigrateLegacyUsageSeedsTodayAndDropsStaleDay(t *testing.T) {
	now := time.Now()
	today := now.Format("2006-01-02")

	fresh := &UsageData{
		LastResetDay:         today,
		APIHitsUsed:          7,
		DownloadsUsed:        3,
		AllTimeAPIHitsUsed:   20,
		AllTimeDownloadsUsed: 11,
	}
	if !migrateLegacyUsage(fresh, today, now) {
		t.Fatal("expected a same-day legacy entry to migrate")
	}
	if len(fresh.APIHitTimes) != 7 || len(fresh.DownloadTimes) != 3 {
		t.Fatalf("expected today's counters seeded as hits, got %d/%d", len(fresh.APIHitTimes), len(fresh.DownloadTimes))
	}
	if fresh.AllTimeAPIHitsUsed != 20 || fresh.AllTimeDownloadsUsed != 11 {
		t.Fatalf("expected all-time untouched, got %d/%d", fresh.AllTimeAPIHitsUsed, fresh.AllTimeDownloadsUsed)
	}
	if fresh.LastResetDay != "" || fresh.APIHitsUsed != 0 || fresh.DownloadsUsed != 0 {
		t.Fatal("expected legacy fields cleared after migration")
	}

	stale := &UsageData{
		LastResetDay:  now.Add(-24 * time.Hour).Format("2006-01-02"),
		APIHitsUsed:   7,
		DownloadsUsed: 3,
	}
	if !migrateLegacyUsage(stale, today, now) {
		t.Fatal("expected a stale-day legacy entry to migrate")
	}
	if len(stale.APIHitTimes) != 0 || len(stale.DownloadTimes) != 0 {
		t.Fatal("expected a stale day's counters dropped, not seeded")
	}
	// Pre-all-time databases carry the daily counts as the only usage ever
	// recorded; migration folds them in once.
	if stale.AllTimeAPIHitsUsed != 7 || stale.AllTimeDownloadsUsed != 3 {
		t.Fatalf("expected all-time backfilled from legacy counters, got %d/%d", stale.AllTimeAPIHitsUsed, stale.AllTimeDownloadsUsed)
	}
}
