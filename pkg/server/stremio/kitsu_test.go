package stremio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestKitsuClientGetAnimeDetailsWithMappings(t *testing.T) {
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
					"abbreviatedTitles": ["Pokemon"],
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
				},
				{
					"type": "mappings",
					"attributes": {
						"externalSite": "imdb",
						"externalId": "0168366"
					}
				}
			]
		}`))
	}))
	defer ts.Close()

	client := NewKitsuClient(ts.Client())
	client.baseURL = ts.URL

	details, err := client.GetAnimeDetails(context.Background(), "486")
	if err != nil {
		t.Fatalf("GetAnimeDetails failed: %v", err)
	}

	if details.CanonicalTitle != "Pocket Monsters" {
		t.Fatalf("expected CanonicalTitle Pocket Monsters, got %s", details.CanonicalTitle)
	}
	if details.EnglishTitle != "Pokémon" {
		t.Fatalf("expected EnglishTitle Pokémon, got %s", details.EnglishTitle)
	}
	if details.TVDBID != "76703" {
		t.Fatalf("expected TVDBID 76703, got %s", details.TVDBID)
	}
	if details.IMDbID != "tt0168366" {
		t.Fatalf("expected IMDbID tt0168366, got %s", details.IMDbID)
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
		kitsuClient: NewKitsuClient(ts.Client()),
	}
	srv.kitsuClient.baseURL = ts.URL

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

	validationProfiles := validationQueryProfilesFromMetadata(params.Metadata, "anime", []string{"en-US"}, false)
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
