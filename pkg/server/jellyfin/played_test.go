package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// folderFixture is a series around upcomingEpoch with every case a season
// or series mark has to tell apart:
//
//   - S1E1 aired; S1E2 undated after it, so aired, and listed a second time
//     with a date; S1E3 still to air.
//   - Specials: S0E1 aired, S0E2 still to air, S0E3 undated.
//   - S2, announced and undated after the last aired episode, so unaired.
//
// Aired: S1E1, S1E2, S0E1, S0E3.
func folderFixture(t *testing.T) (*fixture, itemID) {
	atEpoch(t)
	f := newFixture()
	show := addSeries(f, "tt0000201", "Show",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-30)},
		stremio.MetaVideo{Season: 1, Episode: 2},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(-1)},
		stremio.MetaVideo{Season: 1, Episode: 3, Released: day(5)},
		stremio.MetaVideo{Season: 0, Episode: 1, Released: day(-40)},
		stremio.MetaVideo{Season: 0, Episode: 2, Released: day(10)},
		stremio.MetaVideo{Season: 0, Episode: 3},
		stremio.MetaVideo{Season: 2, Episode: 1},
		stremio.MetaVideo{Season: 2, Episode: 2})
	return f, show
}

var kitchenAuth = []string{"Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-2", Token="` + testOtherToken + `"`}

// played lists the stream's played items, sorted.
func played(f *fixture, stream string) []string {
	var out []string
	for _, st := range f.play.ListForStream(stream) {
		if st.Played {
			out = append(out, st.ItemID)
		}
	}
	slices.Sort(out)
	return out
}

func wantPlayed(t *testing.T, f *fixture, stream string, want ...itemID) {
	t.Helper()
	ids := encodeIDs(want...)
	slices.Sort(ids)
	if got := played(f, stream); !slices.Equal(got, ids) {
		t.Fatalf("%s played %v, want %v", stream, got, ids)
	}
}

func wantFolder(t *testing.T, ud userData, isPlayed bool, unplayed int, percent float64) {
	t.Helper()
	if ud.Played != isPlayed || ud.UnplayedItemCount == nil || *ud.UnplayedItemCount != unplayed ||
		ud.PlayedPercentage == nil || *ud.PlayedPercentage != percent || ud.PlaybackPositionTicks != 0 {
		t.Fatalf("UserData %+v (unplayed %v, percent %v), want played=%v unplayed=%d percent=%v",
			ud, deref(ud.UnplayedItemCount), deref(ud.PlayedPercentage), isPlayed, unplayed, percent)
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestFolderEpisodes(t *testing.T) {
	f, show := folderFixture(t)
	meta := f.catalog.metas["series/tt0000201"]
	for _, c := range []struct {
		id   itemID
		want []itemID
	}{
		{show.season(1), []itemID{show.episode(1, 1), show.episode(1, 2)}},
		{show.season(0), []itemID{show.episode(0, 1), show.episode(0, 3)}},
		{show.season(2), nil},
		{show.season(9), nil},
		{show, []itemID{show.episode(1, 1), show.episode(1, 2), show.episode(0, 1), show.episode(0, 3)}},
	} {
		if got := folderEpisodes(c.id, meta); !slices.Equal(encodeIDs(got...), encodeIDs(c.want...)) {
			t.Errorf("%v S%d: %v, want %v", c.id.Kind, c.id.Season, encodeIDs(got...), encodeIDs(c.want...))
		}
	}
	// A Kitsu film is a one-episode series, and that episode is what it marks.
	film := &stremio.MetaObject{ID: "kitsu:1", Type: "anime", Released: "2001-07-20"}
	kitsu, _ := itemIDFor("anime", "kitsu:1")
	if got := folderEpisodes(kitsu, film); !slices.Equal(encodeIDs(got...), encodeIDs(kitsu.episode(1, 0))) {
		t.Errorf("kitsu film: %v", encodeIDs(got...))
	}
}

func TestMarkSeasonPlayed(t *testing.T) {
	f, show := folderFixture(t)
	s1 := show.season(1)
	// Part way through S1E2, a favourite; the season a favourite too.
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: show.episode(1, 2).encode(), ContentType: "series",
		ContentID: "tt0000201:1:2", PositionTicks: 600 * ticksPerSecond, RuntimeTicks: 2700 * ticksPerSecond, LastPlayedAt: upcomingEpoch})
	f.play.SetFavorite("living-room", show.episode(1, 2).encode(), "series", "tt0000201:1:2", true)
	favorite(f, "living-room", s1)
	if r := resume(t, f); len(r.Items) != 1 {
		t.Fatalf("resume before: %v", r.Items)
	}

	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+s1.encode()+"?datePlayed=2026-10-29T15%3A19%3A46Z&userId=u", ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	if !ud.IsFavorite || ud.ItemID != s1.encode() {
		t.Fatalf("season favourite lost: %+v", ud)
	}
	wantPlayed(t, f, "living-room", show.episode(1, 1), show.episode(1, 2))
	e2, _ := f.play.Get("living-room", show.episode(1, 2).encode())
	if e2.PositionTicks != 0 || !e2.IsFavorite || e2.PlayCount != 1 {
		t.Fatalf("part-played favourite: %+v", e2)
	}
	if r := resume(t, f); len(r.Items) != 0 {
		t.Fatalf("Continue Watching still lists %v", r.Items)
	}
	if n := len(f.play.ListForStream("living-room")); n != 3 {
		t.Fatalf("%d rows: the duplicate listing or an unaired episode was written", n)
	}

	// The detail page and the season list read what the mark answered.
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+s1.encode(), ""), &item)
	wantFolder(t, *item.UserData, true, 0, 100)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+s1.encode()+"/UserData", ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+show.encode(), ""), &item)
	wantFolder(t, *item.UserData, false, 2, 50)
	var seasons queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+show.encode()+"/Seasons", ""), &seasons)
	bySeason := map[string]*userData{}
	for _, it := range seasons.Items {
		bySeason[it.ID] = it.UserData
	}
	wantFolder(t, *bySeason[s1.encode()], true, 0, 100)
	wantFolder(t, *bySeason[show.season(0).encode()], false, 2, 0)
	// Nothing in S2 has aired: unplayed, with nothing to count.
	if s2 := bySeason[show.season(2).encode()]; s2.Played || s2.UnplayedItemCount != nil || s2.PlayedPercentage != nil {
		t.Fatalf("unaired season: %+v", s2)
	}

	// The Favourites row counts the season as its page does.
	var favs queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?Recursive=true&Filters=IsFavorite", ""), &favs)
	for _, it := range favs.Items {
		if it.ID == s1.encode() {
			wantFolder(t, *it.UserData, true, 0, 100)
		} else if it.ID != show.episode(1, 2).encode() || !it.UserData.Played || !it.UserData.IsFavorite {
			t.Fatalf("favourite row %s: %+v", it.ID, it.UserData)
		}
	}
	if len(favs.Items) != 2 {
		t.Fatalf("%d favourite rows", len(favs.Items))
	}

	// Again: the same answer, and the history left as it was.
	before, _ := f.play.Get("living-room", show.episode(1, 1).encode())
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/Users/u/PlayedItems/"+s1.encode(), ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	if after, _ := f.play.Get("living-room", show.episode(1, 1).encode()); after != before {
		t.Fatalf("repeat mark rewrote %+v as %+v", before, after)
	}

	// Another stream sees none of it.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+s1.encode(), "", kitchenAuth...), &item)
	wantFolder(t, *item.UserData, false, 2, 0)
	wantPlayed(t, f, "kitchen")

	// Unmarked through the other route form: having cleared episodes, it
	// answers uncounted without waiting on the metadata; again, with nothing
	// left to clear, it asks the metadata and answers counted.
	ud = unmark(t, f, "/jellyfin/Users/u/PlayedItems/"+s1.encode())
	if !ud.IsFavorite {
		t.Fatalf("unmarking dropped the season favourite: %+v", ud)
	}
	wantFolder(t, folderUD(t, f, s1), false, 2, 0)
	ud = userData{}
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/Users/u/PlayedItems/"+s1.encode(), ""), &ud)
	wantFolder(t, ud, false, 2, 0)
	if !ud.IsFavorite {
		t.Fatalf("unmarking again dropped the season favourite: %+v", ud)
	}
	wantPlayed(t, f, "living-room")
	if e2, _ := f.play.Get("living-room", show.episode(1, 2).encode()); !e2.IsFavorite || e2.PositionTicks != 0 {
		t.Fatalf("unmarked favourite: %+v", e2)
	}
}

func TestMarkSeriesPlayed(t *testing.T) {
	f, show := folderFixture(t)
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.encode(), ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	wantPlayed(t, f, "living-room", show.episode(1, 1), show.episode(1, 2), show.episode(0, 1), show.episode(0, 3))
	// Caught up: no Next Up, and Upcoming waits on S1E3.
	if p := nextUp(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("Next Up after the series: %v", p.ids())
	}
	wantUpcoming(t, upcoming(t, f, ""), show.episode(1, 3))

	// Unmarking a season leaves the rest of the series marked.
	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.season(0).encode())
	wantFolder(t, folderUD(t, f, show.season(0)), false, 2, 0)
	wantPlayed(t, f, "living-room", show.episode(1, 1), show.episode(1, 2))

	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.encode())
	wantFolder(t, folderUD(t, f, show), false, 4, 0)
	wantPlayed(t, f, "living-room")
	if p := nextUp(t, f, "seriesId="+show.encode()); !slices.Equal(p.ids(), encodeIDs(show.episode(1, 1))) {
		t.Fatalf("Next Up after unmarking: %v", p.ids())
	}
}

// TestMarkSeasonMovesNextUp marks seasons the way a viewer catching up
// would, and reads Next Up and Upcoming after each.
func TestMarkSeasonMovesNextUp(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := addSeries(f, "tt0000202", "Catching Up",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-60)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(-53)},
		stremio.MetaVideo{Season: 2, Episode: 1, Released: day(-7)},
		stremio.MetaVideo{Season: 2, Episode: 2, Released: day(7)})
	mark := func(method string, id itemID) {
		t.Helper()
		if rec := f.do(method, "/jellyfin/UserPlayedItems/"+id.encode(), ""); rec.Code != http.StatusOK {
			t.Fatalf("%s %v: %d", method, id.Kind, rec.Code)
		}
	}
	mark(http.MethodPost, show.season(1))
	if p := nextUp(t, f, ""); !slices.Equal(p.ids(), encodeIDs(show.episode(2, 1))) {
		t.Fatalf("Next Up after S1: %v", p.ids())
	}
	if p := upcoming(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("Upcoming with an aired episode left: %v", p.ids())
	}
	mark(http.MethodPost, show.season(2))
	if p := nextUp(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("Next Up after S2: %v", p.ids())
	}
	wantUpcoming(t, upcoming(t, f, ""), show.episode(2, 2))
	mark(http.MethodDelete, show.season(2))
	if p := nextUp(t, f, ""); !slices.Equal(p.ids(), encodeIDs(show.episode(2, 1))) {
		t.Fatalf("Next Up after unmarking S2: %v", p.ids())
	}
}

func TestMarkFolderPlayedWithoutMetadata(t *testing.T) {
	f, show := folderFixture(t)
	gone, _ := itemIDFor("series", "tt0000999")
	f.play.SetPlayed("living-room", gone.episode(1, 1).encode(), "series", "tt0000999:1:1", true)
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: gone.episode(1, 2).encode(), ContentType: "series", ContentID: "tt0000999:1:2", PositionTicks: 60})

	// Marking watched needs to know what has aired.
	if rec := f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+gone.season(1).encode(), ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("mark without metadata: %d", rec.Code)
	}
	wantPlayed(t, f, "living-room", gone.episode(1, 1))
	// Unmarking does not: it clears what is stored, and answers unplayed
	// with nothing counted.
	var ud userData
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/UserPlayedItems/"+gone.season(1).encode(), ""), &ud)
	if ud.Played || ud.UnplayedItemCount != nil || ud.PlayedPercentage != nil {
		t.Fatalf("unmark without metadata: %+v", ud)
	}
	wantPlayed(t, f, "living-room")
	if st, _ := f.play.Get("living-room", gone.episode(1, 2).encode()); st.PositionTicks != 0 {
		t.Fatalf("progress kept: %+v", st)
	}
	// A favourite needs no metadata either.
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+gone.encode()+"/UserData", `{"IsFavorite":true,"Played":false}`), &ud)
	if !ud.IsFavorite || ud.Played || ud.UnplayedItemCount != nil {
		t.Fatalf("favourite without metadata: %+v", ud)
	}
	// With an episode played too: the favourite is stored and answered,
	// uncounted, and the echoed Played changes nothing.
	f.play.SetPlayed("living-room", gone.episode(1, 1).encode(), "series", "tt0000999:1:1", true)
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+gone.season(1).encode()+"/UserData", `{"IsFavorite":true,"Played":false}`), &ud)
	if !ud.IsFavorite || ud.UnplayedItemCount != nil {
		t.Fatalf("favourite of a part-watched season without metadata: %+v", ud)
	}
	wantPlayed(t, f, "living-room", gone.episode(1, 1))

	// With metadata switched off the title does not exist here.
	f.catalog.disabled = true
	if rec := f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.encode(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("mark with metadata disabled: %d", rec.Code)
	}
	wantPlayed(t, f, "living-room", gone.episode(1, 1))
}

func TestMarkPlayedWriteFailure(t *testing.T) {
	f, show := folderFixture(t)
	f.play.SetPlayed("living-room", show.episode(1, 1).encode(), "series", "tt0000201:1:1", true)
	f.play.playedErr = errors.New("disk full")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + show.season(1).encode(), ""},
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + show.encode(), ""},
		{http.MethodDelete, "/jellyfin/Users/u/PlayedItems/" + show.season(1).encode(), ""},
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + show.episode(1, 2).encode(), ""},
		{http.MethodDelete, "/jellyfin/UserPlayedItems/" + show.episode(1, 1).encode(), ""},
		{http.MethodPost, "/jellyfin/UserItems/" + show.episode(1, 2).encode() + "/UserData", `{"Played":true}`},
	} {
		if rec := f.do(c.method, c.path, c.body); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s %s: %d, want 500", c.method, c.path, c.body, rec.Code)
		}
	}
	wantPlayed(t, f, "living-room", show.episode(1, 1))
}

func TestMarkPlayedRejectsOtherItems(t *testing.T) {
	f, _ := folderFixture(t)
	for _, raw := range []string{viewID("tmdb.trending.movie"), genreID("series", "Drama"), "0402010000019813d000000001000000", "not-an-id"} {
		for _, path := range []string{"/jellyfin/UserPlayedItems/" + raw, "/jellyfin/Users/u/PlayedItems/" + raw} {
			if rec := f.do(http.MethodPost, path, ""); rec.Code != http.StatusNotFound {
				t.Errorf("POST %s: %d", path, rec.Code)
			}
		}
	}
	if rows := f.play.ListForStream("living-room"); len(rows) != 0 {
		t.Fatalf("refused marks wrote %v", rows)
	}
}

// A client that has gone by the time the episodes are known gets nothing
// written.
func TestMarkFolderPlayedCancelled(t *testing.T) {
	f, show := folderFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/jellyfin/UserPlayedItems/"+show.encode(), strings.NewReader(""))
	req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-1", Token="`+testToken+`"`)
	f.server.Handler().ServeHTTP(httptest.NewRecorder(), req)
	wantPlayed(t, f, "living-room")
}

