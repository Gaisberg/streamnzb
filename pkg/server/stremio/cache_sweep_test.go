package stremio

import (
	"testing"
	"time"
)

func TestSweepSearchCachesDropsExpiredEntriesWithoutAnyRead(t *testing.T) {
	s := &Server{}
	now := time.Unix(1_700_000_000, 0)

	s.playlistCache.Store("fresh", &playlistCacheEntry{result: &playlistResult{}, until: now.Add(time.Hour)})
	s.playlistCache.Store("stale", &playlistCacheEntry{result: &playlistResult{}, until: now.Add(-time.Minute)})
	s.rawSearchCache.Store("fresh", &rawSearchCacheEntry{raw: &rawSearchResult{}, until: now.Add(time.Hour)})
	s.rawSearchCache.Store("stale", &rawSearchCacheEntry{raw: &rawSearchResult{}, until: now.Add(-time.Minute)})

	// The point of the sweep: nothing reads these keys again, so before it
	// existed the stale entries were held until a config change cleared the map.
	playlists, raws, _ := s.sweepSearchCaches(now)
	if playlists != 1 || raws != 1 {
		t.Fatalf("swept %d playlists and %d raw entries, want 1 and 1", playlists, raws)
	}

	if _, ok := s.playlistCache.Load("stale"); ok {
		t.Fatal("the expired playlist entry survived the sweep")
	}
	if _, ok := s.rawSearchCache.Load("stale"); ok {
		t.Fatal("the expired raw-search entry survived the sweep")
	}
	if _, ok := s.playlistCache.Load("fresh"); !ok {
		t.Fatal("the sweep dropped a playlist entry that was still valid")
	}
	if _, ok := s.rawSearchCache.Load("fresh"); !ok {
		t.Fatal("the sweep dropped a raw-search entry that was still valid")
	}
}

func TestSweepSearchCachesExpiresIdleNextReleaseCursors(t *testing.T) {
	s := &Server{}
	now := time.Unix(1_700_000_000, 0)
	ttl := s.playlistCacheTTL()

	s.nextReleaseIndex.Store("active", &nextReleaseCursor{nextIndex: 2, lastTouched: now.Add(-time.Minute)})
	s.nextReleaseIndex.Store("abandoned", &nextReleaseCursor{nextIndex: 5, lastTouched: now.Add(-2 * ttl)})

	// Cursors had no deadline at all before this: they were only ever cleared
	// wholesale by a config change.
	_, _, cursors := s.sweepSearchCaches(now)
	if cursors != 1 {
		t.Fatalf("swept %d cursors, want 1", cursors)
	}
	if _, ok := s.nextReleaseIndex.Load("abandoned"); ok {
		t.Fatal("an abandoned cursor survived the sweep")
	}
	if _, ok := s.nextReleaseIndex.Load("active"); !ok {
		t.Fatal("the sweep dropped a cursor that was still in use")
	}
}

func TestSweepSearchCachesDropsUnusableEntries(t *testing.T) {
	s := &Server{}
	now := time.Unix(1_700_000_000, 0)

	// Nothing can ever be served from these, so holding them is pure leak.
	s.playlistCache.Store("nil-entry", (*playlistCacheEntry)(nil))
	s.rawSearchCache.Store("wrong-type", "not an entry")
	s.nextReleaseIndex.Store("nil-cursor", (*nextReleaseCursor)(nil))

	playlists, raws, cursors := s.sweepSearchCaches(now)
	if playlists != 1 || raws != 1 || cursors != 1 {
		t.Fatalf("swept %d/%d/%d, want 1/1/1", playlists, raws, cursors)
	}
}

func TestShutdownStopsTheJanitorAndIsIdempotent(t *testing.T) {
	s := &Server{stopCh: make(chan struct{})}

	stopped := make(chan struct{})
	go func() {
		s.searchCacheJanitor()
		close(stopped)
	}()

	s.Shutdown()
	s.Shutdown() // reachable from both a signal and a failed listener

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the janitor did not stop on shutdown")
	}

	// Tests build bare Servers, which never started a janitor to stop.
	(&Server{}).Shutdown()
}
