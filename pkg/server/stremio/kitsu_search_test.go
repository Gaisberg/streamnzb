package stremio

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/kitsu"
)

func TestManifestAnnouncesAnimeAndKitsu(t *testing.T) {
	m := NewManifest("1.0.0")

	for _, expectedType := range []string{"anime", "other", "documentary"} {
		found := false
		for _, typ := range m.Types {
			if typ == expectedType {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected Manifest.Types to include '%s', got %v", expectedType, m.Types)
		}
	}

	hasKitsu := false
	for _, p := range m.IDPrefixes {
		if p == "kitsu" {
			hasKitsu = true
			break
		}
	}
	if !hasKitsu {
		t.Fatalf("expected Manifest.IDPrefixes to include 'kitsu', got %v", m.IDPrefixes)
	}
}

func TestBuildSearchParamsBaseKitsuIDAndMappings(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"id": "486",
				"type": "anime",
				"attributes": {
					"canonicalTitle": "Pocket Monsters",
					"titles": {
						"en": "Pokémon",
						"en_jp": "Pocket Monsters"
					},
					"abbreviatedTitles": [],
					"startDate": "1997-04-01",
					"showType": "TV"
				}
			},
			"included": [
				{
					"type": "mappings",
					"attributes": {
						"externalSite": "thetvdb/series",
						"externalId": "76703"
					}
				}
			]
		}`))
	}))
	defer ts.Close()

	srv := &Server{
		kitsuClient: kitsu.NewClient(ts.Client()),
	}
	srv.kitsuClient.BaseURL = ts.URL

	params, err := srv.buildSearchParamsBase("anime", "kitsu:486:1", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase failed: %v", err)
	}

	if len(params.SeriesTitleQueries) == 0 {
		t.Fatal("expected SeriesTitleQueries to be populated for kitsu request")
	}

	foundPokemon := false
	for title := range params.SeriesTitleQueries {
		if title == "Pokemon" || title == "Pokémon" {
			foundPokemon = true
			break
		}
	}
	if !foundPokemon {
		t.Fatalf("expected Pokemon title query in %v", params.SeriesTitleQueries)
	}

	validationProfiles := query.ValidationQueryProfilesFromMetadata(params.Metadata, "anime", []string{"en-US"}, false)
	foundPokemonVal := false
	for _, prof := range validationProfiles {
		if prof.Query == "Pokemon" || prof.Query == "Pokémon" {
			foundPokemonVal = true
			break
		}
	}
	if !foundPokemonVal {
		t.Fatalf("expected Pokemon validation profile, got %v", validationProfiles)
	}
}
