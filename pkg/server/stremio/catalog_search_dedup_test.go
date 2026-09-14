package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
)

// searchStubTMDB answers the series search with the anime from the anime-lists
// fixture (tmdb tv 88046 -> tt9307686, Kitsu 49016) plus an unrelated title
// that no anime source carries.
func searchStubTMDB(t *testing.T) (*tmdb.Client, *int64) {
	t.Helper()
	var calls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/tv":
			fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":2,"results":[
				{"id":88046,"name":"Fire Force","poster_path":"/ff.jpg"},
				{"id":900,"name":"The Making of Fire Force","poster_path":"/mff.jpg"}]}`)
		case "/tv/88046/external_ids":
			fmt.Fprint(w, `{"imdb_id":"tt9307686"}`)
		default:
			fmt.Fprint(w, `{"imdb_id":"tt5550000"}`)
		}
	}))
	t.Cleanup(ts.Close)
	client := tmdb.NewClient("test-key")
	client.BaseURL = ts.URL
	return client, &calls
}

// searchStubKitsu answers an anime search with the two cours Kitsu splits the
// same series into. Only 49016 is in the anime-lists fixture.
func searchStubKitsu(t *testing.T) *kitsu.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[
			{"id":"49016","attributes":{"canonicalTitle":"Enen no Shouboutai: San no Shou Part 2","titles":{"en":"Fire Force Season 3 Part 2"},"posterImage":{"medium":"https://kitsu/49016.jpg"}}},
			{"id":"51000","attributes":{"canonicalTitle":"Enen no Shouboutai: Yon no Shou","titles":{"en":"Fire Force Season 4"},"posterImage":{"medium":"https://kitsu/51000.jpg"}}}]}`)
	}))
	t.Cleanup(ts.Close)
	c := kitsu.NewClient(ts.Client())
	c.BaseURL = ts.URL
	return c
}

func searchPreviewIDs(metas []MetaPreview) []string {
	ids := make([]string, len(metas))
	for i, preview := range metas {
		ids[i] = preview.ID
	}
	return ids
}

// The reported bug: searching for an anime returned it twice, once from the
// series carrier and once from the anime carrier, because the two id spaces
// never collide — "tt9307686" and "kitsu:49016" are the same show. The anime
// priority list is what ranks them, and it does not name TMDB at all, so the
// series carrier's copy is the one that goes.
func TestSeriesSearchDropsWhatTheAnimeCarrierAlreadyServes(t *testing.T) {
	logger.Init("ERROR")
	tmdbClient, _ := searchStubTMDB(t)
	srv := &Server{tmdbClient: tmdbClient, kitsuClient: searchStubKitsu(t), animeLists: animeListsStore(t)}

	// TMDB leads series here, so the carrier under test is TMDB's; the anime
	// carrier is Kitsu by default.
	profile := &config.MetadataProfileConfig{SeriesSources: []string{"tmdb"}}
	def, ok := searchCatalogDefByID(profile, "tmdb.search.series")
	if !ok {
		t.Fatal("tmdb.search.series missing from the search carriers")
	}
	req := catalogRequest{Type: "series", ID: def.ID, Search: "fire force", Profile: profile}

	got := searchPreviewIDs(srv.serveCatalog(context.Background(), def, req))
	want := []string{"tt5550000"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("series search returned %v, want only the title no anime source carries (%v)", got, want)
	}
}

// The winning side must not pay for the losing side's query, and must keep
// every entry it has: Kitsu's per-cour split is the shape the profile asked
// for by ranking Kitsu first, not a duplicate to collapse.
func TestAnimeSearchKeepsEveryCourAndNeverFetchesTheSeriesCarrier(t *testing.T) {
	logger.Init("ERROR")
	tmdbClient, tmdbCalls := searchStubTMDB(t)
	srv := &Server{tmdbClient: tmdbClient, kitsuClient: searchStubKitsu(t), animeLists: animeListsStore(t)}

	profile := &config.MetadataProfileConfig{}
	def, ok := searchCatalogDefByID(profile, "kitsu.search.anime")
	if !ok {
		t.Fatal("kitsu.search.anime missing from the search carriers")
	}
	req := catalogRequest{Type: "anime", ID: def.ID, Search: "fire force", Profile: profile}

	got := searchPreviewIDs(srv.serveCatalog(context.Background(), def, req))
	if len(got) != 2 || got[0] != "kitsu:49016" || got[1] != "kitsu:51000" {
		t.Fatalf("anime search returned %v, want both cours kept", got)
	}
	if calls := atomic.LoadInt64(tmdbCalls); calls != 0 {
		t.Fatalf("anime search made %d TMDB calls, want none: it outranks the series carrier, so it has nothing to de-duplicate against", calls)
	}
}

