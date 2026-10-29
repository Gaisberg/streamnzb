package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

func TestNextUpVideo(t *testing.T) {
	// Out of order on purpose, with a special and an undated episode.
	shows := []stremio.MetaVideo{
		{Season: 1, Episode: 2, Released: "2020-01-08"},
		{Season: 1, Episode: 1, Released: "2020-01-01T00:00:00.000Z"},
		{Season: 0, Episode: 1, Released: "2019-12-01"},
		{Season: 1, Episode: 3},
		{Season: 2, Episode: 1, Released: "2021-01-01"},
		{Season: 2, Episode: 2, Released: "2099-01-01"},
	}
	// A new season announced: its premiere dated, the rest to be announced.
	announced := []stremio.MetaVideo{
		{Season: 1, Episode: 10, Released: "2020-01-01"},
		{Season: 2, Episode: 1, Released: "2099-01-01"},
		{Season: 2, Episode: 2},
		{Season: 2, Episode: 3},
	}
	played := persistence.JellyfinPlaystate{Played: true}
	partial := persistence.JellyfinPlaystate{PositionTicks: 600 * ticksPerSecond}
	type states = map[episodeKey]persistence.JellyfinPlaystate
	cases := []struct {
		name      string
		videos    []stremio.MetaVideo
		states    states
		resumable bool
		want      episodeKey
		none      bool
	}{
		{name: "nothing played starts the series", videos: shows, want: episodeKey{1, 1}},
		{name: "next after the one played", videos: shows, states: states{{1, 1}: played}, want: episodeKey{1, 2}},
		{name: "undated after an aired episode counts as aired", videos: shows, states: states{{1, 1}: played, {1, 2}: played}, want: episodeKey{1, 3}},
		{name: "finale continues into the next season", videos: shows, states: states{{1, 3}: played}, want: episodeKey{2, 1}},
		{name: "furthest played anchors, not the earliest unplayed", videos: shows, states: states{{1, 1}: played, {1, 3}: played}, want: episodeKey{2, 1}},
		{name: "next unaired means none", videos: shows, states: states{{2, 1}: played}, none: true},
		{name: "every aired episode played", videos: shows, states: states{{1, 1}: played, {1, 2}: played, {1, 3}: played, {2, 1}: played}, none: true},
		{name: "a special neither anchors nor is picked", videos: shows, states: states{{0, 1}: played}, want: episodeKey{1, 1}},
		{name: "part-played pick offered when resumable", videos: shows, states: states{{1, 1}: played, {1, 2}: partial}, resumable: true, want: episodeKey{1, 2}},
		{name: "part-played pick drops the series otherwise", videos: shows, states: states{{1, 1}: played, {1, 2}: partial}, none: true},
		{name: "part-played first episode with nothing played", videos: shows, states: states{{1, 1}: partial}, resumable: true, want: episodeKey{1, 1}},
		{name: "part-played behind the anchor is not a resume", videos: shows, states: states{{1, 1}: partial, {1, 2}: played}, resumable: true, want: episodeKey{1, 3}},
		{name: "numbering gap is not an end", videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 4}}, states: states{{1, 1}: played}, want: episodeKey{1, 4}},
		{name: "unaired episode passed over", videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2, Released: "2099-01-01"}, {Season: 1, Episode: 3, Released: "2020-01-01"}}, states: states{{1, 1}: played}, want: episodeKey{1, 3}},
		{name: "undated after an unaired premiere is unaired", videos: announced, states: states{{1, 10}: played}, none: true},
		{name: "a later aired date ends the inherited unaired", videos: append(slices.Clone(announced), stremio.MetaVideo{Season: 2, Episode: 4, Released: "2020-06-01"}), states: states{{1, 10}: played}, want: episodeKey{2, 4}},
		{name: "undated new season after a dated one is unaired", videos: []stremio.MetaVideo{{Season: 1, Episode: 12, Released: "2020-01-01"}, {Season: 2, Episode: 1}, {Season: 2, Episode: 2}}, states: states{{1, 12}: played}, none: true},
		{name: "undated season before an aired one counts as aired", videos: []stremio.MetaVideo{{Season: 1, Episode: 12, Released: "2020-01-01"}, {Season: 2, Episode: 1}, {Season: 2, Episode: 2}, {Season: 3, Episode: 1, Released: "2022-01-01"}}, states: states{{1, 12}: played}, want: episodeKey{2, 1}},
		{name: "undated season premiere before an aired episode counts as aired", videos: []stremio.MetaVideo{{Season: 1, Episode: 12, Released: "2020-01-01"}, {Season: 2, Episode: 1}, {Season: 2, Episode: 2, Released: "2021-01-01"}}, states: states{{1, 12}: played}, want: episodeKey{2, 1}},
		{name: "undated season before an unaired one is unaired", videos: []stremio.MetaVideo{{Season: 1, Episode: 12, Released: "2020-01-01"}, {Season: 2, Episode: 1}, {Season: 3, Episode: 1, Released: "2099-01-01"}}, states: states{{1, 12}: played}, none: true},
		{name: "undated first season of a dated series counts as aired", videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}, {Season: 2, Episode: 1, Released: "2099-01-01"}}, states: states{{1, 1}: played}, want: episodeKey{1, 2}},
		{name: "undated seasons of an undated series count as aired", videos: []stremio.MetaVideo{{Season: 1, Episode: 12}, {Season: 2, Episode: 1}}, states: states{{1, 12}: played}, want: episodeKey{2, 1}},
		{name: "undated with nothing dated before counts as aired", videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}}, states: states{{1, 1}: played}, want: episodeKey{1, 2}},
		{name: "anchor the metadata no longer lists still counts", videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}, {Season: 2, Episode: 1}}, states: states{{1, 1}: played, {1, 9}: played}, want: episodeKey{2, 1}},
		{name: "nothing after an anchor the metadata no longer lists", videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}}, states: states{{1, 9}: played}, none: true},
		{name: "no episodes", none: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, ok := nextUpVideo(c.videos, c.states, c.resumable)
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
	if shows[0].Episode != 2 || announced[2].Released != "" {
		t.Fatalf("the caller's episode list was changed")
	}
}

