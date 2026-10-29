package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

func TestUpcomingVideo(t *testing.T) {
	shows := []stremio.MetaVideo{
		{Season: 1, Episode: 1, Released: "2020-01-01"},
		{Season: 1, Episode: 2, Released: "2020-01-08"},
		{Season: 0, Episode: 1, Released: "2098-06-01"},
		{Season: 1, Episode: 3, Released: "2099-01-01"},
		{Season: 1, Episode: 4, Released: "2099-01-08T20:00:00Z"},
	}
	// A new season announced: its premiere dated, the rest to be announced.
	announced := []stremio.MetaVideo{
		{Season: 1, Episode: 10, Released: "2020-01-01"},
		{Season: 2, Episode: 1, Released: "2099-01-01"},
		{Season: 2, Episode: 2},
		{Season: 2, Episode: 3},
	}
	unreleased := []stremio.MetaVideo{{Season: 1, Episode: 1, Released: "2099-03-01"}, {Season: 1, Episode: 2, Released: "2099-03-08"}}
	played := persistence.JellyfinPlaystate{Played: true}
	partial := persistence.JellyfinPlaystate{PositionTicks: 600 * ticksPerSecond}
	type states = map[episodeKey]persistence.JellyfinPlaystate
	cases := []struct {
		name   string
		videos []stremio.MetaVideo
		states states
		want   episodeKey
		none   bool
	}{
		{name: "caught up waits on the next dated episode", videos: shows, states: states{{1, 1}: played, {1, 2}: played}, want: episodeKey{1, 3}},
		{name: "an aired episode left to watch means none", videos: shows, states: states{{1, 1}: played}, none: true},
		{name: "a part-played aired episode means none", videos: shows, states: states{{1, 1}: played, {1, 2}: partial}, none: true},
		{name: "part-played behind the anchor is not left to watch", videos: shows, states: states{{1, 1}: partial, {1, 2}: played}, want: episodeKey{1, 3}},
		{name: "unwatched with aired episodes means none", videos: shows, none: true},
		{name: "unwatched and unreleased waits on the premiere", videos: unreleased, want: episodeKey{1, 1}},
		{name: "the next after a played future episode", videos: shows, states: states{{1, 3}: played}, want: episodeKey{1, 4}},
		{name: "nothing dated after the anchor means none", videos: announced, states: states{{2, 1}: played}, none: true},
		{name: "a dated premiere before undated episodes", videos: announced, states: states{{1, 10}: played}, want: episodeKey{2, 1}},
		{name: "undated after aired is aired", videos: []stremio.MetaVideo{{Season: 1, Episode: 1, Released: "2020-01-01"}, {Season: 1, Episode: 2}, {Season: 1, Episode: 3, Released: "2099-01-01"}}, states: states{{1, 1}: played}, none: true},
		{name: "an unreadable date is no date", videos: []stremio.MetaVideo{{Season: 1, Episode: 1, Released: "2020-01-01"}, {Season: 1, Episode: 2, Released: "soon"}}, states: states{{1, 1}: played}, none: true},
		{name: "a special is never it", videos: []stremio.MetaVideo{{Season: 1, Episode: 1, Released: "2020-01-01"}, {Season: 0, Episode: 1, Released: "2099-01-01"}}, states: states{{1, 1}: played}, none: true},
		{name: "every episode played", videos: shows, states: states{{1, 4}: played}, none: true},
		// Playback contradicting the dates: the dates are read as Next Up reads them.
		{name: "undated after a played future episode is passed over", videos: []stremio.MetaVideo{{Season: 2, Episode: 1, Released: "2098-01-01"}, {Season: 2, Episode: 2}, {Season: 2, Episode: 3, Released: "2099-01-01"}}, states: states{{2, 1}: played}, want: episodeKey{2, 3}},
		{name: "a part-played episode still to air is the one waited on", videos: shows, states: states{{1, 1}: played, {1, 2}: played, {1, 3}: partial}, want: episodeKey{1, 3}},
		{name: "no episodes", none: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := upcomingVideo(c.videos, c.states, now())
			if c.none {
				if ok {
					t.Fatalf("picked S%dE%d, want none", v.Season, v.Episode)
				}
				return
			}
			if !ok || (episodeKey{v.Season, v.Episode}) != c.want {
				t.Fatalf("picked S%dE%d (%v), want S%dE%d", v.Season, v.Episode, ok, c.want[0], c.want[1])
			}
		})
	}
}

