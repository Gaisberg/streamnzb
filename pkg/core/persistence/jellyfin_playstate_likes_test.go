package persistence

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func likesPtr(v bool) *bool { return &v }

// likesOf names a row's rating: "" for unrated, else "like" or "dislike".
func likesOf(st JellyfinPlaystate) string {
	switch {
	case !st.Likes.Valid:
		return ""
	case st.Likes.Bool:
		return "like"
	}
	return "dislike"
}

// TestJellyfinPlaystateLikesRoundTrip covers setting, changing and clearing a
// rating, the row it creates, and that every read returns it.
func TestJellyfinPlaystateLikesRoundTrip(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	// Clearing what was never rated creates nothing.
	if err := store.SetLikes("alice", "aa01", "movie", "tta", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := store.Get("alice", "aa01"); ok {
		t.Fatal("clearing a rating created a row")
	}

	for _, step := range []struct {
		likes *bool
		want  string
	}{{likesPtr(true), "like"}, {likesPtr(false), "dislike"}, {likesPtr(true), "like"}, {nil, ""}, {likesPtr(false), "dislike"}} {
		if err := store.SetLikes("alice", "aa01", "movie", "tta", step.likes); err != nil {
			t.Fatalf("set %v: %v", step.likes, err)
		}
		got, ok := store.Get("alice", "aa01")
		if !ok || likesOf(got) != step.want || got.ContentType != "movie" || got.ContentID != "tta" {
			t.Fatalf("after %v: %+v (ok=%v), want %q", step.likes, got, ok, step.want)
		}
	}

	// A rating-only row has no progress, play, favourite or date.
	got, _ := store.Get("alice", "aa01")
	if got.PositionTicks != 0 || got.PlayCount != 0 || got.Played || got.IsFavorite || !got.LastPlayedAt.IsZero() {
		t.Fatalf("a rating wrote more than the rating: %+v", got)
	}
	if rows := store.ListResume("alice", 10); len(rows) != 0 {
		t.Fatalf("a rating-only row is resumable: %+v", rows)
	}
	if rows := store.ListFavorites("alice"); len(rows) != 0 {
		t.Fatalf("a rating-only row is a favourite: %+v", rows)
	}

	// Clearing keeps the row.
	if err := store.SetLikes("alice", "aa01", "movie", "tta", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, ok := store.Get("alice", "aa01"); !ok || got.Likes.Valid {
		t.Fatalf("clear: %+v (ok=%v)", got, ok)
	}
}

// TestJellyfinPlaystateLikesInEveryRead reads NULL, true and false through all
// five reads, each over rows of all three states.
func TestJellyfinPlaystateLikesInEveryRead(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	want := map[string]string{"aa01": "", "aa02": "like", "aa03": "dislike"}
	for id, state := range want {
		// Progress and a favourite, so ListResume and ListFavorites list it.
		if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: id, ContentType: "movie", ContentID: "tt" + id, PositionTicks: 5, RuntimeTicks: 100}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := store.SetFavorite("alice", id, "movie", "tt"+id, true); err != nil {
			t.Fatalf("favourite: %v", err)
		}
		var likes *bool
		if state != "" {
			likes = likesPtr(state == "like")
		}
		if err := store.SetLikes("alice", id, "movie", "tt"+id, likes); err != nil {
			t.Fatalf("likes: %v", err)
		}
	}
	prefixed, err := store.ListPrefix("alice", "aa")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	reads := map[string][]JellyfinPlaystate{
		"ListForStream": store.ListForStream("alice"),
		"ListResume":    store.ListResume("alice", 10),
		"ListFavorites": store.ListFavorites("alice"),
		"ListPrefix":    prefixed,
	}
	for name, rows := range reads {
		if len(rows) != len(want) {
			t.Fatalf("%s: %d rows, want %d", name, len(rows), len(want))
		}
		for _, st := range rows {
			if likesOf(st) != want[st.ItemID] {
				t.Errorf("%s %s: %q, want %q", name, st.ItemID, likesOf(st), want[st.ItemID])
			}
		}
	}
	for id, state := range want {
		if got, ok := store.Get("alice", id); !ok || likesOf(got) != state {
			t.Errorf("Get %s: %q (ok=%v), want %q", id, likesOf(got), ok, state)
		}
	}
}

// TestJellyfinPlaystateLikesAndPlaybackWritesAreDisjoint: a rating write
// touches nothing else, and no other write touches the rating, including a
// progress report built from a read taken before the rating changed.
func TestJellyfinPlaystateLikesAndPlaybackWritesAreDisjoint(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	playedAt := time.UnixMilli(time.Now().Add(-time.Hour).UnixMilli())
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "aa01", ContentType: "movie", ContentID: "tta", PositionTicks: 5, RuntimeTicks: 100, PlayCount: 2, LastPlayedAt: playedAt}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.SetFavorite("alice", "aa01", "movie", "tta", true); err != nil {
		t.Fatalf("favourite: %v", err)
	}
	before, _ := store.Get("alice", "aa01")
	stale := before
	if err := store.SetLikes("alice", "aa01", "movie", "tta", likesPtr(false)); err != nil {
		t.Fatalf("rate: %v", err)
	}
	after, _ := store.Get("alice", "aa01")
	if !after.Likes.Valid || after.Likes.Bool {
		t.Fatalf("not disliked: %+v", after)
	}
	after.Likes = before.Likes
	if after != before {
		t.Fatalf("rating changed another field:\nbefore %+v\nafter  %+v", before, after)
	}

	keeps := func(what string) {
		t.Helper()
		if got, _ := store.Get("alice", "aa01"); likesOf(got) != "dislike" {
			t.Fatalf("%s overwrote the rating: %+v", what, got)
		}
	}
	stale.PositionTicks = 11
	if err := store.Upsert(stale); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}
	keeps("a stale upsert")
	for _, played := range []bool{true, false} {
		if err := store.SetPlayed("alice", "aa01", "movie", "tta", played); err != nil {
			t.Fatalf("set played: %v", err)
		}
		keeps("SetPlayed")
	}
	if err := store.MarkPlayedMany("alice", []JellyfinPlaystate{{ItemID: "aa01", ContentType: "movie", ContentID: "tta"}, {ItemID: "aa02", ContentType: "movie", ContentID: "ttb"}}); err != nil {
		t.Fatalf("mark many: %v", err)
	}
	keeps("MarkPlayedMany")
	if _, err := store.ClearPlayedPrefix("alice", "aa"); err != nil {
		t.Fatalf("clear played: %v", err)
	}
	keeps("ClearPlayedPrefix")
	for _, fav := range []bool{false, true} {
		if err := store.SetFavorite("alice", "aa01", "movie", "tta", fav); err != nil {
			t.Fatalf("favourite: %v", err)
		}
		keeps("SetFavorite")
	}
}

