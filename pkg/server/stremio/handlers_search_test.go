package stremio

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/session"
)

type recordingIndexer struct {
	lastReq indexer.SearchRequest
}

type requestLabelIndexer struct{}

func (r *recordingIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	r.lastReq = req
	return &indexer.SearchResponse{}, nil
}

func (r *recordingIndexer) Name() string               { return "Recording" }
func (r *recordingIndexer) GetUsage() indexer.Usage    { return indexer.Usage{} }
func (r *recordingIndexer) Ping(context.Context) error { return nil }
func (r *recordingIndexer) DownloadNZB(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (r *requestLabelIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	itemFor := func(indexerName string) indexer.Item {
		return indexer.Item{
			Title:         "Zootopia 2 2025",
			GUID:          "https://example.invalid/" + indexerName,
			Comments:      "https://example.invalid/" + indexerName,
			ActualIndexer: indexerName,
		}
	}
	switch req.RequestLabel {
	case "Q1":
		return &indexer.SearchResponse{}, nil
	case "Q2":
		return &indexer.SearchResponse{Channel: indexer.Channel{Items: []indexer.Item{itemFor("IndexerB")}}}, nil
	case "Q3":
		return &indexer.SearchResponse{Channel: indexer.Channel{Items: []indexer.Item{itemFor("IndexerC")}}}, nil
	default:
		return &indexer.SearchResponse{}, nil
	}
}

func (r *requestLabelIndexer) Name() string               { return "RequestLabelIndexer" }
func (r *requestLabelIndexer) GetUsage() indexer.Usage    { return indexer.Usage{} }
func (r *requestLabelIndexer) Ping(context.Context) error { return nil }
func (r *requestLabelIndexer) DownloadNZB(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func TestValidationQueryProfilesFromMetadataSplitMultipleLanguages(t *testing.T) {
	metadata := &query.ResolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "Witch Hat Atelier",
			OriginalName:     "とんがり帽子のアトリエ",
			OriginalLanguage: "ja",
		},
		TVAlternativeTitles: &tmdb.TVAlternativeTitlesResponse{
			Results: []tmdb.AlternativeTitle{
				{ISO3166_1: "JP", Title: "Tongari Boushi no Atelier", Type: "Romaji"},
			},
		},
	}

	got := query.ValidationQueryProfilesFromMetadata(metadata, "series", []string{"en-US", ""}, false)
	want := []indexer.ValidationQueryProfile{
		{Languages: []string{"en-US"}, Query: "Witch Hat Atelier"},
		{Languages: []string{"original"}, Query: "Tongari Boushi no Atelier"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query.ValidationQueryProfilesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestValidationQueryProfilesFromMetadataMergesDuplicateQueriesAcrossLanguages(t *testing.T) {
	metadata := &query.ResolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "Dragon Ball Z",
			OriginalName:     "ドラゴンボールゼット",
			OriginalLanguage: "ja",
		},
		TVTranslations: &tmdb.TVTranslationsResponse{
			Translations: []tmdb.TVTranslationEntry{
				{
					ISO639_1:  "en",
					ISO3166_1: "US",
					Data: tmdb.TVTranslationData{
						Name: "Dragon Ball Z",
					},
				},
			},
		},
	}

	got := query.ValidationQueryProfilesFromMetadata(metadata, "series", []string{"en-US", ""}, false)
	want := []indexer.ValidationQueryProfile{
		{Languages: []string{"en-US", "original"}, Query: "Dragon Ball Z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query.ValidationQueryProfilesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestMetadataLogTitlesHandleMissingJapaneseAlternativeTitles(t *testing.T) {
	metadata := &query.ResolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "Spirited Away",
			OriginalTitle:    "千と千尋の神隠し",
			OriginalLanguage: "ja",
		},
	}

	if got := query.MetadataAlternativeTitle(metadata, "movie"); got != "" {
		t.Fatalf("query.MetadataAlternativeTitle() = %q, want empty", got)
	}

	params := &query.SearchParams{
		Req: indexer.SearchRequest{TMDBID: "129"},
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt0245429",
		},
		Metadata: metadata,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logMetadataLookupFinished() panicked: %v", r)
		}
	}()

	logMetadataLookupFinished("Stream01", "movie", "tt0245429", params)
}

// idAttempt / titleAttempt build the attempts the tests dispatch, and planOf
// wraps them in the smallest plan that can carry them. A plan is the unit of
// configuration now, so the tests say what they mean in those terms.
func idAttempt(target string) config.SearchAttempt {
	return config.SearchAttempt{Address: config.SearchAddressID, Target: target}
}

func titleAttempt(target, language string, year bool) config.SearchAttempt {
	attempt := config.SearchAttempt{Address: config.SearchAddressTitle, Target: target, Title: &language}
	if year {
		attempt.Year = boolPtr(true)
	}
	return attempt
}

func planOf(name string, accept *config.SearchAcceptance, attempts ...config.SearchAttempt) config.SearchQueryConfig {
	return config.SearchQueryConfig{Name: name, Attempts: attempts, Stop: config.SearchStopFirstHit, Accept: accept}
}

