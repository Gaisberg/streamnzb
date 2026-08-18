package stremio

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/kitsu"
)

func boolPtrCert(v bool) *bool { return &v }

func TestCapForProfile(t *testing.T) {
	if _, capped := capForProfile(nil); capped {
		t.Error("nil profile must be uncapped")
	}
	if _, capped := capForProfile(&config.MetadataProfileConfig{}); capped {
		t.Error("empty max_certification must be uncapped")
	}
	if _, capped := capForProfile(&config.MetadataProfileConfig{MaxCertification: "nope"}); capped {
		t.Error("unknown cap id must be uncapped")
	}
	cap, capped := capForProfile(&config.MetadataProfileConfig{MaxCertification: "13"})
	if !capped || cap.MaxAge != 13 || cap.AllowUnrated {
		t.Fatalf("cap = %+v capped=%v", cap, capped)
	}
	open, _ := capForProfile(&config.MetadataProfileConfig{MaxCertification: "13", AllowUnrated: boolPtrCert(true)})
	if !open.AllowUnrated {
		t.Error("allow_unrated must carry into the cap")
	}
}

func TestCertGateMetaFailsClosed(t *testing.T) {
	capped := &config.MetadataProfileConfig{MaxCertification: "13"}
	if err := certGateMeta(capped, 13, true); err != nil {
		t.Errorf("PG-13 under a 13 cap must pass: %v", err)
	}
	if err := certGateMeta(capped, 17, true); !errors.Is(err, errCertificationBlocked) {
		t.Errorf("R over a 13 cap must block, got %v", err)
	}
	// The parental-control departure from fail-open: unknown blocks.
	if err := certGateMeta(capped, 0, false); !errors.Is(err, errCertificationBlocked) {
		t.Errorf("unknown certification must fail closed, got %v", err)
	}
	openProfile := &config.MetadataProfileConfig{MaxCertification: "13", AllowUnrated: boolPtrCert(true)}
	if err := certGateMeta(openProfile, 0, false); err != nil {
		t.Errorf("allow_unrated must let unknown through: %v", err)
	}
	if err := certGateMeta(&config.MetadataProfileConfig{}, 18, true); err != nil {
		t.Errorf("uncapped profile must never block: %v", err)
	}
}

