package stremio

import (
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

// mergedTestRelease is one release listed by two indexers, as the search merge
// hands it to the playlist: one candidate, two playable NZBs.
func mergedTestRelease() *release.Release {
	return &release.Release{
		Title:      "Movie.2160p.Remux.HDR10-FraMeSToR",
		Link:       "https://geek.invalid/nzb/1",
		DetailsURL: "https://geek.invalid/details/1",
		Indexer:    "NZBGeek",
		Variants: []*release.Release{{
			Title:      "Movie.2160p.Remux.HDR10-FraMeSToR",
			Link:       "https://slug.invalid/nzb/2",
			DetailsURL: "https://slug.invalid/details/2",
			Indexer:    "DrunkenSlug",
		}},
	}
}

func variantTestPlaylist(key StreamSlotKey, rel *release.Release) *playlistResult {
	return &playlistResult{
		Candidates: []triage.Candidate{{Release: rel}},
		Params:     &query.SearchParams{ContentType: key.ContentType, ID: key.ID},
	}
}

func TestResolveStreamSlotPlaysTheCopyTheCursorPointsAt(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	slotPath := key.SlotPath(0)
	list := variantTestPlaylist(key, mergedTestRelease())
	// Walking copies is opt-in, so this stream asks for it.
	stream := &auth.Stream{Username: key.StreamID, VariantAttempts: 2}

	sess, err := server.resolveStreamSlotFromPlaylist(key, 0, list, stream)
	if err != nil {
		t.Fatalf("resolveStreamSlotFromPlaylist returned error: %v", err)
	}
	if got := sess.Release().Indexer; got != "NZBGeek" {
		t.Fatalf("expected the primary copy to play first, got %q", got)
	}

	if !manager.AdvanceSlotCopy(slotPath, stream.EffectiveVariantAttempts()) {
		t.Fatal("expected the slot to have an untried copy")
	}
	manager.DeleteSession(slotPath)

	sess, err = server.resolveStreamSlotFromPlaylist(key, 0, list, stream)
	if err != nil {
		t.Fatalf("resolveStreamSlotFromPlaylist after advancing returned error: %v", err)
	}
	if got := sess.Release().Indexer; got != "DrunkenSlug" {
		t.Fatalf("expected the slot to rebuild against the next copy, got %q", got)
	}
	if sess.ID != slotPath {
		t.Fatalf("expected the slot id to stay %q so the client is not redirected, got %q", slotPath, sess.ID)
	}
	if manager.GetSlotFailedDuringPlayback(slotPath) {
		t.Fatal("advancing to another copy must not mark the slot failed")
	}
}

func TestAdvanceSlotCopyStopsAtTheAttemptCap(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	slotPath := "stream_test:movie:tt123:0"
	manager.NoteSlotCopies(slotPath, 3)

	if !manager.AdvanceSlotCopy(slotPath, 2) {
		t.Fatal("expected the first advance to be allowed")
	}
	if manager.SlotCopyIndex(slotPath) != 1 {
		t.Fatalf("expected the cursor at copy 1, got %d", manager.SlotCopyIndex(slotPath))
	}
	if manager.AdvanceSlotCopy(slotPath, 2) {
		t.Fatal("expected the attempt cap to stop the second advance")
	}
	// Copy 2 is the third of three, so a cap of 3 reaches it — the cap counts
	// attempts, not variants.
	if !manager.AdvanceSlotCopy(slotPath, 3) {
		t.Fatal("expected a cap of 3 to allow the third copy")
	}
	if manager.SlotCopyIndex(slotPath) != 2 {
		t.Fatalf("expected the cursor at copy 2, got %d", manager.SlotCopyIndex(slotPath))
	}
	if manager.AdvanceSlotCopy(slotPath, 10) {
		t.Fatal("expected no advance past the last copy")
	}
}

func TestMergeOnlyStreamNeverSwitchesCopies(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	slotPath := "stream_test:movie:tt123:0"
	manager.NoteSlotCopies(slotPath, 3)

	stream := &auth.Stream{Username: "stream_test", VariantAttempts: 1}
	if manager.AdvanceSlotCopy(slotPath, stream.EffectiveVariantAttempts()) {
		t.Fatal("a one-attempt stream must keep its copies for display only")
	}
}

func TestCopyScopedVerdictLeavesTheReleasePlayable(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	slotPath := key.SlotPath(0)
	rel := mergedTestRelease()

	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result: variantTestPlaylist(key, rel),
		until:  time.Now().Add(time.Minute),
	})

	failedSess := &session.Session{ID: slotPath}
	failedSess.SetRelease(&release.Release{Title: rel.Title, DetailsURL: rel.DetailsURL})
	server.applyReportedBadReleaseToCaches(failedSess, availnzb.SentOutcome(false), true, true)

	cached, ok := server.playlistCache.Load(key.CacheKey())
	if !ok {
		t.Fatal("expected the playlist cache entry to survive a copy-scoped verdict")
	}
	list := cached.(*playlistCacheEntry).result
	if len(list.Candidates) != 1 {
		t.Fatalf("expected the release to stay in the play list, got %d candidates", len(list.Candidates))
	}
	if manager.GetSlotFailedDuringPlayback(slotPath) {
		t.Fatal("a copy-scoped verdict must leave the slot playable")
	}
}

