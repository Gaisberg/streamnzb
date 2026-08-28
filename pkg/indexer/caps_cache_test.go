package indexer

import (
	"testing"
	"time"
)

func testCaps() *Caps {
	return &Caps{
		Searching: CapsSearching{
			MovieSearch:                true,
			TVSearch:                   true,
			MovieSearchSupportedParams: map[string]bool{"imdbid": true},
		},
		Limits:        CapsLimits{Max: 100},
		RetentionDays: 4000,
	}
}

func TestCapsStoreRoundTripsAcrossInstances(t *testing.T) {
	state := testUsageManagerState(t)

	store := NewCapsStore(state)
	store.Put("geek", "https://api.example|/api|abcd", testCaps())

	reloaded := NewCapsStore(state)
	caps, fresh, ok := reloaded.Lookup("geek", "https://api.example|/api|abcd")
	if !ok || !fresh {
		t.Fatalf("Lookup after reload = ok=%v fresh=%v, want a fresh hit", ok, fresh)
	}
	if !caps.Searching.MovieSearch || !caps.Searching.MovieSearchSupportedParams["imdbid"] || caps.Limits.Max != 100 {
		t.Fatalf("cached caps lost content: %+v", caps)
	}
}

func TestCapsStoreMissesOnIdentityChange(t *testing.T) {
	store := NewCapsStore(nil)
	store.Put("geek", "old-identity", testCaps())

	if _, _, ok := store.Lookup("geek", "new-identity"); ok {
		t.Fatal("a changed URL/key identity must invalidate the cached caps")
	}
}

func TestCapsStoreReportsStaleEntries(t *testing.T) {
	store := NewCapsStore(nil)
	store.data["geek"] = &capsCacheEntry{
		Identity:  "id",
		FetchedAt: time.Now().Add(-CapsCacheTTL - time.Hour),
		Caps:      testCaps(),
	}

	caps, fresh, ok := store.Lookup("geek", "id")
	if !ok || fresh {
		t.Fatalf("Lookup = ok=%v fresh=%v, want a stale hit for fallback use", ok, fresh)
	}
	if caps == nil {
		t.Fatal("a stale hit must still carry the caps")
	}
}

func TestCapsStoreSyncDropsDeletedIndexers(t *testing.T) {
	store := NewCapsStore(nil)
	store.Put("keep", "id", testCaps())
	store.Put("gone", "id", testCaps())

	store.Sync([]string{"keep"})

	if _, _, ok := store.Lookup("keep", "id"); !ok {
		t.Fatal("a still-configured indexer must keep its caps")
	}
	if _, _, ok := store.Lookup("gone", "id"); ok {
		t.Fatal("a deleted indexer must not leave caps behind")
	}
}
