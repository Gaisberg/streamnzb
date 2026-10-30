package persistence

import (
	"fmt"
	"os"
	"slices"
	"sync"
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

func TestJellyfinPlaystateMarkPlayedMany(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	item := func(id string) JellyfinPlaystate {
		return JellyfinPlaystate{ItemID: id, ContentType: "series", ContentID: "tt1:1:" + id}
	}
	long := time.Now().Add(-48 * time.Hour).Truncate(time.Millisecond)
	// e1 played long ago, e2 part-way and a favourite, e3 never touched.
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "e1", ContentType: "series", ContentID: "tt1:1:e1", Played: true, PlayCount: 3, LastPlayedAt: long}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "e2", ContentType: "series", ContentID: "tt1:1:e2", PositionTicks: 500, RuntimeTicks: 1000, LastPlayedAt: long}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite("alice", "e2", "series", "tt1:1:e2", true); err != nil {
		t.Fatal(err)
	}

	if err := store.MarkPlayedMany("alice", []JellyfinPlaystate{item("e1"), item("e2"), item("e3")}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	e1, _ := store.Get("alice", "e1")
	if !e1.Played || e1.PlayCount != 3 || !e1.LastPlayedAt.Equal(long) {
		t.Fatalf("an episode already played must keep its history: %+v", e1)
	}
	e2, _ := store.Get("alice", "e2")
	e3, ok := store.Get("alice", "e3")
	if !ok || !e2.Played || e2.PositionTicks != 0 || e2.PlayCount != 1 || !e2.IsFavorite || e2.RuntimeTicks != 1000 {
		t.Fatalf("part-played favourite: %+v", e2)
	}
	if !e3.Played || e3.PlayCount != 1 || e3.ContentID != "tt1:1:e3" || !e3.LastPlayedAt.Equal(e2.LastPlayedAt) {
		t.Fatalf("new row: %+v (stamped with e2's %v)", e3, e2.LastPlayedAt)
	}
	if _, ok := store.Get("bob", "e3"); ok {
		t.Fatal("marks are per stream")
	}
	// Again: nothing changes, the date included.
	if err := store.MarkPlayedMany("alice", []JellyfinPlaystate{item("e2"), item("e3")}); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.Get("alice", "e3"); !again.LastPlayedAt.Equal(e3.LastPlayedAt) {
		t.Fatalf("repeat mark restamped: %v, was %v", again.LastPlayedAt, e3.LastPlayedAt)
	}
}