// Marks racing each other, on one stream and across two, end with every
// aired episode marked and nothing lost; run under -race.
func TestMarkFolderPlayedConcurrently(t *testing.T) {
	f, show := folderFixture(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := show.season(1)
			if i%2 == 1 {
				id = show
			}
			headers := []string(nil)
			if i%4 == 3 {
				headers = kitchenAuth
			}
			if rec := f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+id.encode(), "", headers...); rec.Code != http.StatusOK {
				t.Errorf("mark %d: %d", i, rec.Code)
			}
		}()
	}
	wg.Wait()
	all := []itemID{show.episode(1, 1), show.episode(1, 2), show.episode(0, 1), show.episode(0, 3)}
	wantPlayed(t, f, "living-room", all...)
	wantPlayed(t, f, "kitchen", all...)
}

// The marks over the real store: one transaction per folder, per stream.
func TestMarkFolderPlayedOverTheStore(t *testing.T) {
	mgr, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("GetManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	f, show := folderFixture(t)
	store := mgr.JellyfinPlaystateStore()
	f.server.opts.Playstate = store
	if err := store.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: show.episode(1, 2).encode(), ContentType: "series", ContentID: "tt0000201:1:2", PositionTicks: 600}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite("living-room", show.episode(1, 2).encode(), "series", "tt0000201:1:2", true); err != nil {
		t.Fatal(err)
	}

	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.encode(), ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	for _, ep := range []itemID{show.episode(1, 1), show.episode(1, 2), show.episode(0, 1), show.episode(0, 3)} {
		if st, ok := store.Get("living-room", ep.encode()); !ok || !st.Played || st.PositionTicks != 0 || st.ContentID != ep.playStremioID() {
			t.Fatalf("S%dE%d: %+v", ep.Season, ep.Episode, st)
		}
	}
	if st, _ := store.Get("living-room", show.episode(1, 2).encode()); !st.IsFavorite {
		t.Fatalf("favourite lost: %+v", st)
	}
	if _, ok := store.Get("living-room", show.episode(1, 3).encode()); ok {
		t.Fatal("an unaired episode was marked")
	}
	if rows := store.ListForStream("kitchen"); len(rows) != 0 {
		t.Fatalf("kitchen has %v", rows)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+show.season(1).encode(), "", kitchenAuth...), new(baseItem))

	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.season(1).encode())
	wantFolder(t, folderUD(t, f, show.season(1)), false, 2, 0)
	if st, _ := store.Get("living-room", show.episode(0, 1).encode()); !st.Played {
		t.Fatal("unmarking S1 cleared a special")
	}
	if n := len(store.ListForStream("living-room")); n != 4 {
		t.Fatalf("%d rows after unmarking, want the 4 marked", n)
	}
}

