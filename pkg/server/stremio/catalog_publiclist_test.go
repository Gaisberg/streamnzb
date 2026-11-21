package stremio

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/services/metadata/tmdb"
)

// TestPublicListCatalogStopsOnRepeatedPage pins the paging guard for a source
// that answers a page past the end of the list by re-serving the last one
// instead of an empty document — MDBList's infinite-scroll endpoint does
// exactly that. Only "the page was empty" used to end the walk, so a request
// past the end of a list fetched all externalListMaxPages (100 upstream HTML
// pages) before giving up.
func TestPublicListCatalogStopsOnRepeatedPage(t *testing.T) {
	s := &Server{}
	page := []tmdb.PublicListItem{
		{ID: 1, Type: "movie", Name: "One"},
		{ID: 2, Type: "movie", Name: "Two"},
	}
	fetched := 0
	metas, err := s.publicListCatalog(context.Background(),
		CatalogDef{Type: "movie"},
		catalogRequest{Skip: 0},
		1,
		func(int) ([]tmdb.PublicListItem, error) {
			fetched++
			return page, nil
		})
	if err != nil {
		t.Fatalf("publicListCatalog: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("metas = %d, want the 2 distinct rows", len(metas))
	}
	// Page 1 is new, page 2 repeats it and ends the walk.
	if fetched != 2 {
		t.Fatalf("upstream pages fetched = %d, want 2 — a repeated page must end paging", fetched)
	}
}

// TestPublicListCatalogKeepsPagingPastOtherMediaType is the other half: a page
// holding only the *other* media type is progress, not the end of the list, so
// a mixed list must keep paging until it has filled the requested window.
func TestPublicListCatalogKeepsPagingPastOtherMediaType(t *testing.T) {
	s := &Server{}
	fetched := 0
	metas, err := s.publicListCatalog(context.Background(),
		CatalogDef{Type: "movie"},
		catalogRequest{Skip: 0},
		1,
		func(page int) ([]tmdb.PublicListItem, error) {
			fetched++
			switch page {
			case 1, 2:
				// Series-only pages: nothing for a movie catalog, but new rows.
				return []tmdb.PublicListItem{{ID: page * 10, Type: "tv", Name: fmt.Sprintf("Show %d", page)}}, nil
			case 3:
				return []tmdb.PublicListItem{{ID: 3, Type: "movie", Name: "Finally"}}, nil
			}
			return nil, nil
		})
	if err != nil {
		t.Fatalf("publicListCatalog: %v", err)
	}
	if len(metas) != 1 || metas[0].Name != "Finally" {
		t.Fatalf("metas = %+v, want the movie found on page 3", metas)
	}
	if fetched < 3 {
		t.Fatalf("upstream pages fetched = %d, want the walk to pass the series-only pages", fetched)
	}
}

// TestPublicListCatalogNamesRowsFromIMDbIDWhenPresent keeps the id preference
// pinned while the loop around it changes: a canonical IMDb id wins over the
// TMDB numeric id, and a row with neither is dropped.
func TestPublicListCatalogNamesRowsFromIMDbIDWhenPresent(t *testing.T) {
	s := &Server{}
	metas, err := s.publicListCatalog(context.Background(),
		CatalogDef{Type: "movie"},
		catalogRequest{Skip: 0},
		1,
		func(page int) ([]tmdb.PublicListItem, error) {
			if page > 1 {
				return nil, nil
			}
			return []tmdb.PublicListItem{
				{ID: 603, IMDbID: "tt0133093", Type: "movie", Name: "The Matrix (1999)"},
				{ID: 604, Type: "movie", Name: "No IMDb Id"},
				{Type: "movie", Name: "Nothing Canonical"},
			}, nil
		})
	if err != nil {
		t.Fatalf("publicListCatalog: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("metas = %+v, want the two rows carrying a canonical id", metas)
	}
	if metas[0].ID != "tt0133093" || metas[0].Name != "The Matrix" {
		t.Fatalf("metas[0] = %+v, want the IMDb id and the year stripped", metas[0])
	}
	if metas[1].ID != "tmdb:604" {
		t.Fatalf("metas[1] = %+v, want the TMDB fallback id", metas[1])
	}
}

// publicListIDs runs publicListCatalog over fixed pages (page numbers start at
// 1; a page past the map is empty) and returns the emitted ids in order.
func publicListIDs(t *testing.T, defType string, skip int, pages map[int][]tmdb.PublicListItem) []string {
	t.Helper()
	s := &Server{}
	metas, err := s.publicListCatalog(context.Background(), CatalogDef{Type: defType}, catalogRequest{Skip: skip}, 1,
		func(page int) ([]tmdb.PublicListItem, error) { return pages[page], nil })
	if err != nil {
		t.Fatalf("publicListCatalog: %v", err)
	}
	ids := make([]string, len(metas))
	for i, m := range metas {
		ids[i] = m.ID
	}
	return ids
}

func assertIDs(t *testing.T, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

// MDBList can serve one title with an IMDb id on one page and without on
// another; the source identity is the TMDB id, so it is emitted once, in
// whichever form came first.
func TestPublicListCatalogDedupesTMDBTitleAcrossIMDbAvailability(t *testing.T) {
	assertIDs(t, publicListIDs(t, "movie", 0, map[int][]tmdb.PublicListItem{
		1: {{ID: 603, IMDbID: "tt0133093", Type: "movie", Name: "The Matrix"}},
		2: {{ID: 603, Type: "movie", Name: "The Matrix"}, {ID: 604, Type: "movie", Name: "Other"}},
	}), []string{"tt0133093", "tmdb:604"})
	assertIDs(t, publicListIDs(t, "movie", 0, map[int][]tmdb.PublicListItem{
		1: {{ID: 603, Type: "movie", Name: "The Matrix"}},
		2: {{ID: 603, IMDbID: "tt0133093", Type: "movie", Name: "The Matrix"}, {ID: 604, Type: "movie", Name: "Other"}},
	}), []string{"tmdb:603", "tmdb:604"})
}

// A movie and a series can share a numeric TMDB id; source tracking must keep
// them apart.
func TestPublicListCatalogKeepsMediaTypesApartForSharedTMDBID(t *testing.T) {
	assertIDs(t, publicListIDs(t, "movie", 0, map[int][]tmdb.PublicListItem{
		1: {{ID: 603, Type: "tv", Name: "Show"}},
		2: {{ID: 603, Type: "movie", Name: "Movie"}},
	}), []string{"tmdb:603"})
}

// Letterboxd rows carry only an IMDb id, which stays their source identity.
func TestPublicListCatalogDedupesIMDbOnlyRows(t *testing.T) {
	assertIDs(t, publicListIDs(t, "movie", 0, map[int][]tmdb.PublicListItem{
		1: {{IMDbID: "tt0000001", Type: "movie", Name: "One"}, {IMDbID: "tt0000002", Type: "movie", Name: "Two"}},
		2: {{IMDbID: "tt0000001", Type: "movie", Name: "One"}, {IMDbID: "tt0000003", Type: "movie", Name: "Three"}},
	}), []string{"tt0000001", "tt0000002", "tt0000003"})
}

// Skip counts deduplicated rows, and a repeated or other-media page does not
// end the walk early.
func TestPublicListCatalogSkipAfterCrossPageDedup(t *testing.T) {
	pages := map[int][]tmdb.PublicListItem{
		1: {{ID: 603, IMDbID: "tt0133093", Type: "movie", Name: "A"}, {ID: 604, Type: "movie", Name: "B"}},
		2: {{ID: 603, Type: "movie", Name: "A"}, {ID: 900, Type: "tv", Name: "Show"}},
		3: {{ID: 605, Type: "movie", Name: "C"}},
	}
	assertIDs(t, publicListIDs(t, "movie", 1, pages), []string{"tmdb:604", "tmdb:605"})
	assertIDs(t, publicListIDs(t, "movie", 2, pages), []string{"tmdb:605"})
}

// An MDBList row keeps its IMDb id as the emitted identity (poster overlays
// key on it) and carries the TMDB id it already knows; IMDb-only rows carry none.
func TestPublicListCatalogKeepsKnownTMDBID(t *testing.T) {
	s := &Server{}
	metas, err := s.publicListCatalog(context.Background(), CatalogDef{Type: "movie"}, catalogRequest{}, 1,
		func(page int) ([]tmdb.PublicListItem, error) {
			if page > 1 {
				return nil, nil
			}
			return []tmdb.PublicListItem{
				{ID: 603, IMDbID: "tt0133093", Type: "movie", Name: "The Matrix"},
				{ID: 604, Type: "movie", Name: "TMDB Only"},
				{IMDbID: "tt0000001", Type: "movie", Name: "Letterboxd Row"},
			}, nil
		})
	if err != nil {
		t.Fatalf("publicListCatalog: %v", err)
	}
	want := []struct {
		id   string
		tmdb int
	}{{"tt0133093", 603}, {"tmdb:604", 604}, {"tt0000001", 0}}
	if len(metas) != len(want) {
		t.Fatalf("metas = %+v", metas)
	}
	for i, w := range want {
		if metas[i].ID != w.id || metas[i].tmdbID != w.tmdb {
			t.Errorf("metas[%d] = id %q tmdbID %d, want %q / %d", i, metas[i].ID, metas[i].tmdbID, w.id, w.tmdb)
		}
	}
}

// Artwork for a row whose TMDB id is known comes straight from the numeric
// endpoints: a failing Find must not cost the row its poster, and no Find is
// sent at all. A row with only an IMDb id still resolves through Find.
func TestFillPreviewFromMetadataPrefersKnownTMDBID(t *testing.T) {
	var finds atomic.Int32
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/603":
			_, _ = w.Write([]byte(`{"title":"The Matrix","poster_path":"/m.jpg","backdrop_path":"/mb.jpg"}`))
		case "/tv/1396":
			_, _ = w.Write([]byte(`{"name":"Breaking Bad","poster_path":"/t.jpg","backdrop_path":"/tb.jpg"}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/find/") {
				finds.Add(1)
				if strings.HasSuffix(r.URL.Path, "tt0000603") {
					_, _ = w.Write([]byte(`{"movie_results":[{"id":603}]}`))
					return
				}
			}
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}
	}, nil, nil)
	ctx := context.Background()

	movie := MetaPreview{ID: "tt0133093", Name: "stub", tmdbID: 603}
	srv.fillPreviewFromMetadata(ctx, &movie, "movie", "")
	if movie.Name != "The Matrix" || movie.Poster != tmdbPosterURL+"/m.jpg" || movie.Background != tmdbBackdropURL+"/mb.jpg" || movie.ID != "tt0133093" {
		t.Errorf("movie = %+v, want TMDB artwork and the IMDb id kept", movie)
	}
	series := MetaPreview{ID: "tt0903747", Name: "stub", tmdbID: 1396}
	srv.fillPreviewFromMetadata(ctx, &series, "series", "")
	if series.Name != "Breaking Bad" || series.Poster != tmdbPosterURL+"/t.jpg" {
		t.Errorf("series = %+v, want TMDB artwork", series)
	}
	tmdbOnly := MetaPreview{ID: "tmdb:603", Name: "stub"}
	srv.fillPreviewFromMetadata(ctx, &tmdbOnly, "movie", "")
	if tmdbOnly.Poster != tmdbPosterURL+"/m.jpg" {
		t.Errorf("tmdb-only row poster = %q", tmdbOnly.Poster)
	}
	if n := finds.Load(); n != 0 {
		t.Fatalf("Find requests = %d, want 0 when the TMDB id is known", n)
	}

	imdbOnly := MetaPreview{ID: "tt0000603", Name: "stub"}
	srv.fillPreviewFromMetadata(ctx, &imdbOnly, "movie", "")
	if imdbOnly.Poster != tmdbPosterURL+"/m.jpg" || finds.Load() != 1 {
		t.Errorf("imdb-only row = %+v after %d Find requests, want one Find and the artwork", imdbOnly, finds.Load())
	}
}