// TestJellyfinPlaystatePrefix covers the reads and clears scoped to an item
// id prefix: exactly the rows that start with it, in the stream given.
func TestJellyfinPlaystatePrefix(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	long := time.Now().Add(-48 * time.Hour).Truncate(time.Millisecond)
	for _, st := range []JellyfinPlaystate{
		{StreamName: "alice", ItemID: "aa01", Played: true, PlayCount: 3, LastPlayedAt: long},
		{StreamName: "alice", ItemID: "aa02", PositionTicks: 500, LastPlayedAt: long},
		{StreamName: "alice", ItemID: "aa03", LastPlayedAt: long},
		{StreamName: "alice", ItemID: "ab01", Played: true, PlayCount: 1, LastPlayedAt: long},
		{StreamName: "alice", ItemID: "0aa01", Played: true, PlayCount: 1, LastPlayedAt: long},
		// No id is stored in capitals, but a collation that sets case aside
		// would put these under "aa", and SQLite's LIKE would match them.
		{StreamName: "alice", ItemID: "AA01", Played: true, PlayCount: 1, LastPlayedAt: long},
		{StreamName: "alice", ItemID: "aA02", Played: true, PlayCount: 1, LastPlayedAt: long},
		{StreamName: "bob", ItemID: "aa01", Played: true, PlayCount: 1, LastPlayedAt: long},
	} {
		st.ContentType, st.ContentID = "series", st.ItemID
		if err := store.Upsert(st); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"aa02", "aa03"} {
		if err := store.SetFavorite("alice", id, "series", id, true); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(rows []JellyfinPlaystate) []string {
		var out []string
		for _, st := range rows {
			out = append(out, st.ItemID)
		}
		slices.Sort(out)
		return out
	}
	rows, err := store.ListPrefix("alice", "aa")
	if err != nil || !slices.Equal(ids(rows), []string{"aa01", "aa02", "aa03"}) {
		t.Fatalf("ListPrefix: %v, %v", ids(rows), err)
	}

	for want := range []int64{2, 0} {
		// Two rows to clear, then none: the count is what changed.
		cleared, err := store.ClearPlayedPrefix("alice", "aa")
		if err != nil || cleared != []int64{2, 0}[want] {
			t.Fatalf("cleared %d, %v; want %d", cleared, err, []int64{2, 0}[want])
		}
		a1, _ := store.Get("alice", "aa01")
		a2, _ := store.Get("alice", "aa02")
		a3, _ := store.Get("alice", "aa03")
		if a1.Played || a1.PlayCount != 3 || a1.LastPlayedAt.Equal(long) {
			t.Fatalf("aa01: %+v", a1)
		}
		if a2.Played || a2.PositionTicks != 0 || !a2.IsFavorite {
			t.Fatalf("aa02: %+v", a2)
		}
		// Nothing to clear: left as it was, its date included.
		if a3.Played || !a3.IsFavorite || !a3.LastPlayedAt.Equal(long) {
			t.Fatalf("aa03: %+v", a3)
		}
	}
	for _, key := range [][2]string{{"alice", "ab01"}, {"alice", "0aa01"}, {"alice", "AA01"}, {"alice", "aA02"}, {"bob", "aa01"}} {
		if st, _ := store.Get(key[0], key[1]); !st.Played {
			t.Fatalf("%s %s cleared out of the prefix: %+v", key[0], key[1], st)
		}
	}
	if n := len(store.ListForStream("alice")); n != 7 {
		t.Fatalf("%d rows: clearing created some", n)
	}

	// Only lowercase hex is a prefix, so nothing reads as a wildcard.
	for _, prefix := range []string{"", "%", "a_", "a%", "AA", "0g", "a'"} {
		if _, err := store.ListPrefix("alice", prefix); err == nil {
			t.Errorf("ListPrefix %q: no error", prefix)
		}
		if _, err := store.ClearPlayedPrefix("alice", prefix); err == nil {
			t.Errorf("ClearPlayedPrefix %q: no error", prefix)
		}
	}
	if st, _ := store.Get("alice", "ab01"); !st.Played {
		t.Fatal("a refused prefix cleared a row")
	}
}

// TestJellyfinPlaystatePrefixFailedReads closes the database under the
// store: the scoped read and the writes report it, rather than read as no
// rows or as done.
func TestJellyfinPlaystatePrefixFailedReads(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	if err := store.MarkPlayedMany("alice", []JellyfinPlaystate{{ItemID: "aa01"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.ListPrefix("alice", "aa"); err == nil {
		t.Fatalf("read of a closed database: %v, no error", rows)
	}
	if _, err := store.ClearPlayedPrefix("alice", "aa"); err == nil {
		t.Fatal("clear on a closed database: no error")
	}
	if err := store.MarkPlayedMany("alice", []JellyfinPlaystate{{ItemID: "aa02"}}); err == nil {
		t.Fatal("mark on a closed database: no error")
	}
}

// TestJellyfinPlaystateMarkPlayedManyRollsBack fails a batch in its second
// statement, after the first has written its rows, and expects none of it
// kept.
func TestJellyfinPlaystateMarkPlayedManyRollsBack(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "e000", ContentType: "series", ContentID: "a", PositionTicks: 100}); err != nil {
		t.Fatal(err)
	}
	var batch []JellyfinPlaystate
	for i := range 600 {
		batch = append(batch, JellyfinPlaystate{ItemID: fmt.Sprintf("e%03d", i), ContentType: "series", ContentID: "a"})
	}
	broken := append(slices.Clone(batch), JellyfinPlaystate{ItemID: " "})
	if err := store.MarkPlayedMany("alice", broken); err == nil {
		t.Fatal("a batch with an empty id must fail")
	}
	if rows := store.ListForStream("alice"); len(rows) != 1 || rows[0].Played || rows[0].PositionTicks != 100 {
		t.Fatalf("rolled-back mark kept %d rows: %+v", len(rows), rows[0])
	}
	// The store is usable afterwards: the transaction did not leak.
	if err := store.MarkPlayedMany("alice", batch); err != nil {
		t.Fatal(err)
	}
	if n := len(store.ListForStream("alice")); n != 600 {
		t.Fatalf("%d rows after the retry", n)
	}
}

// TestJellyfinPlaystateMarkPlayedManyDuplicates gives a batch the same id
// twice, as duplicate metadata listings would, which one Postgres upsert
// statement cannot take.
func TestJellyfinPlaystateMarkPlayedManyDuplicates(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	batch := []JellyfinPlaystate{{ItemID: "c1"}, {ItemID: "c2"}, {ItemID: "c1"}, {ItemID: " c2 "}}
	if err := store.MarkPlayedMany("alice", batch); err != nil {
		t.Fatal(err)
	}
	if rows := store.ListForStream("alice"); len(rows) != 2 || !rows[0].Played || !rows[1].Played {
		t.Fatalf("rows %+v", rows)
	}
}

// TestJellyfinPlaystateLargeBatch marks, clears and marks again a
// long-running series while another stream keeps reading and writing its
// own rows, and logs how long each took and how long those operations
// waited on it. It asserts correctness only; the timings are for the record.
func TestJellyfinPlaystateLargeBatch(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	const n = 2000
	items := make([]JellyfinPlaystate, n)
	for i := range items {
		items[i] = JellyfinPlaystate{ItemID: fmt.Sprintf("ee%04x", i), ContentType: "series", ContentID: fmt.Sprintf("tt1:1:%d", i)}
	}
	if err := store.Upsert(JellyfinPlaystate{StreamName: "bob", ItemID: "bob-ep", ContentType: "series", ContentID: "tt2:1:1", PositionTicks: 1}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var ops int
	var slowest time.Duration
	wg.Add(1)
	go func() {
		defer wg.Done()
		for pos := int64(2); ; pos++ {
			select {
			case <-stop:
				return
			default:
			}
			start := time.Now()
			_, _ = store.Get("bob", "bob-ep")
			if err := store.Upsert(JellyfinPlaystate{StreamName: "bob", ItemID: "bob-ep", ContentType: "series", ContentID: "tt2:1:1", PositionTicks: pos}); err != nil {
				t.Errorf("concurrent upsert: %v", err)
				return
			}
			slowest = max(slowest, time.Since(start))
			ops++
		}
	}()

	for _, step := range []string{"mark", "clear", "mark"} {
		start := time.Now()
		var err error
		if step == "mark" {
			err = store.MarkPlayedMany("alice", items)
		} else {
			_, err = store.ClearPlayedPrefix("alice", "ee")
		}
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		t.Logf("%s: %s %d rows in %v", testBackend(), step, n, time.Since(start))
	}
	close(stop)
	wg.Wait()
	t.Logf("%s: %d concurrent Get+Upsert pairs on another stream, slowest %v", testBackend(), ops, slowest)

	rows := store.ListForStream("alice")
	if len(rows) != n {
		t.Fatalf("%d rows, want %d", len(rows), n)
	}
	for _, st := range rows {
		if !st.Played || st.PlayCount != 1 || st.PositionTicks != 0 {
			t.Fatalf("row %+v", st)
		}
	}
	if bob, _ := store.Get("bob", "bob-ep"); bob.Played || bob.PositionTicks < 2 {
		t.Fatalf("other stream: %+v", bob)
	}
}

// TestJellyfinPlaystateListPrefixScale reads one series out of a stream
// holding many, and logs it against a read of the whole stream.
func TestJellyfinPlaystateListPrefixScale(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	const series, episodes = 200, 50
	for s := range series {
		items := make([]JellyfinPlaystate, episodes)
		for e := range items {
			items[e] = JellyfinPlaystate{ItemID: fmt.Sprintf("05%04x%04x", s, e), ContentType: "series", ContentID: "tt"}
		}
		if err := store.MarkPlayedMany("alice", items); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	rows, err := store.ListPrefix("alice", fmt.Sprintf("05%04x", 123))
	scoped := time.Since(start)
	if err != nil || len(rows) != episodes {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	start = time.Now()
	all := store.ListForStream("alice")
	full := time.Since(start)
	if len(all) != series*episodes {
		t.Fatalf("%d rows in the stream", len(all))
	}
	t.Logf("%s: one series of %d rows in %v; the whole stream of %d in %v", testBackend(), episodes, scoped, len(all), full)
}

// TestJellyfinPlaystateMarkAndClearRace starts a mark and a clear of the
// same rows together, many times over. Each is atomic and they run one
// after the other, in whichever order, so the rows end all marked or all
// cleared, never some of each.
func TestJellyfinPlaystateMarkAndClearRace(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	items := make([]JellyfinPlaystate, 600)
	for i := range items {
		items[i] = JellyfinPlaystate{ItemID: fmt.Sprintf("f%03x", i), ContentType: "series", ContentID: "tt"}
	}
	outcomes := map[bool]int{}
	for range 20 {
		if err := store.MarkPlayedMany("alice", items[:300]); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if err := store.MarkPlayedMany("alice", items); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.ClearPlayedPrefix("alice", "f"); err != nil {
				t.Error(err)
			}
		}()
		close(start)
		wg.Wait()
		rows := store.ListForStream("alice")
		played := 0
		for _, st := range rows {
			if st.Played {
				played++
			}
		}
		if len(rows) != 600 || (played != 0 && played != 600) {
			t.Fatalf("%d rows, %d played: a mark and a clear interleaved", len(rows), played)
		}
		outcomes[played == 600]++
		if _, err := store.ClearPlayedPrefix("alice", "f"); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%s: mark last %d times, clear last %d times", testBackend(), outcomes[true], outcomes[false])
}

// TestJellyfinPlaystateMarkPlayedManyConcurrent runs overlapping batches on
// two streams at once, against per-item marks on the same rows, and expects
// every batch whole and the streams apart.
func TestJellyfinPlaystateMarkPlayedManyConcurrent(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	batch := func(from, to int) []JellyfinPlaystate {
		var out []JellyfinPlaystate
		for i := from; i < to; i++ {
			out = append(out, JellyfinPlaystate{ItemID: fmt.Sprintf("e%03d", i), ContentType: "series", ContentID: "tt1"})
		}
		return out
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := []string{"alice", "bob"}[g%2]
			// Seasons overlapping by half, each stream marking all of them.
			if err := store.MarkPlayedMany(stream, batch(g/2*50, g/2*50+100)); err != nil {
				t.Errorf("batch %d: %v", g, err)
			}
			if err := store.SetPlayed(stream, fmt.Sprintf("e%03d", g), "series", "tt1", true); err != nil {
				t.Errorf("single %d: %v", g, err)
			}
		}()
	}
	wg.Wait()
	for _, stream := range []string{"alice", "bob"} {
		rows := store.ListForStream(stream)
		if len(rows) != 250 {
			t.Fatalf("%s: %d rows, want 250", stream, len(rows))
		}
		for _, st := range rows {
			if !st.Played || st.PlayCount != 1 || st.StreamName != stream {
				t.Fatalf("%s: %+v", stream, st)
			}
		}
	}
}

// TestJellyfinPlaystateListPrefixMalformedRow stores a row whose
// last_played_at is not a number, as an import from a damaged database
// could: the scoped read fails on it rather than read the season without
// that episode, while the listings still skip it as they always have.
// Postgres types its columns and will not hold such a row, so only SQLite
// can be given one.
func TestJellyfinPlaystateListPrefixMalformedRow(t *testing.T) {
	if testBackend() == "postgres" {
		t.Skip("Postgres will not store a non-integer in an integer column")
	}
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	if err := store.MarkPlayedMany("alice", []JellyfinPlaystate{{ItemID: "aa01"}, {ItemID: "aa02"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wdb.Exec(`UPDATE jellyfin_playstate SET last_played_at = 'yesterday' WHERE item_id = 'aa02'`); err != nil {
		t.Fatal(err)
	}
	if rows, err := store.ListPrefix("alice", "aa"); err == nil {
		t.Fatalf("read a malformed row as %v, no error", rows)
	}
	if rows := store.ListForStream("alice"); len(rows) != 1 || rows[0].ItemID != "aa01" {
		t.Fatalf("listing: %+v", rows)
	}
}

func testBackend() string {
	if os.Getenv(testPostgresDSNEnv) != "" {
		return "postgres"
	}
	return "sqlite"
}
