package stremio

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/animelists"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
)

// Real anime-lists rows for the entries in the reported bug: each is a season
// or cour of a longer series, which is exactly what Kitsu ids alone cannot say.
const animeListsFixture = `[
	{
		"type": "TV",
		"kitsu_id": 49016,
		"imdb_id": ["tt9307686"],
		"tvdb_id": 355480,
		"themoviedb_id": {"tv": 88046},
		"season": {"tvdb": 3, "tmdb": 3},
		"episode_offset": {"tvdb": 12, "tmdb": 12}
	},
	{
		"type": "TV",
		"kitsu_id": 50154,
		"imdb_id": ["tt32991344"],
		"tvdb_id": 451793,
		"themoviedb_id": {"tv": 258348},
		"season": {"tvdb": 2, "tmdb": 2}
	},
	{
		"type": "TV",
		"kitsu_id": 486,
		"imdb_id": ["tt0168366"],
		"tvdb_id": 76703,
		"themoviedb_id": {"tv": 60572}
	},
	{
		"type": "Movie",
		"kitsu_id": 176,
		"imdb_id": ["tt0245429"],
		"themoviedb_id": {"movie": [129]}
	}
]`

// kitsuStub answers with the per-cour titles Kitsu really returns, and with the
// AniList/MAL-only mappings that make its ids useless for searching.
func kitsuStub(t *testing.T) *kitsu.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"id": "49016",
				"type": "anime",
				"attributes": {
					"canonicalTitle": "Enen no Shouboutai: San no Shou Part 2",
					"titles": {"en": "Fire Force Season 3 Part 2", "en_jp": "Enen no Shouboutai: San no Shou Part 2"},
					"abbreviatedTitles": [],
					"startDate": "2026-01-09",
					"showType": "TV"
				}
			},
			"included": [
				{"type": "mappings", "attributes": {"externalSite": "anilist/anime", "externalId": "179062"}},
				{"type": "mappings", "attributes": {"externalSite": "myanimelist/anime", "externalId": "59229"}}
			]
		}`))
	}))
	t.Cleanup(ts.Close)
	c := kitsu.NewClient(ts.Client())
	c.BaseURL = ts.URL
	return c
}

// animeListsStore imports the fixture into the test database. GetManager is a
// process-wide singleton, so every test in this package shares one database and
// each import replaces what the last one left.
func animeListsStore(t *testing.T) *animelists.Store {
	t.Helper()
	store := animelists.NewStore(mappingStoreForTest(t))
	if err := store.Load(strings.NewReader(animeListsFixture)); err != nil {
		t.Fatalf("load anime lists: %v", err)
	}
	return store
}

// emptyAnimeLists stands in for a list that has not loaded yet, or an entry too
// new to appear in it.
func emptyAnimeLists(t *testing.T) *animelists.Store {
	t.Helper()
	return animelists.NewStore(mappingStoreForTest(t))
}

// testMappings is the package's database. persistence.GetManager is a
// process-wide singleton and stays open for the whole run, so the suite owns
// one directory here rather than leaving an open database in a t.TempDir the
// cleanup cannot remove.
var testMappings *persistence.AnimeMappingStore

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stremio")
	if err != nil {
		panic(err)
	}
	mgr, err := persistence.GetManager(dir)
	if err != nil {
		panic(err)
	}
	testMappings = mgr.AnimeMappingStore()

	code := m.Run()

	mgr.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

func mappingStoreForTest(t *testing.T) *persistence.AnimeMappingStore {
	t.Helper()
	if err := testMappings.Replace(nil, time.Time{}); err != nil {
		t.Fatalf("reset mappings: %v", err)
	}
	return testMappings
}

// kitsu:49016:3 is "Fire Force Season 3 Part 2" episode 3, which releases name
// S03E15. Before the mapping this searched "…Part 2 S01E03" with no external
// id at all, so it matched nothing.
func TestKitsuCourRequestResolvesToAiredSeasonEpisode(t *testing.T) {
	srv := &Server{kitsuClient: kitsuStub(t), animeLists: animeListsStore(t)}

	params, err := srv.buildSearchParamsBase("series", "kitsu:49016:3", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase: %v", err)
	}

	if params.Req.Season != "3" || params.Req.Episode != "15" {
		t.Fatalf("season/episode = %q/%q, want 3/15", params.Req.Season, params.Req.Episode)
	}
	if params.Req.IMDbID != "tt9307686" || params.Req.TVDBID != "355480" || params.Req.TMDBID != "88046" {
		t.Fatalf("ids = %q/%q/%q", params.Req.IMDbID, params.Req.TVDBID, params.Req.TMDBID)
	}
	if params.Req.KitsuID != "49016" {
		t.Fatalf("kitsu id = %q, want 49016", params.Req.KitsuID)
	}
	// The id must reach the text query too, or id-less indexers stay unusable.
	if params.ImdbForText != "tt9307686" {
		t.Fatalf("imdb for text = %q, want tt9307686", params.ImdbForText)
	}
	// A mapped request is an ordinary series request: Kitsu's per-cour title
	// must not displace the series title the releases are named by.
	if params.Metadata.KitsuDetails != nil {
		t.Fatalf("expected no Kitsu title metadata for a mapped request, got %+v", params.Metadata.KitsuDetails)
	}
}

// A season entry with no cour offset still needs its season applied.
func TestKitsuSeasonRequestWithoutOffset(t *testing.T) {
	srv := &Server{kitsuClient: kitsuStub(t), animeLists: animeListsStore(t)}

	params, err := srv.buildSearchParamsBase("series", "kitsu:50154:3", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase: %v", err)
	}
	if params.Req.Season != "2" || params.Req.Episode != "3" {
		t.Fatalf("season/episode = %q/%q, want 2/3", params.Req.Season, params.Req.Episode)
	}
	if params.Req.IMDbID != "tt32991344" {
		t.Fatalf("imdb id = %q, want tt32991344", params.Req.IMDbID)
	}
}

// An entry covering a whole series numbers episodes absolutely — there is no
// season to map onto, so the number carries through as the absolute episode.
func TestKitsuSeriesSpanningRequestKeepsAbsoluteEpisode(t *testing.T) {
	srv := &Server{kitsuClient: kitsuStub(t), animeLists: animeListsStore(t)}

	params, err := srv.buildSearchParamsBase("series", "kitsu:486:154", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase: %v", err)
	}
	if params.Req.Season != "" {
		t.Fatalf("season = %q, want empty", params.Req.Season)
	}
	if params.Req.AbsoluteEpisode != "154" {
		t.Fatalf("absolute episode = %q, want 154", params.Req.AbsoluteEpisode)
	}
	if params.ContentIDs.AbsoluteEpisode != 154 {
		t.Fatalf("content ids absolute episode = %d, want 154", params.ContentIDs.AbsoluteEpisode)
	}
	if params.Req.TVDBID != "76703" {
		t.Fatalf("tvdb id = %q, want 76703", params.Req.TVDBID)
	}
}

// Without a mapping — an entry too new for the list, or a list that has not
// loaded yet — the Kitsu titles remain the fallback rather than nothing.
func TestKitsuRequestFallsBackWhenUnmapped(t *testing.T) {
	srv := &Server{kitsuClient: kitsuStub(t), animeLists: emptyAnimeLists(t)}

	params, err := srv.buildSearchParamsBase("series", "kitsu:49016:3", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase: %v", err)
	}
	if params.Metadata.KitsuDetails == nil {
		t.Fatal("expected the Kitsu fallback to supply metadata when unmapped")
	}
	if len(params.SeriesTitleQueries) == 0 {
		t.Fatal("expected the Kitsu fallback to supply title queries")
	}
}

// kitsuFilmStub answers as Kitsu does for a film entry: showType "movie" is
// the only signal that distinguishes it from episodic anime.
func kitsuFilmStub(t *testing.T) *kitsu.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": {
				"id": "176",
				"type": "anime",
				"attributes": {
					"canonicalTitle": "Spirited Away",
					"titles": {"en": "Spirited Away", "en_jp": "Sen to Chihiro no Kamikakushi"},
					"abbreviatedTitles": [],
					"startDate": "2001-07-20",
					"showType": "movie"
				}
			},
			"included": [
				{"type": "mappings", "attributes": {"externalSite": "anilist/anime", "externalId": "199"}}
			]
		}`))
	}))
	t.Cleanup(ts.Close)
	c := kitsu.NewClient(ts.Client())
	c.BaseURL = ts.URL
	return c
}

