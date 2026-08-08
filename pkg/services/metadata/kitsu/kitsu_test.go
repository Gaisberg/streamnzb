package kitsu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

	client := NewClient(ts.Client())
	client.BaseURL = ts.URL

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
