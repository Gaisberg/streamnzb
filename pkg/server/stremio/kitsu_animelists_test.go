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
