package jellyfin

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/stremio"
)

// selectorFixture favourites movies, series, a season and episodes of
// deliberately different genres, named by a German genre list where the
// metadata itself says "Comedy", and puts some of each partway through,
// alongside a resumable movie that is no favourite.
func selectorFixture(t *testing.T) (f *fixture, m1, m2, s1, s2 itemID) {
	t.Helper()
	f = genreFixture()
	f.catalog.genreLists = map[string][]stremio.Genre{
		"movie":  {{ID: 28, Name: "Action"}, {ID: 35, Name: "Komödie"}, {ID: 18, Name: "Drama"}},
		"series": {{ID: 10759, Name: "Action & Abenteuer"}, {ID: 35, Name: "Komödie"}, {ID: 18, Name: "Drama"}},
	}
	rows := f.catalog.rows["tmdb.trending.series"]
	rows[0].GenreIDs, rows[1].GenreIDs, rows[2].GenreIDs = []int{35}, []int{18}, []int{10759}
	episodes := func(id string) []stremio.MetaVideo {
		return []stremio.MetaVideo{{ID: id + ":1:1", Season: 1, Episode: 1}, {ID: id + ":1:2", Season: 1, Episode: 2}}
	}
	for key, meta := range map[string]*stremio.MetaObject{
		"movie/tt0000001":  {ID: "tt0000001", Type: "movie", Name: "Title 1", Genres: []string{"Comedy"}, GenreIDs: []int{35}},
		"movie/tt0000002":  {ID: "tt0000002", Type: "movie", Name: "Title 2", Genres: []string{"Drama"}, GenreIDs: []int{18}},
		"series/tt0000001": {ID: "tt0000001", Type: "series", Name: "Title 1", Genres: []string{"Comedy"}, GenreIDs: []int{35}, Videos: episodes("tt0000001")},
		"series/tt0000002": {ID: "tt0000002", Type: "series", Name: "Title 2", Genres: []string{"Drama"}, GenreIDs: []int{18}, Videos: episodes("tt0000002")},
	} {
		f.catalog.metas[key] = meta
	}
	f.catalog.rows["tmdb.search.movie"] = f.catalog.rows["tmdb.trending.movie"][:2]
	f.catalog.rows["tmdb.search.series"] = rows[:2]
	m1, _ = itemIDFor("movie", "tt0000001")
	m2, _ = itemIDFor("movie", "tt0000002")
	s1, _ = itemIDFor("series", "tt0000001")
	s2, _ = itemIDFor("series", "tt0000002")
	for _, id := range []itemID{m1, m2, s1, s2, s1.season(1), s1.episode(1, 1), s2.episode(1, 1)} {
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+id.encode(), ""), &ud)
	}
	shawshank, _ := itemIDFor("movie", "tt0111161")
	then := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, id := range []itemID{m1, m2, s1.episode(1, 1), s2.episode(1, 1), shawshank} {
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: id.encode(), ContentType: id.ContentType,
			ContentID: id.playStremioID(), PositionTicks: 60 * ticksPerSecond, LastPlayedAt: then.Add(time.Duration(i) * time.Hour)})
	}
	return f, m1, m2, s1, s2
}

func idSet(ids []string) []string { return slices.Sorted(slices.Values(ids)) }

func encodeAll(ids ...itemID) []string {
	out := []string{}
	for _, id := range ids {
		out = append(out, id.encode())
	}
	return idSet(out)
}