// kitsu:176 is Spirited Away — a film browsed through the "anime" catalogue
// type. Its mapped ids are movie ids; treating the request as series-like read
// TMDB 129 and TVDB 276 in the TV id namespaces, which are unrelated shows
// ("Soccer Aid"). The Kitsu subtype is what says "film", so it is fetched even
// for mapped entries and steers the request onto the movie path.
func TestKitsuFilmRequestTakesMoviePath(t *testing.T) {
	srv := &Server{kitsuClient: kitsuFilmStub(t), animeLists: animeListsStore(t), config: &config.Config{}}

	params, err := srv.buildSearchParamsBase("anime", "kitsu:176", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase: %v", err)
	}
	if params.Req.IMDbID != "tt0245429" || params.Req.TMDBID != "129" {
		t.Fatalf("ids = %q/%q, want tt0245429/129", params.Req.IMDbID, params.Req.TMDBID)
	}
	if params.Req.TVDBID != "" {
		t.Fatalf("tvdb id = %q, want none — a film must not resolve a series id", params.Req.TVDBID)
	}
	if params.Metadata.KitsuDetails == nil || params.Metadata.KitsuDetails.ShowType != "movie" {
		t.Fatalf("kitsu details = %+v, want the film subtype attached despite the mapping", params.Metadata.KitsuDetails)
	}
	if !query.MovieLike(params.Metadata, "anime") {
		t.Fatal("expected the request to classify as movie-like")
	}
	// The validator skips the "anime" type entirely; a film must reach it as
	// a movie or episode-titled series releases pass title validation.
	if got := validationContentType(params, "anime"); got != "movie" {
		t.Fatalf("validation content type = %q, want movie", got)
	}

	plan := config.DefaultMoviePlan("Movie")
	attempt := plan.Attempts[1]
	full, err := srv.buildSearchParamsForAttempt(params, &plan, attempt, srv.searchFacts("anime", "default", params))
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt: %v", err)
	}
	if len(full.PreparedQueries) == 0 || !strings.HasPrefix(full.PreparedQueries[0], "Spirited Away") {
		t.Fatalf("prepared queries = %v, want a Spirited Away movie query", full.PreparedQueries)
	}
	if len(full.MovieTitleQueries) == 0 {
		t.Fatalf("expected movie title queries, got series: %v", full.SeriesTitleQueries)
	}

	profiles := query.ValidationQueryProfilesFromMetadata(full.Metadata, "anime", []string{"en-US"}, false)
	found := false
	for _, prof := range profiles {
		if prof.Query == "Spirited Away" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("validation profiles = %v, want Spirited Away", profiles)
	}
}