func acceptTitles(languages ...string) *config.SearchAcceptance {
	return &config.SearchAcceptance{Titles: languages}
}

// An attempt's address and target are what the request carries to the wire.
func TestBuildSearchParamsForAttemptMapsAddressAndTargetOntoTheRequest(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := func() *query.SearchParams {
		return &query.SearchParams{
			ContentType: "series",
			ID:          "tt1234567:1:4",
			Req: indexer.SearchRequest{
				Season:  "1",
				Episode: "4",
				IMDbID:  "tt1234567",
				Limit:   1000,
			},
			MovieTitleQueries:  make(map[string][]string),
			SeriesTitleQueries: make(map[string][]string),
		}
	}
	plan := planOf("TV", nil, idAttempt(config.SearchTargetEpisode))
	facts := searchFacts{IsSeries: true, HasSeason: true, HasEpisode: true, Class: config.SearchClassTV}

	params, err := srv.buildSearchParamsForAttempt(base(), &plan, idAttempt(config.SearchTargetEpisode), facts)
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}
	if params.Req.SearchMode != "id" {
		t.Fatalf("SearchMode = %q, want id", params.Req.SearchMode)
	}
	if params.Req.SeriesSearchScope != config.SeriesSearchScopeSeasonEpisode {
		t.Fatalf("scope = %q, want %q", params.Req.SeriesSearchScope, config.SeriesSearchScopeSeasonEpisode)
	}
	if params.Req.Query != "S01E04" {
		t.Fatalf("Query = %q, want S01E04", params.Req.Query)
	}
	if params.Req.Class != config.SearchClassTV {
		t.Fatalf("Class = %q, want %q", params.Req.Class, config.SearchClassTV)
	}
	if params.Req.Season != "1" || params.Req.Episode != "4" {
		t.Fatalf("expected season/episode params to be preserved, got season=%q episode=%q", params.Req.Season, params.Req.Episode)
	}

	seasonParams, err := srv.buildSearchParamsForAttempt(base(), &plan, idAttempt(config.SearchTargetSeason), facts)
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}
	if seasonParams.Req.SeriesSearchScope != config.SeriesSearchScopeSeason {
		t.Fatalf("season scope = %q, want %q", seasonParams.Req.SeriesSearchScope, config.SeriesSearchScopeSeason)
	}
	if seasonParams.Req.Query != "S01" {
		t.Fatalf("season Query = %q, want S01", seasonParams.Req.Query)
	}

	// A title attempt with no metadata has nothing to build a query from, and
	// says so by leaving the query empty rather than dispatching a bare title.
	textParams, err := srv.buildSearchParamsForAttempt(base(), &plan, titleAttempt(config.SearchTargetEpisode, "", false), facts)
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}
	if textParams.Req.SearchMode != "text" {
		t.Fatalf("SearchMode = %q, want text", textParams.Req.SearchMode)
	}
	if textParams.Req.Query != "" {
		t.Fatalf("Query = %q, want empty without metadata", textParams.Req.Query)
	}
}

func TestBuildSearchParamsForAttemptUsesTheAttemptLanguageNotPerIndexerOverrides(t *testing.T) {
	srv := &Server{config: &config.Config{
		Indexers: []config.IndexerConfig{
			{Name: "IndexerA", SearchTitleLanguage: "de"},
			{Name: "IndexerB", SearchTitleLanguage: "en"},
			{Name: "Easynews", Type: "easynews", SearchTitleLanguage: "fr"},
		},
	}}
	base := &query.SearchParams{
		ContentType: "movie",
		ID:          "tt0110357",
		Req: indexer.SearchRequest{
			IMDbID: "tt0110357",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "The Lion King",
				OriginalTitle:    "The Lion King",
				OriginalLanguage: "en",
				ReleaseDate:      "1994-06-15",
			},
			MovieTranslations: &tmdb.MovieTranslationsResponse{
				Translations: []tmdb.MovieTranslationEntry{
					{
						ISO639_1:  "de",
						ISO3166_1: "DE",
						Data: tmdb.MovieTranslationData{
							Title: "König der Löwen",
						},
					},
				},
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
	}

	attempt := titleAttempt("", "de-DE", false)
	plan := planOf("Movie", acceptTitles("de-DE"), attempt)
	params, err := srv.buildSearchParamsForAttempt(base, &plan, attempt, searchFacts{Class: config.SearchClassMovie})
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}

	if params.Req.Query != "Koenig der Loewen" {
		t.Fatalf("expected request-level localized query, got %q", params.Req.Query)
	}
	if params.Req.ValidationQuery != "Koenig der Loewen" {
		t.Fatalf("expected validation query to use the normalized localized title, got %q", params.Req.ValidationQuery)
	}
}