// TestFavoriteSelectorsMatchTheListing is the parameter-interaction matrix
// for the favourite filter. Ids, searchTerm and IsResumable narrow by the
// same ParentId, genre and IncludeItemTypes rules as the favourites listing,
// so for every filter each selector returns exactly the listing's
// favourites among the items it selects, in each spelling of the filter.
func TestFavoriteSelectorsMatchTheListing(t *testing.T) {
	f, m1, m2, s1, s2 := selectorFixture(t)
	favs := []itemID{m1, m2, s1, s2, s1.season(1), s1.episode(1, 1), s2.episode(1, 1)}
	var raws []string
	for _, id := range favs {
		raws = append(raws, id.encode())
	}
	searchable := encodeAll(m1, m2, s1, s2)
	resumable := encodeAll(m1, m2, s1.episode(1, 1), s2.episode(1, 1))
	movieComedy, seriesComedy := genreID("movie", "Komödie"), genreID("series", "Komödie")

	// The listing is the reference the selectors are held to, so the cases
	// the review named are pinned outright.
	for query, want := range map[string][]string{
		"":                                     encodeAll(favs...),
		"IncludeItemTypes=Movie":               encodeAll(m1, m2),
		"IncludeItemTypes=Season,Episode":      encodeAll(s1.season(1), s1.episode(1, 1), s2.episode(1, 1)),
		"ParentId=" + seriesComedy:             encodeAll(s1),
		"ParentId=" + movieComedy:              encodeAll(m1),
		"GenreIds=" + movieComedy:              encodeAll(m1, s1), // a genre id names its genre, as the genre listing reads it
		"Genres=" + url.QueryEscape("Komödie"): encodeAll(m1, s1),
		"Genres=Comedy":                        {}, // the metadata's English name, not a TMDB genre here
		"ParentId=" + s1.encode():              encodeAll(s1.episode(1, 1)),
		"ParentId=" + seriesComedy + "&IncludeItemTypes=Movie": {},
	} {
		total, ids := favoriteIDs(t, f, "Recursive=true&Filters=IsFavorite&"+query)
		if got := idSet(ids); total != len(want) || !slices.Equal(got, want) {
			t.Fatalf("listing %s: total %d, %v, want %v", query, total, got, want)
		}
	}

	searches := f.catalog.playlistCalls
	for _, filter := range []string{"", "IncludeItemTypes=Movie", "IncludeItemTypes=Series", "IncludeItemTypes=Season",
		"IncludeItemTypes=Episode", "IncludeItemTypes=Movie,Episode", "IncludeItemTypes=Series,Season",
		"ParentId=" + viewID("tmdb.trending.movie"), "ParentId=" + viewID("tmdb.trending.series"),
		"ParentId=" + movieComedy, "ParentId=" + seriesComedy, "ParentId=" + genreID("series", "Drama"),
		"ParentId=" + genreID("movie", "Western"), "ParentId=" + seriesComedy + "&IncludeItemTypes=Movie",
		"GenreIds=" + movieComedy, "GenreIds=" + genreID("series", "Drama"), "GenreIds=" + movieComedy + "," + seriesComedy,
		"GenreIds=not-an-id", "Genres=" + url.QueryEscape("Komödie"), "Genres=" + url.QueryEscape("Komödie|Drama"), "Genres=Comedy",
		"GenreIds=" + movieComedy + "&IncludeItemTypes=Series", "Genres=Drama&IncludeItemTypes=Movie,Series",
		"ParentId=" + viewID("tmdb.trending.movie") + "&GenreIds=" + genreID("movie", "Drama"),
		"ParentId=" + viewID("tmdb.trending.series") + "&Genres=" + url.QueryEscape("Komödie"),
		"ParentId=" + s1.encode(), "ParentId=" + s1.encode() + "&IncludeItemTypes=Season", "ParentId=" + s1.season(1).encode(),
	} {
		for _, favorite := range []string{"Filters=IsFavorite", "Filters=IsFavoriteOrLikes", "isFavorite=true"} {
			query := "Recursive=true&" + favorite + "&" + filter
			total, listed := favoriteIDs(t, f, query)
			listed = idSet(listed)
			if total != len(listed) {
				t.Fatalf("listing %s: total %d for %d rows", query, total, len(listed))
			}
			among := func(set []string) []string {
				out := []string{}
				for _, id := range listed {
					if slices.Contains(set, id) {
						out = append(out, id)
					}
				}
				return out
			}

			_, byIds := favoriteIDs(t, f, query+"&Ids="+strings.Join(raws, ","))
			if got := idSet(byIds); !slices.Equal(got, listed) {
				t.Fatalf("Ids %s: %v, listing %v", query, got, listed)
			}
			_, found := favoriteIDs(t, f, query+"&searchTerm=title")
			if got, want := idSet(found), among(searchable); !slices.Equal(got, want) {
				t.Fatalf("searchTerm %s: %v, want %v", query, got, want)
			}
			resume := "Recursive=true&Filters=IsResumable&" + favorite + "&" + filter
			if v, ok := strings.CutPrefix(favorite, "Filters="); ok {
				resume = "Recursive=true&Filters=IsResumable," + v + "&" + filter
			}
			_, inProgress := favoriteIDs(t, f, resume)
			if got, want := idSet(inProgress), among(resumable); !slices.Equal(got, want) {
				t.Fatalf("IsResumable %s: %v, want %v", resume, got, want)
			}

			// Paged after every filter, with the total unchanged.
			if len(listed) > 1 {
				_, full := favoriteIDs(t, f, query)
				if total, ids := favoriteIDs(t, f, query+"&StartIndex=1&Limit=1"); total != len(listed) || !slices.Equal(ids, full[1:2]) {
					t.Fatalf("paged %s: total %d, %v, want %v", query, total, ids, full[1:2])
				}
			}
		}
	}
	if f.catalog.playlistCalls != searches {
		t.Fatalf("favourite selectors searched %d times", f.catalog.playlistCalls-searches)
	}

	// The review's reproductions, in the order a client sends the ids, with
	// an id that is no item and one that is no favourite.
	for query, want := range map[string][]string{
		"Ids=" + m1.encode() + "," + s1.encode() + "&isFavorite=true&IncludeItemTypes=Movie":            {m1.encode()},
		"Ids=" + m1.encode() + "," + s1.encode() + ",zzz&isFavorite=true&IncludeItemTypes=Series":       {s1.encode()},
		"Ids=" + m1.encode() + "," + s1.season(2).encode() + "&isFavorite=true&IncludeItemTypes=Season": {},
		"Ids=" + m1.encode() + "," + m2.encode() + "&isFavorite=true&GenreIds=" + movieComedy:           {m1.encode()},
		"Filters=IsResumable,IsFavorite&ParentId=" + seriesComedy:                                       {},
		"Filters=IsResumable,IsFavorite&ParentId=" + movieComedy:                                        {m1.encode()},
	} {
		if total, ids := favoriteIDs(t, f, query); total != len(want) || !slices.Equal(ids, want) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, ids, want)
		}
	}
}

