package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/cinemeta"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
)

// tvdbSeriesSearchStub answers with the shape TVDB really returns: the record
// is named in its own language, the readable name is in translations, and the
// ids search already knows are in remote_ids. The first record is the one
// TMDB has never populated.
func tvdbSeriesSearchStub(t *testing.T, results string) (*tvdb.Client, *int64) {
	t.Helper()
	var calls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/login") {
			fmt.Fprint(w, `{"status":"success","data":{"token":"test-token"}}`)
			return
		}
		atomic.AddInt64(&calls, 1)
		fmt.Fprintf(w, `{"status":"success","data":[%s]}`, results)
	}))
	t.Cleanup(ts.Close)
	client := tvdb.NewClient("test-key", t.TempDir())
	client.BaseURL = ts.URL
	return client, &calls
}

const tvdbUnpopulatedOnTMDB = `{"tvdb_id":"465668","name":"나 혼자만 레벨업","primary_language":"kor",
	"translations":{"eng":"Solo Leveling (2027)"},"image_url":"https://artworks.thetvdb.com/x.jpg",
	"overview":"A Korean adaptation.","remote_ids":[{"id":"tt35506453","sourceName":"IMDB"},{"id":"295389","sourceName":"TheMovieDB.com"}]}`

// The reported bug: a series TMDB has never populated rendered as a blank row
// because search always asked TMDB, whatever the profile ranked first. With
// TVDB leading series, the row comes from TVDB — name, artwork and all — and
// keeps the IMDb id the rest of the addon plays by.
func TestSeriesSearchFollowsTheProfilesLeadingSource(t *testing.T) {
	logger.Init("ERROR")
	tvdbClient, _ := tvdbSeriesSearchStub(t, tvdbUnpopulatedOnTMDB)
	srv := &Server{tvdbClient: tvdbClient}

	profile := &config.MetadataProfileConfig{} // default series order: tvdb, tmdb
	def, ok := searchCatalogDefByID(profile, "tvdb.search.series")
	if !ok {
		t.Fatal("a TVDB-led profile must carry its series search on TVDB")
	}
	metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Search: "solo leveling", Profile: profile})
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("returned %d rows, want 1: %+v", len(metas), metas)
	}
	row := metas[0]
	if row.ID != "tt35506453" {
		t.Errorf("id = %q, want the IMDb id TVDB's remote_ids already carried", row.ID)
	}
	if row.Name != "Solo Leveling (2027)" {
		t.Errorf("name = %q, want the English translation rather than the record's own language", row.Name)
	}
	if row.Poster == "" {
		t.Error("poster is empty, want TVDB's artwork — a blank card is the bug being fixed")
	}
}

// Each media type's carrier is whichever source that type's list leads with.
func TestSearchCarriersFollowEachTypesPriorityList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile *config.MetadataProfileConfig
		want    []string
	}{
		{"defaults", &config.MetadataProfileConfig{}, []string{"tmdb.search.movie", "tvdb.search.series", "kitsu.search.anime"}},
		{"tmdb-led series", &config.MetadataProfileConfig{SeriesSources: []string{"tmdb", "tvdb"}}, []string{"tmdb.search.movie", "tmdb.search.series", "kitsu.search.anime"}},
		{"cinemeta-led movies", &config.MetadataProfileConfig{MovieSources: []string{"cinemeta", "tmdb"}}, []string{"cinemeta.search.movie", "tvdb.search.series", "kitsu.search.anime"}},
		{"tvdb-led anime", &config.MetadataProfileConfig{AnimeSources: []string{"tvdb", "kitsu"}}, []string{"tmdb.search.movie", "tvdb.search.series", "tvdb.search.anime"}},
		// No profile is not "no search": an unbound request still resolves to
		// each type's default source.
		{"no profile", nil, []string{"tmdb.search.movie", "tvdb.search.series", "kitsu.search.anime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, def := range searchCatalogDefs(tc.profile) {
				got = append(got, def.ID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("carriers = %v, want %v", got, tc.want)
			}
		})
	}
}

// The rest of the list is not a second result set — it is what answers when
// the leading source cannot.
func TestSearchFallsBackToTheNextSourceInTheList(t *testing.T) {
	logger.Init("ERROR")
	tvdbClient, tvdbCalls := tvdbSeriesSearchStub(t, "")
	tmdbClient, _ := searchStubTMDB(t)
	srv := &Server{tvdbClient: tvdbClient, tmdbClient: tmdbClient}

	profile := &config.MetadataProfileConfig{} // tvdb, then tmdb
	def, ok := searchCatalogDefByID(profile, "tvdb.search.series")
	if !ok {
		t.Fatal("tvdb.search.series missing from the search carriers")
	}
	metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Search: "fire force", Profile: profile})
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("an empty answer from the leading source must fall through to the next one, not end the search")
	}
	if atomic.LoadInt64(tvdbCalls) == 0 {
		t.Error("the leading source was never asked")
	}
}

