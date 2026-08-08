package stremio

import (
	"context"
	"reflect"
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

func TestBuildSearchParamsFromBaseSeriesIDQueryModeMovesSeasonEpisodeIntoQuery(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &query.SearchParams{
		ContentType: "series",
		ID:          "tt1234567:1:4",
		Req: indexer.SearchRequest{
			Season:  "1",
			Episode: "4",
			IMDbID:  "tt1234567",
			Cat:     "5000",
			Limit:   1000,
		},
	}

	params, err := srv.buildSearchParamsFromBase(base, &config.SearchQueryConfig{
		SearchMode: "id",
	})
	if err != nil {
		t.Fatalf("buildSearchParamsFromBase() error = %v", err)
	}

	if params.Req.SearchMode != "id" {
		t.Fatalf("expected SearchMode to be id, got %q", params.Req.SearchMode)
	}
	if params.Req.Query != "S01E04" {
		t.Fatalf("expected S/E query suffix, got %q", params.Req.Query)
	}
	if params.Req.Season != "1" || params.Req.Episode != "4" {
		t.Fatalf("expected season/episode params to be preserved, got season=%q episode=%q", params.Req.Season, params.Req.Episode)
	}
}

func TestBuildSearchParamsFromBaseSeriesTextQueryModeKeepsSeasonEpisodeForLaterDispatchDecision(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &query.SearchParams{
		ContentType: "series",
		ID:          "tt1234567:1:4",
		Req: indexer.SearchRequest{
			Season:  "1",
			Episode: "4",
			IMDbID:  "tt1234567",
			Cat:     "5000",
			Limit:   1000,
		},
	}

	params, err := srv.buildSearchParamsFromBase(base, &config.SearchQueryConfig{
		SearchMode: "text",
	})
	if err != nil {
		t.Fatalf("buildSearchParamsFromBase() error = %v", err)
	}

	if params.Req.SearchMode != "text" {
		t.Fatalf("expected SearchMode to be text, got %q", params.Req.SearchMode)
	}
	if params.Req.Query != "" {
		t.Fatalf("expected query to stay empty before text query expansion, got %q", params.Req.Query)
	}
	if params.Req.Season != "1" || params.Req.Episode != "4" {
		t.Fatalf("expected base season/episode to remain available, got season=%q episode=%q", params.Req.Season, params.Req.Episode)
	}
}

func TestBuildSearchParamsFromBaseTextModeUsesRequestLanguageNotPerIndexerOverrides(t *testing.T) {
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

	params, err := srv.buildSearchParamsFromBase(base, &config.SearchQueryConfig{
		SearchMode:          "text",
		SearchTitleLanguage: "de-DE",
		IncludeYear:         boolPtr(false),
	})
	if err != nil {
		t.Fatalf("buildSearchParamsFromBase() error = %v", err)
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

func TestBuildSearchParamsFromBaseAlwaysBuildsValidationInputs(t *testing.T) {
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

	params, err := srv.buildSearchParamsFromBase(base, &config.SearchQueryConfig{
		SearchMode: "id",
	})
	if err != nil {
		t.Fatalf("buildSearchParamsFromBase() error = %v", err)
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

func TestBuildSearchParamsFromBaseIDModeBuildsValidationQueriesForMultipleLanguages(t *testing.T) {
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

	params, err := srv.buildSearchParamsFromBase(base, &config.SearchQueryConfig{
		SearchMode:           "id",
		SearchTitleLanguage:  "",
		SearchTitleLanguages: []string{"", "de-DE"},
		IncludeYear:          boolPtr(false),
	})
	if err != nil {
		t.Fatalf("buildSearchParamsFromBase() error = %v", err)
	}

	if !reflect.DeepEqual(params.Req.ValidationQueries, []string{"The Lion King", "Koenig der Loewen"}) {
		t.Fatalf("ValidationQueries = %#v, want %#v", params.Req.ValidationQueries, []string{"The Lion King", "Koenig der Loewen"})
	}
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

func TestBuildSearchParamsFromBaseSeriesFallbackUsesNormalizedMetadataQueries(t *testing.T) {
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

	params, err := srv.buildSearchParamsFromBase(base, &config.SearchQueryConfig{
		SearchMode:          "text",
		SearchTitleLanguage: "original",
		IncludeYear:         boolPtr(false),
	})
	if err != nil {
		t.Fatalf("buildSearchParamsFromBase() error = %v", err)
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
				{
					Name:                "MovieQuery03",
					SearchMode:          "text",
					SearchTitleLanguage: "de-DE",
					IncludeYear:         boolPtr(true),
				},
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
				{Name: "TVQuery01", SearchMode: "id"},
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

func TestRunConfiguredSearchRequestsUniqueHitsOnlyFirstResultRequestInCombineMode(t *testing.T) {
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				{Name: "Q1", SearchMode: "text"},
				{Name: "Q2", SearchMode: "text"},
				{Name: "Q3", SearchMode: "text"},
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
	hits := srv.GetUniqueIndexerHits()
	if got := hits["IndexerB"]; got != 0 {
		t.Fatalf("IndexerB unique hits = %d, want 0", got)
	}
	if got := hits["IndexerC"]; got != 0 {
		t.Fatalf("IndexerC unique hits = %d, want 0", got)
	}
}

func TestRunConfiguredSearchRequestsUniqueHitsInFirstHitMode(t *testing.T) {
	combine := false
	stream := &auth.Stream{CombineResults: &combine}
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				{Name: "Q1", SearchMode: "text"},
				{Name: "Q2", SearchMode: "text"},
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
	releases, executed, err := srv.runConfiguredSearchRequests(context.Background(), "movie", "tt123", "stream-01", stream, []string{"Q1", "Q2"}, params)
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if executed != 2 {
		t.Fatalf("executedRequests = %d, want 2", executed)
	}
	if len(releases) != 1 {
		t.Fatalf("releases len = %d, want 1", len(releases))
	}
	hits := srv.GetUniqueIndexerHits()
	if got := hits["IndexerB"]; got != 1 {
		t.Fatalf("IndexerB unique hits = %d, want 1", got)
	}
}

func TestSingleIndexerFromReleases(t *testing.T) {
	one, ok := singleIndexerFromReleases([]*release.Release{
		{Indexer: "IndexerA"},
		{Indexer: "IndexerA"},
	})
	if !ok || one != "IndexerA" {
		t.Fatalf("singleIndexerFromReleases single = (%q,%v), want (IndexerA,true)", one, ok)
	}

	_, ok = singleIndexerFromReleases([]*release.Release{
		{Indexer: "IndexerA"},
		{Indexer: "IndexerB"},
	})
	if ok {
		t.Fatal("singleIndexerFromReleases should return false for mixed indexers")
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

// Combine mode runs the configured requests concurrently, so results must be
// merged by configured order rather than by whichever finished first.
func TestRunConfiguredSearchRequestsCombineKeepsConfiguredOrder(t *testing.T) {
	combine := true
	stream := &auth.Stream{CombineResults: &combine}
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				{Name: "Q1", SearchMode: "text"},
				{Name: "Q2", SearchMode: "text"},
			},
		},
		// Q1 is the slow one; without ordering it would land second.
		indexer:           &delayedLabelIndexer{slowLabel: "Q1", delay: 120 * time.Millisecond},
		uniqueIndexerHits: make(map[string]int64),
	}

	releases, executed, err := srv.runConfiguredSearchRequests(
		context.Background(), "movie", "tt123", "stream-01", stream, []string{"Q1", "Q2"}, combineModeSearchParams())
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
	combine := true
	stream := &auth.Stream{CombineResults: &combine}
	const delay = 150 * time.Millisecond
	srv := &Server{
		config: &config.Config{
			MovieSearchQueries: []config.SearchQueryConfig{
				{Name: "Q1", SearchMode: "text"},
				{Name: "Q2", SearchMode: "text"},
			},
		},
		indexer:           &delayedLabelIndexer{slowAll: true, delay: delay},
		uniqueIndexerHits: make(map[string]int64),
	}

	start := time.Now()
	_, _, err := srv.runConfiguredSearchRequests(
		context.Background(), "movie", "tt123", "stream-01", stream, []string{"Q1", "Q2"}, combineModeSearchParams())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("runConfiguredSearchRequests() error = %v", err)
	}
	if elapsed >= 2*delay {
		t.Fatalf("requests look sequential: took %v for two %v requests", elapsed, delay)
	}
}