// upcomingEpoch is the clock Upcoming's HTTP tests read: noon, so that a
// bare air date, midnight UTC, is never the instant itself.
var upcomingEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func atEpoch(t *testing.T) {
	t.Helper()
	prev := now
	now = func() time.Time { return upcomingEpoch }
	t.Cleanup(func() { now = prev })
}

// day is the air date days after upcomingEpoch.
func day(days int) string { return upcomingEpoch.AddDate(0, 0, days).Format("2006-01-02") }

func addSeries(f *fixture, contentID, name string, videos ...stremio.MetaVideo) itemID {
	f.catalog.metas["series/"+contentID] = &stremio.MetaObject{ID: contentID, Type: "series", Name: name, Poster: "https://img.test/" + contentID + ".jpg", Videos: videos}
	id, _ := itemIDFor("series", contentID)
	return id
}

func favorite(f *fixture, stream string, series itemID) {
	f.play.SetFavorite(stream, series.encode(), series.ContentType, series.baseStremioID(), true)
}

// upcomingFixture is four series around upcomingEpoch: Alpha airing, with a
// special; Bravo airing with an aired episode between; Charlie not yet
// premiered; Delta with an aired episode and one to come.
func upcomingFixture(t *testing.T) (f *fixture, alpha, bravo, charlie, delta itemID) {
	atEpoch(t)
	f = newFixture()
	alpha = addSeries(f, "tt0000101", "Alpha",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-10)},
		stremio.MetaVideo{Season: 0, Episode: 1, Released: day(3)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(5)},
		stremio.MetaVideo{Season: 1, Episode: 3, Released: day(12)})
	bravo = addSeries(f, "tt0000102", "Bravo",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-20)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(-3)},
		stremio.MetaVideo{Season: 1, Episode: 3, Released: day(2)})
	charlie = addSeries(f, "tt0000103", "Charlie",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(8)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(15)},
		stremio.MetaVideo{Season: 1, Episode: 3})
	delta = addSeries(f, "tt0000104", "Delta",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-30)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(1)})
	return f, alpha, bravo, charlie, delta
}

type upcomingPage struct {
	Items []struct {
		ID                    string `json:"Id"`
		Type                  string
		SeriesID              string `json:"SeriesId"`
		SeriesName            string
		SeasonID              string `json:"SeasonId"`
		ParentIndexNumber     *int
		IndexNumber           *int
		PremiereDate          string
		LocationType          string
		SeriesPrimaryImageTag string
		ImageTags             map[string]string
		UserData              *struct{ Played bool }
	}
	TotalRecordCount int
	StartIndex       int
}

func upcoming(t *testing.T, f *fixture, query string, headers ...string) upcomingPage {
	t.Helper()
	var p upcomingPage
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/Upcoming?"+query, "", headers...), &p)
	return p
}

func (p upcomingPage) ids() []string {
	out := []string{}
	for _, item := range p.Items {
		out = append(out, item.ID)
	}
	return out
}

func wantUpcoming(t *testing.T, got upcomingPage, want ...itemID) {
	t.Helper()
	if !slices.Equal(got.ids(), encodeIDs(want...)) {
		t.Fatalf("items %v, want %v", got.ids(), encodeIDs(want...))
	}
}