// TVDB publishes no rating, so ranking it first would cost every row the
// badge a TMDB row carries. The fill is per-field: what TVDB did publish is
// what the profile asked for and must survive.
func TestSearchFillsOnlyTheGapsTheLeadingSourceLeft(t *testing.T) {
	logger.Init("ERROR")
	tvdbClient, _ := tvdbSeriesSearchStub(t, tvdbUnpopulatedOnTMDB)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tv_results":[{"id":295389,"name":"Solo Leveling","vote_average":7.5,"poster_path":"/tmdb.jpg","overview":"From TMDB."}]}`)
	}))
	t.Cleanup(ts.Close)
	tmdbClient := tmdb.NewClient("test-key")
	tmdbClient.BaseURL = ts.URL
	srv := &Server{tvdbClient: tvdbClient, tmdbClient: tmdbClient}

	profile := &config.MetadataProfileConfig{} // tvdb, then tmdb
	def, _ := searchCatalogDefByID(profile, "tvdb.search.series")
	metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Search: "solo leveling", Profile: profile})
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("returned %d rows, want 1", len(metas))
	}
	if metas[0].IMDBRating != "7.5" {
		t.Errorf("rating = %q, want 7.5 filled from the next source", metas[0].IMDBRating)
	}
	if metas[0].Poster != "https://artworks.thetvdb.com/x.jpg" {
		t.Errorf("poster = %q, want TVDB's own artwork kept", metas[0].Poster)
	}
	if metas[0].Description != "A Korean adaptation." {
		t.Errorf("description = %q, want TVDB's own overview kept", metas[0].Description)
	}
}

// A row TVDB knows only by its own id still reaches TMDB, through the TMDB id
// TVDB volunteered alongside it — that row is the one most likely to be
// missing a rating, since find has no IMDb id to work with.
func TestSearchFillsGapsForRowsWithNoIMDbID(t *testing.T) {
	logger.Init("ERROR")
	tvdbClient, _ := tvdbSeriesSearchStub(t, `{"tvdb_id":"446883","name":"THE LEVELING OF SOLO LEVELING",
		"translations":{"eng":"THE LEVELING OF SOLO LEVELING"},"image_url":"https://artworks.thetvdb.com/y.jpg",
		"remote_ids":[{"id":"247376","sourceName":"TheMovieDB.com"}]}`)
	var detailsFor string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detailsFor = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":247376,"name":"THE LEVELING OF SOLO LEVELING","vote_average":6.8,"first_air_date":"2024-03-04"}`)
	}))
	t.Cleanup(ts.Close)
	tmdbClient := tmdb.NewClient("test-key")
	tmdbClient.BaseURL = ts.URL
	srv := &Server{tvdbClient: tvdbClient, tmdbClient: tmdbClient}

	profile := &config.MetadataProfileConfig{} // tvdb, then tmdb
	def, _ := searchCatalogDefByID(profile, "tvdb.search.series")
	metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Search: "solo leveling", Profile: profile})
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "tvdb:446883" {
		t.Fatalf("rows = %+v, want the single tvdb-keyed row", metas)
	}
	if metas[0].IMDBRating != "6.8" {
		t.Errorf("rating = %q, want 6.8 filled through the TMDB id TVDB supplied", metas[0].IMDBRating)
	}
	if !strings.Contains(detailsFor, "247376") {
		t.Errorf("TMDB was asked for %q, want the series details for 247376", detailsFor)
	}
}

// Cinemeta is opt-in, and a profile that ranks it first gets its catalog
// search rather than TMDB's.
func TestCinemetaSearchCarrier(t *testing.T) {
	logger.Init("ERROR")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"metas":[{"id":"tt0111161","name":"The Shawshank Redemption","poster":"https://images/p.jpg","releaseInfo":"1994","imdbRating":"9.3"}]}`)
	}))
	t.Cleanup(ts.Close)
	client := cinemeta.NewClient(ts.Client())
	client.BaseURL = ts.URL
	srv := &Server{cinemetaClient: client}

	profile := &config.MetadataProfileConfig{MovieSources: []string{"cinemeta"}}
	def, ok := searchCatalogDefByID(profile, "cinemeta.search.movie")
	if !ok {
		t.Fatal("a Cinemeta-led profile must carry its movie search on Cinemeta")
	}
	metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: "movie", ID: def.ID, Search: "shawshank", Profile: profile})
	if err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "tt0111161" || metas[0].IMDBRating != "9.3" {
		t.Fatalf("rows = %+v, want the single IMDb-keyed Cinemeta row", metas)
	}
	if metas[0].released != "1994" {
		t.Errorf("released = %q, want the year read out of releaseInfo", metas[0].released)
	}
}

// A record TVDB marks Upcoming with no date at all is what the priority-list
// fix started surfacing: real, but not placeable in the profile's window.
func TestSeriesSearchHidesUndatedUpcomingRecords(t *testing.T) {
	logger.Init("ERROR")
	tvdbClient, _ := tvdbSeriesSearchStub(t, `{"tvdb_id":"465668","name":"나 혼자만 레벨업","status":"Upcoming",
		"translations":{"eng":"Solo Leveling (2027)"},"image_url":"https://artworks.thetvdb.com/x.jpg",
		"remote_ids":[{"id":"tt35506453","sourceName":"IMDB"}]},
		{"tvdb_id":"389597","name":"Solo Leveling","status":"Continuing","year":"2024",
		"translations":{"eng":"Solo Leveling"},"image_url":"https://artworks.thetvdb.com/y.jpg",
		"remote_ids":[{"id":"tt21209876","sourceName":"IMDB"}]}`)
	srv := &Server{tvdbClient: tvdbClient}

	window := func(days int) *int { return &days }
	for _, tc := range []struct {
		days int
		want []string
	}{
		{config.DefaultUnreleasedWindowDays, []string{"tt21209876"}},
		// The far end of the slider means "everything upcoming".
		{config.MaxUnreleasedWindowDays, []string{"tt35506453", "tt21209876"}},
	} {
		profile := &config.MetadataProfileConfig{UnreleasedWindowDays: window(tc.days)}
		def, _ := searchCatalogDefByID(profile, "tvdb.search.series")
		got := searchPreviewIDs(srv.serveCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Search: "solo leveling", Profile: profile}))
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("window %d returned %v, want %v", tc.days, got, tc.want)
		}
	}
}