func resume(t *testing.T, f *fixture) queryResult {
	t.Helper()
	var r queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/Resume", ""), &r)
	return r
}

// TestOdinSequentialEpisodeMarks replays Odin's "mark all episodes until
// here": one POST /UserPlayedItems per episode, 24 in a row with one
// datePlayed, on the episode route this change leaves as it was. A second
// stream marks the same season with one season-level POST. The episodes end
// in the same watched state either way; what differs is the dates, since a
// single mark restamps an episode already watched and a season mark keeps
// it.
func TestOdinSequentialEpisodeMarks(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	var videos []stremio.MetaVideo
	for e := 1; e <= 24; e++ {
		videos = append(videos, stremio.MetaVideo{Season: 1, Episode: e, Released: day(-200 + 7*e)})
	}
	videos = append(videos, stremio.MetaVideo{Season: 1, Episode: 25, Released: day(14)})
	show := addSeries(f, "tt0000301", "Daily", videos...)
	for _, stream := range []string{"living-room", "kitchen"} {
		// E3 part-way and a favourite; E5 watched three times.
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: stream, ItemID: show.episode(1, 3).encode(), ContentType: "series",
			ContentID: "tt0000301:1:3", PositionTicks: 600 * ticksPerSecond, RuntimeTicks: 2700 * ticksPerSecond, PlayCount: 1, LastPlayedAt: upcomingEpoch})
		f.play.SetFavorite(stream, show.episode(1, 3).encode(), "series", "tt0000301:1:3", true)
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: stream, ItemID: show.episode(1, 5).encode(), ContentType: "series",
			ContentID: "tt0000301:1:5", Played: true, PlayCount: 3, LastPlayedAt: upcomingEpoch.AddDate(0, -1, 0)})
	}

	odin := func() {
		t.Helper()
		for e := 1; e <= 24; e++ {
			ep := show.episode(1, e)
			var ud userData
			decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+ep.encode()+"?datePlayed=2026-10-29T15%3A51%3A40Z&userId=u", ""), &ud)
			if !ud.Played || ud.PlaybackPositionTicks != 0 || ud.ItemID != ep.encode() || ud.UnplayedItemCount != nil {
				t.Fatalf("E%d: %+v", e, ud)
			}
		}
	}
	odin()
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), "", kitchenAuth...), &ud)
	wantFolder(t, ud, true, 0, 100)

	for e := 1; e <= 25; e++ {
		key := show.episode(1, e).encode()
		a, aok := f.play.Get("living-room", key)
		b, bok := f.play.Get("kitchen", key)
		if aok != bok || a.Played != b.Played || a.PositionTicks != b.PositionTicks || a.IsFavorite != b.IsFavorite || a.PlayCount != b.PlayCount {
			t.Fatalf("E%d: sequential %+v, season %+v", e, a, b)
		}
		if e <= 24 && (!a.Played || a.PositionTicks != 0) || e == 25 && aok {
			t.Fatalf("E%d: %+v (stored %v)", e, a, aok)
		}
	}
	if e3, _ := f.play.Get("living-room", show.episode(1, 3).encode()); !e3.IsFavorite || e3.PlayCount != 1 {
		t.Fatalf("E3: %+v", e3)
	}
	e5a, _ := f.play.Get("living-room", show.episode(1, 5).encode())
	e5b, _ := f.play.Get("kitchen", show.episode(1, 5).encode())
	if e5a.PlayCount != 3 || e5b.PlayCount != 3 || !e5b.LastPlayedAt.Equal(upcomingEpoch.AddDate(0, -1, 0)) {
		t.Fatalf("E5: sequential %+v, season %+v", e5a, e5b)
	}

	// Both streams: caught up, nothing to resume, waiting on E25.
	for _, headers := range [][]string{nil, kitchenAuth} {
		if p := nextUp(t, f, "", headers...); len(p.Items) != 0 {
			t.Fatalf("Next Up: %v", p.ids())
		}
		var r queryResult
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/Resume", "", headers...), &r)
		if len(r.Items) != 0 {
			t.Fatalf("Continue Watching: %d items", len(r.Items))
		}
		wantUpcoming(t, upcoming(t, f, "", headers...), show.episode(1, 25))
	}

	// Repeated, the sequence still succeeds and counts no new plays.
	odin()
	if e5, _ := f.play.Get("living-room", show.episode(1, 5).encode()); e5.PlayCount != 3 {
		t.Fatalf("repeat counted a play: %+v", e5)
	}
}

// TestMarkSeasonWithNothingAired covers seasons whose episodes have not
// aired. Jellyfin counts only real episodes, and its unaired ones are
// virtual, so it calls such a season watched; it also hides the season.
// Here the season is listed, so it stays unwatched with nothing counted,
// the same in the mark's answer and every read after it, and nothing is
// written.
func TestMarkSeasonWithNothingAired(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := addSeries(f, "tt0000302", "Returning",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-10)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(10)},
		stremio.MetaVideo{Season: 2, Episode: 1, Released: day(20)},
		stremio.MetaVideo{Season: 2, Episode: 2, Released: day(30)},
		stremio.MetaVideo{Season: 3, Episode: 1},
		stremio.MetaVideo{Season: 3, Episode: 2})
	unaired := addSeries(f, "tt0000303", "Announced",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(40)},
		stremio.MetaVideo{Season: 1, Episode: 2})
	empty := func(where string, ud *userData) {
		t.Helper()
		if ud.Played || ud.UnplayedItemCount != nil || ud.PlayedPercentage != nil {
			t.Fatalf("%s: %+v", where, ud)
		}
	}
	for _, id := range []itemID{show.season(2), show.season(3), unaired.season(1), unaired} {
		var seasons queryResult
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+id.encode(), ""), &ud)
		empty("mark", &ud)
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+id.encode()+"/UserData", ""), &ud)
		empty("UserData", &ud)
		var item baseItem
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+id.encode(), ""), &item)
		empty("item", item.UserData)
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+id.series().encode()+"/Seasons", ""), &seasons)
		for _, it := range seasons.Items {
			if it.ID == id.encode() {
				empty("season list", it.UserData)
			}
		}
	}
	if rows := f.play.ListForStream("living-room"); len(rows) != 0 {
		t.Fatalf("nothing aired, yet wrote %v", rows)
	}
	// Mixed: the aired episode is the season.
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	wantPlayed(t, f, "living-room", show.episode(1, 1))
	// A season the metadata does not list is not found, as its item is not.
	if rec := f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(9).encode(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("mark a missing season: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+show.season(9).encode(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing season item: %d", rec.Code)
	}
}

