package jellyfin

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"streamnzb/pkg/server/stremio"
)

// genreFixture marks the TMDB libraries as TMDB and gives the movie rows
// genres: odd rows Comedy, every tenth Action, the rest Drama.
func genreFixture() *fixture {
	f := newFixture()
	for i := range f.catalog.catalogs {
		f.catalog.catalogs[i].Provider = "tmdb"
		if f.catalog.catalogs[i].Type == "anime" {
			f.catalog.catalogs[i].Provider = "kitsu"
		}
	}
	rows := f.catalog.rows["tmdb.trending.movie"]
	for i := range rows {
		switch n := i + 1; {
		case n%2 == 1:
			rows[i].GenreIDs = []int{35}
		case n%10 == 0:
			rows[i].GenreIDs = []int{28}
		default:
			rows[i].GenreIDs = []int{18}
		}
	}
	return f
}

func genreNames(items []*baseItem) []string {
	var out []string
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func TestGenresListPerLibrary(t *testing.T) {
	f := genreFixture()
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Genres?ParentId="+viewID("tmdb.trending.movie"), ""), &result)
	if got := fmt.Sprint(genreNames(result.Items)); got != "[Action Comedy Drama]" || result.TotalRecordCount != 3 {
		t.Fatalf("movie library genres: %s (total %d)", got, result.TotalRecordCount)
	}
	for _, item := range result.Items {
		id, err := decodeItemID(item.ID)
		if err != nil || id.Kind != kindGenre || item.Type != "Genre" {
			t.Fatalf("genre %q: id %q decodes to %+v, %v", item.Name, item.ID, id, err)
		}
	}
	// Across libraries a name is listed once.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Genres", ""), &result)
	if got := fmt.Sprint(genreNames(result.Items)); got != "[Action Action & Adventure Comedy Drama]" {
		t.Fatalf("all genres: %s", got)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Genres?IncludeItemTypes=Series", ""), &result)
	if got := fmt.Sprint(genreNames(result.Items)); got != "[Action & Adventure Comedy Drama]" {
		t.Fatalf("series genres: %s", got)
	}
	// A library whose source has no genres lists none.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Genres?ParentId="+viewID("kitsu.trending.anime"), ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("kitsu library listed genres: %s", genreNames(result.Items))
	}
	// The genre picker names the same genres.
	var filters struct {
		Genres []nameGUID
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items/Filters2?ParentId="+viewID("tmdb.trending.movie"), ""), &filters)
	if len(filters.Genres) != 3 || filters.Genres[1].Name != "Comedy" || filters.Genres[1].ID != genreID("movie", "Comedy") {
		t.Fatalf("filters2: %+v", filters.Genres)
	}
	// A genre opens as an item, by id and by name.
	var item baseItem
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Users/u/Items/"+genreID("movie", "Comedy"), ""), &item)
	if item.Name != "Comedy" || item.Type != "Genre" {
		t.Fatalf("genre item: %+v", item)
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Genres/Comedy", ""), &item)
	if item.Name != "Comedy" || item.ID != genreID("movie", "Comedy") {
		t.Fatalf("genre by name: %+v", item)
	}
}

func TestItemsFilterByGenre(t *testing.T) {
	f := genreFixture()
	view := viewID("tmdb.trending.movie")
	comedy := genreID("movie", "Comedy")
	var result queryResult
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Users/u/Items?ParentId=%s&GenreIds=%s&StartIndex=0&Limit=10", view, comedy), ""), &result)
	if len(result.Items) != 10 || result.Items[0].Name != "Title 1" || result.Items[9].Name != "Title 19" {
		t.Fatalf("first comedy page: %s", genreNames(result.Items))
	}
	if result.TotalRecordCount <= 10 {
		t.Fatalf("a full filtered page must leave room for more: total %d", result.TotalRecordCount)
	}
	for _, item := range result.Items {
		if !hasAnyGenre(item, []string{"Comedy"}) || len(item.GenreItems) != 1 || item.GenreItems[0].ID != comedy {
			t.Fatalf("row %q genres %v / %+v", item.Name, item.Genres, item.GenreItems)
		}
	}
	// The last page reaches the end of the library: an exact total.
	decodeInto(t, f.do(http.MethodGet, fmt.Sprintf("/jellyfin/Users/u/Items?ParentId=%s&GenreIds=%s&StartIndex=20&Limit=10", view, comedy), ""), &result)
	if len(result.Items) != 3 || result.Items[0].Name != "Title 41" || result.TotalRecordCount != 23 {
		t.Fatalf("last comedy page: %s, total %d", genreNames(result.Items), result.TotalRecordCount)
	}
	// By name, the way Jellyfin's Genres parameter names them.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+view+"&Genres=Action&Limit=50", ""), &result)
	if got := fmt.Sprint(genreNames(result.Items)); got != "[Title 10 Title 20 Title 30 Title 40]" || result.TotalRecordCount != 4 {
		t.Fatalf("action by name: %s, total %d", got, result.TotalRecordCount)
	}
	// A genre this stream does not know, and a library without genres, are
	// empty rather than unfiltered.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+view+"&GenreIds="+genreID("movie", "Western"), ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("unknown genre matched %d rows", len(result.Items))
	}
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+viewID("kitsu.trending.anime")+"&GenreIds="+comedy, ""), &result)
	if len(result.Items) != 0 {
		t.Fatalf("genre filter on a genreless library matched %d rows", len(result.Items))
	}
	// A genre as the parent lists it across libraries.
	decodeInto(t, f.do(http.MethodGet, "/jellyfin/Items?ParentId="+genreID("movie", "Action")+"&Recursive=true", ""), &result)
	if got := fmt.Sprint(genreNames(result.Items)); got != "[Title 10 Title 20]" {
		t.Fatalf("genre parent: %s", got)
	}
}

