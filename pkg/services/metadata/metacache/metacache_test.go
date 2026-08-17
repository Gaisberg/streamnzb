package metacache

import (
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
)

// TestCacheSurvivesInstanceRebuild is the reload scenario: metadata clients are
// rebuilt on config reload, so a fresh Cache over the same persistent store
// must still hit entries the previous instance wrote.
func TestCacheSurvivesInstanceRebuild(t *testing.T) {
	sm, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("open persistence manager: %v", err)
	}
	// Close before TempDir cleanup: Windows cannot delete an open SQLite file.
	t.Cleanup(func() { _ = sm.Close() })

	first := New(sm.MetadataCacheStore(), "tmdb")
	first.Put("/movie/603?", []byte(`body`), time.Hour)

	rebuilt := New(sm.MetadataCacheStore(), "tmdb")
	got, ok := rebuilt.Get("/movie/603?")
	if !ok || string(got) != "body" {
		t.Fatalf("expected L2 hit after instance rebuild, got %q ok=%v", got, ok)
	}
}

func TestCacheNilStoreIsInMemoryOnly(t *testing.T) {
	c := New(nil, "tmdb")
	c.Put("/k?", []byte(`v`), time.Hour)
	if got, ok := c.Get("/k?"); !ok || string(got) != "v" {
		t.Fatalf("expected in-memory hit, got %q ok=%v", got, ok)
	}

	// A second instance shares nothing without a store.
	if _, ok := New(nil, "tmdb").Get("/k?"); ok {
		t.Fatal("nil-store caches must not share entries")
	}
}

func TestCacheExpiry(t *testing.T) {
	c := New(nil, "tmdb")
	c.Put("/k?", []byte(`v`), time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("/k?"); ok {
		t.Fatal("expired L1 entry must miss")
	}
}

func TestCacheNilReceiver(t *testing.T) {
	var c *Cache
	c.Put("/k?", []byte(`v`), time.Hour)
	if _, ok := c.Get("/k?"); ok {
		t.Fatal("nil cache must miss")
	}
}