func TestBuildSearchParamsBaseNumericIDMapsToTMDBID(t *testing.T) {
	srv := &Server{config: &config.Config{}}

	params, err := srv.buildSearchParamsBase("series", "123456:1:1", nil)
	if err != nil {
		t.Fatalf("buildSearchParamsBase() error = %v", err)
	}

	if params.Req.TMDBID != "123456" {
		t.Fatalf("expected numeric base ID to map to TMDB ID, got %q", params.Req.TMDBID)
	}
	if params.Req.IMDbID != "" {
		t.Fatalf("expected IMDb ID to stay empty for numeric base ID, got %q", params.Req.IMDbID)
	}
	if params.ContentIDs.Season != 1 || params.ContentIDs.Episode != 1 {
		t.Fatalf("expected content IDs to preserve season/episode, got season=%d episode=%d", params.ContentIDs.Season, params.ContentIDs.Episode)
	}
}

func TestBuildSearchParamsForAttemptAlwaysBuildsValidationInputs(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &query.SearchParams{
		ContentType: "movie",
		ID:          "tmdb:123",
		Req: indexer.SearchRequest{
			TMDBID: "123",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "The Lion King",
				OriginalTitle:    "The Lion King",
				OriginalLanguage: "en",
				ReleaseDate:      "1994-06-15",
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
	}

	attempt := idAttempt("")
	plan := planOf("Movie", acceptTitles(""), attempt)
	params, err := srv.buildSearchParamsForAttempt(base, &plan, attempt, searchFacts{Class: config.SearchClassMovie})
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}

	if params.Req.EnableYearValidation {
		t.Fatalf("expected year validation to stay disabled by default for ID searches")
	}
	if params.Req.ValidationQuery != "The Lion King" {
		t.Fatalf("expected validation query to use the metadata title, got %q", params.Req.ValidationQuery)
	}
	if !reflect.DeepEqual(params.Req.ValidationQueries, []string{"The Lion King"}) {
		t.Fatalf("expected validation queries to keep the deduped metadata title, got %#v", params.Req.ValidationQueries)
	}
}

func TestBuildSearchParamsForAttemptBuildsValidationQueriesForEveryAcceptedTitle(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &query.SearchParams{
		ContentType: "movie",
		ID:          "tmdb:123",
		Req: indexer.SearchRequest{
			TMDBID: "123",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "The Lion King",
				OriginalTitle:    "The Lion King",
				OriginalLanguage: "en",
				ReleaseDate:      "1994-06-15",
			},
			MovieTranslations: &tmdb.MovieTranslationsResponse{
				Translations: []tmdb.MovieTranslationEntry{
					{
						ISO639_1:  "de",
						ISO3166_1: "DE",
						Data: tmdb.MovieTranslationData{
							Title: "König der Löwen",
						},
					},
				},
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
	}

	attempt := idAttempt("")
	plan := planOf("Movie", acceptTitles("", "de-DE"), attempt)
	params, err := srv.buildSearchParamsForAttempt(base, &plan, attempt, searchFacts{Class: config.SearchClassMovie})
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}

	if !reflect.DeepEqual(params.Req.ValidationQueries, []string{"The Lion King", "Koenig der Loewen"}) {
		t.Fatalf("ValidationQueries = %#v, want %#v", params.Req.ValidationQueries, []string{"The Lion King", "Koenig der Loewen"})
	}
	// The accepted titles are exactly what the plan listed — no language rides
	// along implicitly, which is the whole point of stating acceptance once.
	if !reflect.DeepEqual(params.Req.ValidationQueryProfiles, []indexer.ValidationQueryProfile{
		{Languages: []string{"original"}, Query: "The Lion King"},
		{Languages: []string{"de-DE"}, Query: "Koenig der Loewen"},
	}) {
		t.Fatalf("ValidationQueryProfiles = %#v", params.Req.ValidationQueryProfiles)
	}
	if params.Req.ValidationQuery != "The Lion King" {
		t.Fatalf("ValidationQuery = %q, want %q", params.Req.ValidationQuery, "The Lion King")
	}
}

func TestBuildSearchParamsForAttemptUsesNormalizedMetadataQueries(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &query.SearchParams{
		ContentType: "series",
		ID:          "tmdb:241609",
		Req: indexer.SearchRequest{
			TMDBID: "241609",
			Cat:    "5000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			TVDetails: &tmdb.TVDetails{
				Name:             "Your Friends & Neighbors",
				OriginalName:     "Your Friends & Neighbors",
				OriginalLanguage: "en",
				FirstAirDate:     "2025-01-01",
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
	}

	attempt := titleAttempt(config.SearchTargetSeries, "original", false)
	plan := planOf("TV", acceptTitles("original"), attempt)
	params, err := srv.buildSearchParamsForAttempt(base, &plan, attempt, searchFacts{IsSeries: true, Class: config.SearchClassTV})
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}

	if params.Req.Query != "Your Friends Neighbors" {
		t.Fatalf("expected normalized fallback series query, got %q", params.Req.Query)
	}
	if params.Req.ValidationQuery != "Your Friends Neighbors" {
		t.Fatalf("expected normalized validation query, got %q", params.Req.ValidationQuery)
	}
}