// TestFolderFavouriteKeepsEpisodeHistory sends a series and a season the
// UserData bodies clients send: whole UserData with the favourite, its
// Played true or false, stale or not, with the metadata there and gone, and
// echoes without IsFavorite. A body with IsFavorite sets the favourite and
// nothing else; one without is refused. No episode is touched by either.
// Before, Played with IsFavorite was compared with the counted state and
// refused with 409 when it differed, which a fully watched series shown
// unwatched in a catalog row always did, and Played alone marked or cleared
// every episode.
func TestFolderFavouriteKeepsEpisodeHistory(t *testing.T) {
	f, show := folderFixture(t)
	// The series fully watched, its S1E2 part-way through a rewatch and a
	// favourite.
	f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.encode(), "")
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: show.episode(1, 1).encode(), ContentType: "series",
		ContentID: "tt0000201:1:1", Played: true, PlayCount: 3, LastPlayedAt: upcomingEpoch.AddDate(0, -2, 0)})
	f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: show.episode(1, 2).encode(), ContentType: "series",
		ContentID: "tt0000201:1:2", Played: true, PlayCount: 1, PositionTicks: 600, RuntimeTicks: 2700, LastPlayedAt: upcomingEpoch})
	f.play.SetFavorite("living-room", show.episode(1, 2).encode(), "series", "tt0000201:1:2", true)
	episodes := func() map[string]persistence.JellyfinPlaystate {
		out := map[string]persistence.JellyfinPlaystate{}
		for _, st := range f.play.ListForStream("living-room") {
			if id, _ := decodeItemID(st.ItemID); id.Kind == kindEpisode {
				out[st.ItemID] = st
			}
		}
		return out
	}
	before := episodes()
	post := func(id itemID, body string) *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "/jellyfin/UserItems/"+id.encode()+"/UserData", body)
	}
	unchanged := func(what string) {
		t.Helper()
		if after := episodes(); !maps.Equal(before, after) {
			t.Fatalf("%s rewrote episodes:\n%v\n%v", what, before, after)
		}
	}
	for _, id := range []itemID{show, show.season(1)} {
		for _, body := range []string{
			// What a catalog row, which does not count episodes, shows.
			`{"IsFavorite":true,"Played":false,"PlaybackPositionTicks":0}`,
			`{"IsFavorite":false,"Played":true}`,
			`{"IsFavorite":true,"Played":true,"PlayCount":0}`,
			`{"IsFavorite":false,"Played":false}`,
		} {
			var ud userData
			decodeInto(t, post(id, body), &ud)
			fav := strings.Contains(body, `"IsFavorite":true`)
			if ud.IsFavorite != fav || !ud.Played || *ud.UnplayedItemCount != 0 {
				t.Fatalf("%v %s: %+v", id.Kind, body, ud)
			}
			if st, _ := f.play.Get("living-room", id.encode()); st.IsFavorite != fav {
				t.Fatalf("%v %s: favourite stored %v", id.Kind, body, st.IsFavorite)
			}
			unchanged(body)
		}
		// Without IsFavorite there is nothing a folder takes here.
		for _, body := range []string{`{"Played":false,"PlaybackPositionTicks":0}`, `{"Played":true}`, `{"Played":false}`} {
			if rec := post(id, body); rec.Code != http.StatusNotFound {
				t.Fatalf("%v %s: %d, want 404", id.Kind, body, rec.Code)
			}
			unchanged(body)
		}
	}

	// Metadata gone: the favourite still saves and answers, uncounted,
	// whatever Played says.
	delete(f.catalog.metas, "series/tt0000201")
	for _, body := range []string{`{"IsFavorite":true,"Played":false}`, `{"IsFavorite":true,"Played":true}`} {
		var ud userData
		decodeInto(t, post(show.season(1), body), &ud)
		if !ud.IsFavorite || ud.UnplayedItemCount != nil {
			t.Fatalf("outage %s: %+v", body, ud)
		}
		unchanged("outage " + body)
	}
}

// seasonOfTen is a season of ten aired episodes, five watched, two of the
// rest part-way, on the stream given.
func seasonOfTen(t *testing.T, f *fixture, stream string) itemID {
	t.Helper()
	var videos []stremio.MetaVideo
	for e := 1; e <= 10; e++ {
		videos = append(videos, stremio.MetaVideo{Season: 1, Episode: e, Released: day(-100 + e)})
	}
	show := addSeries(f, "tt0000401", "Ten", videos...)
	for e := 1; e <= 5; e++ {
		f.play.SetPlayed(stream, show.episode(1, e).encode(), "series", show.episode(1, e).playStremioID(), true)
	}
	for _, e := range []int{6, 8} {
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: stream, ItemID: show.episode(1, e).encode(), ContentType: "series",
			ContentID: show.episode(1, e).playStremioID(), PositionTicks: 300 * ticksPerSecond, LastPlayedAt: upcomingEpoch})
	}
	return show
}

// TestUnmarkPartWatchedSeason unmarks a part-watched season through both
// played routes, on two streams, and expects every episode cleared, its
// progress too, the same way. Played false in a UserData body, which may be
// any client's echo, changes nothing.
func TestUnmarkPartWatchedSeason(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := seasonOfTen(t, f, "living-room")
	seasonOfTen(t, f, "kitchen")
	before := f.play.ListForStream("living-room")
	if rec := f.do(http.MethodPost, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", `{"Played":false}`); rec.Code != http.StatusNotFound {
		t.Fatalf("UserData Played false: %d, want 404", rec.Code)
	}
	if !sameRows(before, f.play.ListForStream("living-room")) {
		t.Fatal("UserData Played false changed episodes")
	}
	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.season(1).encode())
	wantFolder(t, folderUD(t, f, show.season(1)), false, 10, 0)
	unmark(t, f, "/jellyfin/Users/u/PlayedItems/"+show.season(1).encode(), kitchenAuth...)
	wantFolder(t, folderUD(t, f, show.season(1), kitchenAuth...), false, 10, 0)
	for e := 1; e <= 10; e++ {
		key := show.episode(1, e).encode()
		a, aok := f.play.Get("living-room", key)
		b, bok := f.play.Get("kitchen", key)
		if a.Played || a.PositionTicks != 0 || aok != bok || a.Played != b.Played || a.PositionTicks != b.PositionTicks || a.PlayCount != b.PlayCount {
			t.Fatalf("E%d: %+v, legacy route %+v", e, a, b)
		}
	}
	if r := resume(t, f); len(r.Items) != 0 {
		t.Fatalf("Continue Watching after unmarking: %d", len(r.Items))
	}
}

