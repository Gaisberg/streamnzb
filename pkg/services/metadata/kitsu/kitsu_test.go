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

func TestDisplayTitle(t *testing.T) {
	cases := []struct {
		name      string
		lang      string
		english   string
		canonical string
		want      string
	}{
		{"empty language prefers English", "", "Attack on Titan", "Shingeki no Kyojin", "Attack on Titan"},
		{"en prefers English", "en", "Attack on Titan", "Shingeki no Kyojin", "Attack on Titan"},
		{"en-US prefers English", "en-US", "Attack on Titan", "Shingeki no Kyojin", "Attack on Titan"},
		{"non-English keeps canonical", "de-DE", "Attack on Titan", "Shingeki no Kyojin", "Shingeki no Kyojin"},
		{"English requested but missing falls back to canonical", "en", "", "Shingeki no Kyojin", "Shingeki no Kyojin"},
		{"neither carries a canonical falls back to English", "de-DE", "Attack on Titan", "", "Attack on Titan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DisplayTitle(tc.lang, tc.english, tc.canonical)
			if got != tc.want {
				t.Fatalf("DisplayTitle(%q, %q, %q) = %q, want %q", tc.lang, tc.english, tc.canonical, got, tc.want)
			}
		})
	}
}

func TestGetAnimeEpisodesDecodesEnglishTitle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [
			{"attributes": {"canonicalTitle": "Toubousha Miku", "titles": {"en": "The Runaway"},
			 "number": 1, "airdate": "2013-04-06"}}
		]}`))
	}))
	defer ts.Close()

	client := NewClient(ts.Client())
	client.BaseURL = ts.URL

	episodes, err := client.GetAnimeEpisodes(context.Background(), "1")
	if err != nil {
		t.Fatalf("GetAnimeEpisodes failed: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("episodes = %d, want 1", len(episodes))
	}
	if episodes[0].CanonicalTitle != "Toubousha Miku" {
		t.Fatalf("CanonicalTitle = %q", episodes[0].CanonicalTitle)
	}
	if episodes[0].EnglishTitle != "The Runaway" {
		t.Fatalf("EnglishTitle = %q, want %q", episodes[0].EnglishTitle, "The Runaway")
	}
}

func TestGetAnimeListingDecodesEnglishTitle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [
			{"id": "42", "attributes": {"canonicalTitle": "Shingeki no Kyojin", "titles": {"en": "Attack on Titan"},
			 "synopsis": "Humanity fights titans.", "ageRating": "R",
			 "posterImage": {"original": "https://img/poster.jpg"}}}
		]}`))
	}))
	defer ts.Close()

	client := NewClient(ts.Client())
	client.BaseURL = ts.URL

	listings, err := client.GetAnimeListing(context.Background(), "trending", 0)
	if err != nil {
		t.Fatalf("GetAnimeListing failed: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("listings = %d, want 1", len(listings))
	}
	if listings[0].CanonicalTitle != "Shingeki no Kyojin" {
		t.Fatalf("CanonicalTitle = %q", listings[0].CanonicalTitle)
	}
	if listings[0].EnglishTitle != "Attack on Titan" {
		t.Fatalf("EnglishTitle = %q, want %q", listings[0].EnglishTitle, "Attack on Titan")
	}
}
