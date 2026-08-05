package nntp

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
	providerUsageStateOnce sync.Once
	providerUsageStateMgr  *persistence.StateManager
	providerUsageStateErr  error
)

func testProviderUsageState(t *testing.T) *persistence.StateManager {
	t.Helper()

	providerUsageStateOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "streamnzb-provider-usage-")
		if err != nil {
			providerUsageStateErr = err
			return
		}
		providerUsageStateMgr, providerUsageStateErr = persistence.GetManager(tempDir)
	})
	if providerUsageStateErr != nil {
		t.Fatalf("GetManager: %v", providerUsageStateErr)
	}
	return providerUsageStateMgr
}

func newTestProviderUsageManager(t *testing.T) *ProviderUsageManager {
	t.Helper()
	return &ProviderUsageManager{
		state: testProviderUsageState(t),
		data:  make(map[string]*ProviderUsageData),
	}
}

func TestGetUsageResetsDailyProviderBytesWithoutChangingAllTime(t *testing.T) {
	um := newTestProviderUsageManager(t)
	name := "provider-reset-get"
	um.data[name] = &ProviderUsageData{
		LastResetDay: time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
		TotalBytes:   7,
		AllTimeBytes: 42,
	}

	got := um.GetUsage(name)

	if got.TotalBytes != 0 {
		t.Fatalf("expected daily bytes to reset, got %d", got.TotalBytes)
	}
	if got.AllTimeBytes != 42 {
		t.Fatalf("expected all-time bytes unchanged, got %d", got.AllTimeBytes)
	}
}

func TestGetUsageMigratesLegacyTotalsOutOfDailyCount(t *testing.T) {
	um := newTestProviderUsageManager(t)
	name := "provider-legacy-migrate"
	um.data[name] = &ProviderUsageData{TotalBytes: 21}

	got := um.GetUsage(name)

	if got.TotalBytes != 0 {
		t.Fatalf("expected legacy daily bytes to reset, got %d", got.TotalBytes)
	}
	if got.AllTimeBytes != 21 {
		t.Fatalf("expected legacy total to be preserved in all-time bytes, got %d", got.AllTimeBytes)
	}
}

func TestAddBytesAfterRolloverStartsNewDailyCount(t *testing.T) {
	um := newTestProviderUsageManager(t)
	name := "provider-reset-add"
	um.data[name] = &ProviderUsageData{
		LastResetDay: time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
		TotalBytes:   8,
		AllTimeBytes: 100,
	}

	um.AddBytes(name, 3)
	got := um.GetUsage(name)

	if got.TotalBytes != 3 {
		t.Fatalf("expected new daily bytes to start at 3, got %d", got.TotalBytes)
	}
	if got.AllTimeBytes != 103 {
		t.Fatalf("expected all-time bytes to increase by delta, got %d", got.AllTimeBytes)
	}
}

func TestClientPoolTotalMegabytesUsesDailyUsageAfterRollover(t *testing.T) {
	um := newTestProviderUsageManager(t)
	name := "provider-pool-total"
	const mb = 1024 * 1024
	um.data[name] = &ProviderUsageData{
		LastResetDay: time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
		TotalBytes:   20 * mb,
		AllTimeBytes: 20 * mb,
	}

	pool := NewClientPool("example.invalid", 119, false, "user", "pass", 1)
	defer pool.Shutdown()
	pool.RestoreTotalBytes(20 * mb)
	pool.SetUsageManager(name, um)

	if got := pool.TotalMegabytes(); got != 0 {
		t.Fatalf("expected stale daily total to reset to 0 MB, got %v", got)
	}

	um.AddBytes(name, 2*mb)
	if got := pool.TotalMegabytes(); got != 2 {
		t.Fatalf("expected provider total to use daily usage bytes, got %v", got)
	}
}