func TestNextUpCutoffParsing(t *testing.T) {
	want := time.Date(2026, 2, 15, 6, 30, 0, 0, time.UTC)
	for _, raw := range []string{"2026-02-15T06:30:00Z", "2026-02-15T07:30:00+01:00", "2026-02-15T06:30:00", "2026-02-15T06:30:00.0000000"} {
		if got := nextUpCutoff(raw); !got.Equal(want) {
			t.Errorf("%q: %v, want %v", raw, got, want)
		}
	}
	if got := nextUpCutoff("2026-02-15"); !got.Equal(time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date only: %v", got)
	}
	for _, raw := range []string{"", "whenever", "2026-02-15 06:30"} {
		if got := nextUpCutoff(raw); !got.IsZero() {
			t.Errorf("%q: %v, want no cutoff", raw, got)
		}
	}
	// The zone-less form is the cutoff's alone: metadata dates read as before.
	if premiereDate("2026-02-15T06:30:00") != "" || unaired("2099-02-15T06:30:00") {
		t.Fatalf("the shared date parser was widened")
	}
}

// nextUpFixture adds a second series beside Breaking Bad: three episodes in
// season 1, the last undated, a second season whose second episode is
// unaired, and a special.
func nextUpFixture() (*fixture, itemID, itemID) {
	f := newFixture()
	f.catalog.metas["series/tt0000042"] = &stremio.MetaObject{
		ID: "tt0000042", Type: "series", Name: "The Show", Runtime: "45 min",
		Videos: []stremio.MetaVideo{
			{ID: "tt0000042:0:1", Season: 0, Episode: 1, Released: "2019-12-01"},
			{ID: "tt0000042:1:1", Season: 1, Episode: 1, Released: "2020-01-01"},
			{ID: "tt0000042:1:2", Season: 1, Episode: 2, Released: "2020-01-08"},
			{ID: "tt0000042:1:3", Season: 1, Episode: 3},
			{ID: "tt0000042:2:1", Season: 2, Episode: 1, Released: "2021-01-01"},
			{ID: "tt0000042:2:2", Season: 2, Episode: 2, Released: "2099-01-01"},
		},
	}
	show, _ := itemIDFor("series", "tt0000042")
	bb, _ := itemIDFor("series", "tt0903747")
	return f, show, bb
}

var nextUpEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// record records a stream's state on an episode, minutes after nextUpEpoch.
func record(f *fixture, stream string, episode itemID, played bool, position int64, minute int) {
	f.play.Upsert(persistence.JellyfinPlaystate{
		StreamName: stream, ItemID: episode.encode(), ContentType: episode.ContentType, ContentID: episode.playStremioID(),
		PositionTicks: position, RuntimeTicks: 45 * 60 * ticksPerSecond, Played: played, PlayCount: 1,
		LastPlayedAt: nextUpEpoch.Add(time.Duration(minute) * time.Minute),
	})
}

type nextUpPage struct {
	Items []struct {
		ID                string `json:"Id"`
		Type              string
		SeriesID          string `json:"SeriesId"`
		ParentIndexNumber *int
		IndexNumber       *int
		UserData          *struct {
			Played                bool
			PlaybackPositionTicks int64
		}
	}
	TotalRecordCount int
	StartIndex       int
}

func nextUp(t *testing.T, f *fixture, query string, headers ...string) nextUpPage {
	t.Helper()
	var p nextUpPage
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Shows/NextUp?"+query, "", headers...), &p)
	return p
}

// ids lists a page's item ids, for comparing with encoded episode ids.
func (p nextUpPage) ids() []string {
	out := []string{}
	for _, item := range p.Items {
		out = append(out, item.ID)
	}
	return out
}

func wantIDs(t *testing.T, got nextUpPage, want ...itemID) {
	t.Helper()
	encoded := []string{}
	for _, id := range want {
		encoded = append(encoded, id.encode())
	}
	if fmt.Sprint(got.ids()) != fmt.Sprint(encoded) {
		t.Fatalf("items %v, want %v", got.ids(), encoded)
	}
}

func TestNextUpListsWatchedSeriesNewestFirst(t *testing.T) {
	f, show, bb := nextUpFixture()
	// A movie and a favourite-only episode are not series being watched.
	movie, _ := itemIDFor("movie", "tt0111161")
	record(f, "living-room", movie, false, 60*ticksPerSecond, 3)
	f.play.SetFavorite("living-room", show.episode(1, 1).encode(), "series", "tt0000042:1:1", true)
	record(f, "living-room", bb.episode(1, 1), true, 0, 1)
	wantIDs(t, nextUp(t, f, ""), bb.episode(1, 2))

	record(f, "living-room", show.episode(1, 1), true, 0, 2)
	p := nextUp(t, f, "UserId=u")
	wantIDs(t, p, show.episode(1, 2), bb.episode(1, 2))
	if p.TotalRecordCount != 2 || p.StartIndex != 0 {
		t.Fatalf("total %d start %d", p.TotalRecordCount, p.StartIndex)
	}
	first := p.Items[0]
	if first.Type != "Episode" || first.SeriesID != show.encode() || *first.ParentIndexNumber != 1 || *first.IndexNumber != 2 ||
		first.UserData == nil || first.UserData.Played {
		t.Fatalf("item %+v", first)
	}

	p = nextUp(t, f, "StartIndex=1&Limit=1")
	wantIDs(t, p, bb.episode(1, 2))
	if p.TotalRecordCount != 2 || p.StartIndex != 1 {
		t.Fatalf("page 2: total %d start %d", p.TotalRecordCount, p.StartIndex)
	}
	if p = nextUp(t, f, "StartIndex=5"); len(p.Items) != 0 || p.TotalRecordCount != 2 {
		t.Fatalf("past the end: %v total %d", p.ids(), p.TotalRecordCount)
	}
	// Jellyfin reports no total when asked not to count.
	if p = nextUp(t, f, "EnableTotalRecordCount=false"); len(p.Items) != 2 || p.TotalRecordCount != 0 {
		t.Fatalf("uncounted: %v total %d", p.ids(), p.TotalRecordCount)
	}

	// Watching the last aired episode ends the series' Next Up.
	record(f, "living-room", show.episode(2, 1), true, 0, 4)
	wantIDs(t, nextUp(t, f, ""), bb.episode(1, 2))
}

