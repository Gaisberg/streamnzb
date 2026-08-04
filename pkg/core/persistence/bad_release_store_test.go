package persistence

import (
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
)

func newTestBadReleaseStore(t *testing.T) *BadReleaseStore {
	t.Helper()
	logger.Init("ERROR")
	tempDir, err := os.MkdirTemp("", "badrel_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })
	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	bs := mgr.BadReleaseStore()
	if bs == nil {
		t.Fatal("nil bad release store")
	}
	return bs
}

func TestBadReleaseStoreMarkFilterForget(t *testing.T) {
	bs := newTestBadReleaseStore(t)

	urlBad := "https://idx/details/bad"
	urlGood := "https://idx/details/good"

	bs.MarkBad(urlBad, "Broken.Release.2160p", "segment unavailable: 430", 14*24*time.Hour)

	bad := bs.BadSet([]string{urlBad, urlGood})
	if _, ok := bad[urlBad]; !ok {
		t.Fatal("expected marked release to be in bad set")
	}
	if _, ok := bad[urlGood]; ok {
		t.Fatal("unmarked release must not be in bad set")
	}

	// Re-marking refreshes rather than duplicating; Forget clears the verdict.
	bs.MarkBad(urlBad, "Broken.Release.2160p", "again", 14*24*time.Hour)
	bs.Forget(urlBad)
	if bad := bs.BadSet([]string{urlBad}); len(bad) != 0 {
		t.Fatal("forgotten release must not be in bad set")
	}
}

func TestBadReleaseStoreTTLExpiry(t *testing.T) {
	bs := newTestBadReleaseStore(t)

	url := "https://idx/details/expiring"
	// Expired-in-the-past entry must not filter, and PurgeExpired removes it.
	bs.MarkBad(url, "t", "reason", 1*time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if bad := bs.BadSet([]string{url}); len(bad) != 0 {
		t.Fatal("expired verdict must not be in bad set")
	}
	bs.PurgeExpired()

	// Zero/negative TTL is a no-op (feature disabled).
	bs.MarkBad(url, "t", "reason", 0)
	if bad := bs.BadSet([]string{url}); len(bad) != 0 {
		t.Fatal("zero-TTL mark must be a no-op")
	}
}