func TestRunConfiguredSearchRequestsKeepsMetadataValidationQueryForTextSearch(t *testing.T) {
	rec := &recordingIndexer{}
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				func() config.SearchQueryConfig {
					plan := planOf("MovieQuery03", acceptTitles("de-DE"), titleAttempt("", "de-DE", true))
					plan.Accept.Year = boolPtr(true)
					return plan
				}(),
			},
		},
		indexer: rec,
	}

	params := &query.SearchParams{
		ContentType: "movie",
		ID:          "tmdb:1084242",
		Req: indexer.SearchRequest{
			TMDBID: "1084242",
			IMDbID: "tt26443597",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "Zootopia 2",
				OriginalTitle:    "Zootopia 2",
				OriginalLanguage: "en",
				ReleaseDate:      "2025-11-26",
			},
			MovieTranslations: &tmdb.MovieTranslationsResponse{
				Translations: []tmdb.MovieTranslationEntry{
					{
						ISO639_1:  "de",
						ISO3166_1: "DE",
						Data: tmdb.MovieTranslationData{
							Title: "Zoomania 2",
						},
					},
				},
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt26443597",
			TmdbID: "1084242",
		},
	}

	_, executed, err := srv.runConfiguredSearchRequests(context.Background(), "movie", "tmdb:1084242", "Stream01", nil, []string{"MovieQuery03"}, params)
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if executed != 1 {
		t.Fatalf("executedRequests = %d, want 1", executed)
	}
	if rec.lastReq.Query != "Zoomania 2 2025" {
		t.Fatalf("Query = %q, want %q", rec.lastReq.Query, "Zoomania 2 2025")
	}
	if rec.lastReq.ValidationQuery != "Zoomania 2 2025" {
		t.Fatalf("ValidationQuery = %q, want %q", rec.lastReq.ValidationQuery, "Zoomania 2 2025")
	}
	if !rec.lastReq.EnableYearValidation {
		t.Fatal("expected year validation to stay enabled")
	}
}

func TestHasResolvedIdentifiers(t *testing.T) {
	if hasResolvedIdentifiers(indexer.SearchRequest{}) {
		t.Fatal("expected empty request to report no resolved identifiers")
	}
	if !hasResolvedIdentifiers(indexer.SearchRequest{IMDbID: "tt1234567"}) {
		t.Fatal("expected IMDb ID to count as resolved identifier")
	}
	if !hasResolvedIdentifiers(indexer.SearchRequest{TMDBID: "123"}) {
		t.Fatal("expected TMDB ID to count as resolved identifier")
	}
	if !hasResolvedIdentifiers(indexer.SearchRequest{TVDBID: "456"}) {
		t.Fatal("expected TVDB ID to count as resolved identifier")
	}
}

func TestHasUsableIDSearchIdentifier(t *testing.T) {
	if !hasUsableIDSearchIdentifier(indexer.SearchRequest{TMDBID: "123"}, "movie") {
		t.Fatal("expected movie ID search to accept TMDB ID")
	}
	if !hasUsableIDSearchIdentifier(indexer.SearchRequest{IMDbID: "tt1234567"}, "movie") {
		t.Fatal("expected movie ID search to accept IMDb ID")
	}
	if !hasUsableIDSearchIdentifier(indexer.SearchRequest{IMDbID: "tt1234567"}, "series") {
		t.Fatal("expected series ID search to accept IMDb ID")
	}
	if !hasUsableIDSearchIdentifier(indexer.SearchRequest{TMDBID: "123"}, "series") {
		t.Fatal("expected series ID search to accept TMDB ID")
	}
	if !hasUsableIDSearchIdentifier(indexer.SearchRequest{TVDBID: "456"}, "series") {
		t.Fatal("expected series ID search to accept TVDB ID")
	}
}

func TestBuildSearchParamsBaseParsesTVDBPrefixedSeriesID(t *testing.T) {
	srv := &Server{}
	params, err := srv.buildSearchParamsBase("series", "tvdb:462053:1:1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params.Req.TVDBID != "462053" {
		t.Fatalf("expected TVDBID '462053', got '%s'", params.Req.TVDBID)
	}
	if params.Req.Season != "1" || params.Req.Episode != "1" {
		t.Fatalf("expected Season '1' and Episode '1', got Season '%s' Episode '%s'", params.Req.Season, params.Req.Episode)
	}
}

func TestHasPreparedTextQueries(t *testing.T) {
	if hasPreparedTextQueries(indexer.SearchRequest{}) {
		t.Fatal("expected empty request to report no prepared text queries")
	}
	if !hasPreparedTextQueries(indexer.SearchRequest{Query: "Invincible"}) {
		t.Fatal("expected explicit query to count as prepared text query")
	}
	if hasPreparedTextQueries(indexer.SearchRequest{ValidationQuery: "Invincible S01E04"}) {
		t.Fatal("expected validation query alone not to count as prepared text query")
	}
}