// Overlay services key on IMDb ids, so kitsu: rows resolve through the
// series-level mapping; unmapped anime and non-tt fallbacks keep their art.
func TestApplyPosterOverlaysMapsKitsuRows(t *testing.T) {
	srv := &Server{animeLists: animeListsStore(t)}
	profile := &config.MetadataProfileConfig{
		PosterURLPattern: "https://btttr.cc/poster/imdb/poster-default/{imdb_id}.jpg",
	}
	metas := []MetaPreview{
		{ID: "tt0111161", Poster: "https://image.tmdb.org/t/p/w500/orig.jpg"},
		{ID: "kitsu:49016", Poster: "https://kitsu.example/mapped.jpg"},
		{ID: "kitsu:999999", Poster: "https://kitsu.example/unmapped.jpg"},
		{ID: "tmdb:278", Poster: "https://image.tmdb.org/t/p/w500/fallback.jpg"},
	}
	srv.applyPosterOverlays(profile, metas)

	if want := "https://btttr.cc/poster/imdb/poster-default/tt0111161.jpg"; metas[0].Poster != want {
		t.Errorf("tt row poster = %q, want %q", metas[0].Poster, want)
	}
	if want := "https://btttr.cc/poster/imdb/poster-default/tt9307686.jpg"; metas[1].Poster != want {
		t.Errorf("mapped kitsu row poster = %q, want %q", metas[1].Poster, want)
	}
	if want := "https://kitsu.example/unmapped.jpg"; metas[2].Poster != want {
		t.Errorf("unmapped kitsu row poster = %q, want untouched %q", metas[2].Poster, want)
	}
	if want := "https://image.tmdb.org/t/p/w500/fallback.jpg"; metas[3].Poster != want {
		t.Errorf("tmdb fallback row poster = %q, want untouched %q", metas[3].Poster, want)
	}

	// Without a pattern nothing is touched (and no lookups happen at all).
	bare := []MetaPreview{{ID: "tt0111161", Poster: "https://image.tmdb.org/t/p/w500/orig.jpg"}}
	srv.applyPosterOverlays(&config.MetadataProfileConfig{}, bare)
	if want := "https://image.tmdb.org/t/p/w500/orig.jpg"; bare[0].Poster != want {
		t.Errorf("pattern-less profile changed poster to %q", bare[0].Poster)
	}
}

// The mapped request must build the same query as the equivalent tt request —
// that equivalence is the whole point of the mapping.
func TestKitsuMappedRequestBuildsAiredSeasonQuery(t *testing.T) {
	srv := &Server{kitsuClient: kitsuStub(t), animeLists: animeListsStore(t)}

	params, err := srv.buildSearchParamsBase("series", "kitsu:49016:3", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase: %v", err)
	}
	// Stand in for the TMDB lookup this test does not make; the mapping's job
	// is to have put the right season and episode on the request.
	params.Metadata.TVDBDetails = nil
	params.Metadata.KitsuDetails = nil
	params.Metadata.TVDetails = &tmdb.TVDetails{Name: "Fire Force", OriginalLanguage: "ja"}

	queries := query.BuildSeriesQueriesFromMetadata(params.Metadata, "", false,
		params.Req.Season, params.Req.Episode, config.SeriesSearchScopeSeasonEpisode)
	if len(queries) == 0 || queries[0] != "Fire Force S03E15" {
		t.Fatalf("queries = %v, want [Fire Force S03E15]", queries)
	}
}