func TestUpcomingListsFollowedSeriesByAirDate(t *testing.T) {
	f, alpha, bravo, charlie, delta := upcomingFixture(t)
	if p := upcoming(t, f, ""); len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("nothing followed: %v total %d", p.ids(), p.TotalRecordCount)
	}

	record(f, "living-room", alpha.episode(1, 1), true, 0, 1)
	record(f, "living-room", bravo.episode(1, 1), true, 0, 2)
	favorite(f, "living-room", charlie)
	favorite(f, "living-room", delta)
	// Bravo has its aired second episode to watch, Delta its aired first:
	// both are Next Up's. Alpha's special airs first but is not listed.
	p := upcoming(t, f, "UserId=u")
	wantUpcoming(t, p, alpha.episode(1, 2), charlie.episode(1, 1))
	if p.TotalRecordCount != 2 || p.StartIndex != 0 {
		t.Fatalf("total %d start %d", p.TotalRecordCount, p.StartIndex)
	}
	first := p.Items[0]
	if first.Type != "Episode" || first.SeriesID != alpha.encode() || first.SeriesName != "Alpha" ||
		first.SeasonID != alpha.season(1).encode() || *first.ParentIndexNumber != 1 || *first.IndexNumber != 2 ||
		first.PremiereDate != premiereDate(day(5)) || first.LocationType != "Virtual" ||
		first.SeriesPrimaryImageTag == "" || first.UserData == nil || first.UserData.Played {
		t.Fatalf("item %+v", first)
	}

	// Part way through Bravo's aired episode still leaves it to watch.
	record(f, "living-room", bravo.episode(1, 2), false, 600*ticksPerSecond, 3)
	wantUpcoming(t, upcoming(t, f, ""), alpha.episode(1, 2), charlie.episode(1, 1))
	// Caught up, Bravo waits on its next, which airs soonest.
	record(f, "living-room", bravo.episode(1, 2), true, 0, 4)
	p = upcoming(t, f, "")
	wantUpcoming(t, p, bravo.episode(1, 3), alpha.episode(1, 2), charlie.episode(1, 1))

	// Paged over the sorted list, with the total of all of it.
	p = upcoming(t, f, "StartIndex=1&Limit=1")
	wantUpcoming(t, p, alpha.episode(1, 2))
	if p.TotalRecordCount != 3 || p.StartIndex != 1 {
		t.Fatalf("page 2: total %d start %d", p.TotalRecordCount, p.StartIndex)
	}
	if p = upcoming(t, f, "StartIndex=3&Limit=1"); len(p.Items) != 0 || p.TotalRecordCount != 3 {
		t.Fatalf("past the end: %v total %d", p.ids(), p.TotalRecordCount)
	}

	// Watching Delta's aired episode makes it wait on the next.
	record(f, "living-room", delta.episode(1, 1), true, 0, 5)
	wantUpcoming(t, upcoming(t, f, ""), delta.episode(1, 2), bravo.episode(1, 3), alpha.episode(1, 2), charlie.episode(1, 1))
}

// A favourite is not history: one with an aired episode left is in neither
// Next Up nor the general Upcoming, while its series page still lists what
// is to come.
func TestUpcomingFavouriteIsNotHistory(t *testing.T) {
	f, _, _, _, delta := upcomingFixture(t)
	favorite(f, "living-room", delta)
	if p := nextUp(t, f, ""); len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("Next Up for a favourite never watched: %v total %d", p.ids(), p.TotalRecordCount)
	}
	if p := upcoming(t, f, ""); len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("Upcoming for a favourite with an aired episode left: %v total %d", p.ids(), p.TotalRecordCount)
	}
	wantUpcoming(t, upcoming(t, f, "ParentId="+delta.encode()), delta.episode(1, 2))
}

func TestUpcomingWindow(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	at := func(d time.Duration) string { return upcomingEpoch.Add(d).Format(time.RFC3339) }
	window := addSeries(f, "tt0000201", "Window",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: at(0)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: at(time.Second)},
		stremio.MetaVideo{Season: 1, Episode: 3, Released: at(90 * 24 * time.Hour)},
		stremio.MetaVideo{Season: 1, Episode: 4, Released: at(90*24*time.Hour + time.Second)})
	// The instant itself has aired; ninety days on is the last inside.
	wantUpcoming(t, upcoming(t, f, "ParentId="+window.encode()), window.episode(1, 2), window.episode(1, 3))

	// A followed series waiting on an episode past the window is not
	// listed, and no later one stands in.
	far := addSeries(f, "tt0000202", "Far",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(91)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(10)})
	favorite(f, "living-room", far)
	near := addSeries(f, "tt0000203", "Near",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-5)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(90)})
	record(f, "living-room", near.episode(1, 1), true, 0, 1)
	wantUpcoming(t, upcoming(t, f, ""), near.episode(1, 2))
}