// TestUnmarkWithoutMetadata unmarks while the metadata source is down, and
// while the request is cancelled as the answer's metadata is fetched: the
// stored episodes are cleared either way, since the unmark comes first and
// needs none. Before, DELETE fetched it first and failed.
func TestUnmarkWithoutMetadata(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := seasonOfTen(t, f, "living-room")
	delete(f.catalog.metas, "series/tt0000401")
	var ud userData
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), ""), &ud)
	if ud.Played || ud.UnplayedItemCount != nil || ud.PlayedPercentage != nil {
		t.Fatalf("unmark without metadata: %+v", ud)
	}
	wantPlayed(t, f, "living-room")
	if r := resume(t, f); len(r.Items) != 0 {
		t.Fatalf("progress kept: %d resumable", len(r.Items))
	}

	seasonOfTen(t, f, "living-room")
	ctx, cancel := context.WithCancel(context.Background())
	f.catalog.onMeta = func(context.Context) { cancel() }
	req := httptest.NewRequestWithContext(ctx, http.MethodDelete, "/jellyfin/UserPlayedItems/"+show.encode(), strings.NewReader(""))
	req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-1", Token="`+testToken+`"`)
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE cancelled during the answer's metadata: %d", rec.Code)
	}
	wantPlayed(t, f, "living-room")
}

// TestMissingSeason covers a season the metadata does not list, on every
// route, against a metadata source that fails. The listed metadata decides
// it is not there: 404, and nothing written, but for an unmark that found
// episodes stored under it, which it clears and answers as any unmark does,
// so a 404 never follows a change. A failed metadata source never makes a
// 404: a mark needs the metadata and fails with it, while an unmark and a
// favourite need none and go ahead.
func TestMissingSeason(t *testing.T) {
	f, show := folderFixture(t)
	gone, _ := itemIDFor("series", "tt0000999")
	orphan := show.season(7).episode(7, 1)
	f.play.SetPlayed("living-room", orphan.encode(), "series", orphan.playStremioID(), true)
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + show.season(9).encode(), "", http.StatusNotFound},
		{http.MethodDelete, "/jellyfin/UserPlayedItems/" + show.season(9).encode(), "", http.StatusNotFound},
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + show.season(9).encode(), "", http.StatusNotFound},
		{http.MethodDelete, "/jellyfin/UserFavoriteItems/" + show.season(9).encode(), "", http.StatusNotFound},
		{http.MethodPost, "/jellyfin/UserItems/" + show.season(9).encode() + "/UserData", `{"IsFavorite":true}`, http.StatusNotFound},
		{http.MethodGet, "/jellyfin/UserItems/" + show.season(9).encode() + "/UserData", "", http.StatusNotFound},
		{http.MethodGet, "/jellyfin/Items/" + show.season(9).encode(), "", http.StatusNotFound},
		// Season 7 is unlisted too, but has a played episode stored.
		{http.MethodDelete, "/jellyfin/Users/u/PlayedItems/" + show.season(7).encode(), "", http.StatusOK},
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + gone.season(1).encode(), "", http.StatusBadGateway},
		// Nothing of it watched: surely unwatched, without metadata too.
		{http.MethodGet, "/jellyfin/UserItems/" + gone.season(1).encode() + "/UserData", "", http.StatusOK},
		{http.MethodDelete, "/jellyfin/UserPlayedItems/" + gone.season(1).encode(), "", http.StatusOK},
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + gone.season(1).encode(), "", http.StatusOK},
		{http.MethodPost, "/jellyfin/UserItems/" + show.encode() + "/UserData", `{"IsFavorite":true}`, http.StatusOK},
	} {
		if rec := f.do(c.method, c.path, c.body); rec.Code != c.status {
			t.Errorf("%s %s %s: %d, want %d", c.method, c.path, c.body, rec.Code, c.status)
		}
	}
	if st, ok := f.play.Get("living-room", show.season(9).encode()); ok {
		t.Fatalf("a missing season got a row: %+v", st)
	}
	if st, _ := f.play.Get("living-room", orphan.encode()); st.Played {
		t.Fatalf("the unmark that answered 200 kept %+v", st)
	}
	if st, _ := f.play.Get("living-room", gone.season(1).encode()); !st.IsFavorite {
		t.Fatal("a favourite was refused for a failed metadata source")
	}
}

// TestFavouriteWithoutMetadata favourites a part-watched season whose
// metadata cannot be had: ten episodes, E1-E3 played, no rows for the rest.
// The favourite is stored and answered 200, since a stored favourite must
// not fail; the answer cannot count the season, so it is the season's own
// UserData, uncounted, rather than a guess from the stored rows (all played,
// though seven episodes are not). A read of the season's UserData, which
// stores nothing, fails as the season's page does. Once the metadata is back
// the counts are right, and an echo of either Played still only favourites.
func TestFavouriteWithoutMetadata(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	var videos []stremio.MetaVideo
	for e := 1; e <= 10; e++ {
		videos = append(videos, stremio.MetaVideo{Season: 1, Episode: e, Released: day(-100 + e)})
	}
	show := addSeries(f, "tt0000403", "Partway", videos...)
	for e := 1; e <= 3; e++ {
		f.play.SetPlayed("living-room", show.episode(1, e).encode(), "series", show.episode(1, e).playStremioID(), true)
	}
	meta := f.catalog.metas["series/tt0000403"]
	delete(f.catalog.metas, "series/tt0000403")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + show.season(1).encode(), ""},
		{http.MethodPost, "/jellyfin/UserItems/" + show.season(1).encode() + "/UserData", `{"IsFavorite":true,"Played":true}`},
	} {
		var ud userData
		decodeInto(t, f.do(c.method, c.path, c.body), &ud)
		if !ud.IsFavorite || ud.Played || ud.UnplayedItemCount != nil || ud.PlayedPercentage != nil {
			t.Fatalf("%s %s %s: %+v", c.method, c.path, c.body, ud)
		}
	}
	if st, _ := f.play.Get("living-room", show.season(1).encode()); !st.IsFavorite {
		t.Fatal("favourite not stored")
	}
	if rec := f.do(http.MethodGet, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("UserData read without metadata: %d, want 502", rec.Code)
	}

	// Back: counted, and the echoes of a client that cached either Played
	// only set the favourite.
	f.catalog.metas["series/tt0000403"] = meta
	var ud userData
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", ""), &ud)
	wantFolder(t, ud, false, 7, 30)
	for _, body := range []string{`{"IsFavorite":false,"Played":true}`, `{"IsFavorite":false,"Played":false}`} {
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", body), &ud)
		if ud.IsFavorite || ud.Played || *ud.UnplayedItemCount != 7 {
			t.Fatalf("echo %s after the outage: %+v", body, ud)
		}
	}
	wantPlayed(t, f, "living-room", show.episode(1, 1), show.episode(1, 2), show.episode(1, 3))

	// Slow metadata delays the answer, which is counted.
	f.catalog.metaDelay = 50 * time.Millisecond
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+show.season(1).encode(), ""), &ud)
	wantFolder(t, ud, false, 7, 30)
	if !ud.IsFavorite {
		t.Fatalf("slow metadata: %+v", ud)
	}
}

