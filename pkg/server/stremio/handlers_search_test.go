package stremio

import (
	"context"
	"reflect"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/session"
)

type recordingIndexer struct {
	lastReq indexer.SearchRequest
}

func (r *recordingIndexer) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	r.lastReq = req
	return &indexer.SearchResponse{}, nil
}

func (r *recordingIndexer) Name() string            { return "Recording" }
func (r *recordingIndexer) GetUsage() indexer.Usage { return indexer.Usage{} }
func (r *recordingIndexer) Ping() error             { return nil }
func (r *recordingIndexer) DownloadNZB(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func TestBuildSeriesQueriesReturnsGenericShowName(t *testing.T) {
	got := buildSeriesQueries("Game of Thrones")
	want := []string{"Game of Thrones"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSeriesQueries() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesWithOptionsCanIncludeYear(t *testing.T) {
	got := buildSeriesQueriesWithOptions("Game of Thrones", "2011", true)
	want := []string{"Game of Thrones 2011"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSeriesQueriesWithOptions() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesWithOptionsCanOmitYear(t *testing.T) {
	got := buildSeriesQueriesWithOptions("Game of Thrones", "2011", false)
	want := []string{"Game of Thrones"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSeriesQueriesWithOptions() = %#v, want %#v", got, want)
	}
}

func TestMetadataLogTitlesPreferOriginalAndJapaneseRomajiAlternative(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "Spirited Away",
			OriginalTitle:    "千と千尋の神隠し",
			OriginalLanguage: "ja",
		},
		MovieAlternativeTitles: &tmdb.MovieAlternativeTitlesResponse{
			Titles: []tmdb.AlternativeTitle{
				{ISO3166_1: "JP", Title: "Sen to Chihiro no Kamikakushi", Type: "Romaji"},
			},
		},
	}

	if got := metadataOriginalTitle(metadata, "movie"); got != "千と千尋の神隠し" {
		t.Fatalf("metadataOriginalTitle() = %q, want %q", got, "千と千尋の神隠し")
	}
	if got := metadataAlternativeTitle(metadata, "movie"); got != "Sen to Chihiro no Kamikakushi" {
		t.Fatalf("metadataAlternativeTitle() = %q, want %q", got, "Sen to Chihiro no Kamikakushi")
	}
}

func TestMetadataLogTitlesDoNotAddAlternativeForNonJapaneseOriginals(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "The Lion King",
			OriginalTitle:    "The Lion King",
			OriginalLanguage: "en",
		},
		MovieAlternativeTitles: &tmdb.MovieAlternativeTitlesResponse{
			Titles: []tmdb.AlternativeTitle{
				{ISO3166_1: "US", Title: "Lion King", Type: "Working Title"},
			},
		},
	}

	if got := metadataOriginalTitle(metadata, "movie"); got != "The Lion King" {
		t.Fatalf("metadataOriginalTitle() = %q, want %q", got, "The Lion King")
	}
	if got := metadataAlternativeTitle(metadata, "movie"); got != "" {
		t.Fatalf("metadataAlternativeTitle() = %q, want empty", got)
	}
}

func TestMetadataLogTitlesHandleMissingJapaneseAlternativeTitles(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "Spirited Away",
			OriginalTitle:    "千と千尋の神隠し",
			OriginalLanguage: "ja",
		},
	}

	if got := metadataAlternativeTitle(metadata, "movie"); got != "" {
		t.Fatalf("metadataAlternativeTitle() = %q, want empty", got)
	}

	params := &SearchParams{
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

func TestBuildMovieQueriesFromMetadataAddsGermanTransliterationVariant(t *testing.T) {
	metadata := &resolvedSearchMetadata{
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
	}

	got := buildMovieQueriesFromMetadata(metadata, "de-DE", false)
	want := []string{"Koenig der Loewen"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildMovieQueriesFromMetadataUsesOriginalTitleWhenRequested(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "Downfall",
			OriginalTitle:    "Der Untergang",
			OriginalLanguage: "de",
			ReleaseDate:      "2004-09-16",
		},
		MovieTranslations: &tmdb.MovieTranslationsResponse{
			Translations: []tmdb.MovieTranslationEntry{
				{
					ISO639_1:  "en",
					ISO3166_1: "US",
					Data: tmdb.MovieTranslationData{
						Title: "Downfall",
					},
				},
			},
		},
	}

	got := buildMovieQueriesFromMetadata(metadata, "original", false)
	want := []string{"Der Untergang"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildMovieQueriesFromMetadataUsesRomanizedJapaneseOriginalTitle(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "Spirited Away",
			OriginalTitle:    "千と千尋の神隠し",
			OriginalLanguage: "ja",
			ReleaseDate:      "2001-07-20",
		},
		MovieAlternativeTitles: &tmdb.MovieAlternativeTitlesResponse{
			Titles: []tmdb.AlternativeTitle{
				{ISO3166_1: "JP", Title: "Sen to Chihiro no Kamikakushi", Type: "Romaji"},
			},
		},
	}

	got := buildMovieQueriesFromMetadata(metadata, "original", false)
	want := []string{"Sen to Chihiro no Kamikakushi"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataAddsGermanTransliterationVariant(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "The Lion King",
			OriginalName:     "The Lion King",
			OriginalLanguage: "en",
			FirstAirDate:     "1994-09-10",
		},
		TVTranslations: &tmdb.TVTranslationsResponse{
			Translations: []tmdb.TVTranslationEntry{
				{
					ISO639_1:  "de",
					ISO3166_1: "DE",
					Data: tmdb.TVTranslationData{
						Name: "König der Löwen",
					},
				},
			},
		},
	}

	got := buildSeriesQueriesFromMetadata(metadata, "de-DE", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"Koenig der Loewen S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataUsesOriginalTitleWhenRequested(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "Money Heist",
			OriginalName:     "La Casa de Papel",
			OriginalLanguage: "es",
			FirstAirDate:     "2017-05-02",
		},
		TVTranslations: &tmdb.TVTranslationsResponse{
			Translations: []tmdb.TVTranslationEntry{
				{
					ISO639_1:  "en",
					ISO3166_1: "US",
					Data: tmdb.TVTranslationData{
						Name: "Money Heist",
					},
				},
			},
		},
	}

	got := buildSeriesQueriesFromMetadata(metadata, "original", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"La Casa de Papel S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataUsesRomanizedJapaneseOriginalTitle(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "Attack on Titan",
			OriginalName:     "進撃の巨人",
			OriginalLanguage: "ja",
			FirstAirDate:     "2013-04-07",
		},
		TVAlternativeTitles: &tmdb.TVAlternativeTitlesResponse{
			Results: []tmdb.AlternativeTitle{
				{ISO3166_1: "JP", Title: "Shingeki no Kyojin", Type: "Romaji"},
			},
		},
	}

	got := buildSeriesQueriesFromMetadata(metadata, "original", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"Shingeki no Kyojin S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestPickRomanizedAlternativeTitleRequiresExactRomajiType(t *testing.T) {
	alts := []tmdb.AlternativeTitle{
		{ISO3166_1: "JP", Title: "TenSura", Type: "Romaji (Short)"},
		{ISO3166_1: "JP", Title: "Tensei Shitara Slime Datta Ken 3rd Season", Type: "Romaji (Season 3)"},
		{ISO3166_1: "JP", Title: "Tensei shitara Slime Datta Ken", Type: "Romaji"},
	}

	if got := pickRomanizedAlternativeTitle(alts); got != "Tensei shitara Slime Datta Ken" {
		t.Fatalf("pickRomanizedAlternativeTitle() = %q, want %q", got, "Tensei shitara Slime Datta Ken")
	}
}