func TestHasUsableResolvedMetadata(t *testing.T) {
	if query.HasUsableResolvedMetadata(nil, "series") {
		t.Fatal("expected nil params not to have usable resolved metadata")
	}
	if query.HasUsableResolvedMetadata(&query.SearchParams{}, "series") {
		t.Fatal("expected empty params not to have usable resolved metadata")
	}
	if query.HasUsableResolvedMetadata(&query.SearchParams{Req: indexer.SearchRequest{IMDbID: "tt1234567"}}, "series") {
		t.Fatal("expected bare identifiers alone not to count as usable series metadata")
	}
	if !query.HasUsableResolvedMetadata(&query.SearchParams{
		Metadata: &query.ResolvedSearchMetadata{
			TVDetails: &tmdb.TVDetails{Name: "Invincible"},
		},
	}, "series") {
		t.Fatal("expected resolved title to count as usable metadata")
	}
}

func TestBuildRawSearchResultShortCircuitsWhenMetadataCannotBeResolved(t *testing.T) {
	srv := &Server{
		config: &config.Config{
			SeriesSearchQueries: []config.SearchQueryConfig{
				config.DefaultTVPlan("TVQuery01"),
			},
		},
	}
	stream := &auth.Stream{
		Username:            "Stream04",
		SeriesSearchQueries: []string{"TVQuery01"},
	}

	raw, err := srv.buildRawSearchResult(t.Context(), "series", "stremevent_866", stream)
	if err != nil {
		t.Fatalf("buildRawSearchResult() error = %v", err)
	}
	if raw == nil {
		t.Fatal("expected zero-result raw search result, got nil")
	}
	if len(raw.IndexerReleases) != 0 {
		t.Fatalf("expected no releases after metadata short-circuit, got indexer=%d", len(raw.IndexerReleases))
	}
}

func TestRunConfiguredSearchRequestsMergesResultsFromEveryPlan(t *testing.T) {
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				planOf("Q1", acceptTitles("original"), titleAttempt("", "original", false)),
				planOf("Q2", acceptTitles("original"), titleAttempt("", "original", false)),
				planOf("Q3", acceptTitles("original"), titleAttempt("", "original", false)),
			},
		},
		indexer:           &requestLabelIndexer{},
		uniqueIndexerHits: make(map[string]int64),
	}
	params := &query.SearchParams{
		ContentType: "movie",
		ID:          "tmdb:1084242",
		Req: indexer.SearchRequest{
			TMDBID: "1084242",
			IMDbID: "tt26443597",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "Zootopia 2",
				OriginalTitle:    "Zootopia 2",
				OriginalLanguage: "en",
				ReleaseDate:      "2025-11-26",
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt26443597",
			TmdbID: "1084242",
		},
	}
	releases, executed, err := srv.runConfiguredSearchRequests(context.Background(), "movie", "tt123", "stream-01", nil, []string{"Q1", "Q2", "Q3"}, params)
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if executed != 3 {
		t.Fatalf("executedRequests = %d, want 3", executed)
	}
	if len(releases) != 2 {
		t.Fatalf("releases len = %d, want 2", len(releases))
	}
}

// Every selected plan runs — there is no stream-level fallback chain any more.
func TestRunConfiguredSearchRequestsRunsEveryPlan(t *testing.T) {
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				planOf("Q1", acceptTitles("original"), titleAttempt("", "original", false)),
				planOf("Q2", acceptTitles("original"), titleAttempt("", "original", false)),
			},
		},
		indexer:           &requestLabelIndexer{},
		uniqueIndexerHits: make(map[string]int64),
	}
	params := &query.SearchParams{
		ContentType: "movie",
		ID:          "tmdb:1084242",
		Req: indexer.SearchRequest{
			TMDBID: "1084242",
			IMDbID: "tt26443597",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "Zootopia 2",
				OriginalTitle:    "Zootopia 2",
				OriginalLanguage: "en",
				ReleaseDate:      "2025-11-26",
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt26443597",
			TmdbID: "1084242",
		},
	}
	releases, executed, err := srv.runConfiguredSearchRequests(context.Background(), "movie", "tt123", "stream-01", nil, []string{"Q1", "Q2"}, params)
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if executed != 2 {
		t.Fatalf("executedRequests = %d, want 2", executed)
	}
	if len(releases) != 1 {
		t.Fatalf("releases len = %d, want 1", len(releases))
	}
}