// TestFavoritesNarrowedByGenre covers a genre filter on the favourites row:
// GenreIds, Genres and a genre folder ParentId each keep only the favourites
// of that genre and of the genre's content type, counted before paging, from
// the metadata the row already fetches.
func TestFavoritesNarrowedByGenre(t *testing.T) {
	f := genreFixture()
	f.resolveOnOpen = true
	f.catalog.metas["movie/tt0000003"] = &stremio.MetaObject{ID: "tt0000003", Type: "movie", Name: "A Comedy", Genres: []string{"Comedy"}}
	f.catalog.metas["series/tt0000004"] = &stremio.MetaObject{ID: "tt0000004", Type: "series", Name: "A Sitcom", Genres: []string{"Comedy"}}
	drama, _ := itemIDFor("movie", "tt0111161") // Drama
	comedy, _ := itemIDFor("movie", "tt0000003")
	sitcom, _ := itemIDFor("series", "tt0000004")
	bb, _ := itemIDFor("series", "tt0903747") // no genres
	for _, id := range []itemID{drama, comedy, sitcom, bb.episode(1, 1)} {
		var ud userData
		decodeInto(t, f.do(http.MethodPost, "/jellyfin/UserFavoriteItems/"+id.encode(), ""), &ud)
	}
	movies := "Recursive=true&Filters=IsFavorite&ParentId=" + viewID("tmdb.trending.movie")

	for query, want := range map[string][]string{
		movies + "&GenreIds=" + genreID("movie", "Comedy"):                          {comedy.encode()},
		movies + "&Genres=Comedy":                                                   {comedy.encode()},
		movies + "&Genres=comedy|Drama":                                             {comedy.encode(), drama.encode()},
		movies + "&GenreIds=" + genreID("movie", "Western"):                         {},
		movies + "&GenreIds=not-an-id":                                              {},
		"Recursive=true&Filters=IsFavorite&ParentId=" + genreID("movie", "Comedy"):  {comedy.encode()},
		"Recursive=true&Filters=IsFavorite&ParentId=" + genreID("series", "Comedy"): {sitcom.encode()},
		"Recursive=true&Filters=IsFavorite&Genres=Comedy":                           {comedy.encode(), sitcom.encode()},
		"Recursive=true&Filters=IsFavorite&Genres=Comedy&IncludeItemTypes=Series":   {sitcom.encode()},
		"Recursive=true&Filters=IsFavorite":                                         {drama.encode(), comedy.encode(), sitcom.encode(), bb.episode(1, 1).encode()},
	} {
		total, ids := favoriteIDs(t, f, query)
		if want := slices.Sorted(slices.Values(want)); total != len(want) || !slices.Equal(slices.Sorted(slices.Values(ids)), want) {
			t.Fatalf("%s: total %d, %v, want %v", query, total, ids, want)
		}
	}

	// Paged after the genre narrows, at no metadata cost beyond the row's own.
	query := movies + "&Genres=Comedy|Drama"
	_, full := favoriteIDs(t, f, query)
	metas, searches := f.catalog.metaCalls, f.catalog.playlistCalls
	if total, ids := favoriteIDs(t, f, query+"&StartIndex=1&Limit=1"); total != 2 || !slices.Equal(ids, full[1:]) {
		t.Fatalf("paged: total %d, %v, want %v", total, ids, full[1:])
	}
	if n := f.catalog.metaCalls - metas; n != 2 {
		t.Fatalf("genre-filtered favourites looked up metadata %d times, want 2", n)
	}
	if f.catalog.playlistCalls != searches {
		t.Fatalf("genre-filtered favourites searched %d times", f.catalog.playlistCalls-searches)
	}
}