// TestPlayStateReads counts the stream's play-state reads per request: full
// reads of the stream (ListForStream) and reads of one series' rows
// (ListPrefix). Before, a page read the whole stream once per series or
// season it rendered, and a mark two or three times. Next Up and Upcoming
// read the stream once, as they did, and nothing more.
func TestPlayStateReads(t *testing.T) {
	f, show := folderFixture(t)
	other := addSeries(f, "tt0000402", "Other", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-5)})
	f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), "")
	favorite(f, "living-room", show)
	reads := func(method, path, body string) (full, scoped int) {
		t.Helper()
		f.play.mu.Lock()
		f.play.lists, f.play.prefixReads = 0, 0
		f.play.mu.Unlock()
		if rec := f.do(method, path, body); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d", method, path, rec.Code)
		}
		f.play.mu.Lock()
		defer f.play.mu.Unlock()
		return f.play.lists, f.play.prefixReads
	}
	ids := strings.Join(encodeIDs(show, show.season(1), show.season(0), other, other.season(1)), ",")
	for _, c := range []struct {
		method, path, body string
		full, scoped       int
	}{
		// One read per series, however many of its seasons are asked for.
		{http.MethodGet, "/jellyfin/Items?Ids=" + ids, "", 0, 2},
		// The favourites query itself (the fake's ListFavorites reads the
		// stream), and the one favourite series' episodes.
		{http.MethodGet, "/jellyfin/Items?Filters=IsFavorite&Ids=" + ids, "", 1, 1},
		{http.MethodGet, "/jellyfin/Items?Recursive=true&Filters=IsFavorite", "", 1, 1},
		// Its episodes, and its seasons' own rows.
		{http.MethodGet, "/jellyfin/Shows/" + show.encode() + "/Seasons", "", 0, 2},
		{http.MethodGet, "/jellyfin/Items/" + show.season(1).encode(), "", 0, 1},
		// A mark reads the series once, to answer; an unmark that cleared
		// something, nothing.
		{http.MethodPost, "/jellyfin/UserPlayedItems/" + show.encode(), "", 0, 1},
		{http.MethodDelete, "/jellyfin/UserPlayedItems/" + show.season(1).encode(), "", 0, 0},
		{http.MethodPost, "/jellyfin/UserItems/" + show.season(0).encode() + "/UserData", `{"IsFavorite":true,"Played":true}`, 0, 1},
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + show.encode(), "", 0, 1},
		{http.MethodGet, "/jellyfin/Shows/NextUp", "", 1, 0},
		{http.MethodGet, "/jellyfin/Shows/Upcoming", "", 1, 0},
	} {
		if full, scoped := reads(c.method, c.path, c.body); full != c.full || scoped != c.scoped {
			t.Errorf("%s %s %s: %d full and %d series reads, want %d and %d", c.method, c.path, c.body, full, scoped, c.full, c.scoped)
		}
	}
}

// TestFolderPlayStateReadFailure fails every read of a series' stored
// episodes. Nothing is read as no rows: a lookup fails; a mark or unmark
// reads nothing, neither to decide what to write nor to answer, so it is
// unaffected; a favourite, which must not fail once stored, and a page show
// the folder uncounted, as an unreadable row is shown unplayed. Before, a
// failed read was an empty stream: an unmark cleared nothing and answered
// 200 unwatched.
func TestFolderPlayStateReadFailure(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := seasonOfTen(t, f, "living-room")
	f.play.readErr = errors.New("database is closed")
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		// A lookup has nothing to show but what it cannot read.
		{http.MethodGet, "/jellyfin/UserItems/" + show.season(1).encode() + "/UserData", "", http.StatusInternalServerError},
		// A mark reads nothing, and answers what it wrote.
		{http.MethodDelete, "/jellyfin/UserPlayedItems/" + show.season(1).encode(), "", http.StatusOK},
		// A favourite is stored, and answered uncounted.
		{http.MethodPost, "/jellyfin/UserItems/" + show.season(1).encode() + "/UserData", `{"IsFavorite":true,"Played":false}`, http.StatusOK},
	} {
		if rec := f.do(c.method, c.path, c.body); rec.Code != c.status {
			t.Fatalf("%s %s %s: %d, want %d", c.method, c.path, c.body, rec.Code, c.status)
		}
	}
	wantPlayed(t, f, "living-room")
	if st, _ := f.play.Get("living-room", show.season(1).encode()); !st.IsFavorite {
		t.Fatal("the favourite was not stored")
	}
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/"+show.season(1).encode(), ""), &item)
	if item.UserData.Played || item.UserData.UnplayedItemCount != nil || item.UserData.PlayedPercentage != nil {
		t.Fatalf("page with unreadable episodes: %+v", item.UserData)
	}
	f.play.readErr = nil
	var ud userData
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", ""), &ud)
	wantFolder(t, ud, false, 10, 0)
}

// TestUnmarkDecidesAsItWrites: an unmark reads nothing, before or after it
// writes, so nothing played between a read and the write can be missed, as
// one was when the episodes to clear came from a read made first. A read
// here would mark E5 played first, as another device finishing it would;
// none happens, and the clear catches everything played, E5 included.
func TestUnmarkDecidesAsItWrites(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := seasonOfTen(t, f, "living-room")
	e5 := show.episode(1, 5)
	reads := 0
	f.play.onRead = func() {
		reads++
		f.play.SetPlayed("living-room", e5.encode(), "series", e5.playStremioID(), true)
	}
	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.season(1).encode())
	if reads != 0 {
		t.Fatalf("the unmark read the season %d times", reads)
	}
	wantPlayed(t, f, "living-room")
}

// TestUnmarkScope unmarks one season of a series with seasons whose numbers
// share digits, beside another series and another stream: only that season
// is cleared.
func TestUnmarkScope(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	var videos []stremio.MetaVideo
	for _, s := range []int{1, 10, 16, 256} {
		videos = append(videos, stremio.MetaVideo{Season: s, Episode: 1, Released: day(-10)}, stremio.MetaVideo{Season: s, Episode: 16, Released: day(-9)})
	}
	show := addSeries(f, "tt0000501", "Long", videos...)
	near := addSeries(f, "tt0000510", "Near", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-10)})
	for _, stream := range []string{"living-room", "kitchen"} {
		f.play.MarkPlayedMany(stream, []persistence.JellyfinPlaystate{{ItemID: near.episode(1, 1).encode()}})
		for _, v := range videos {
			f.play.MarkPlayedMany(stream, []persistence.JellyfinPlaystate{{ItemID: show.episode(v.Season, v.Episode).encode()}})
		}
	}
	if rec := f.do(http.MethodDelete, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), ""); rec.Code != http.StatusOK {
		t.Fatalf("unmark: %d", rec.Code)
	}
	var want []itemID
	for _, v := range videos {
		if v.Season != 1 {
			want = append(want, show.episode(v.Season, v.Episode))
		}
	}
	wantPlayed(t, f, "living-room", append(want, near.episode(1, 1))...)
	if n := len(played(f, "kitchen")); n != len(videos)+1 {
		t.Fatalf("kitchen: %d played", n)
	}
	if rec := f.do(http.MethodDelete, "/jellyfin/UserPlayedItems/"+show.encode(), ""); rec.Code != http.StatusOK {
		t.Fatalf("unmark series: %d", rec.Code)
	}
	wantPlayed(t, f, "living-room", near.episode(1, 1))
}

// TestFavouritesPageCountsOnlyItsRows lists 200 favourite series twenty at a
// time: each page reads the episodes of its own twenty, not of all 200.
// Before, every favourite series was counted before the page was cut.
func TestFavouritesPageCountsOnlyItsRows(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	for i := range 200 {
		id := addSeries(f, fmt.Sprintf("tt%07d", 600+i), fmt.Sprintf("Fav %03d", i), stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-5)})
		favorite(f, "living-room", id)
		if i%2 == 0 {
			f.play.SetPlayed("living-room", id.episode(1, 1).encode(), "series", id.episode(1, 1).playStremioID(), true)
		}
	}
	seen := map[string]bool{}
	for start := 0; start < 200; start += 20 {
		f.play.mu.Lock()
		f.play.prefixReads = 0
		f.play.mu.Unlock()
		var page queryResult
		decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Items?Recursive=true&Filters=IsFavorite&StartIndex=%d&Limit=20", start), ""), &page)
		f.play.mu.Lock()
		reads := f.play.prefixReads
		f.play.mu.Unlock()
		if page.TotalRecordCount != 200 || len(page.Items) != 20 || reads != 20 {
			t.Fatalf("page at %d: total %d, %d rows, %d series reads", start, page.TotalRecordCount, len(page.Items), reads)
		}
		for _, item := range page.Items {
			if seen[item.ID] || !item.UserData.IsFavorite || item.UserData.UnplayedItemCount == nil {
				t.Fatalf("row %s: seen %v, %+v", item.ID, seen[item.ID], item.UserData)
			}
			seen[item.ID] = true
		}
	}
}

