package stremio

import (
	"context"
	"errors"
	"net/http"
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