func TestNextUpResumable(t *testing.T) {
	f, show, _ := nextUpFixture()
	record(f, "living-room", show.episode(1, 1), true, 0, 1)
	record(f, "living-room", show.episode(1, 2), false, 600*ticksPerSecond, 2)

	p := nextUp(t, f, "")
	wantIDs(t, p, show.episode(1, 2))
	if p.Items[0].UserData.PlaybackPositionTicks != 600*ticksPerSecond {
		t.Fatalf("resume position %d", p.Items[0].UserData.PlaybackPositionTicks)
	}
	if p = nextUp(t, f, "enableResumable=true"); len(p.Items) != 1 {
		t.Fatalf("explicit true: %v", p.ids())
	}
	if p = nextUp(t, f, "EnableResumable=false"); len(p.Items) != 0 || p.TotalRecordCount != 0 {
		t.Fatalf("not resumable: %v total %d", p.ids(), p.TotalRecordCount)
	}
}

func TestNextUpSeriesAndParent(t *testing.T) {
	f, show, bb := nextUpFixture()
	record(f, "living-room", show.episode(1, 1), true, 0, 1)

	// A series page asks for its own, watched or not.
	wantIDs(t, nextUp(t, f, "SeriesId="+bb.encode()), bb.episode(1, 1))
	wantIDs(t, nextUp(t, f, "SeriesId="+show.encode()), show.episode(1, 2))
	// Jellyfin ignores a SeriesId that is not a series and lists the rest.
	movie, _ := itemIDFor("movie", "tt0111161")
	for _, other := range []string{movie.encode(), show.season(1).encode(), show.episode(1, 1).encode(), "not-an-id"} {
		wantIDs(t, nextUp(t, f, "SeriesId="+other), show.episode(1, 2))
	}

	// A library narrows to its content type; Jellyfin narrows by library
	// only, so any other ParentId lists nothing.
	record(f, "living-room", bb.episode(1, 1), true, 0, 2)
	wantIDs(t, nextUp(t, f, "ParentId="+viewID("tmdb.trending.series")), bb.episode(1, 2), show.episode(1, 2))
	for _, parent := range []string{
		viewID("tmdb.trending.movie"), viewID("no.such.catalog"), "junk",
		show.encode(), show.season(1).encode(), show.episode(1, 1).encode(),
	} {
		if p := nextUp(t, f, "ParentId="+parent); len(p.Items) != 0 {
			t.Fatalf("ParentId %s: %v", parent, p.ids())
		}
	}
}

func TestNextUpDateCutoff(t *testing.T) {
	f, show, bb := nextUpFixture()
	record(f, "living-room", show.episode(1, 1), true, 0, -60*24*30)
	record(f, "living-room", bb.episode(1, 1), true, 0, 0)

	for _, cutoff := range []string{"2026-02-15", "2026-02-15T00:00:00.000Z", "2026-02-15T00:00:00", "2026-03-01T12:00:00Z", "2026-03-01T12:00:00"} {
		wantIDs(t, nextUp(t, f, "NextUpDateCutoff="+cutoff), bb.episode(1, 2))
	}
	// Inclusive: a second later than the last play cuts it.
	if p := nextUp(t, f, "NextUpDateCutoff=2026-03-01T12:00:01Z"); len(p.Items) != 0 {
		t.Fatalf("past the last play: %v", p.ids())
	}
	if p := nextUp(t, f, "NextUpDateCutoff=whenever"); len(p.Items) != 2 {
		t.Fatalf("unreadable cutoff: %v", p.ids())
	}
}