func TestReleaseVerdictStillRetiresEveryCopy(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	slotPath := key.SlotPath(0)
	rel := mergedTestRelease()

	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result: variantTestPlaylist(key, rel),
		until:  time.Now().Add(time.Minute),
	})

	// The last copy is the one that failed, so the candidate has to go even
	// though it is not the copy the merged release leads with.
	failedSess := &session.Session{ID: slotPath}
	failedSess.SetRelease(&release.Release{Title: rel.Title, DetailsURL: rel.Variants[0].DetailsURL})
	server.applyReportedBadReleaseToCaches(failedSess, availnzb.SentOutcome(false), true, false)

	cached, ok := server.playlistCache.Load(key.CacheKey())
	if ok {
		list := cached.(*playlistCacheEntry).result
		if len(list.Candidates) != 0 {
			t.Fatalf("expected the release to leave the play list, got %d candidates", len(list.Candidates))
		}
	}
	if !manager.GetSlotFailedDuringPlayback(slotPath) {
		t.Fatal("expected the slot to be marked failed once the release is given up on")
	}
}

func TestDedupeSearchResultsMergesCopiesOfOneRelease(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	server := &Server{config: &config.Config{}}
	releases := []*release.Release{
		{Title: "Movie.2160p.Remux-GRP", DetailsURL: "https://geek.invalid/1", Indexer: "NZBGeek"},
		{Title: "Movie.2160p.Remux-GRP", DetailsURL: "https://slug.invalid/2", Indexer: "DrunkenSlug"},
		{Title: "Movie.1080p.WEB-DL-GRP", DetailsURL: "https://geek.invalid/3", Indexer: "NZBGeek"},
	}
	stream := &auth.Stream{Username: "stream_test"}

	got := server.dedupeSearchResults("stream_test", stream, releases, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 merged releases, got %d", len(got))
	}
	if got[0].CopyCount() != 2 {
		t.Fatalf("expected the duplicate to survive as a variant, got %d copies", got[0].CopyCount())
	}
}

// The counter behind the "Unique hits" statistic reads the merge output: an
// indexer is credited for the releases it alone carried, and the release two
// indexers both had credits neither.
func TestDedupeSearchResultsFeedsUniqueIndexerHits(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	server := &Server{config: &config.Config{}, uniqueIndexerHits: make(map[string]int64)}
	releases := []*release.Release{
		{Title: "Movie.2160p.Remux-GRP", DetailsURL: "https://geek.invalid/1", Indexer: "NZBGeek"},
		{Title: "Movie.2160p.Remux-GRP", DetailsURL: "https://slug.invalid/2", Indexer: "DrunkenSlug"},
		{Title: "Movie.1080p.WEB-DL-GRP", DetailsURL: "https://geek.invalid/3", Indexer: "NZBGeek"},
	}
	stream := &auth.Stream{Username: "stream_test"}

	merged := server.dedupeSearchResults("stream_test", stream, releases, nil)
	server.addUniqueIndexerHits(markUniqueIndexerHits(merged))

	hits := server.GetUniqueIndexerHits()
	if got := hits["NZBGeek"]; got != 1 {
		t.Fatalf("NZBGeek unique hits = %d, want 1 (only the 1080p release was its alone)", got)
	}
	if got := hits["DrunkenSlug"]; got != 0 {
		t.Fatalf("DrunkenSlug unique hits = %d, want 0 (its only release was shared)", got)
	}
}

func TestDefaultStreamMergesWithoutWalkingCopies(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	// The default is Merge only: the list is de-cluttered and the copies are
	// there for search-time swaps, but playback never spends a second startup
	// on another copy of the same release.
	stream := &auth.Stream{Username: "stream_test"}
	if got := stream.EffectiveVariantAttempts(); got != 1 {
		t.Fatalf("expected one attempt per release by default, got %d", got)
	}

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	slotPath := "stream_test:movie:tt123:0"
	manager.NoteSlotCopies(slotPath, 3)
	if manager.AdvanceSlotCopy(slotPath, stream.EffectiveVariantAttempts()) {
		t.Fatal("a default stream must not walk to another copy of the same release")
	}
}