// A unique hit is per merged release, not per search: an indexer earns one for
// every deduplicated release no other indexer had a copy of, so a busy
// multi-indexer search credits several indexers at once.
func TestMarkUniqueIndexerHits(t *testing.T) {
	onlyA := &release.Release{Indexer: "IndexerA"}
	shared := &release.Release{
		Indexer:  "IndexerA",
		Variants: []*release.Release{{Indexer: "IndexerB"}},
	}
	onlyB := &release.Release{
		Indexer:  "IndexerB",
		Variants: []*release.Release{{Indexer: "indexerb"}},
	}
	// A library copy is the same content back from disk, not a rival indexer.
	cached := &release.Release{
		Indexer:  "IndexerC",
		Variants: []*release.Release{{Indexer: "StreamNZB Library - IndexerC", IsLibrary: true}},
	}
	unattributed := &release.Release{Indexer: "  "}

	hits := markUniqueIndexerHits([]*release.Release{onlyA, shared, onlyB, cached, unattributed})
	want := map[string]int{"IndexerA": 1, "IndexerB": 1, "IndexerC": 1}
	if len(hits) != len(want) {
		t.Fatalf("markUniqueIndexerHits() = %v, want %v", hits, want)
	}
	for name, n := range want {
		if hits[name] != n {
			t.Fatalf("markUniqueIndexerHits()[%q] = %d, want %d (got %v)", name, hits[name], n, hits)
		}
	}

	// Every copy is stamped, so playback still knows the release was exclusive
	// after failover moved the slot onto a variant.
	for _, rel := range []*release.Release{onlyA, onlyB, cached} {
		for i, c := range rel.Copies() {
			if !c.UniqueHit {
				t.Fatalf("copy %d of %q: UniqueHit = false, want true", i, rel.Indexer)
			}
		}
	}
	for i, c := range shared.Copies() {
		if c.UniqueHit {
			t.Fatalf("copy %d of a two-indexer release: UniqueHit = true, want false", i)
		}
	}
	if unattributed.UniqueHit {
		t.Fatal("a release naming no indexer must not be marked unique")
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func TestBuildStreamDescriptionIncludesJhinScore(t *testing.T) {
	descStandalone := buildAIOStreamDescription("The Movie", "The.Movie.2024.1080p", "altHUB", 1450, true)
	if !reflect.DeepEqual(descStandalone, "The Movie\nThe.Movie.2024.1080p\n🔍 altHUB • 🎯 Score: +1450") {
		t.Errorf("standalone desc = %q, want score included", descStandalone)
	}

	descAIO := buildAIOStreamDescription("The Movie", "The.Movie.2024.1080p", "altHUB", 1450, false)
	if !reflect.DeepEqual(descAIO, "The Movie\nThe.Movie.2024.1080p\n🔍 altHUB") {
		t.Errorf("AIO desc = %q, want no score included", descAIO)
	}
}

// delayedLabelIndexer answers per request label and can stall a chosen label,
// so a test can make completion order differ from configured order.
type delayedLabelIndexer struct {
	slowLabel string
	slowAll   bool
	delay     time.Duration
}

func (d *delayedLabelIndexer) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if d.slowAll || req.RequestLabel == d.slowLabel {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &indexer.SearchResponse{Channel: indexer.Channel{Items: []indexer.Item{{
		Title:         "Zootopia 2 2025",
		GUID:          "https://example.invalid/" + req.RequestLabel,
		Comments:      "https://example.invalid/" + req.RequestLabel,
		ActualIndexer: req.RequestLabel,
	}}}}, nil
}

func (d *delayedLabelIndexer) Name() string               { return "Delayed" }
func (d *delayedLabelIndexer) GetUsage() indexer.Usage    { return indexer.Usage{} }
func (d *delayedLabelIndexer) Ping(context.Context) error { return nil }
func (d *delayedLabelIndexer) DownloadNZB(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func combineModeSearchParams() *query.SearchParams {
	return &query.SearchParams{
		ContentType: "movie",
		ID:          "tmdb:1084242",
		Req: indexer.SearchRequest{
			TMDBID: "1084242",
			IMDbID: "tt26443597",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &query.ResolvedSearchMetadata{
			MovieDetails: &tmdb.MovieDetails{
				Title:            "Zootopia 2",
				OriginalTitle:    "Zootopia 2",
				OriginalLanguage: "en",
				ReleaseDate:      "2025-11-26",
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		ContentIDs: &session.AvailReportMeta{
			ImdbID: "tt26443597",
			TmdbID: "1084242",
		},
	}
}

// The selected plans run concurrently, so results must be
// merged by configured order rather than by whichever finished first.
func TestRunConfiguredSearchRequestsCombineKeepsConfiguredOrder(t *testing.T) {
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				planOf("Q1", acceptTitles("original"), titleAttempt("", "original", false)),
				planOf("Q2", acceptTitles("original"), titleAttempt("", "original", false)),
			},
		},
		// Q1 is the slow one; without ordering it would land second.
		indexer:           &delayedLabelIndexer{slowLabel: "Q1", delay: 120 * time.Millisecond},
		uniqueIndexerHits: make(map[string]int64),
	}

	releases, executed, err := srv.runConfiguredSearchRequests(
		context.Background(), "movie", "tt123", "stream-01", nil, []string{"Q1", "Q2"}, combineModeSearchParams())
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if executed != 2 {
		t.Fatalf("executedRequests = %d, want 2", executed)
	}
	if len(releases) != 2 {
		t.Fatalf("releases len = %d, want 2", len(releases))
	}
	if releases[0].Indexer != "Q1" || releases[1].Indexer != "Q2" {
		t.Fatalf("expected configured order Q1,Q2; got %s,%s", releases[0].Indexer, releases[1].Indexer)
	}
}

// The two requests should overlap, so the whole call costs about one delay
// rather than the sum of both.
func TestRunConfiguredSearchRequestsCombineRunsConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				planOf("Q1", acceptTitles("original"), titleAttempt("", "original", false)),
				planOf("Q2", acceptTitles("original"), titleAttempt("", "original", false)),
			},
		},
		indexer:           &delayedLabelIndexer{slowAll: true, delay: delay},
		uniqueIndexerHits: make(map[string]int64),
	}

	start := time.Now()
	_, _, err := srv.runConfiguredSearchRequests(
		context.Background(), "movie", "tt123", "stream-01", nil, []string{"Q1", "Q2"}, combineModeSearchParams())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if elapsed >= 2*delay {
		t.Fatalf("requests look sequential: took %v for two %v requests", elapsed, delay)
	}
}