func TestNextUpKeepsStreamsApart(t *testing.T) {
	f, show, _ := nextUpFixture()
	kitchen := []string{"Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-2", Token="` + testOtherToken + `"`}
	record(f, "living-room", show.episode(1, 1), true, 0, 1)
	if p := nextUp(t, f, "", kitchen...); len(p.Items) != 0 {
		t.Fatalf("kitchen sees %v", p.ids())
	}
	record(f, "kitchen", show.episode(1, 2), true, 0, 2)
	wantIDs(t, nextUp(t, f, "", kitchen...), show.episode(1, 3))
	wantIDs(t, nextUp(t, f, ""), show.episode(1, 2))
}

// A stop past the played threshold moves Next Up on the very next request.
func TestNextUpFollowsPlaybackReports(t *testing.T) {
	f, show, _ := nextUpFixture()
	episode := show.episode(1, 1)
	report := func(event string, ticks int64) {
		body := fmt.Sprintf(`{"ItemId":"%s","MediaSourceId":"%s","PositionTicks":%d,"PlaySessionId":"ps"}`, episode.encode(), episode.source(0).encode(), ticks)
		if rec := f.do(http.MethodPost, "/jellyfin/Sessions/Playing"+event, body); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", event, rec.Code)
		}
	}
	report("", 0)
	if p := nextUp(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("a start at zero is not watching yet: %v", p.ids())
	}
	report("/Stopped", 10*60*ticksPerSecond)
	wantIDs(t, nextUp(t, f, ""), episode)
	report("/Stopped", 44*60*ticksPerSecond)
	wantIDs(t, nextUp(t, f, ""), show.episode(1, 2))
}

func TestNextUpSkipsFailedMetadata(t *testing.T) {
	f, show, _ := nextUpFixture()
	lost, _ := itemIDFor("series", "tt0000099")
	record(f, "living-room", show.episode(1, 1), true, 0, 1)
	record(f, "living-room", lost.episode(1, 1), true, 0, 2)
	p := nextUp(t, f, "")
	wantIDs(t, p, show.episode(1, 2))
	if p.TotalRecordCount != 1 {
		t.Fatalf("total %d", p.TotalRecordCount)
	}
}

// A Kitsu series goes on within its entry; a Kitsu film, which has no
// episode list of its own, is never a Next Up.
func TestNextUpKitsu(t *testing.T) {
	f := newFixture()
	f.catalog.metas["anime/kitsu:11"] = &stremio.MetaObject{ID: "kitsu:11", Type: "anime", Name: "An Anime",
		Videos: []stremio.MetaVideo{{ID: "kitsu:11:1", Season: 1, Episode: 1}, {ID: "kitsu:11:2", Season: 1, Episode: 2}}}
	anime, _ := itemIDFor("anime", "kitsu:11")
	film, _ := itemIDFor("anime", "kitsu:9")
	record(f, "living-room", anime.episode(1, 1), true, 0, 1)
	record(f, "living-room", film.episode(1, 0), false, 600*ticksPerSecond, 2)
	wantIDs(t, nextUp(t, f, ""), anime.episode(1, 2))
	if p := nextUp(t, f, "SeriesId="+film.encode()); len(p.Items) != 0 {
		t.Fatalf("film by SeriesId: %v", p.ids())
	}
}

// manySeries gives the stream n watched two-episode series, the highest
// numbered watched last, newest first in the returned order.
func manySeries(f *fixture, n int) []itemID {
	var series []itemID
	for i := n; i >= 1; i-- {
		contentID := fmt.Sprintf("tt8%06d", i)
		f.catalog.metas["series/"+contentID] = &stremio.MetaObject{ID: contentID, Type: "series", Name: contentID,
			Videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}}}
		id, _ := itemIDFor("series", contentID)
		record(f, "living-room", id.episode(1, 1), true, 0, i)
		series = append(series, id)
	}
	return series
}

// seriesOf is the series of each item on a page.
func (p nextUpPage) seriesOf() []string {
	out := []string{}
	for _, item := range p.Items {
		out = append(out, item.SeriesID)
	}
	return out
}

func encodeIDs(ids ...itemID) []string {
	out := []string{}
	for _, id := range ids {
		out = append(out, id.encode())
	}
	return out
}

func TestNextUpPagesProgressively(t *testing.T) {
	f := newFixture()
	series := manySeries(f, 20)
	page := func(query string, total int, want ...itemID) {
		t.Helper()
		f.catalog.metaCalls = 0
		p := nextUp(t, f, query)
		if !slices.Equal(p.seriesOf(), encodeIDs(want...)) || p.TotalRecordCount != total {
			t.Fatalf("%s: series %v total %d, want %v total %d", query, p.seriesOf(), p.TotalRecordCount, encodeIDs(want...), total)
		}
	}
	// A page looks at one batch, not the twenty: the total is what was
	// found, at least one past the page, until the series run out.
	page("Limit=2", metaFetchLimit, series[0], series[1])
	if f.catalog.metaCalls != metaFetchLimit {
		t.Fatalf("first page: %d lookups", f.catalog.metaCalls)
	}
	page("StartIndex=8&Limit=2", 11, series[8], series[9])
	page("StartIndex=18&Limit=2", 20, series[18], series[19])
	page("StartIndex=20&Limit=2", 20)
	// Uncounted, the page and nothing more.
	page("StartIndex=2&Limit=2&EnableTotalRecordCount=false", 0, series[2], series[3])
	if f.catalog.metaCalls != metaFetchLimit {
		t.Fatalf("uncounted page: %d lookups", f.catalog.metaCalls)
	}

	// A series whose metadata fails is skipped and the next fills its place.
	delete(f.catalog.metas, "series/"+series[1].baseStremioID())
	page("Limit=2", metaFetchLimit-1, series[0], series[2])
	page("StartIndex=17&Limit=5", 19, series[18], series[19])
}

func TestNextUpBoundsTheScan(t *testing.T) {
	f := newFixture()
	f.catalog.metaDelay = 2 * time.Millisecond
	series := manySeries(f, nextUpMaxSeries+5)

	// The last page reaches the bound: every series inside it is looked
	// at, at most metaFetchLimit at once, and the five watched longest ago
	// never.
	p := nextUp(t, f, fmt.Sprintf("StartIndex=%d&Limit=10", nextUpMaxSeries-5))
	if p.TotalRecordCount != nextUpMaxSeries || len(p.Items) != 5 || p.Items[4].SeriesID != series[nextUpMaxSeries-1].encode() {
		t.Fatalf("last page: total %d, %d items", p.TotalRecordCount, len(p.Items))
	}
	if f.catalog.metaCalls != nextUpMaxSeries {
		t.Fatalf("%d lookups, want %d", f.catalog.metaCalls, nextUpMaxSeries)
	}
	if f.catalog.peakMeta < 2 || f.catalog.peakMeta > metaFetchLimit {
		t.Fatalf("%d lookups at once, want 2..%d", f.catalog.peakMeta, metaFetchLimit)
	}
}

func TestNextUpOrdersEqualTimesStably(t *testing.T) {
	f := newFixture()
	var want []string
	for i := 1; i <= 6; i++ {
		contentID := fmt.Sprintf("tt7%06d", i)
		f.catalog.metas["series/"+contentID] = &stremio.MetaObject{ID: contentID, Type: "series", Name: contentID,
			Videos: []stremio.MetaVideo{{Season: 1, Episode: 1}, {Season: 1, Episode: 2}}}
		id, _ := itemIDFor("series", contentID)
		record(f, "living-room", id.episode(1, 1), true, 0, 0)
		want = append(want, id.encode())
	}
	slices.Sort(want)
	for range 10 {
		if got := nextUp(t, f, "").seriesOf(); !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func TestNextUpStopsWhenTheClientGoes(t *testing.T) {
	f := newFixture()
	// Forty series with nothing left to watch, so nothing fills the page
	// and only the client going away ends the scan.
	for i := 1; i <= 40; i++ {
		contentID := fmt.Sprintf("tt6%06d", i)
		f.catalog.metas["series/"+contentID] = &stremio.MetaObject{ID: contentID, Type: "series", Name: contentID,
			Videos: []stremio.MetaVideo{{Season: 1, Episode: 1}}}
		id, _ := itemIDFor("series", contentID)
		record(f, "living-room", id.episode(1, 1), true, 0, i)
	}
	f.catalog.metaDelay = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/jellyfin/Shows/NextUp?Limit=1&EnableTotalRecordCount=false", nil).WithContext(ctx)
	req.Header.Set("Authorization", `MediaBrowser Client="Swiftfin", DeviceId="dev-1", Token="`+testToken+`"`)
	done := make(chan struct{})
	go func() {
		f.server.Handler().ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	// Gone while the first batch is being looked up.
	for {
		f.catalog.mu.Lock()
		started := f.catalog.metaCalls > 0
		f.catalog.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if f.catalog.metaCalls == 0 || f.catalog.metaCalls > metaFetchLimit {
		t.Fatalf("%d lookups, want the first batch at most (%d)", f.catalog.metaCalls, metaFetchLimit)
	}
}

// Next Up over the real store: favourite-only rows, played marks and their
// undoing as the database keeps them.
func TestNextUpOverTheStore(t *testing.T) {
	mgr, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("GetManager: %v", err)
	}
	// The manager is a process-wide singleton; closing it lets the next
	// test open its own.
	t.Cleanup(func() { _ = mgr.Close() })
	f, show, bb := nextUpFixture()
	f.server.opts.Playstate = mgr.JellyfinPlaystateStore()
	mark := func(method string, episode itemID) {
		t.Helper()
		if rec := f.do(method, "/jellyfin/Users/u/PlayedItems/"+episode.encode(), ""); rec.Code != http.StatusOK {
			t.Fatalf("%s %v: %d", method, episode, rec.Code)
		}
	}

	if rec := f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+bb.episode(1, 1).encode(), ""); rec.Code != http.StatusOK {
		t.Fatalf("favourite: %d", rec.Code)
	}
	if p := nextUp(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("a favourite alone: %v", p.ids())
	}
	mark(http.MethodPost, show.episode(1, 1))
	mark(http.MethodPost, show.episode(1, 2))
	wantIDs(t, nextUp(t, f, ""), show.episode(1, 3))
	mark(http.MethodDelete, show.episode(1, 2))
	wantIDs(t, nextUp(t, f, ""), show.episode(1, 2))
	mark(http.MethodDelete, show.episode(1, 1))
	if p := nextUp(t, f, ""); len(p.Items) != 0 {
		t.Fatalf("every mark undone: %v", p.ids())
	}
}

// Next Up read while progress is written. It asserts little on its own; it
// is there for the race detector.
func TestNextUpConcurrentWithProgress(t *testing.T) {
	f, show, bb := nextUpFixture()
	record(f, "living-room", show.episode(1, 1), true, 0, 1)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if i%2 == 0 {
				record(f, "living-room", bb.episode(1, 1), i%4 == 0, int64(i)*ticksPerSecond, 2+i)
				return
			}
			if rec := f.do(http.MethodGet, "/jellyfin/Shows/NextUp", ""); rec.Code != http.StatusOK {
				t.Errorf("status %d", rec.Code)
			}
		})
	}
	wg.Wait()
	if p := nextUp(t, f, ""); len(p.Items) == 0 {
		t.Fatalf("no Next Up after concurrent writes")
	}
}
