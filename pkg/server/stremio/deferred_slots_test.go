package stremio

import (
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/session"
)

func slotTestPlaylist(key StreamSlotKey, links ...string) *playlistResult {
	list := &playlistResult{Params: &query.SearchParams{ContentType: key.ContentType, ID: key.ID}}
	for i, link := range links {
		list.Candidates = append(list.Candidates, triage.Candidate{Release: &release.Release{
			Title: "Movie.2160p",
			Link:  link,
		}})
		list.SlotPaths = append(list.SlotPaths, key.SlotPath(i))
	}
	return list
}

// A play request needs one slot, not a hundred. Building a session for every
// candidate put a full fan-out in front of the stream the user is waiting on.
func TestPlaylistRefreshLeavesUntouchedSlotsToFirstPlay(t *testing.T) {
	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	stream := &auth.Stream{Username: key.StreamID}
	list := slotTestPlaylist(key, "https://idx.invalid/nzb/1", "https://idx.invalid/nzb/2", "https://idx.invalid/nzb/3")

	server.refreshLiveDeferredSessionsForPlaylist(list, key, stream)

	for i := range list.Candidates {
		if sess := manager.PeekSession(key.SlotPath(i)); sess != nil {
			t.Fatalf("slot %d was created up front; it should wait for a play", i)
		}
	}

	// The slot actually asked for still resolves — lazily, from the same list.
	if _, err := server.resolveStreamSlotFromPlaylist(key, 1, list, stream); err != nil {
		t.Fatalf("resolveStreamSlotFromPlaylist returned error: %v", err)
	}
	if manager.PeekSession(key.SlotPath(1)) == nil {
		t.Fatal("expected the played slot to have a session")
	}
}

// A live slot whose release moved under it must be re-bound, or the client is
// served the release the slot used to mean.
func TestPlaylistRefreshRebindsLiveSlots(t *testing.T) {
	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	stream := &auth.Stream{Username: key.StreamID}

	old := slotTestPlaylist(key, "https://idx.invalid/nzb/old")
	live, err := server.resolveStreamSlotFromPlaylist(key, 0, old, stream)
	if err != nil {
		t.Fatalf("resolveStreamSlotFromPlaylist returned error: %v", err)
	}
	// A slot just handed to playback is protected from replacement; age it out
	// of that grace window so the rebind under test is the one being checked.
	live.LastAccess = time.Now().Add(-time.Minute)

	rebuilt := slotTestPlaylist(key, "https://idx.invalid/nzb/new")
	server.refreshLiveDeferredSessionsForPlaylist(rebuilt, key, stream)

	sess := manager.PeekSession(key.SlotPath(0))
	if sess == nil {
		t.Fatal("expected the live slot to survive the refresh")
	}
	if got := sess.Release().Link; got != "https://idx.invalid/nzb/new" {
		t.Fatalf("live slot still bound to %q", got)
	}
}