// attemptIndexer records the label of every attempt it is asked for, and
// answers only the one it is told to hit. The label is the plan read back at
// dispatch, so a test asserts on the plan it configured.
type attemptIndexer struct {
	mu       sync.Mutex
	attempts []string
	queries  []string
	hit      string
	// hits lists, per attempt label, the release groups the attempt answers
	// with; one item each, and the same group is the same release wherever
	// it is listed. When set it replaces the single hit.
	hits map[string][]string
}

func (s *attemptIndexer) Search(_ context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	s.mu.Lock()
	s.attempts = append(s.attempts, req.AttemptLabel)
	s.queries = append(s.queries, req.Query)
	s.mu.Unlock()
	if s.hits != nil {
		items := make([]indexer.Item, 0, len(s.hits[req.AttemptLabel]))
		for _, group := range s.hits[req.AttemptLabel] {
			items = append(items, indexer.Item{
				Title:         "Show Name S03E07 1080p WEB-DL x264-" + group,
				GUID:          "https://example.invalid/" + group,
				Comments:      "https://example.invalid/" + group,
				ActualIndexer: "AttemptIndexer",
			})
		}
		return &indexer.SearchResponse{Channel: indexer.Channel{Items: items}}, nil
	}
	if req.AttemptLabel != s.hit {
		return &indexer.SearchResponse{}, nil
	}
	title := "Show Name S03E07 1080p WEB-DL x264-GRP"
	if req.SeriesSearchScope == config.SeriesSearchScopeSeason {
		title = "Show Name S03 1080p WEB-DL x264-GRP"
	}
	return &indexer.SearchResponse{Channel: indexer.Channel{Items: []indexer.Item{{
		Title:         title,
		GUID:          "https://example.invalid/" + req.AttemptLabel,
		Comments:      "https://example.invalid/" + req.AttemptLabel,
		ActualIndexer: "AttemptIndexer",
	}}}}, nil
}

func (s *attemptIndexer) Name() string               { return "AttemptIndexer" }
func (s *attemptIndexer) GetUsage() indexer.Usage    { return indexer.Usage{} }
func (s *attemptIndexer) Ping(context.Context) error { return nil }
func (s *attemptIndexer) DownloadNZB(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (s *attemptIndexer) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.attempts...), append([]string(nil), s.queries...)
}

func seriesSearchParams() *query.SearchParams {
	return &query.SearchParams{
		ContentType: "series",
		ID:          "tt1234567:3:7",
		Req: indexer.SearchRequest{
			TMDBID:  "555",
			IMDbID:  "tt1234567",
			Limit:   1000,
			Season:  "3",
			Episode: "7",
		},
		Metadata: &query.ResolvedSearchMetadata{
			TVDetails: &tmdb.TVDetails{
				Name:             "Show Name",
				OriginalName:     "Show Name",
				OriginalLanguage: "en",
			},
		},
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		ContentIDs: &session.AvailReportMeta{
			ImdbID:  "tt1234567",
			TmdbID:  "555",
			Season:  3,
			Episode: 7,
		},
	}
}

func planServer(idx indexer.Indexer, plan config.SearchQueryConfig) *Server {
	return &Server{
		config:            &config.Config{SeriesSearchQueries: []config.SearchQueryConfig{plan}},
		indexer:           idx,
		uniqueIndexerHits: make(map[string]int64),
	}
}

func runSeriesPlan(t *testing.T, idx *attemptIndexer, plan config.SearchQueryConfig) ([]*release.Release, int, []string) {
	t.Helper()
	srv := planServer(idx, plan)
	releases, executed, err := srv.runConfiguredSearchRequests(
		context.Background(), "series", "tt1234567:3:7", "stream-01", nil, []string{plan.Name}, seriesSearchParams())
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	attempts, _ := idx.snapshot()
	return releases, executed, attempts
}

// The plan is walked in order and stops at the first attempt that matched: the
// broader question is only paid for when the narrower one came back empty.
func TestRunConfiguredSearchRequestsWalksThePlanUntilSomethingMatches(t *testing.T) {
	plan := config.DefaultTVPlan("TVPlan")
	plan.Order = config.SearchOrderAsListed

	idx := &attemptIndexer{hit: "title·season"}
	releases, executed, attempts := runSeriesPlan(t, idx, plan)
	want := []string{"id·episode", "title·episode", "id·season", "title·season"}
	if !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	if executed != 4 {
		t.Fatalf("executedRequests = %d, want 4", executed)
	}
	if len(releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(releases))
	}
}