// TestKitsuCatalogFiltersByCap pins the zero-fetch path: listing rows carry
// ageRating inline, and a capped profile drops the R-rated one.
func TestKitsuCatalogFiltersByCap(t *testing.T) {
	srv := metaTestServer(t, nil, nil, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": [
			{"id": "1", "attributes": {"canonicalTitle": "Kids Show", "ageRating": "G"}},
			{"id": "2", "attributes": {"canonicalTitle": "Violent Show", "ageRating": "R"}},
			{"id": "3", "attributes": {"canonicalTitle": "Mystery Show"}}
		]}`))
	})
	def, _ := catalogDefByID("kitsu.trending.anime")

	capped := &config.MetadataProfileConfig{MaxCertification: "7"}
	previews, err := srv.kitsuCatalog(context.Background(), def, catalogRequest{Type: "anime", ID: def.ID, Profile: capped})
	if err != nil {
		t.Fatalf("kitsuCatalog: %v", err)
	}
	if len(previews) != 1 || previews[0].Name != "Kids Show" {
		t.Fatalf("previews = %+v, want only the G-rated row (unrated fails closed)", previews)
	}

	open := &config.MetadataProfileConfig{MaxCertification: "7", AllowUnrated: boolPtrCert(true)}
	previews, err = srv.kitsuCatalog(context.Background(), def, catalogRequest{Type: "anime", ID: def.ID, Profile: open})
	if err != nil {
		t.Fatalf("kitsuCatalog: %v", err)
	}
	if len(previews) != 2 {
		t.Fatalf("previews = %+v, want G-rated plus unrated with allow_unrated", previews)
	}

	uncapped := &config.MetadataProfileConfig{}
	previews, err = srv.kitsuCatalog(context.Background(), def, catalogRequest{Type: "anime", ID: def.ID, Profile: uncapped})
	if err != nil {
		t.Fatalf("kitsuCatalog: %v", err)
	}
	if len(previews) != 3 {
		t.Fatalf("previews = %d, want all rows without a cap", len(previews))
	}
}

// TestFamilyMoviesDiscoverPushesCeilingUpstream pins the cap-aware discover
// request shape: the family catalog's built-in PG ceiling on an uncapped
// profile, tightened to G by an all-ages cap, and never widened by a looser
// one.
func TestFamilyMoviesDiscoverPushesCeilingUpstream(t *testing.T) {
	var gotQuery atomic.Value
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/discover/movie") {
			gotQuery.Store(r.URL.Query())
			_, _ = w.Write([]byte(`{"page": 1, "results": []}`))
			return
		}
		http.NotFound(w, r)
	}, nil, nil)
	def, ok := catalogDefByID("tmdb.family.movie")
	if !ok {
		t.Fatal("tmdb.family.movie missing from the registry")
	}

	cases := []struct {
		name    string
		profile *config.MetadataProfileConfig
		want    string
	}{
		{"uncapped keeps the built-in PG ceiling", &config.MetadataProfileConfig{}, "PG"},
		{"all-ages cap tightens to G", &config.MetadataProfileConfig{MaxCertification: "0"}, "G"},
		{"looser cap never widens the built-in ceiling", &config.MetadataProfileConfig{MaxCertification: "18"}, "PG"},
	}
	for i, tc := range cases {
		// Distinct pages keep each case out of the response cache, so the stub
		// sees every request.
		req := catalogRequest{Type: def.Type, ID: def.ID, Skip: i * catalogPageSize, Profile: tc.profile}
		if _, err := srv.tmdbCatalog(context.Background(), def, req); err != nil {
			t.Fatalf("%s: tmdbCatalog: %v", tc.name, err)
		}
		q := gotQuery.Load().(url.Values)
		if got := q.Get("certification.lte"); got != tc.want {
			t.Errorf("%s: certification.lte = %q, want %q", tc.name, got, tc.want)
		}
		if q.Get("certification_country") != "US" || q.Get("with_genres") != "10751" {
			t.Errorf("%s: params = %v, want US certification country and the family genre", tc.name, q)
		}
	}
}

// Kids TV has no built-in ceiling and TV discover has no certification
// filter — the request must carry the genre but never certification params.
func TestKidsTVDiscoverCarriesGenreOnly(t *testing.T) {
	var gotQuery atomic.Value
	srv := metaTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/discover/tv") {
			gotQuery.Store(r.URL.Query())
			_, _ = w.Write([]byte(`{"page": 1, "results": []}`))
			return
		}
		http.NotFound(w, r)
	}, nil, nil)
	def, _ := catalogDefByID("tmdb.kids.series")

	profile := &config.MetadataProfileConfig{MaxCertification: "0"}
	if _, err := srv.tmdbCatalog(context.Background(), def, catalogRequest{Type: def.Type, ID: def.ID, Profile: profile}); err != nil {
		t.Fatalf("tmdbCatalog: %v", err)
	}
	q := gotQuery.Load().(url.Values)
	if q.Get("with_genres") != "10762" {
		t.Errorf("with_genres = %q, want the kids genre", q.Get("with_genres"))
	}
	if q.Get("certification.lte") != "" || q.Get("certification_country") != "" {
		t.Errorf("TV discover must not carry certification params, got %v", q)
	}
}

// TestKidsAnimeListingTightensWithCap pins the Kitsu server-side filter: G,PG
// by default, G under an all-ages cap.
func TestKidsAnimeListingTightensWithCap(t *testing.T) {
	var gotPath atomic.Value
	srv := metaTestServer(t, nil, nil, func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.RawQuery)
		_, _ = w.Write([]byte(`{"data": []}`))
	})
	def, ok := catalogDefByID("kitsu.kids.anime")
	if !ok {
		t.Fatal("kitsu.kids.anime missing from the registry")
	}

	if _, err := srv.kitsuCatalog(context.Background(), def, catalogRequest{Type: def.Type, ID: def.ID, Profile: &config.MetadataProfileConfig{}}); err != nil {
		t.Fatalf("kitsuCatalog: %v", err)
	}
	if q := gotPath.Load().(string); !strings.Contains(q, "ageRating]=G,PG") {
		t.Errorf("uncapped kids listing query = %q, want the G,PG filter", q)
	}

	capped := &config.MetadataProfileConfig{MaxCertification: "0"}
	if _, err := srv.kitsuCatalog(context.Background(), def, catalogRequest{Type: def.Type, ID: def.ID, Profile: capped}); err != nil {
		t.Fatalf("kitsuCatalog capped: %v", err)
	}
	if q := gotPath.Load().(string); !strings.Contains(q, "ageRating]=G&") {
		t.Errorf("capped kids listing query = %q, want the G-only filter", q)
	}
}

func TestResolveSearchCertificationKitsuInline(t *testing.T) {
	srv := &Server{}
	params := &query.SearchParams{Metadata: &query.ResolvedSearchMetadata{
		KitsuDetails: &kitsu.AnimeDetails{AgeRating: "R18"},
	}}
	age, known := srv.resolveSearchCertification("anime", params)
	if !known || age != 18 {
		t.Fatalf("kitsu inline cert = (%d, %v), want (18, true)", age, known)
	}
	// No metadata at all: unknown.
	if _, known := srv.resolveSearchCertification("movie", &query.SearchParams{}); known {
		t.Error("no ids must resolve to unknown")
	}
}