// TestMarkDropsCachedEpisodeStates reads a season's episodes, marks it,
// and reads again within one request: the second read is the stored state,
// not the first read's. Marking and unmarking both.
func TestMarkDropsCachedEpisodeStates(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := seasonOfTen(t, f, "living-room")
	rq := parseRequest(httptest.NewRequest(http.MethodGet, "/jellyfin/Items", nil))
	rq.stream = &auth.Stream{Username: "living-room"}
	meta := f.catalog.metas["series/tt0000401"]
	count := func() int {
		t.Helper()
		ud, err := f.server.countedUserData(rq, show.season(1), meta, f.server.userData(rq, show.season(1)))
		if err != nil {
			t.Fatal(err)
		}
		return *ud.UnplayedItemCount
	}
	if n := count(); n != 5 {
		t.Fatalf("before: %d unplayed", n)
	}
	if err := f.server.markFolderPlayed(rq, show.season(1), meta); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("after marking: %d unplayed", n)
	}
	if _, err := f.server.clearFolderPlayed(rq, show.season(1)); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 10 {
		t.Fatalf("after unmarking: %d unplayed", n)
	}
}

// TestMarkWithoutStore marks a season with no play-state store, which keeps
// nothing: the answer is what is stored, unwatched, as a single episode's
// is, not the watched state a mark would have left. Before, it answered
// every aired episode watched.
func TestMarkWithoutStore(t *testing.T) {
	f, show := folderFixture(t)
	f.server.opts.Playstate = nil
	var mark, read userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), ""), &mark)
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", ""), &read)
	if mark.Played || read.Played || *mark.UnplayedItemCount != 2 || *read.UnplayedItemCount != 2 {
		t.Fatalf("mark %+v, read %+v", mark, read)
	}
	var ep userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.episode(1, 1).encode(), ""), &ep)
	if ep.Played {
		t.Fatalf("single episode without a store: %+v", ep)
	}
}

// TestWatchedUnairedEpisodesCount: an episode the stream has watched counts
// though the metadata dates it in the future or lists it undated in an
// announced season, as Jellyfin counts an episode it has a file for; one
// unaired and unwatched does not, and a mark still writes aired episodes
// only. Before, watched unaired episodes were left out, so a season of them
// read unwatched and uncounted.
func TestWatchedUnairedEpisodesCount(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := addSeries(f, "tt0000701", "Early",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-10)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(2)},
		stremio.MetaVideo{Season: 1, Episode: 3, Released: day(9)},
		stremio.MetaVideo{Season: 2, Episode: 1},
		stremio.MetaVideo{Season: 2, Episode: 2},
		stremio.MetaVideo{Season: 2, Episode: 3})
	// S1E2 dated two days ahead, watched already; S2 announced undated,
	// its first two watched.
	for _, ep := range []itemID{show.episode(1, 2), show.episode(2, 1), show.episode(2, 2)} {
		f.play.SetPlayed("living-room", ep.encode(), "series", ep.playStremioID(), true)
	}
	season := func(id itemID) userData {
		t.Helper()
		var ud userData
		decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+id.encode()+"/UserData", ""), &ud)
		return ud
	}
	// S1 counts E1 (aired) and E2 (watched), not E3.
	wantFolder(t, season(show.season(1)), false, 1, 50)
	// S2 counts its two watched episodes; S2E3, unaired and unwatched, not.
	wantFolder(t, season(show.season(2)), true, 0, 100)
	var seasons queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+show.encode()+"/Seasons", ""), &seasons)
	for _, it := range seasons.Items {
		switch it.ID {
		case show.season(1).encode():
			wantFolder(t, *it.UserData, false, 1, 50)
		case show.season(2).encode():
			wantFolder(t, *it.UserData, true, 0, 100)
		}
	}
	// Marking S1 writes E1 only, and answers what reads after it see.
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	wantFolder(t, season(show.season(1)), true, 0, 100)
	if _, ok := f.play.Get("living-room", show.episode(1, 3).encode()); ok {
		t.Fatal("marking wrote an unaired episode")
	}
	// Marking S2, nothing aired, writes nothing and answers its stored state.
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserPlayedItems/"+show.season(2).encode(), ""), &ud)
	wantFolder(t, ud, true, 0, 100)
	if _, ok := f.play.Get("living-room", show.episode(2, 3).encode()); ok {
		t.Fatal("marking wrote an unaired episode")
	}
	// Unmarking clears the watched unaired ones too, and the answer agrees.
	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.encode())
	wantFolder(t, season(show), false, 1, 0)
	if r := season(show.season(2)); r.Played || r.UnplayedItemCount != nil {
		t.Fatalf("S2 after unmarking: %+v", r)
	}
}

// TestUnfavouriteDroppedSeason: a season favourited while the metadata
// listed it, then renumbered away, can still be unfavourited, on its stream
// alone; nothing else is done to it, and one with no favourite stays not
// found. Before, every request for it was refused, so the favourite could
// never be cleared.
func TestUnfavouriteDroppedSeason(t *testing.T) {
	f, show := folderFixture(t)
	s1, s2 := show.season(1), show.season(2)
	for _, headers := range [][]string{nil, kitchenAuth} {
		for _, id := range []itemID{s1, s2} {
			if rec := f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+id.encode(), "", headers...); rec.Code != http.StatusOK {
				t.Fatalf("favourite: %d", rec.Code)
			}
		}
	}
	// The source renumbers: season 1 is gone.
	meta := *f.catalog.metas["series/tt0000201"]
	meta.Videos = slices.DeleteFunc(slices.Clone(meta.Videos), func(v stremio.MetaVideo) bool { return v.Season == 1 })
	f.catalog.metas["series/tt0000201"] = &meta
	// What is stored for it stays readable, through both route forms and
	// UserData, and a favourite already there is no change to refuse.
	writes := f.play.favWrites
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/jellyfin/UserFavoriteItems/" + s1.encode(), ""},
		{http.MethodGet, "/jellyfin/Users/u/FavoriteItems/" + s1.encode(), ""},
		{http.MethodGet, "/jellyfin/UserItems/" + s1.encode() + "/UserData", ""},
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + s1.encode(), ""},
		{http.MethodPost, "/jellyfin/UserItems/" + s1.encode() + "/UserData", `{"IsFavorite":true}`},
	} {
		var ud userData
		decodeInto(t, f.do(c.method, c.path, c.body), &ud)
		if !ud.IsFavorite || ud.UnplayedItemCount != nil {
			t.Fatalf("%s %s %s: %+v", c.method, c.path, c.body, ud)
		}
	}
	if f.play.favWrites != writes {
		t.Fatalf("reading a dropped season wrote %d times", f.play.favWrites-writes)
	}
	var ud userData
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/UserFavoriteItems/"+s1.encode(), ""), &ud)
	if ud.IsFavorite {
		t.Fatalf("unfavourited: %+v", ud)
	}
	for _, c := range []struct {
		stream string
		id     itemID
		want   bool
	}{{"living-room", s1, false}, {"living-room", s2, true}, {"kitchen", s1, true}, {"kitchen", s2, true}} {
		if st, _ := f.play.Get(c.stream, c.id.encode()); st.IsFavorite != c.want {
			t.Fatalf("%s S%d: favourite %v, want %v", c.stream, c.id.Season, st.IsFavorite, c.want)
		}
	}
	// Cleared, favouriting it again would be a new favourite on a season
	// that is not there: refused, through either route form, nothing
	// stored. Clearing it again changes nothing, and answers what is stored.
	for _, path := range []string{"/jellyfin/UserFavoriteItems/" + s1.encode(), "/jellyfin/Users/u/FavoriteItems/" + s1.encode()} {
		if rec := f.do(http.MethodPost, path, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("refavourite %s: %d, want 404", path, rec.Code)
		}
	}
	if st, _ := f.play.Get("living-room", s1.encode()); st.IsFavorite {
		t.Fatal("a refused favourite was stored")
	}
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/UserFavoriteItems/"+s1.encode(), ""), &ud)
	if ud.IsFavorite {
		t.Fatalf("second unfavourite: %+v", ud)
	}
	// Never stored at all, a season the metadata does not list is not found.
	if rec := f.do(http.MethodGet, "/jellyfin/UserFavoriteItems/"+show.season(9).encode(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unstored dropped season: %d", rec.Code)
	}
	// On the other stream, through UserData.
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+s1.encode()+"/UserData", `{"IsFavorite":false,"Played":true}`, kitchenAuth...), &ud)
	if st, _ := f.play.Get("kitchen", s1.encode()); ud.IsFavorite || st.IsFavorite {
		t.Fatalf("kitchen unfavourite: %+v", ud)
	}
	// A failed metadata source is not a dropped season.
	delete(f.catalog.metas, "series/tt0000201")
	decodeInto(t, f.do(http.MethodDelete, "/jellyfin/UserFavoriteItems/"+s2.encode(), ""), &ud)
	if st, _ := f.play.Get("living-room", s2.encode()); ud.IsFavorite || st.IsFavorite {
		t.Fatalf("unfavourite during an outage: %+v", ud)
	}
}