func TestRunConfiguredSearchRequestsStopsAtTheFirstAttemptThatMatches(t *testing.T) {
	plan := config.DefaultTVPlan("TVPlan")
	plan.Order = config.SearchOrderAsListed

	idx := &attemptIndexer{hit: "id·episode"}
	releases, executed, attempts := runSeriesPlan(t, idx, plan)
	if !reflect.DeepEqual(attempts, []string{"id·episode"}) {
		t.Fatalf("attempts = %v, want only the first", attempts)
	}
	if executed != 1 {
		t.Fatalf("executedRequests = %d, want 1", executed)
	}
	if len(releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(releases))
	}
}

// stop=all runs every attempt and merges what they found, which is the plan's
// own breadth setting — not the stream's.
func TestRunConfiguredSearchRequestsRunsEveryAttemptWhenThePlanSaysAll(t *testing.T) {
	plan := planOf("TVPlan", acceptTitles("original"),
		idAttempt(config.SearchTargetEpisode),
		titleAttempt(config.SearchTargetEpisode, "original", false),
	)
	plan.Stop = config.SearchStopAll

	idx := &attemptIndexer{hit: "id·episode"}
	_, executed, attempts := runSeriesPlan(t, idx, plan)
	if !reflect.DeepEqual(attempts, []string{"id·episode", "title·episode"}) {
		t.Fatalf("attempts = %v, want both", attempts)
	}
	if executed != 2 {
		t.Fatalf("executedRequests = %d, want 2", executed)
	}
}

// stop=enough_hits keeps walking while the attempts so far have found fewer
// distinct releases than the threshold between them, and counts the same
// release listed twice as one: three attempts answer 2 + 1 (a repeat) + 2, and
// a threshold of four is only met after the third.
func TestRunConfiguredSearchRequestsWalksThePlanUntilEnoughDistinctHits(t *testing.T) {
	plan := planOf("TVPlan", acceptTitles("original"),
		idAttempt(config.SearchTargetEpisode),
		titleAttempt(config.SearchTargetEpisode, "original", false),
		idAttempt(config.SearchTargetSeason),
		titleAttempt(config.SearchTargetSeason, "original", false),
	)
	plan.Stop = config.SearchStopEnoughHits
	plan.MinHits = 4

	idx := &attemptIndexer{hits: map[string][]string{
		"id·episode":    {"A", "B"},
		"title·episode": {"A"},
		"id·season":     {"C", "D"},
		"title·season":  {"E"},
	}}
	releases, executed, attempts := runSeriesPlan(t, idx, plan)
	want := []string{"id·episode", "title·episode", "id·season"}
	if !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempts = %v, want %v", attempts, want)
	}
	if executed != 3 {
		t.Fatalf("executedRequests = %d, want 3", executed)
	}
	// Everything found on the way is kept; merging the repeat is the caller's
	// job, as it is for stop=all.
	if len(releases) != 5 {
		t.Fatalf("releases = %d, want 5 listings from the three attempts", len(releases))
	}

	// A first attempt that clears the threshold on its own costs one request.
	idx = &attemptIndexer{hits: map[string][]string{"id·episode": {"A", "B", "C", "D"}}}
	_, executed, attempts = runSeriesPlan(t, idx, plan)
	if !reflect.DeepEqual(attempts, []string{"id·episode"}) || executed != 1 {
		t.Fatalf("attempts = %v (%d executed), want only the first", attempts, executed)
	}

	// A plan that never gets there runs out of attempts rather than results.
	idx = &attemptIndexer{hits: map[string][]string{"id·episode": {"A"}, "title·season": {"B"}}}
	releases, executed, attempts = runSeriesPlan(t, idx, plan)
	if executed != 4 || len(attempts) != 4 {
		t.Fatalf("attempts = %v (%d executed), want every attempt", attempts, executed)
	}
	if len(releases) != 2 {
		t.Fatalf("releases = %d, want 2", len(releases))
	}
}

// The query text an attempt dispatches is built from its own language and its
// own year setting, so two title attempts of one plan can differ.
func TestRunConfiguredSearchRequestsBuildsEachAttemptsOwnQuery(t *testing.T) {
	plan := planOf("TVPlan", acceptTitles("original"),
		titleAttempt(config.SearchTargetEpisode, "original", false),
		titleAttempt(config.SearchTargetSeason, "original", false),
	)
	plan.Stop = config.SearchStopAll

	idx := &attemptIndexer{}
	srv := planServer(idx, plan)
	if _, _, err := srv.runConfiguredSearchRequests(
		context.Background(), "series", "tt1234567:3:7", "stream-01", nil, []string{plan.Name}, seriesSearchParams()); err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	_, queries := idx.snapshot()
	want := []string{"Show Name S03E07", "Show Name S03"}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("queries = %v, want %v", queries, want)
	}
}
