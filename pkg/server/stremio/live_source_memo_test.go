package stremio

import (
	"context"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/search/triage"
)

func memoTestPlaylist(title string) *playlistResult {
	return &playlistResult{
		Candidates: []triage.Candidate{{Release: &release.Release{Title: title, Link: "https://idx.invalid/nzb/1"}}},
		Params:     &query.SearchParams{ContentType: "movie", ID: "tt123"},
	}
}

func memoCacheEntry(t *testing.T, s *Server, key StreamSlotKey) *playlistCacheEntry {
	t.Helper()
	v, ok := s.playlistCache.Load(key.CacheKey())
	if !ok {
		t.Fatal("playlist entry vanished from the cache")
	}
	ent, _ := v.(*playlistCacheEntry)
	if ent == nil {
		t.Fatal("playlist entry has the wrong type")
	}
	return ent
}

// Re-resolving live source state walks every candidate and asks the library
// about the content, and a single play reads the playlist several times. The
// derived view is memoized so those reads cost one derivation, not one each.
func TestPlaylistReadReusesTheDerivedLiveView(t *testing.T) {
	server := &Server{config: &config.Config{}}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	stream := &auth.Stream{Username: key.StreamID}

	stored := memoTestPlaylist("stored")
	derived := memoTestPlaylist("already-derived")
	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result:    stored,
		until:     time.Now().Add(time.Hour),
		live:      derived,
		liveUntil: time.Now().Add(liveSourceMemoTTL),
	})

	list, err := server.buildPlaylist(context.Background(), key, false, stream)
	if err != nil {
		t.Fatalf("buildPlaylist returned error: %v", err)
	}
	if list != derived {
		t.Fatal("playlist read re-derived the live view instead of reusing the memo")
	}
	if ent := memoCacheEntry(t, server, key); ent.live != derived {
		t.Fatal("the refreshed entry dropped the memo it just reused")
	}
}

// The memo is a short-lived view of state that does change — a release landing
// in the library, a daily budget coming back — so it has to expire.
func TestPlaylistReadRederivesAnExpiredLiveView(t *testing.T) {
	server := &Server{config: &config.Config{}}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	stream := &auth.Stream{Username: key.StreamID}

	stored := memoTestPlaylist("stored")
	stale := memoTestPlaylist("stale")
	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result:    stored,
		until:     time.Now().Add(time.Hour),
		live:      stale,
		liveUntil: time.Now().Add(-time.Second),
	})

	list, err := server.buildPlaylist(context.Background(), key, false, stream)
	if err != nil {
		t.Fatalf("buildPlaylist returned error: %v", err)
	}
	if list == stale {
		t.Fatal("an expired memo was served")
	}
	ent := memoCacheEntry(t, server, key)
	if ent.live == stale {
		t.Fatal("the expired memo was kept")
	}
	if !ent.liveUntil.After(time.Now()) {
		t.Fatal("the re-derived view was stored already expired")
	}
}

// A writer that changes the cached list must not leave the view derived from
// the old one behind it.
func TestMarkingReleaseUnavailableDropsTheLiveView(t *testing.T) {
	server := &Server{config: &config.Config{}}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}

	stored := memoTestPlaylist("stored")
	stored.Candidates[0].Release.DetailsURL = "https://idx.invalid/details/1"
	stored.Candidates = append(stored.Candidates, triage.Candidate{Release: &release.Release{
		Title:      "second",
		Link:       "https://idx.invalid/nzb/2",
		DetailsURL: "https://idx.invalid/details/2",
	}})
	stored.SlotPaths = []string{key.SlotPath(0), key.SlotPath(1)}
	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result:    stored,
		until:     time.Now().Add(time.Hour),
		live:      memoTestPlaylist("derived"),
		liveUntil: time.Now().Add(liveSourceMemoTTL),
	})

	server.markCachedReleaseUnavailable(key, "https://idx.invalid/details/1", key.SlotPath(0))

	if ent := memoCacheEntry(t, server, key); ent.live != nil {
		t.Fatal("a changed playlist kept the view derived from the old one")
	}
}