// TestResumableFavoritesFetchConcurrently covers the favourites-only resume
// page on a cold cache: its titles are fetched at once, a bounded number at
// a time, rather than one after another, and only as many as the page
// needs, newest first, with a title whose metadata has gone replaced by the
// next one in progress.
func TestResumableFavoritesFetchConcurrently(t *testing.T) {
	f := newFixture()
	f.catalog.metaDelay = 5 * time.Millisecond
	then := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var newestFirst []string
	for i := 1; i <= 30; i++ {
		contentID := fmt.Sprintf("tt9%06d", i)
		// The newest has lost its metadata.
		if i != 30 {
			f.catalog.metas["movie/"+contentID] = &stremio.MetaObject{ID: contentID, Type: "movie", Name: contentID}
		}
		id, _ := itemIDFor("movie", contentID)
		f.play.Upsert(persistence.JellyfinPlaystate{StreamName: "living-room", ItemID: id.encode(), ContentType: "movie",
			ContentID: contentID, PositionTicks: 60 * ticksPerSecond, LastPlayedAt: then.Add(time.Duration(i) * time.Minute)})
		f.play.SetFavorite("living-room", id.encode(), "movie", contentID, true)
		if i != 30 {
			newestFirst = append([]string{id.encode()}, newestFirst...)
		}
	}

	total, ids := favoriteIDs(t, f, "Filters=IsResumable,IsFavorite&Limit=10")
	if total != 10 || !slices.Equal(ids, newestFirst[:10]) {
		t.Fatalf("resumable favourites: total %d, %v, want %v", total, ids, newestFirst[:10])
	}
	if f.catalog.peakMeta < 2 || f.catalog.peakMeta > metaFetchLimit {
		t.Fatalf("%d lookups at once, want 2..%d", f.catalog.peakMeta, metaFetchLimit)
	}
	// The ten rows and the one that did not resolve, never the whole set.
	if f.catalog.metaCalls > 10+metaFetchLimit {
		t.Fatalf("%d lookups for a page of 10", f.catalog.metaCalls)
	}
}