// unmark sends a DELETE that clears stored episodes, and checks its answer:
// unwatched, and uncounted, since an unmark that changed something does not
// wait on the metadata for the counts.
func unmark(t *testing.T, f *fixture, path string, headers ...string) userData {
	t.Helper()
	var ud userData
	decodeInto(t, f.do(http.MethodDelete, path, "", headers...), &ud)
	if ud.Played || ud.UnplayedItemCount != nil || ud.PlayedPercentage != nil {
		t.Fatalf("unmark %s: %+v", path, ud)
	}
	return ud
}

// folderUD reads a season's or series' UserData afresh.
func folderUD(t *testing.T, f *fixture, id itemID, headers ...string) userData {
	t.Helper()
	var ud userData
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/UserItems/"+id.encode()+"/UserData", "", headers...), &ud)
	return ud
}

// TestUnmarkDoesNotWaitOnMetadata: an unmark that has cleared episodes
// answers without asking the metadata, slow, failing or switched off. Only
// one that cleared nothing asks it, to tell a missing season. Before, every
// unmark waited on the metadata after it had committed.
func TestUnmarkDoesNotWaitOnMetadata(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	show := seasonOfTen(t, f, "living-room")
	asked := 0
	f.catalog.onMeta = func(context.Context) { asked++ }
	f.catalog.disabled = true
	unmark(t, f, "/jellyfin/UserPlayedItems/"+show.season(1).encode())
	if asked != 0 {
		t.Fatalf("an unmark that cleared episodes asked the metadata %d times", asked)
	}
	wantPlayed(t, f, "living-room")
	// Nothing left to clear: the metadata decides, and switched off it does
	// not make the season missing.
	if rec := f.do(http.MethodDelete, "/jellyfin/UserPlayedItems/"+show.season(1).encode(), ""); rec.Code != http.StatusOK || asked != 1 {
		t.Fatalf("unmark with nothing to clear: %d, metadata asked %d times", rec.Code, asked)
	}
}

// TestEpisodeUserDataRetryConverges: a UserData body setting an episode's
// favourite and Played, when the played write fails, keeps the favourite it
// wrote first (as the favourite has always gone first) and answers 500; the
// same body sent again, the store working, applies what is left and nothing
// twice.
func TestEpisodeUserDataRetryConverges(t *testing.T) {
	f, show := folderFixture(t)
	ep := show.episode(1, 1)
	body := `{"IsFavorite":true,"Played":true}`
	f.play.playedErr = errors.New("disk full")
	if rec := f.do(http.MethodPost, "/jellyfin/UserItems/"+ep.encode()+"/UserData", body); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed played write: %d", rec.Code)
	}
	if st, _ := f.play.Get("living-room", ep.encode()); !st.IsFavorite || st.Played {
		t.Fatalf("after the failure: %+v", st)
	}
	f.play.playedErr = nil
	favWrites := f.play.favWrites
	var ud userData
	decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserItems/"+ep.encode()+"/UserData", body), &ud)
	if !ud.IsFavorite || !ud.Played || f.play.favWrites != favWrites {
		t.Fatalf("retry: %+v, %d favourite writes", ud, f.play.favWrites-favWrites)
	}
}

// TestFolderFavouriteWritesOnlyChanges: a favourite already set is not
// written again, through either route, and a request already cancelled
// writes nothing.
func TestFolderFavouriteWritesOnlyChanges(t *testing.T) {
	f, show := folderFixture(t)
	s1 := show.season(1)
	f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+s1.encode(), "")
	writes := f.play.favWrites
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/jellyfin/UserFavoriteItems/" + s1.encode(), ""},
		{http.MethodPost, "/jellyfin/UserItems/" + s1.encode() + "/UserData", `{"IsFavorite":true,"Played":false,"Likes":true}`},
	} {
		var ud userData
		decodeInto(t, f.do(c.method, c.path, c.body), &ud)
		if !ud.IsFavorite {
			t.Fatalf("%s %s: %+v", c.method, c.path, ud)
		}
	}
	if f.play.favWrites != writes {
		t.Fatalf("an unchanged favourite was written %d times", f.play.favWrites-writes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodDelete, "/jellyfin/UserFavoriteItems/"+s1.encode(), strings.NewReader(""))
	req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-1", Token="`+testToken+`"`)
	f.server.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if st, _ := f.play.Get("living-room", s1.encode()); !st.IsFavorite || f.play.favWrites != writes {
		t.Fatalf("a cancelled request wrote: %+v", st)
	}
}

// TestFolderUserDataReadWithoutMetadata: a season's UserData without its
// metadata, switched off or failing, is answered when none of its stored
// episodes is watched, surely unwatched, as it always was; with one watched
// it cannot be told, and fails with 502, never as a missing season. Before,
// switched-off metadata made it 404.
func TestFolderUserDataReadWithoutMetadata(t *testing.T) {
	f, show := folderFixture(t)
	f.catalog.disabled = true
	if ud := folderUD(t, f, show.season(1)); ud.Played || ud.UnplayedItemCount != nil {
		t.Fatalf("unwatched without metadata: %+v", ud)
	}
	// Watched and a favourite: the state cannot be told, which is a 502, not
	// a missing season, whether the metadata is switched off or failing.
	f.play.SetPlayed("living-room", show.episode(1, 1).encode(), "series", "tt0000201:1:1", true)
	f.play.SetFavorite("living-room", show.season(1).encode(), "series", "tt0000201", true)
	if rec := f.do(http.MethodGet, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("watched, metadata switched off: %d, want 502", rec.Code)
	}
	f.catalog.disabled = false
	delete(f.catalog.metas, "series/tt0000201")
	if rec := f.do(http.MethodGet, "/jellyfin/UserItems/"+show.season(1).encode()+"/UserData", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("watched, metadata failing: %d, want 502", rec.Code)
	}
	f.catalog.disabled = true
	// Season 2 shares the series but none of its episodes is watched.
	if ud := folderUD(t, f, show.season(2)); ud.Played {
		t.Fatalf("season 2: %+v", ud)
	}
}

// TestSeasonListReadFailure: a season list whose play state cannot be read
// fails, rather than list every season unfavourited and unwatched, which a
// client posting its whole UserData back could then store. Before, a failed
// read of the seasons' own rows was taken for none.
func TestSeasonListReadFailure(t *testing.T) {
	f, show := folderFixture(t)
	favorite(f, "living-room", show.season(1))
	f.play.readErr = errors.New("database is busy")
	for _, path := range []string{
		"/jellyfin/Shows/" + show.encode() + "/Seasons",
		"/jellyfin/Shows/" + show.encode() + "/Episodes?seasonId=" + show.season(1).encode(),
		"/jellyfin/Items?ParentId=" + show.encode(),
	} {
		if rec := f.do(http.MethodGet, path, ""); rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: %d, want 500", path, rec.Code)
		}
	}
	f.play.readErr = nil
	var seasons queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/"+show.encode()+"/Seasons", ""), &seasons)
	for _, it := range seasons.Items {
		if it.UserData.IsFavorite != (it.ID == show.season(1).encode()) {
			t.Fatalf("%s: favourite %v", it.ID, it.UserData.IsFavorite)
		}
	}
}
