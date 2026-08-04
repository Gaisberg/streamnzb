package persistence

import (
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
)

func newTestLibraryStore(t *testing.T) *LibraryStore {
	t.Helper()
	logger.Init("ERROR")
	tempDir, err := os.MkdirTemp("", "library_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tempDir) })
	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	ls := mgr.LibraryStore()
	if ls == nil {
		t.Fatal("nil library store")
	}
	return ls
}

func TestLibraryMultiIDLookupAndCapColumns(t *testing.T) {
	ls := newTestLibraryStore(t)

	// Stored with imdb as canonical content_id, but also carrying tmdb + tvdb,
	// plus indexed capability columns.
	item := &LibraryItem{
		ContentType:  "movie",
		ContentID:    "tt0111161", // canonical (imdb)
		ImdbID:       "tt0111161",
		TmdbID:       "278",
		TvdbID:       "111",
		ReleaseTitle: "The Movie 2160p HEVC DV",
		DetailsURL:   "https://x/nzb/1",
		NZBData:      []byte("<nzb/>"),
		VideoCodec:   "hevc",
		Height:       2160,
		BitDepth:     10,
		HDR:          "HDR10",
		DolbyVision:  true,
		AudioCodec:   "eac3",
	}
	if err := ls.StoreItem(item); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Reachable by tmdb even though it was keyed by imdb.
	byTmdb, err := ls.GetCandidatesByIDs("movie", "", "278", "", "", 0, 0)
	if err != nil || len(byTmdb) != 1 {
		t.Fatalf("lookup by tmdb: err=%v n=%d", err, len(byTmdb))
	}
	got := byTmdb[0]
	if got.VideoCodec != "hevc" || got.Height != 2160 || got.BitDepth != 10 || got.HDR != "HDR10" || !got.DolbyVision || got.AudioCodec != "eac3" {
		t.Fatalf("capability columns not round-tripped: %+v", got)
	}
	if got.TmdbID != "278" || got.TvdbID != "111" || got.ImdbID != "tt0111161" {
		t.Fatalf("id columns not round-tripped: %+v", got)
	}

	// Reachable by imdb and by tvdb too; not by an unrelated id.
	if r, _ := ls.GetCandidatesByIDs("movie", "tt0111161", "", "", "", 0, 0); len(r) != 1 {
		t.Fatalf("lookup by imdb should hit, got %d", len(r))
	}
	if r, _ := ls.GetCandidatesByIDs("movie", "", "", "111", "", 0, 0); len(r) != 1 {
		t.Fatalf("lookup by tvdb should hit, got %d", len(r))
	}
	if r, _ := ls.GetCandidatesByIDs("movie", "tt9999999", "", "", "", 0, 0); len(r) != 0 {
		t.Fatalf("unrelated id must not hit, got %d", len(r))
	}
	// Empty query (no ids) returns nothing rather than everything.
	if r, _ := ls.GetCandidatesByIDs("movie", "", "", "", "", 0, 0); len(r) != 0 {
		t.Fatalf("empty id query must return nothing, got %d", len(r))
	}
}

func TestLibraryEnforceQuotaSizeEvictsOldestNonPinned(t *testing.T) {
	ls := newTestLibraryStore(t)

	mkItem := func(id string, nzb []byte, pinned bool) *LibraryItem {
		return &LibraryItem{
			ContentType: "movie", ContentID: id, ReleaseTitle: id,
			DetailsURL: "https://x/" + id, NZBData: nzb, Pinned: pinned,
		}
	}
	// ~2MB of high-entropy (incompressible) bytes so length(nzb_data) after gzip
	// stays large enough to exceed the size budget below.
	big := make([]byte, 2*1024*1024)
	seed := uint32(12345)
	for i := range big {
		seed = seed*1664525 + 1013904223
		big[i] = byte(seed >> 24)
	}
	// Store three heavy items + one pinned heavy item.
	for _, id := range []string{"a", "b", "c"} {
		if err := ls.StoreItem(mkItem(id, big, false)); err != nil {
			t.Fatalf("store %s: %v", id, err)
		}
		// Space out last_accessed so eviction order is deterministic (a oldest).
		ls.db.Exec(`UPDATE library_nzbs SET last_accessed_at = ? WHERE id = ?`, map[string]int64{"a": 100, "b": 200, "c": 300}[id], "movie:"+id+":"+id)
	}
	if err := ls.StoreItem(mkItem("pinned", big, true)); err != nil {
		t.Fatalf("store pinned: %v", err)
	}

	// Enforce a tiny size budget; only non-pinned items may be evicted, oldest first.
	if err := ls.EnforceQuota(0, 1); err != nil { // 1MB budget
		t.Fatalf("enforce quota: %v", err)
	}

	stats, _ := ls.GetStats()
	// Pinned item is never evicted, so total may still exceed the 1MB budget.
	if stats.PinnedItems != 1 {
		t.Fatalf("pinned item must survive, got pinned=%d", stats.PinnedItems)
	}
	// The oldest non-pinned ("a") must be gone; and no orphaned blueprint rows.
	if r, _ := ls.GetCandidatesByIDs("movie", "a", "", "", "", 0, 0); len(r) != 0 {
		t.Errorf("oldest non-pinned item 'a' should have been evicted")
	}
	var orphans int
	ls.db.QueryRow(`SELECT COUNT(*) FROM library_blueprints b LEFT JOIN library_nzbs n ON b.nzb_id = n.id WHERE n.id IS NULL`).Scan(&orphans)
	if orphans != 0 {
		t.Errorf("expected no orphaned blueprint rows, got %d", orphans)
	}
}

func TestLibraryStaleItemsAndMarkVerified(t *testing.T) {
	ls := newTestLibraryStore(t)

	if err := ls.StoreItem(&LibraryItem{ContentType: "movie", ContentID: "tt1", ReleaseTitle: "one", NZBData: []byte("x")}); err != nil {
		t.Fatalf("store: %v", err)
	}
	// Freshly stored => last_verified_at = now, so it is NOT stale against an old cutoff.
	old := time.Unix(1000, 0)
	if items, _ := ls.StaleItems(old, 10); len(items) != 0 {
		t.Fatalf("freshly stored item must not be stale, got %d", len(items))
	}
	// Against a future cutoff it IS stale.
	future := time.Now().Add(1 * time.Hour)
	items, err := ls.StaleItems(future, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected 1 stale item, err=%v n=%d", err, len(items))
	}
	// MarkVerified with a future time removes it from a now-based stale query.
	ls.MarkVerified(items[0].ID, future)
	if again, _ := ls.StaleItems(time.Now(), 10); len(again) != 0 {
		t.Fatalf("verified item must not be stale, got %d", len(again))
	}
	// Pinned items are never returned as stale.
	if err := ls.StoreItem(&LibraryItem{ContentType: "movie", ContentID: "tt2", ReleaseTitle: "two", NZBData: []byte("y"), Pinned: true}); err != nil {
		t.Fatalf("store pinned: %v", err)
	}
	ls.db.Exec(`UPDATE library_nzbs SET last_verified_at = 0`) // force stale
	for _, it := range mustStale(t, ls, future) {
		if it.ReleaseTitle == "two" {
			t.Fatal("pinned item must never be swept as stale")
		}
	}
}

func mustStale(t *testing.T, ls *LibraryStore, before time.Time) []*LibraryItem {
	t.Helper()
	items, err := ls.StaleItems(before, 100)
	if err != nil {
		t.Fatalf("stale items: %v", err)
	}
	return items
}

// TestGetFilteredItemsReportsNZBSizeWithoutBlob guards the Library UI's Play
// button: listings intentionally skip loading nzb_data, so "has NZB" must come
// from length(nzb_data) — a regression here disables every Play button.
func TestGetFilteredItemsReportsNZBSizeWithoutBlob(t *testing.T) {
	ls := newTestLibraryStore(t)

	if err := ls.StoreItem(&LibraryItem{
		ContentType: "movie", ContentID: "tt7", ReleaseTitle: "listing-test",
		NZBData: []byte("<nzb>payload</nzb>"),
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	items, _, err := ls.GetFilteredItems("", "all", false, "all", 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: err=%v n=%d", err, len(items))
	}
	if len(items[0].NZBData) != 0 {
		t.Fatal("listing must not load the NZB blob")
	}
	if items[0].NZBSizeBytes <= 0 {
		t.Fatal("listing must report a positive NZBSizeBytes for items with a stored NZB")
	}
}

func TestLibraryStatusLifecycle(t *testing.T) {
	ls := newTestLibraryStore(t)

	item := &LibraryItem{
		ContentType: "movie", ContentID: "tt42", ReleaseTitle: "Movie.2024",
		DetailsURL: "https://x/nzb/status", NZBData: []byte("<nzb/>"),
	}
	// Early save defaults to pending.
	if err := ls.StoreItem(item); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, _ := ls.GetCandidates("movie", "tt42", 0, 0)
	if len(got) != 1 || got[0].Status != LibraryStatusPending {
		t.Fatalf("expected pending candidate, got %+v", got)
	}

	// Promote to good via re-save; a later pending re-save must not downgrade.
	item.Status = LibraryStatusGood
	if err := ls.StoreItem(item); err != nil {
		t.Fatalf("re-store good: %v", err)
	}
	item.Status = "" // pending default
	if err := ls.StoreItem(item); err != nil {
		t.Fatalf("re-store pending: %v", err)
	}
	got, _ = ls.GetCandidates("movie", "tt42", 0, 0)
	if len(got) != 1 || got[0].Status != LibraryStatusGood {
		t.Fatalf("pending re-save must not downgrade good, got %+v", got[0].Status)
	}

	// Mark bad: excluded from candidates and from the freshness sweep, but still
	// visible via the filtered listing (Library UI).
	ls.MarkStatusByDetailsURL("https://x/nzb/status", LibraryStatusBad, "segment unavailable: 430")
	if got, _ := ls.GetCandidates("movie", "tt42", 0, 0); len(got) != 0 {
		t.Fatal("bad release must not be offered as a playback candidate")
	}
	if stale, _ := ls.StaleItems(time.Now().Add(time.Hour), 10); len(stale) != 0 {
		t.Fatal("bad release must not be re-verified by the freshness sweep")
	}
	badItems, total, err := ls.GetFilteredItems("", "all", false, LibraryStatusBad, 0, 10)
	if err != nil || total != 1 || len(badItems) != 1 {
		t.Fatalf("bad filter should list the item: err=%v total=%d n=%d", err, total, len(badItems))
	}
	if badItems[0].StatusReason == "" {
		t.Fatal("expected status reason to be stored")
	}

	// Stats expose the per-status counts.
	stats, _ := ls.GetStats()
	if stats.BadItems != 1 {
		t.Fatalf("expected 1 bad item in stats, got %d", stats.BadItems)
	}

	// A later good verdict (sustained play) overrides bad.
	item.Status = LibraryStatusGood
	if err := ls.StoreItem(item); err != nil {
		t.Fatalf("re-store good after bad: %v", err)
	}
	if got, _ := ls.GetCandidates("movie", "tt42", 0, 0); len(got) != 1 || got[0].Status != LibraryStatusGood {
		t.Fatal("good verdict must override bad and restore candidacy")
	}
}

func TestLibraryStoreRoundTripMediaCaps(t *testing.T) {
	logger.Init("ERROR")
	tempDir, err := os.MkdirTemp("", "library_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	defer mgr.Close()

	ls := mgr.LibraryStore()
	if ls == nil {
		t.Fatal("nil library store")
	}

	item := &LibraryItem{
		ContentType:   "series",
		ContentID:     "tt11198330",
		Season:        1,
		Episode:       3,
		ReleaseTitle:  "House of the Dragon S01E03 2160p",
		DetailsURL:    "https://indexer.example/nzb/abc",
		IndexerName:   "example",
		SizeBytes:     8_784_483_225,
		NZBData:       []byte("<nzb>payload</nzb>"),
		BlueprintJSON: `{"files":1}`,
		MediaFileName: "hotd.mkv",
		MediaFileSize: 8_700_000_000,
		MediaCapsJSON: `{"VideoCodec":"hevc","Profile":"Main 10","Height":2160,"BitDepth":10,"HDR":"HDR10"}`,
	}
	if err := ls.StoreItem(item); err != nil {
		t.Fatalf("store item: %v", err)
	}

	got, err := ls.GetCandidates("series", "tt11198330", 1, 3)
	if err != nil {
		t.Fatalf("get candidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	if got[0].MediaCapsJSON != item.MediaCapsJSON {
		t.Fatalf("media caps not round-tripped:\n got %q\nwant %q", got[0].MediaCapsJSON, item.MediaCapsJSON)
	}
	if string(got[0].NZBData) != string(item.NZBData) {
		t.Fatalf("nzb data not round-tripped: got %q", string(got[0].NZBData))
	}

	// Upsert with empty caps must preserve the previously stored caps.
	item2 := *item
	item2.MediaCapsJSON = ""
	item2.MediaFileSize = 8_700_000_001
	if err := ls.StoreItem(&item2); err != nil {
		t.Fatalf("re-store item: %v", err)
	}
	got2, err := ls.GetCandidates("series", "tt11198330", 1, 3)
	if err != nil || len(got2) != 1 {
		t.Fatalf("get candidates after upsert: %v n=%d", err, len(got2))
	}
	if got2[0].MediaCapsJSON != item.MediaCapsJSON {
		t.Fatalf("empty-caps upsert clobbered stored caps: got %q", got2[0].MediaCapsJSON)
	}
}