func TestBuildSearchParamsFromBaseSeriesIDQueryModeMovesSeasonEpisodeIntoQuery(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &SearchParams{
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
	base := &SearchParams{
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
	base := &SearchParams{
		ContentType: "movie",
		ID:          "tt0110357",
		Req: indexer.SearchRequest{
			IMDbID: "tt0110357",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &resolvedSearchMetadata{
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

func TestSearchRequestNormalisationLogValuesIncludesOriginalTitle(t *testing.T) {
	metadata := &resolvedSearchMetadata{
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
	}

	originalTitle, inputTitle, normalizedTitle, ok := searchRequestNormalisationLogValues(
		metadata,
		"movie",
		"de-DE",
		true,
		"",
		"",
		"",
	)
	if !ok {
		t.Fatal("expected normalisation log values")
	}
	if originalTitle != "The Lion King" {
		t.Fatalf("originalTitle = %q, want %q", originalTitle, "The Lion King")
	}
	if inputTitle != "König der Löwen" {
		t.Fatalf("inputTitle = %q, want %q", inputTitle, "König der Löwen")
	}
	if normalizedTitle != "Koenig der Loewen" {
		t.Fatalf("normalizedTitle = %q, want %q", normalizedTitle, "Koenig der Loewen")
	}
}

func TestSearchRequestNormalisationLogValuesOmitsSeriesScopeSuffix(t *testing.T) {
	metadata := &resolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "The Rookie",
			OriginalName:     "The Rookie",
			OriginalLanguage: "en",
			FirstAirDate:     "2018-10-16",
		},
		TVTranslations: &tmdb.TVTranslationsResponse{
			Translations: []tmdb.TVTranslationEntry{
				{
					ISO639_1: "de",
					Data: tmdb.TVTranslationData{
						Name: "König der Löwen",
					},
				},
			},
		},
	}

	originalTitle, inputTitle, normalizedTitle, ok := searchRequestNormalisationLogValues(
		metadata,
		"series",
		"de-DE",
		false,
		"1",
		"2",
		config.SeriesSearchScopeSeasonEpisode,
	)
	if !ok {
		t.Fatal("expected normalisation log values")
	}
	if originalTitle != "The Rookie" {
		t.Fatalf("originalTitle = %q, want %q", originalTitle, "The Rookie")
	}
	if inputTitle != "König der Löwen" {
		t.Fatalf("inputTitle = %q, want %q", inputTitle, "König der Löwen")
	}
	if normalizedTitle != "Koenig der Loewen" {
		t.Fatalf("normalizedTitle = %q, want %q", normalizedTitle, "Koenig der Loewen")
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
	base := &SearchParams{
		ContentType: "movie",
		ID:          "tmdb:123",
		Req: indexer.SearchRequest{
			TMDBID: "123",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &resolvedSearchMetadata{
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
}

func TestBuildSearchParamsFromBaseSeriesFallbackUsesNormalizedMetadataQueries(t *testing.T) {
	srv := &Server{config: &config.Config{}}
	base := &SearchParams{
		ContentType: "series",
		ID:          "tmdb:241609",
		Req: indexer.SearchRequest{
			TMDBID: "241609",
			Cat:    "5000",
			Limit:  1000,
		},
		Metadata: &resolvedSearchMetadata{
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

	params := &SearchParams{
		ContentType: "movie",
		ID:          "tmdb:1084242",
		Req: indexer.SearchRequest{
			TMDBID: "1084242",
			IMDbID: "tt26443597",
			Cat:    "2000",
			Limit:  1000,
		},
		Metadata: &resolvedSearchMetadata{
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

	_, executed, err := srv.runConfiguredSearchRequests("movie", "tmdb:1084242", "Stream01", nil, []string{"MovieQuery03"}, params)
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
	if hasUsableResolvedMetadata(nil, "series") {
		t.Fatal("expected nil params not to have usable resolved metadata")
	}
	if hasUsableResolvedMetadata(&SearchParams{}, "series") {
		t.Fatal("expected empty params not to have usable resolved metadata")
	}
	if hasUsableResolvedMetadata(&SearchParams{Req: indexer.SearchRequest{IMDbID: "tt1234567"}}, "series") {
		t.Fatal("expected bare identifiers alone not to count as usable series metadata")
	}
	if !hasUsableResolvedMetadata(&SearchParams{
		Metadata: &resolvedSearchMetadata{
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

func boolPtr(v bool) *bool {
	return &v
}
