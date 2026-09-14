package stremio

import (
	"context"
	"fmt"
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