// TestJellyfinPlaystateLikesConcurrentWithPlayback: ratings and progress
// written together on one row both land.
func TestJellyfinPlaystateLikesConcurrentWithPlayback(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "aa01", ContentType: "movie", ContentID: "tta"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := store.SetLikes("alice", "aa01", "movie", "tta", likesPtr(true)); err != nil {
				errs <- fmt.Errorf("SetLikes: %w", err)
			}
		}()
		go func(n int) {
			defer wg.Done()
			if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "aa01", ContentType: "movie", ContentID: "tta", PositionTicks: int64(n + 1)}); err != nil {
				errs <- fmt.Errorf("Upsert: %w", err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent write failed: %v", err)
	}
	if got, _ := store.Get("alice", "aa01"); likesOf(got) != "like" || got.PositionTicks == 0 {
		t.Fatalf("a concurrent write lost: %+v", got)
	}
}

// TestJellyfinPlaystateLikesAreScopedToTheStream covers isolation between
// streams, and that a rating follows its stream through rename and delete.
func TestJellyfinPlaystateLikesAreScopedToTheStream(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()
	if err := store.SetLikes("alice", "aa01", "movie", "tta", likesPtr(true)); err != nil {
		t.Fatalf("alice: %v", err)
	}
	if err := store.SetLikes("bob", "aa01", "movie", "tta", likesPtr(false)); err != nil {
		t.Fatalf("bob: %v", err)
	}
	if err := store.SetLikes("bob", "aa01", "movie", "tta", nil); err != nil {
		t.Fatalf("bob clear: %v", err)
	}
	if got, _ := store.Get("alice", "aa01"); likesOf(got) != "like" {
		t.Fatalf("a write for bob reached alice: %+v", got)
	}
	if _, err := m.RenameStreamReferences("alice", "alicia"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got, ok := store.Get("alicia", "aa01"); !ok || likesOf(got) != "like" {
		t.Fatalf("rating did not follow the rename: %+v (ok=%v)", got, ok)
	}
	if err := store.DeleteForStream("alicia"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := store.Get("alicia", "aa01"); ok {
		t.Fatal("rating survived the delete")
	}
	if got, ok := store.Get("bob", "aa01"); !ok || got.Likes.Valid {
		t.Fatalf("the delete touched bob: %+v (ok=%v)", got, ok)
	}
}

// TestJellyfinPlaystateLikesSurviveReopen: all three states persist.
func TestJellyfinPlaystateLikesSurviveReopen(t *testing.T) {
	db := suiteDB(t)
	m := db.open(t)
	store := m.JellyfinPlaystateStore()
	for id, likes := range map[string]*bool{"aa01": nil, "aa02": likesPtr(true), "aa03": likesPtr(false)} {
		if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: id, ContentType: "movie", ContentID: "tt" + id}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := store.SetLikes("alice", id, "movie", "tt"+id, likes); err != nil {
			t.Fatalf("rate: %v", err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened := db.open(t)
	defer reopened.Close()
	for id, want := range map[string]string{"aa01": "", "aa02": "like", "aa03": "dislike"} {
		if got, ok := reopened.JellyfinPlaystateStore().Get("alice", id); !ok || likesOf(got) != want {
			t.Errorf("%s after reopen: %q (ok=%v), want %q", id, likesOf(got), ok, want)
		}
	}
}

// TestImportCarriesLikes: a source's likes, dislikes and unrated rows keep
// their three states, and a legacy source without the column imports as
// unrated.
func TestImportCarriesLikes(t *testing.T) {
	dir := t.TempDir()
	src, err := newManager(Settings{Backend: BackendSQLite}, dir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	for id, likes := range map[string]*bool{"aa01": nil, "aa02": likesPtr(true), "aa03": likesPtr(false)} {
		if err := src.JellyfinPlaystateStore().Upsert(JellyfinPlaystate{StreamName: "tv", ItemID: id, ContentType: "movie", ContentID: "tt" + id}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := src.JellyfinPlaystateStore().SetLikes("tv", id, "movie", "tt"+id, likes); err != nil {
			t.Fatalf("seed rating: %v", err)
		}
	}
	if err := src.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	target := openTestManager(t)
	if err := importFromSQLite(target.wdb, dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	for id, want := range map[string]string{"aa01": "", "aa02": "like", "aa03": "dislike"} {
		if got, ok := target.JellyfinPlaystateStore().Get("tv", id); !ok || likesOf(got) != want {
			t.Errorf("%s: %q (ok=%v), want %q", id, likesOf(got), ok, want)
		}
	}

	legacy := t.TempDir()
	seedLegacyPlaystateFile(t, legacy)
	other := openTestManager(t)
	if err := importFromSQLite(other.wdb, legacy); err != nil {
		t.Fatalf("legacy import: %v", err)
	}
	if got, ok := other.JellyfinPlaystateStore().Get("tv", "a"); !ok || got.Likes.Valid || got.PositionTicks != 50 {
		t.Fatalf("legacy row: %+v (ok=%v)", got, ok)
	}
}

// TestLikesColumnMigratesAnExistingDatabase opens a database written before
// favourites and likes existed: its rows keep their progress and read as
// unrated, and can then be rated.
func TestLikesColumnMigratesAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	seedLegacyPlaystateFile(t, dir)
	m, err := newManager(Settings{Backend: BackendSQLite}, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer m.Close()
	store := m.JellyfinPlaystateStore()
	got, ok := store.Get("tv", "a")
	if !ok || got.PositionTicks != 50 || got.Likes.Valid {
		t.Fatalf("migrated row: %+v (ok=%v)", got, ok)
	}
	if rows := store.ListForStream("tv"); len(rows) != 1 {
		t.Fatalf("listing lost the legacy row: %+v", rows)
	}
	if err := store.SetLikes("tv", "a", "movie", "tta", likesPtr(true)); err != nil {
		t.Fatalf("rate: %v", err)
	}
	if got, _ := store.Get("tv", "a"); likesOf(got) != "like" || got.PositionTicks != 50 {
		t.Fatalf("rated row: %+v", got)
	}
}

// TestJellyfinPlaystateGetWithErrorTellsMissingFromFailed: an absent row, a
// present one, an unreadable one and a failed query are four answers, where
// Get has only found and not found, and keeps them so.
func TestJellyfinPlaystateGetWithErrorTellsMissingFromFailed(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if st, ok, err := store.GetWithError("alice", "aa01"); err != nil || ok || st != (JellyfinPlaystate{}) {
		t.Fatalf("missing row: %+v ok=%v err=%v, want none and no error", st, ok, err)
	}
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "aa01", ContentType: "movie", ContentID: "tta", PositionTicks: 5}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.SetLikes("alice", "aa01", "movie", "tta", likesPtr(false)); err != nil {
		t.Fatalf("rate: %v", err)
	}
	st, ok, err := store.GetWithError("alice", "aa01")
	if err != nil || !ok || likesOf(st) != "dislike" || st.PositionTicks != 5 {
		t.Fatalf("present row: %+v ok=%v err=%v", st, ok, err)
	}
	if got, gotOK := store.Get("alice", "aa01"); !gotOK || got != st {
		t.Fatalf("Get disagrees with GetWithError: %+v ok=%v", got, gotOK)
	}

	// Postgres types its columns and will not hold such a row, so only
	// SQLite can be given an unreadable one.
	if testBackend() != "postgres" {
		if _, err := m.wdb.Exec(`UPDATE jellyfin_playstate SET last_played_at = 'yesterday' WHERE item_id = 'aa01'`); err != nil {
			t.Fatal(err)
		}
		if st, ok, err := store.GetWithError("alice", "aa01"); err == nil || ok || st != (JellyfinPlaystate{}) {
			t.Fatalf("unreadable row: %+v ok=%v err=%v, want an error", st, ok, err)
		}
		if _, ok := store.Get("alice", "aa01"); ok {
			t.Fatal("Get read an unreadable row")
		}
	}

	if _, err := m.wdb.Exec(`DROP TABLE jellyfin_playstate`); err != nil {
		t.Fatal(err)
	}
	if st, ok, err := store.GetWithError("alice", "aa01"); err == nil || ok || st != (JellyfinPlaystate{}) {
		t.Fatalf("failed query: %+v ok=%v err=%v, want an error", st, ok, err)
	}
	if _, ok := store.Get("alice", "aa01"); ok {
		t.Fatal("Get found a row after a failed query")
	}
}

// addLegacyPlaystateRow adds an unrated row to a file made by
// seedLegacyPlaystateFile, one that never had a likes column.
func addLegacyPlaystateRow(t *testing.T, dir, itemID string) {
	t.Helper()
	src, closeSrc, err := openSQLiteFile(filepath.Join(dir, dbFilename))
	if err != nil {
		t.Fatalf("open legacy file: %v", err)
	}
	defer closeSrc()
	if _, err := src.Exec(`INSERT INTO jellyfin_playstate VALUES ('tv', ?, 'movie', 'ttx', 0, 0, 0, 0, 0)`, itemID); err != nil {
		t.Fatalf("add legacy row: %v", err)
	}
}

// TestImportOfLegacyPlaystateKeepsDestinationLikes tests the copy of a first
// boot import, which leaves a row the destination already has as it is: a
// legacy file with no likes column changes no rating there, and the rows it
// adds are unrated. It runs the copy directly, not a backend switch.
func TestImportOfLegacyPlaystateKeepsDestinationLikes(t *testing.T) {
	dir := t.TempDir()
	seedLegacyPlaystateFile(t, dir)
	addLegacyPlaystateRow(t, dir, "b")

	target := openTestManager(t)
	store := target.JellyfinPlaystateStore()
	if err := store.Upsert(JellyfinPlaystate{StreamName: "tv", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 80}); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	if err := store.SetLikes("tv", "a", "movie", "tta", likesPtr(true)); err != nil {
		t.Fatalf("seed destination rating: %v", err)
	}
	if err := importFromSQLite(target.wdb, dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got, ok := store.Get("tv", "a"); !ok || likesOf(got) != "like" || got.PositionTicks != 80 {
		t.Fatalf("a destination row after a legacy import: %+v (ok=%v)", got, ok)
	}
	if got, ok := store.Get("tv", "b"); !ok || got.Likes.Valid {
		t.Fatalf("a row the import added: %+v (ok=%v), want unrated", got, ok)
	}
}

// seedRatedStream puts a row for each of items on a manager, rated as given.
func seedRatedStream(t *testing.T, m *StateManager, items map[string]*bool) {
	t.Helper()
	for id, likes := range items {
		if err := m.JellyfinPlaystateStore().Upsert(JellyfinPlaystate{StreamName: "tv", ItemID: id, ContentType: "movie", ContentID: "tt" + id}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if err := m.JellyfinPlaystateStore().SetLikes("tv", id, "movie", "tt"+id, likes); err != nil {
			t.Fatalf("rate %s: %v", id, err)
		}
	}
}

// wantRatings checks what a manager holds for the stream, by item.
func wantRatings(t *testing.T, m *StateManager, want map[string]string) {
	t.Helper()
	for id, state := range want {
		if got, ok := m.JellyfinPlaystateStore().Get("tv", id); !ok || likesOf(got) != state {
			t.Errorf("%s: %q (ok=%v), want %q", id, likesOf(got), ok, state)
		}
	}
}

// destinationRatings is what a database left behind by an earlier switch
// holds, to be overwritten by the live one.
func destinationRatings() map[string]*bool {
	return map[string]*bool{"aa01": likesPtr(false), "aa02": likesPtr(true), "aa03": likesPtr(true), "aa05": likesPtr(true)}
}

// sourceRatings is the live database: each of its three states, laid over
// the destination's rows, and a row the destination lacks.
func sourceRatings() map[string]*bool {
	return map[string]*bool{"aa01": likesPtr(true), "aa02": likesPtr(false), "aa03": nil, "aa04": likesPtr(true)}
}

// wantSwitched is what the destination holds after the live database wins:
// its three states carried over, the unrated one replacing a rating, and a
// row only the destination had left alone.
var wantSwitched = map[string]string{"aa01": "like", "aa02": "dislike", "aa03": "", "aa04": "like", "aa05": "like"}

// TestMigrateIntoCarriesLikes tests the copy a backend switch runs, directly:
// where the source wins, its like, dislike and unrated states replace the
// destination's, an unrated one with NULL.
func TestMigrateIntoCarriesLikes(t *testing.T) {
	source, target := openTestManager(t), openTestManager(t)
	seedRatedStream(t, source, sourceRatings())
	seedRatedStream(t, target, destinationRatings())
	if err := migrateInto(target.wdb, source.wdb); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	wantRatings(t, target, wantSwitched)
}

// TestBackendSwitchCarriesLikes is the switch itself: Reopen onto a database
// used before, with data migration on, carries the live database's ratings
// over it.
func TestBackendSwitchCarriesLikes(t *testing.T) {
	first, second := suiteDB(t), suiteDB(t)
	left := second.open(t)
	seedRatedStream(t, left, destinationRatings())
	if err := left.Close(); err != nil {
		t.Fatalf("close the destination: %v", err)
	}

	live := first.open(t)
	t.Cleanup(func() { _ = live.Close() })
	seedRatedStream(t, live, sourceRatings())

	second.settings.MigrateData = true
	closeOld, err := live.Reopen(second.settings, second.dataDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(closeOld)
	wantRatings(t, live, wantSwitched)
}