func TestUpcomingOrdersEqualDatesStably(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	// Named against id order, two sharing a name, all airing the same day.
	var want []itemID
	for i, name := range []string{"Zulu", "Mike", "Mike", "Alpha"} {
		id := addSeries(f, fmt.Sprintf("tt05%05d", i), name, stremio.MetaVideo{Season: 1, Episode: 1, Released: day(4)})
		favorite(f, "living-room", id)
		want = append(want, id.episode(1, 1))
	}
	want = []itemID{want[3], want[1], want[2], want[0]}
	for range 10 {
		wantUpcoming(t, upcoming(t, f, ""), want...)
	}
}

func TestUpcomingParent(t *testing.T) {
	f, alpha, bravo, charlie, _ := upcomingFixture(t)
	record(f, "living-room", alpha.episode(1, 1), true, 0, 1)
	favorite(f, "living-room", charlie)

	// A series lists all its dated episodes still to air, watched or not,
	// specials and the undated left out.
	wantUpcoming(t, upcoming(t, f, "ParentId="+alpha.encode()), alpha.episode(1, 2), alpha.episode(1, 3))
	wantUpcoming(t, upcoming(t, f, "ParentId="+bravo.encode()), bravo.episode(1, 3))
	p := upcoming(t, f, "ParentId="+charlie.encode()+"&Limit=1")
	wantUpcoming(t, p, charlie.episode(1, 1))
	if p.TotalRecordCount != 2 {
		t.Fatalf("series total %d", p.TotalRecordCount)
	}
	// A season narrows to its own.
	wantUpcoming(t, upcoming(t, f, "ParentId="+alpha.season(1).encode()), alpha.episode(1, 2), alpha.episode(1, 3))
	for _, season := range []int{0, 2} {
		if p := upcoming(t, f, "ParentId="+alpha.season(season).encode()); len(p.Items) != 0 {
			t.Fatalf("season %d: %v", season, p.ids())
		}
	}

	// A library narrows the followed series to its content type.
	wantUpcoming(t, upcoming(t, f, "ParentId="+viewID("tmdb.trending.series")), alpha.episode(1, 2), charlie.episode(1, 1))
	movie, _ := itemIDFor("movie", "tt0111161")
	lost, _ := itemIDFor("series", "tt0000099")
	for _, parent := range []string{
		viewID("tmdb.trending.movie"), viewID("no.such.catalog"), "junk",
		alpha.episode(1, 2).encode(), movie.encode(), lost.encode(),
	} {
		if p := upcoming(t, f, "ParentId="+parent); len(p.Items) != 0 || p.TotalRecordCount != 0 {
			t.Fatalf("ParentId %s: %v total %d", parent, p.ids(), p.TotalRecordCount)
		}
	}
}

