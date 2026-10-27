package persistence

import (
	"testing"
	"time"
)

func TestJellyfinPlaystateUpsertAndGet(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if err := store.Upsert(JellyfinPlaystate{
		StreamName: "alice", ItemID: "item-1", ContentType: "movie", ContentID: "tt0133093",
		PositionTicks: 1_000, RuntimeTicks: 100_000, PlayCount: 1,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert(JellyfinPlaystate{
		StreamName: "alice", ItemID: "item-1", ContentType: "movie", ContentID: "tt0133093",
		PositionTicks: 5_000, RuntimeTicks: 100_000, PlayCount: 1,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, ok := store.Get("alice", "item-1")
	if !ok {
		t.Fatal("expected row")
	}
	if got.PositionTicks != 5_000 || got.ContentID != "tt0133093" || got.Played {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.LastPlayedAt.IsZero() || time.Since(got.LastPlayedAt) > time.Minute {
		t.Fatalf("last played not stamped: %v", got.LastPlayedAt)
	}
	if _, ok := store.Get("bob", "item-1"); ok {
		t.Fatal("rows are per stream")
	}
}

func TestJellyfinPlaystateResumeAndPlayed(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	base := time.Now().Add(-time.Hour)
	for i, item := range []string{"a", "b", "c"} {
		if err := store.Upsert(JellyfinPlaystate{
			StreamName: "alice", ItemID: item, ContentType: "movie", ContentID: "tt" + item,
			PositionTicks: 100, LastPlayedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("upsert %s: %v", item, err)
		}
	}
	// A finished item never shows in resume.
	if err := store.SetPlayed("alice", "b", "movie", "ttb", true); err != nil {
		t.Fatalf("set played: %v", err)
	}
	// A row without progress is not resumable either.
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "d", ContentType: "movie", ContentID: "ttd"}); err != nil {
		t.Fatalf("upsert d: %v", err)
	}

	resume := store.ListResume("alice", 10)
	if len(resume) != 2 || resume[0].ItemID != "c" || resume[1].ItemID != "a" {
		t.Fatalf("unexpected resume rows: %+v", resume)
	}

	played, _ := store.Get("alice", "b")
	if !played.Played || played.PositionTicks != 0 || played.PlayCount != 1 {
		t.Fatalf("unexpected played row: %+v", played)
	}
	if err := store.SetPlayed("alice", "b", "movie", "ttb", false); err != nil {
		t.Fatalf("set unplayed: %v", err)
	}
	unplayed, _ := store.Get("alice", "b")
	if unplayed.Played || unplayed.PlayCount != 1 {
		t.Fatalf("unplayed must keep the play count: %+v", unplayed)
	}
	if n := len(store.ListForStream("alice")); n != 4 {
		t.Fatalf("expected 4 rows for stream, got %d", n)
	}
}

func TestJellyfinPlaystateRenameAndDelete(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 5}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := m.RenameStreamReferences("alice", "alicia"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := store.Get("alice", "a"); ok {
		t.Fatal("old name still has rows")
	}
	if _, ok := store.Get("alicia", "a"); !ok {
		t.Fatal("new name has no rows")
	}
	if err := store.DeleteForStream("alicia"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows := store.ListForStream("alicia"); len(rows) != 0 {
		t.Fatalf("expected no rows after delete, got %+v", rows)
	}
}

// TestJellyfinPlaystateDeleteForStreamIsScoped covers the purge a stream
// delete runs: only that stream's rows go, and running it again is harmless.
func TestJellyfinPlaystateDeleteForStreamIsScoped(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	for _, row := range []JellyfinPlaystate{
		{StreamName: "alice", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 5},
		{StreamName: "alice", ItemID: "b", ContentType: "series", ContentID: "ttb:1:1", Played: true},
		{StreamName: "alice-2", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 7},
		{StreamName: "bob", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 9},
	} {
		if err := store.Upsert(row); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := store.DeleteForStream("alice"); err != nil {
			t.Fatalf("delete pass %d: %v", i+1, err)
		}
	}
	if rows := store.ListForStream("alice"); len(rows) != 0 {
		t.Fatalf("alice kept rows: %+v", rows)
	}
	if got, ok := store.Get("alice-2", "a"); !ok || got.PositionTicks != 7 {
		t.Fatalf("a name sharing the prefix lost its row: %+v (ok=%v)", got, ok)
	}
	if got, ok := store.Get("bob", "a"); !ok || got.PositionTicks != 9 {
		t.Fatalf("bob's row changed: %+v (ok=%v)", got, ok)
	}
}

// TestJellyfinPlaystateRenameOverStaleRows covers renaming onto a name that
// still has play state of its own. The table is keyed (stream_name, item_id),
// so rows left behind by a deleted stream collide with the ones being moved;
// before this was handled the whole rename failed, taking the NZB attempt and
// search diagnostic rewrites down with it.
func TestJellyfinPlaystateRenameOverStaleRows(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	// "tv" was deleted without its play state being purged.
	if err := store.Upsert(JellyfinPlaystate{StreamName: "tv", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 11}); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}
	// The stream being renamed has progress on the same item, and on another.
	if err := store.Upsert(JellyfinPlaystate{StreamName: "living-room", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 99}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert(JellyfinPlaystate{StreamName: "living-room", ItemID: "b", ContentType: "movie", ContentID: "ttb", PositionTicks: 7}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if _, err := m.RenameStreamReferences("living-room", "tv"); err != nil {
		t.Fatalf("rename onto stale rows: %v", err)
	}
	// The renamed stream's own progress wins over what was left behind.
	got, ok := store.Get("tv", "a")
	if !ok || got.PositionTicks != 99 {
		t.Fatalf("stale row was not displaced: %+v (ok=%v)", got, ok)
	}
	if _, ok := store.Get("tv", "b"); !ok {
		t.Fatal("non-colliding row did not move")
	}
	if rows := store.ListForStream("living-room"); len(rows) != 0 {
		t.Fatalf("old name kept rows: %+v", rows)
	}
}

// TestJellyfinPlaystateFavoriteRoundTrip covers marking and clearing: both are
// idempotent, clearing a favourite the stream never had creates nothing, and
// a favourite-only row is no play — no resume entry, no 1970 played date.
func TestJellyfinPlaystateFavoriteRoundTrip(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if err := store.SetFavorite("alice", "a", "movie", "tta", false); err != nil {
		t.Fatalf("clear missing: %v", err)
	}
	if _, ok := store.Get("alice", "a"); ok {
		t.Fatal("clearing a missing favourite created a row")
	}
	for i := 0; i < 2; i++ {
		if err := store.SetFavorite("alice", "a", "movie", "tta", true); err != nil {
			t.Fatalf("mark pass %d: %v", i+1, err)
		}
	}
	got, ok := store.Get("alice", "a")
	if !ok || !got.IsFavorite || got.ContentID != "tta" || got.PositionTicks != 0 || got.PlayCount != 0 || got.Played {
		t.Fatalf("favourite-only row: %+v (ok=%v)", got, ok)
	}
	if !got.LastPlayedAt.IsZero() {
		t.Fatalf("favourite-only row has a played date: %v", got.LastPlayedAt)
	}
	if rows := store.ListResume("alice", 10); len(rows) != 0 {
		t.Fatalf("favourite-only row is resumable: %+v", rows)
	}
	if favs := store.ListFavorites("alice"); len(favs) != 1 || favs[0].ItemID != "a" {
		t.Fatalf("favourites: %+v", favs)
	}

	// Per stream: bob's favourite on the same item is his own.
	if err := store.SetFavorite("bob", "a", "movie", "tta", true); err != nil {
		t.Fatalf("mark bob: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.SetFavorite("alice", "a", "movie", "tta", false); err != nil {
			t.Fatalf("clear pass %d: %v", i+1, err)
		}
	}
	if got, ok := store.Get("alice", "a"); !ok || got.IsFavorite {
		t.Fatalf("cleared favourite: %+v (ok=%v), want the row kept and unmarked", got, ok)
	}
	if favs := store.ListFavorites("alice"); len(favs) != 0 {
		t.Fatalf("cleared favourite still listed: %+v", favs)
	}
	if favs := store.ListFavorites("bob"); len(favs) != 1 {
		t.Fatalf("clearing alice's favourite touched bob's: %+v", favs)
	}
}

// TestJellyfinPlaystateFavoriteAndPlaybackWritesAreDisjoint covers the two
// writers sharing a row: neither may overwrite the other's columns, even when
// one writes back a state it read before the other wrote.
func TestJellyfinPlaystateFavoriteAndPlaybackWritesAreDisjoint(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	playedAt := time.UnixMilli(time.Now().Add(-time.Hour).UnixMilli())
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 5, RuntimeTicks: 100, PlayCount: 2, LastPlayedAt: playedAt}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.SetFavorite("alice", "a", "movie", "tta", true); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, _ := store.Get("alice", "a")
	if !got.IsFavorite || got.PositionTicks != 5 || got.PlayCount != 2 || !got.LastPlayedAt.Equal(playedAt) {
		t.Fatalf("marking touched playback: %+v", got)
	}

	// Playback read the row before the favourite was cleared, and writes back
	// what it read: the clear must stand.
	stale, _ := store.Get("alice", "a")
	if err := store.SetFavorite("alice", "a", "movie", "tta", false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	stale.PositionTicks = 11
	if err := store.Upsert(stale); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}
	if got, _ := store.Get("alice", "a"); got.IsFavorite || got.PositionTicks != 11 {
		t.Fatalf("stale upsert after clear: %+v", got)
	}

	// And the other way: a progress report read before the favourite was set.
	stale, _ = store.Get("alice", "a")
	if err := store.SetFavorite("alice", "a", "movie", "tta", true); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	stale.PositionTicks = 13
	if err := store.Upsert(stale); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}
	if got, _ := store.Get("alice", "a"); !got.IsFavorite || got.PositionTicks != 13 {
		t.Fatalf("stale upsert after mark: %+v", got)
	}

	for _, played := range []bool{true, false} {
		if err := store.SetPlayed("alice", "a", "movie", "tta", played); err != nil {
			t.Fatalf("set played %v: %v", played, err)
		}
		if got, _ := store.Get("alice", "a"); !got.IsFavorite || got.Played != played {
			t.Fatalf("set played %v: %+v", played, got)
		}
	}

	// Clearing keeps the row, and with it the progress.
	if err := store.SetFavorite("alice", "a", "movie", "tta", false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, ok := store.Get("alice", "a"); !ok || got.PlayCount == 0 {
		t.Fatalf("clearing dropped the play state: %+v (ok=%v)", got, ok)
	}
}

// TestJellyfinPlaystateFavoriteFollowsTheStream covers rename and delete,
// which move and drop whole rows, the favourite with them.
func TestJellyfinPlaystateFavoriteFollowsTheStream(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if err := store.SetFavorite("alice", "a", "movie", "tta", true); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if _, err := m.RenameStreamReferences("alice", "alicia"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if favs := store.ListFavorites("alice"); len(favs) != 0 {
		t.Fatalf("old name kept favourites: %+v", favs)
	}
	if favs := store.ListFavorites("alicia"); len(favs) != 1 {
		t.Fatalf("favourite did not move with the rename: %+v", favs)
	}
	if err := store.DeleteForStream("alicia"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if favs := store.ListFavorites("alicia"); len(favs) != 0 {
		t.Fatalf("favourite survived the delete: %+v", favs)
	}
}
