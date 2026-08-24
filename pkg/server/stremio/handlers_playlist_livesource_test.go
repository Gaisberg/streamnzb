package stremio

import (
	"context"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/triage"
)

// budgetIndexer reports a fixed daily download budget and nothing else.
type budgetIndexer struct {
	name      string
	limit     int
	remaining int
}

func (b *budgetIndexer) Search(context.Context, indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return nil, nil
}
func (b *budgetIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (b *budgetIndexer) Ping(context.Context) error                          { return nil }
func (b *budgetIndexer) Name() string                                        { return b.name }
func (b *budgetIndexer) GetUsage() indexer.Usage {
	return indexer.Usage{DownloadsLimit: b.limit, DownloadsRemaining: b.remaining}
}

func candidateFrom(title, detailsURL string, src interface{}) triage.Candidate {
	return triage.Candidate{Release: &release.Release{
		Title:         title,
		Link:          "https://indexer.example/getnzb/" + title,
		DetailsURL:    detailsURL,
		Indexer:       "Treasuremaps",
		SourceIndexer: src,
	}}
}

func TestApplyLiveSourceStateDropsSpentBudgetAndKeepsSlotPaths(t *testing.T) {
	spent := &budgetIndexer{name: "Spent", limit: 10, remaining: 0}
	healthy := &budgetIndexer{name: "Healthy", limit: 10, remaining: 4}
	unlimited := &budgetIndexer{name: "Unlimited"}

	srv := &Server{config: &config.Config{}}
	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt0111161"}
	list := &playlistResult{Candidates: []triage.Candidate{
		candidateFrom("A", "https://x/a", spent),
		candidateFrom("B", "https://x/b", healthy),
		candidateFrom("C", "https://x/c", spent),
		candidateFrom("D", "https://x/d", unlimited),
	}}

	got := srv.applyLiveSourceState(list, key)

	if len(got.Candidates) != 2 {
		t.Fatalf("kept %d candidates, want the 2 whose indexer can still grab", len(got.Candidates))
	}
	for _, c := range got.Candidates {
		if c.Release.Title == "A" || c.Release.Title == "C" {
			t.Fatalf("candidate %q came from a spent download budget and should have been dropped", c.Release.Title)
		}
	}
	// The survivors must keep the slot paths they had before the drop, or a
	// client holding /play/<key>:3 lands on a different release.
	want := []string{key.SlotPath(1), key.SlotPath(3)}
	if len(got.SlotPaths) != len(want) {
		t.Fatalf("slot paths %v, want %v", got.SlotPaths, want)
	}
	for i, path := range want {
		if got.SlotPaths[i] != path {
			t.Fatalf("slot path %d = %q, want %q", i, got.SlotPaths[i], path)
		}
	}
	// The caller's list is the cached one and must be left whole, so a budget
	// that comes back simply stops filtering instead of forcing a re-search.
	if len(list.Candidates) != 4 {
		t.Fatalf("source list now has %d candidates; the cached entry must not be mutated", len(list.Candidates))
	}
}

func TestApplyLiveSourceStateLeavesHealthyListAlone(t *testing.T) {
	healthy := &budgetIndexer{name: "Healthy", limit: 10, remaining: 4}
	srv := &Server{config: &config.Config{}}
	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt0111161"}
	list := &playlistResult{Candidates: []triage.Candidate{
		candidateFrom("A", "https://x/a", healthy),
		candidateFrom("B", "https://x/b", healthy),
	}}

	if got := srv.applyLiveSourceState(list, key); got != list {
		t.Fatalf("a list with nothing to re-resolve should be returned as-is, not cloned")
	}
}

func TestLiveSourceActionRebindsBeforeCheckingBudget(t *testing.T) {
	// The release still names the indexer it came from, and that indexer is out
	// of downloads. Because the library holds its NZB, no grab is needed — so it
	// must rebind rather than drop.
	spent := &budgetIndexer{name: "Spent", limit: 10, remaining: 0}
	srv := &Server{config: &config.Config{}}
	rel := candidateFrom("A", "https://x/a", spent).Release
	items := map[string]*persistence.LibraryItem{
		"https://x/a": {DetailsURL: "https://x/a", IndexerName: "Treasuremaps", NZBData: []byte("<nzb/>")},
	}

	action := srv.liveSourceAction(rel, items, map[indexer.Indexer]bool{})
	if action != liveSourceRebindLibrary {
		t.Fatalf("action = %d, want a library rebind so the spent budget never applies", action)
	}

	// Without the library row the same release is a dead candidate.
	if action := srv.liveSourceAction(rel, nil, map[indexer.Indexer]bool{}); action != liveSourceDrop {
		t.Fatalf("action = %d, want a drop when nothing local can serve it", action)
	}
}

func TestRebindReleaseToLibraryMatchesFreshLibraryLabelling(t *testing.T) {
	item := &persistence.LibraryItem{
		ReleaseTitle: "A",
		DetailsURL:   "https://x/a",
		IndexerName:  "Treasuremaps",
		NZBData:      []byte("<nzb/>"),
	}
	rel := candidateFrom("A", "https://x/a", &budgetIndexer{name: "Spent", limit: 1}).Release
	indexerLink := rel.Link

	rebindReleaseToLibrary(rel, item)

	if !rel.IsLibraryResult() {
		t.Fatalf("release should read as a library hit after the rebind")
	}
	if got, ok := rel.SourceIndexer.(*persistence.LibraryItem); !ok || got != item {
		t.Fatalf("SourceIndexer = %T, want the library item so playback uses its stored NZB", rel.SourceIndexer)
	}
	if want := convertLibraryItemToRelease(item).Indexer; rel.Indexer != want {
		t.Fatalf("indexer label = %q, want %q — the same a fresh library hit would carry", rel.Indexer, want)
	}
	if rel.Link != indexerLink {
		t.Fatalf("link changed to %q; it stays on the indexer so a grab remains available as fallback", rel.Link)
	}
}

func TestFilterPlaylistByOrderPrefersSlotPathsOverIndex(t *testing.T) {
	// A drop already removed candidate 0, so the stored order's parsed indexes
	// no longer address the same releases. Slot paths must win.
	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt0111161"}
	list := &playlistResult{
		Candidates: []triage.Candidate{
			candidateFrom("B", "https://x/b", nil),
			candidateFrom("C", "https://x/c", nil),
		},
		SlotPaths: []string{key.SlotPath(1), key.SlotPath(2)},
	}

	got := filterPlaylistByOrder(list, key, []string{key.SlotPath(2), key.SlotPath(1)})

	if len(got.Candidates) != 2 {
		t.Fatalf("kept %d candidates, want both", len(got.Candidates))
	}
	if got.Candidates[0].Release.Title != "C" || got.Candidates[1].Release.Title != "B" {
		t.Fatalf("order resolved to %q,%q; want C,B as the slot paths name them",
			got.Candidates[0].Release.Title, got.Candidates[1].Release.Title)
	}
}

func TestFilterPlaylistToAvailableForAIOStreamsKeepsSlotPathsInLockstep(t *testing.T) {
	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt0111161"}
	list := &playlistResult{
		Candidates: []triage.Candidate{
			candidateFrom("A", "https://x/a", nil),
			candidateFrom("B", "https://x/b", nil),
			candidateFrom("C", "https://x/c", nil),
		},
		SlotPaths:              []string{key.SlotPath(0), key.SlotPath(1), key.SlotPath(2)},
		UnavailableDetailsURLs: map[string]bool{"https://x/b": true},
	}

	got := filterPlaylistToAvailableForAIOStreams(list)

	if len(got.Candidates) != 2 {
		t.Fatalf("kept %d candidates, want 2", len(got.Candidates))
	}
	want := []string{key.SlotPath(0), key.SlotPath(2)}
	if len(got.SlotPaths) != len(want) {
		t.Fatalf("slot paths %v, want %v", got.SlotPaths, want)
	}
	for i, path := range want {
		if got.SlotPaths[i] != path {
			t.Fatalf("slot path %d = %q, want %q", i, got.SlotPaths[i], path)
		}
	}
}

func TestApplyLiveSourceStatePromotesACopyFromAnIndexerWithBudget(t *testing.T) {
	spent := &budgetIndexer{name: "Spent", limit: 10, remaining: 0}
	healthy := &budgetIndexer{name: "Healthy", limit: 10, remaining: 4}

	srv := &Server{config: &config.Config{}}
	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt0111161"}
	merged := candidateFrom("A", "https://x/a", spent)
	variant := candidateFrom("A", "https://x/a-copy", healthy).Release
	merged.Release.Variants = []*release.Release{variant}
	list := &playlistResult{Candidates: []triage.Candidate{
		merged,
		candidateFrom("B", "https://x/b", spent),
	}}

	got := srv.applyLiveSourceState(list, key)

	if len(got.Candidates) != 1 {
		t.Fatalf("kept %d candidates, want only the release with a grabbable copy", len(got.Candidates))
	}
	kept := got.Candidates[0].Release
	if kept.DetailsURL != "https://x/a-copy" {
		t.Fatalf("expected the copy with budget to lead, got %q", kept.DetailsURL)
	}
	// The spent copy stays behind it: a daily budget comes back, and the copy
	// is one failover hop away either way.
	if kept.CopyCount() != 2 || kept.CopyAt(1).DetailsURL != "https://x/a" {
		t.Fatalf("expected the spent copy to survive as a variant, got %d copies", kept.CopyCount())
	}
	if got.SlotPaths[0] != key.SlotPath(0) {
		t.Fatalf("slot path = %q, want %q", got.SlotPaths[0], key.SlotPath(0))
	}
	if len(list.Candidates[0].Release.Variants) != 1 || list.Candidates[0].Release.DetailsURL != "https://x/a" {
		t.Fatal("the cached list must not be mutated by the promotion")
	}
}