func TestUpcomingKeepsStreamsApart(t *testing.T) {
	f, alpha, _, charlie, _ := upcomingFixture(t)
	kitchen := []string{"Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-2", Token="` + testOtherToken + `"`}
	record(f, "living-room", alpha.episode(1, 1), true, 0, 1)
	favorite(f, "kitchen", charlie)
	wantUpcoming(t, upcoming(t, f, ""), alpha.episode(1, 2))
	wantUpcoming(t, upcoming(t, f, "", kitchen...), charlie.episode(1, 1))
}

func TestUpcomingSkipsFailedMetadata(t *testing.T) {
	f, alpha, _, _, _ := upcomingFixture(t)
	lost, _ := itemIDFor("series", "tt0000099")
	record(f, "living-room", alpha.episode(1, 1), true, 0, 1)
	favorite(f, "living-room", lost)
	p := upcoming(t, f, "")
	wantUpcoming(t, p, alpha.episode(1, 2))
	if p.TotalRecordCount != 1 {
		t.Fatalf("total %d", p.TotalRecordCount)
	}
}

// Every followed series is looked at once, at most metaFetchLimit at a time,
// and the list is sorted by air date across all of them.
func TestUpcomingLooksAtEverySeriesOnce(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	f.catalog.metaDelay = 2 * time.Millisecond
	const n = 30
	var want []itemID
	for i := range n {
		// The most recently watched airs last.
		id := addSeries(f, fmt.Sprintf("tt04%05d", i), fmt.Sprintf("S%02d", i),
			stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-1)},
			stremio.MetaVideo{Season: 1, Episode: 2, Released: day(n - i)})
		record(f, "living-room", id.episode(1, 1), true, 0, i)
		want = append(want, id.episode(1, 2))
	}
	slices.Reverse(want)
	p := upcoming(t, f, "Limit=5")
	wantUpcoming(t, p, want[:5]...)
	if p.TotalRecordCount != n || f.catalog.metaCalls != n {
		t.Fatalf("total %d, %d lookups, want %d each", p.TotalRecordCount, f.catalog.metaCalls, n)
	}
	if f.catalog.peakMeta < 2 || f.catalog.peakMeta > metaFetchLimit {
		t.Fatalf("%d lookups at once, want 2..%d", f.catalog.peakMeta, metaFetchLimit)
	}
}

func TestUpcomingAnswersNothingWhenTheClientGoes(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	for i := 1; i <= 40; i++ {
		favorite(f, "living-room", addSeries(f, fmt.Sprintf("tt03%05d", i), "Show", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(i)}))
	}
	f.catalog.metaDelay = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/jellyfin/Shows/Upcoming", nil).WithContext(ctx)
	req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-1", Token="`+testToken+`"`)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.server.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f.catalog.mu.Lock()
		started := f.catalog.metaCalls > 0
		f.catalog.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("no metadata lookup started")
		}
	}
	cancel()
	<-done
	// The batch in flight finishes, one more may start, and no more.
	if f.catalog.metaCalls == 0 || f.catalog.metaCalls > metaFetchLimit+1 {
		t.Fatalf("%d lookups, want the first batch at most", f.catalog.metaCalls)
	}
	var p upcomingPage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("a cut-short scan answered %v total %d", p.ids(), p.TotalRecordCount)
	}
}

