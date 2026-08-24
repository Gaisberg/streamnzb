package query

import (
	"reflect"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/services/metadata/tmdb"
)

func TestSearchRequestNormalisationLogEntriesSplitMultipleLanguages(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got, ok := SearchRequestNormalisationLogEntries(metadata, "series", []string{"en-US", ""})
	if !ok {
		t.Fatalf("expected normalisation log entries to be emitted")
	}

	want := []TitleLogEntry{
		{Languages: []string{"en-US"}, InputTitle: "Witch Hat Atelier", NormalizedTitle: "Witch Hat Atelier"},
		{Languages: []string{"original"}, InputTitle: "Tongari Boushi no Atelier", NormalizedTitle: "Tongari Boushi no Atelier"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchRequestNormalisationLogEntries() = %#v, want %#v", got, want)
	}
}

func TestSearchRequestNormalisationLogEntriesMergesDuplicateTitlesAcrossLanguages(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got, ok := SearchRequestNormalisationLogEntries(metadata, "series", []string{"en-US", ""})
	if !ok {
		t.Fatalf("expected normalisation log entries to be emitted")
	}

	want := []TitleLogEntry{
		{Languages: []string{"en-US", "original"}, InputTitle: "Dragon Ball Z", NormalizedTitle: "Dragon Ball Z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchRequestNormalisationLogEntries() = %#v, want %#v", got, want)
	}
}

func TestMetadataLogTitlesPreferOriginalAndJapaneseRomajiAlternative(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	if got := MetadataOriginalTitle(metadata, "movie"); got != "千と千尋の神隠し" {
		t.Fatalf("MetadataOriginalTitle() = %q, want %q", got, "千と千尋の神隠し")
	}
	if got := MetadataAlternativeTitle(metadata, "movie"); got != "Sen to Chihiro no Kamikakushi" {
		t.Fatalf("MetadataAlternativeTitle() = %q, want %q", got, "Sen to Chihiro no Kamikakushi")
	}
}

func TestMetadataLogTitlesDoNotAddAlternativeForNonJapaneseOriginals(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	if got := MetadataOriginalTitle(metadata, "movie"); got != "The Lion King" {
		t.Fatalf("MetadataOriginalTitle() = %q, want %q", got, "The Lion King")
	}
	if got := MetadataAlternativeTitle(metadata, "movie"); got != "" {
		t.Fatalf("MetadataAlternativeTitle() = %q, want empty", got)
	}
}

func TestMetadataFallbackTitleReturnsEnglishWhenJapaneseRomajiMissing(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	if got := MetadataAlternativeTitle(metadata, "series"); got != "" {
		t.Fatalf("MetadataAlternativeTitle() = %q, want empty", got)
	}
	if got := MetadataFallbackTitle(metadata, "series"); got != "Dragon Ball Z" {
		t.Fatalf("MetadataFallbackTitle() = %q, want %q", got, "Dragon Ball Z")
	}
}

func TestBuildMovieQueriesFromMetadataAddsGermanTransliterationVariant(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got := BuildMovieQueriesFromMetadata(metadata, "de-DE", false)
	want := []string{"Koenig der Loewen"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildMovieQueriesFromMetadataUsesOriginalTitleWhenRequested(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got := BuildMovieQueriesFromMetadata(metadata, "original", false)
	want := []string{"Der Untergang"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildMovieQueriesFromMetadataUsesRomanizedJapaneseOriginalTitle(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got := BuildMovieQueriesFromMetadata(metadata, "original", false)
	want := []string{"Sen to Chihiro no Kamikakushi"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildMovieQueriesFromMetadataFallsBackToEnglishWhenJapaneseRomajiMissing(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
		MovieDetails: &tmdb.MovieDetails{
			Title:            "Spirited Away",
			OriginalTitle:    "千と千尋の神隠し",
			OriginalLanguage: "ja",
			ReleaseDate:      "2001-07-20",
		},
		MovieTranslations: &tmdb.MovieTranslationsResponse{
			Translations: []tmdb.MovieTranslationEntry{
				{
					ISO639_1:  "en",
					ISO3166_1: "US",
					Data: tmdb.MovieTranslationData{
						Title: "Spirited Away",
					},
				},
			},
		},
		MovieAlternativeTitles: &tmdb.MovieAlternativeTitlesResponse{
			Titles: []tmdb.AlternativeTitle{
				{ISO3166_1: "JP", Title: "SenChihi", Type: "Romaji (Short)"},
			},
		},
	}

	got := BuildMovieQueriesFromMetadata(metadata, "original", false)
	want := []string{"Spirited Away"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildMovieQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataAddsGermanTransliterationVariant(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got := BuildSeriesQueriesFromMetadata(metadata, "de-DE", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"Koenig der Loewen S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataUsesOriginalTitleWhenRequested(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got := BuildSeriesQueriesFromMetadata(metadata, "original", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"La Casa de Papel S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataUsesRomanizedJapaneseOriginalTitle(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	got := BuildSeriesQueriesFromMetadata(metadata, "original", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"Shingeki no Kyojin S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestBuildSeriesQueriesFromMetadataFallsBackToEnglishWhenJapaneseRomajiMissing(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "Witch Hat Atelier",
			OriginalName:     "とんがり帽子のアトリエ",
			OriginalLanguage: "ja",
			FirstAirDate:     "2025-01-01",
		},
		TVTranslations: &tmdb.TVTranslationsResponse{
			Translations: []tmdb.TVTranslationEntry{
				{
					ISO639_1:  "en",
					ISO3166_1: "US",
					Data: tmdb.TVTranslationData{
						Name: "Witch Hat Atelier",
					},
				},
			},
		},
		TVAlternativeTitles: &tmdb.TVAlternativeTitlesResponse{
			Results: []tmdb.AlternativeTitle{
				{ISO3166_1: "JP", Title: "Tongari", Type: "Romaji (Short)"},
			},
		},
	}

	got := BuildSeriesQueriesFromMetadata(metadata, "original", false, "1", "2", config.SeriesSearchScopeSeasonEpisode)
	want := []string{"Witch Hat Atelier S01E02"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildSeriesQueriesFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestPickRomanizedAlternativeTitleRequiresExactRomajiType(t *testing.T) {
	alts := []tmdb.AlternativeTitle{
		{ISO3166_1: "JP", Title: "TenSura", Type: "Romaji (Short)"},
		{ISO3166_1: "JP", Title: "Tensei Shitara Slime Datta Ken 3rd Season", Type: "Romaji (Season 3)"},
		{ISO3166_1: "JP", Title: "Tensei shitara Slime Datta Ken", Type: "Romaji"},
	}

	if got := PickRomanizedAlternativeTitle(alts); got != "Tensei shitara Slime Datta Ken" {
		t.Fatalf("PickRomanizedAlternativeTitle() = %q, want %q", got, "Tensei shitara Slime Datta Ken")
	}
}

func TestSearchRequestNormalisationLogEntriesIncludeSingleMovieTitle(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	entries, ok := SearchRequestNormalisationLogEntries(
		metadata,
		"movie",
		[]string{"de-DE"},
	)
	if !ok {
		t.Fatal("expected normalisation log entries")
	}
	want := []TitleLogEntry{
		{Languages: []string{"de-DE"}, InputTitle: "König der Löwen", NormalizedTitle: "Koenig der Loewen"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

func TestSearchRequestNormalisationLogEntriesOmitSeriesScopeSuffix(t *testing.T) {
	metadata := &ResolvedSearchMetadata{
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

	entries, ok := SearchRequestNormalisationLogEntries(
		metadata,
		"series",
		[]string{"de-DE"},
	)
	if !ok {
		t.Fatal("expected normalisation log entries")
	}
	want := []TitleLogEntry{
		{Languages: []string{"de-DE"}, InputTitle: "König der Löwen", NormalizedTitle: "Koenig der Loewen"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

// An ID request does not gate on the title, so its languages may only widen
// what counts as a match. A stored narrowing must not survive as a stricter
// basis than a freshly created request would use.
func TestValidationTitleLanguagesForIDAlwaysIncludeDefaults(t *testing.T) {
	tests := []struct {
		name      string
		language  string
		languages []string
		want      []string
	}{
		{name: "unset", want: []string{"en-US", ""}},
		{name: "narrowed to one", languages: []string{"en-US"}, want: []string{"en-US", ""}},
		{name: "narrowed to original", languages: []string{"original"}, want: []string{"", "en-US"}},
		{name: "widened", languages: []string{"de-DE"}, want: []string{"de-DE", "en-US", ""}},
		{name: "legacy single", language: "de-DE", want: []string{"de-DE", "en-US", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidationTitleLanguages("id", tt.language, tt.languages)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ValidationTitleLanguages(id, %q, %v) = %#v, want %#v", tt.language, tt.languages, got, tt.want)
			}
		})
	}

	// Text requests are unchanged: they validate against the one language they
	// searched under.
	if got := ValidationTitleLanguages("text", "de-DE", []string{"en-US"}); !reflect.DeepEqual(got, []string{"de-DE"}) {
		t.Fatalf("text mode = %#v, want [de-DE]", got)
	}
}