// Which carrier wins is the profile's metadata priority for the media type,
// not a hardcoded provider.
func TestSearchCarrierOutranksFollowsTheAnimePriorityList(t *testing.T) {
	series := searchCarrier("series", "tmdb")
	tvdbSeries := searchCarrier("series", "tvdb")
	kitsuAnime := searchCarrier("anime", "kitsu")
	tvdbAnime := searchCarrier("anime", "tvdb")

	for _, tc := range []struct {
		name    string
		profile *config.MetadataProfileConfig
		def     CatalogDef
		current CatalogDef
		want    bool
	}{
		{"kitsu first beats the series carrier", &config.MetadataProfileConfig{AnimeSources: []string{"kitsu", "tvdb"}}, kitsuAnime, series, true},
		{"tvdb first beats the series carrier", &config.MetadataProfileConfig{AnimeSources: []string{"tvdb", "kitsu"}}, tvdbAnime, series, true},
		{"the series carrier never beats an anime source", &config.MetadataProfileConfig{AnimeSources: []string{"kitsu", "tvdb"}}, series, kitsuAnime, false},
		{"carriers of the same type never collide", &config.MetadataProfileConfig{}, kitsuAnime, tvdbAnime, false},
		// One source leading both anime and series ranks equally against
		// itself; without a tie-break the same show came back twice, once per
		// carrier.
		{"one source carrying both types gives anime to the anime carrier", &config.MetadataProfileConfig{AnimeSources: []string{"tvdb", "kitsu"}}, tvdbAnime, tvdbSeries, true},
		{"...and never the other way round", &config.MetadataProfileConfig{AnimeSources: []string{"tvdb", "kitsu"}}, tvdbSeries, tvdbAnime, false},
	} {
		if got := searchCarrierOutranks(tc.profile, tc.def, tc.current); got != tc.want {
			t.Errorf("%s: searchCarrierOutranks = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// tvdbSearchStub answers with the shape TVDB's search really returns for an
// anime: the record's own name is its original language, and the readable
// name is in translations.
func tvdbSearchStub(t *testing.T) *tvdb.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			fmt.Fprint(w, `{"status":"success","data":{"token":"test-token"}}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":[{"tvdb_id":"355480","name":"炎炎ノ消防隊",
			"translations":{"eng":"Fire Force","jpn":"炎炎ノ消防隊","fin":"Tulivoimat"},"image_url":"https://artworks.thetvdb.com/x.jpg"}]}`)
	}))
	t.Cleanup(ts.Close)
	client := tvdb.NewClient("test-key", t.TempDir())
	client.BaseURL = ts.URL
	return client
}

// A TVDB-led anime search rendered the show's original-language name, which
// is what TVDB's default record carries — the row looked like a different
// series than the one the user searched for.
func TestTVDBAnimeSearchRendersTheDisplayLanguageTitle(t *testing.T) {
	logger.Init("ERROR")
	srv := &Server{tvdbClient: tvdbSearchStub(t), kitsuClient: searchStubKitsu(t), animeLists: animeListsStore(t)}
	def := CatalogDef{ID: "tvdb.search.anime", Type: "anime", Provider: "tvdb", Kind: "search", SupportsSearch: true}

	for _, tc := range []struct{ language, want string }{
		{"", "Fire Force"},
		{"en-US", "Fire Force"},
		{"fi-FI", "Tulivoimat"},
	} {
		profile := &config.MetadataProfileConfig{Language: tc.language, AnimeSources: []string{"tvdb", "kitsu"}}
		metas, err := srv.tvdbAnimeSearchCatalog(context.Background(), def, catalogRequest{Type: "anime", ID: def.ID, Search: "fire force", Profile: profile})
		if err != nil {
			t.Fatalf("language %q: %v", tc.language, err)
		}
		if len(metas) != 1 || metas[0].Name != tc.want {
			t.Fatalf("language %q returned %v, want the single row named %q", tc.language, searchPreviewIDs(metas), tc.want)
		}
	}
}

// The browse rows have always been localized; search asked TMDB for nothing,
// so a profile in another language searched in English and got English names
// and overviews back.
func TestTMDBSearchRequestsTheProfileLanguage(t *testing.T) {
	logger.Init("ERROR")
	var languages []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/tv" {
			languages = append(languages, r.URL.Query().Get("language"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"page":1,"total_pages":1,"total_results":0,"results":[]}`)
	}))
	t.Cleanup(ts.Close)
	client := tmdb.NewClient("test-key")
	client.BaseURL = ts.URL
	srv := &Server{tmdbClient: client}

	def := searchCarrier("series", "tmdb")
	for _, tc := range []struct{ language, want string }{
		{"fi-FI", "fi-FI"},
		// English resolves to no parameter at all, keeping the default
		// request — and its cache keys — exactly as they were.
		{"en-US", ""},
		{"", ""},
	} {
		profile := &config.MetadataProfileConfig{Language: tc.language, SeriesSources: []string{"tmdb"}}
		if _, err := srv.tmdbCatalog(context.Background(), def, catalogRequest{Type: "series", ID: def.ID, Search: "solo leveling", Profile: profile}); err != nil {
			t.Fatalf("language %q: %v", tc.language, err)
		}
	}
	// Two requests, not three: an English profile and an unset one produce the
	// byte-identical default request, so the second is answered from the
	// client's response cache.
	want := []string{"fi-FI", ""}
	if len(languages) != len(want) {
		t.Fatalf("search made %d upstream requests (%v), want %d", len(languages), languages, len(want))
	}
	for i, lang := range languages {
		if lang != want[i] {
			t.Fatalf("search %d asked TMDB for language %q, want %q", i, lang, want[i])
		}
	}
}