// Upcoming over the real store: favourites, played marks and progress as
// the database keeps them, per stream.
func TestUpcomingOverTheStore(t *testing.T) {
	mgr, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("GetManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	f, alpha, bravo, charlie, _ := upcomingFixture(t)
	store := mgr.JellyfinPlaystateStore()
	f.server.opts.Playstate = store
	kitchen := []string{"Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-2", Token="` + testOtherToken + `"`}
	call := func(method, path string, headers ...string) {
		t.Helper()
		if rec := f.do(method, "/jellyfin/"+path, "", headers...); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d", method, path, rec.Code)
		}
	}

	call(http.MethodPost, "UserFavoriteItems/"+charlie.encode())
	call(http.MethodPost, "Users/u/PlayedItems/"+alpha.episode(1, 1).encode())
	call(http.MethodPost, "Users/u/PlayedItems/"+bravo.episode(1, 1).encode())
	// Part way through Bravo's aired second episode: it is left to watch.
	if err := store.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: bravo.episode(1, 2).encode(),
		ContentType: "series", ContentID: "tt0000102:1:2", PositionTicks: 600 * ticksPerSecond, LastPlayedAt: upcomingEpoch}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	wantUpcoming(t, upcoming(t, f, ""), alpha.episode(1, 2), charlie.episode(1, 1))
	if p := upcoming(t, f, "", kitchen...); len(p.Items) != 0 {
		t.Fatalf("kitchen sees %v", p.ids())
	}

	// A watched favourite unfavourited stays, by its history; a favourite
	// alone unfavourited goes.
	call(http.MethodPost, "UserFavoriteItems/"+alpha.encode())
	call(http.MethodDelete, "UserFavoriteItems/"+alpha.encode())
	call(http.MethodDelete, "UserFavoriteItems/"+charlie.encode())
	wantUpcoming(t, upcoming(t, f, ""), alpha.episode(1, 2))
	// Alpha unmarked is no longer watched; Bravo still has its aired episode.
	call(http.MethodDelete, "Users/u/PlayedItems/"+alpha.episode(1, 1).encode())
	if p := upcoming(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("nothing followed is caught up: %v", p.ids())
	}
}

// caughtUp adds a series whose first episode the stream watched minute
// minutes after nextUpEpoch, waiting on a second that airs days ahead.
func caughtUp(f *fixture, contentID string, minute, days int) itemID {
	id := addSeries(f, contentID, contentID,
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-30)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(days)})
	record(f, "living-room", id.episode(1, 1), true, 0, minute)
	return id
}

// seriesSet is the series of each item on a page, as a set.
func (p upcomingPage) seriesSet() map[string]bool {
	out := map[string]bool{}
	for _, item := range p.Items {
		out[item.SeriesID] = true
	}
	return out
}

// A favourite is looked at however long the history before it.
func TestUpcomingFavouritesOutsideTheWatchedCap(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	// A full cap of series watched, each with an aired episode left: Next
	// Up's, not Upcoming's.
	manySeries(f, nextUpMaxSeries)
	premiere := addSeries(f, "tt0009100", "Premiere", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(3)})
	favorite(f, "living-room", premiere)

	f.catalog.metaCalls = 0
	p := upcoming(t, f, "")
	wantUpcoming(t, p, premiere.episode(1, 1))
	if p.TotalRecordCount != 1 || f.catalog.metaCalls != nextUpMaxSeries+1 {
		t.Fatalf("total %d, %d lookups", p.TotalRecordCount, f.catalog.metaCalls)
	}
	// Next Up is as it was: a page looks at one batch, the favourite is not
	// in it, and the scan stops at its own cap.
	f.catalog.metaCalls = 0
	if p := nextUp(t, f, "Limit=2"); len(p.Items) != 2 || f.catalog.metaCalls != metaFetchLimit {
		t.Fatalf("Next Up page: %v, %d lookups", p.ids(), f.catalog.metaCalls)
	}
	all := nextUp(t, f, "Limit=200")
	if all.TotalRecordCount != nextUpMaxSeries || slices.Contains(all.seriesOf(), premiere.encode()) {
		t.Fatalf("Next Up: total %d, favourite listed %v", all.TotalRecordCount, slices.Contains(all.seriesOf(), premiere.encode()))
	}

	// A favourite watched long ago, behind the cap of newer history, is
	// still looked at as a favourite — and only as long as it is one.
	old := caughtUp(f, "tt0009101", -1000, 5)
	favorite(f, "living-room", old)
	wantUpcoming(t, upcoming(t, f, ""), premiere.episode(1, 1), old.episode(1, 2))
	f.play.SetFavorite("living-room", old.encode(), "series", old.baseStremioID(), false)
	wantUpcoming(t, upcoming(t, f, ""), premiere.episode(1, 1))
}

// Each cap holds at its boundary, and a series in both is looked at once.
func TestUpcomingCaps(t *testing.T) {
	t.Run("watched", func(t *testing.T) {
		atEpoch(t)
		f := newFixture()
		var series []itemID
		for i := range nextUpMaxSeries + 1 {
			series = append(series, caughtUp(f, fmt.Sprintf("tt09%05d", i), i, 1+i%80))
		}
		p := upcoming(t, f, "Limit=200")
		got := p.seriesSet()
		// The one watched longest ago is past the cap; the next is inside.
		if p.TotalRecordCount != nextUpMaxSeries || got[series[0].encode()] || !got[series[1].encode()] {
			t.Fatalf("total %d, oldest listed %v, next listed %v", p.TotalRecordCount, got[series[0].encode()], got[series[1].encode()])
		}
	})
	t.Run("favourites", func(t *testing.T) {
		atEpoch(t)
		f := newFixture()
		var encoded []string
		for i := range nextUpMaxSeries + 1 {
			id := addSeries(f, fmt.Sprintf("tt08%05d", i), "Fav", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(1 + i%80)})
			favorite(f, "living-room", id)
			encoded = append(encoded, id.encode())
		}
		// Never watched, favourites are taken in id order.
		slices.Sort(encoded)
		p := upcoming(t, f, "Limit=200")
		got := p.seriesSet()
		if p.TotalRecordCount != nextUpMaxSeries || got[encoded[nextUpMaxSeries]] || !got[encoded[nextUpMaxSeries-1]] {
			t.Fatalf("total %d, last listed %v, one before listed %v", p.TotalRecordCount, got[encoded[nextUpMaxSeries]], got[encoded[nextUpMaxSeries-1]])
		}
	})
	// Favourites already watched are looked at as watched, and leave the
	// favourites' own bound to the rest.
	watchedFavourites := func(f *fixture) {
		for i := range nextUpMaxSeries {
			favorite(f, "living-room", caughtUp(f, fmt.Sprintf("tt07%05d", i), i, 1+i%80))
		}
	}
	t.Run("watched favourites", func(t *testing.T) {
		atEpoch(t)
		f := newFixture()
		watchedFavourites(f)
		premiere := addSeries(f, "tt0009500", "Premiere", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(3)})
		favorite(f, "living-room", premiere)
		f.catalog.metaCalls = 0
		p := upcoming(t, f, "Limit=200")
		got := p.seriesSet()
		if p.TotalRecordCount != nextUpMaxSeries+1 || len(p.Items) != nextUpMaxSeries+1 || len(got) != len(p.Items) ||
			!got[premiere.encode()] || f.catalog.metaCalls != nextUpMaxSeries+1 {
			t.Fatalf("total %d, %d items over %d series, premiere listed %v, %d lookups",
				p.TotalRecordCount, len(p.Items), len(got), got[premiere.encode()], f.catalog.metaCalls)
		}
		// Every series is caught up, so Next Up has none of them.
		if p := nextUp(t, f, "Limit=200"); len(p.Items) != 0 || p.TotalRecordCount != 0 {
			t.Fatalf("Next Up: %v total %d", p.ids(), p.TotalRecordCount)
		}
	})
	t.Run("favourites beyond the watched", func(t *testing.T) {
		atEpoch(t)
		f := newFixture()
		watchedFavourites(f)
		var encoded []string
		for i := range nextUpMaxSeries + 1 {
			id := addSeries(f, fmt.Sprintf("tt06%05d", i), "Fav", stremio.MetaVideo{Season: 1, Episode: 1, Released: day(1 + i%80)})
			favorite(f, "living-room", id)
			encoded = append(encoded, id.encode())
		}
		slices.Sort(encoded)
		f.catalog.metaCalls = 0
		p := upcoming(t, f, "Limit=200")
		got := p.seriesSet()
		if p.TotalRecordCount != 2*nextUpMaxSeries || len(got) != len(p.Items) || f.catalog.metaCalls != 2*nextUpMaxSeries ||
			got[encoded[nextUpMaxSeries]] || !got[encoded[nextUpMaxSeries-1]] {
			t.Fatalf("total %d, %d items over %d series, %d lookups, last listed %v, one before listed %v",
				p.TotalRecordCount, len(p.Items), len(got), f.catalog.metaCalls, got[encoded[nextUpMaxSeries]], got[encoded[nextUpMaxSeries-1]])
		}
	})
	t.Run("overlap", func(t *testing.T) {
		atEpoch(t)
		f := newFixture()
		both := caughtUp(f, "tt0009200", 1, 4)
		favorite(f, "living-room", both)
		p := upcoming(t, f, "")
		wantUpcoming(t, p, both.episode(1, 2))
		if p.TotalRecordCount != 1 || f.catalog.metaCalls != 1 {
			t.Fatalf("total %d, %d lookups", p.TotalRecordCount, f.catalog.metaCalls)
		}
	})
}

// An episode the metadata lists twice is listed once, as its first listing,
// which is also the one the item itself is rendered from. The second listing
// here is outside the window, so taking it would drop the episode.
func TestUpcomingDuplicateEpisodes(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	dup := addSeries(f, "tt0009300", "Dup",
		stremio.MetaVideo{Season: 1, Episode: 3, Released: day(4)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(4)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(95)},
		stremio.MetaVideo{Season: 1, Episode: 4, Released: day(9)})
	for _, parent := range []itemID{dup, dup.season(1)} {
		for range 5 {
			p := upcoming(t, f, "ParentId="+parent.encode())
			wantUpcoming(t, p, dup.episode(1, 2), dup.episode(1, 3), dup.episode(1, 4))
			if p.TotalRecordCount != 3 || p.Items[0].PremiereDate != premiereDate(day(4)) {
				t.Fatalf("total %d, first aired %s", p.TotalRecordCount, p.Items[0].PremiereDate)
			}
		}
	}
}

// An episode listed undated, then again dated, is undated in both lists:
// the later listing neither dates the followed series' pick nor the item.
func TestUpcomingDuplicateUndatedFirst(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	dup := addSeries(f, "tt0009301", "Dup",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(2)},
		stremio.MetaVideo{Season: 1, Episode: 2},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(5)})
	record(f, "living-room", dup.episode(1, 1), true, 0, 1)
	if p := upcoming(t, f, ""); len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("general: %v total %d", p.ids(), p.TotalRecordCount)
	}
	for _, parent := range []itemID{dup, dup.season(1)} {
		wantUpcoming(t, upcoming(t, f, "ParentId="+parent.encode()), dup.episode(1, 1))
	}
}

// An episode listed to air, then again as aired, airs as first listed in Next
// Up, the general list and the series page alike.
func TestUpcomingDuplicateDatedLater(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	dup := addSeries(f, "tt0009302", "Dup",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-10)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(5)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: day(-1)})
	record(f, "living-room", dup.episode(1, 1), true, 0, 1)
	if p := nextUp(t, f, ""); len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("Next Up: %v total %d", p.ids(), p.TotalRecordCount)
	}
	for _, query := range []string{"", "ParentId=" + dup.encode(), "ParentId=" + dup.season(1).encode()} {
		p := upcoming(t, f, query)
		wantUpcoming(t, p, dup.episode(1, 2))
		if p.Items[0].PremiereDate != premiereDate(day(5)) {
			t.Fatalf("%q: aired %s, want %s", query, p.Items[0].PremiereDate, premiereDate(day(5)))
		}
	}
}

// An episode airing while the request runs is never passed over for a later
// one, wherever between the clock's reads it airs.
func TestUpcomingClockDuringRequest(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	s := addSeries(f, "tt0009303", "Clock",
		stremio.MetaVideo{Season: 1, Episode: 1, Released: day(-10)},
		stremio.MetaVideo{Season: 1, Episode: 2, Released: upcomingEpoch.Add(time.Hour).Format(time.RFC3339)},
		stremio.MetaVideo{Season: 1, Episode: 3, Released: day(7)})
	record(f, "living-room", s.episode(1, 1), true, 0, 1)
	for airsAfter := 1; airsAfter <= 12; airsAfter++ {
		reads := 0
		now = func() time.Time {
			if reads++; reads <= airsAfter {
				return upcomingEpoch
			}
			return upcomingEpoch.Add(2 * time.Hour)
		}
		p := upcoming(t, f, "")
		if len(p.Items) > 1 || (len(p.Items) == 1 && p.ids()[0] != s.episode(1, 2).encode()) {
			t.Fatalf("airing after read %d: %v, want S1E2 or nothing", airsAfter, p.ids())
		}
	}
}

func TestUpcomingAnimeLibrary(t *testing.T) {
	atEpoch(t)
	f := newFixture()
	f.catalog.metas["anime/kitsu:11"] = &stremio.MetaObject{ID: "kitsu:11", Type: "anime", Name: "An Anime",
		Videos: []stremio.MetaVideo{{ID: "kitsu:11:1", Season: 1, Episode: 1, Released: day(-7)}, {ID: "kitsu:11:2", Season: 1, Episode: 2, Released: day(6)}}}
	anime, _ := itemIDFor("anime", "kitsu:11")
	record(f, "living-room", anime.episode(1, 1), true, 0, 1)
	show := caughtUp(f, "tt0009400", 2, 3)
	wantUpcoming(t, upcoming(t, f, "ParentId="+viewID("kitsu.trending.anime")), anime.episode(1, 2))
	wantUpcoming(t, upcoming(t, f, "ParentId="+viewID("tmdb.trending.series")), show.episode(1, 2))
	wantUpcoming(t, upcoming(t, f, ""), show.episode(1, 2), anime.episode(1, 2))
}
