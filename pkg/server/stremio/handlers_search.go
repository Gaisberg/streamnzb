package stremio

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
)

type SearchParams struct {
	ContentType        string
	ID                 string
	ContentTitle       string
	Req                indexer.SearchRequest
	PreparedQueries    []string
	ContentIDs         *session.AvailReportMeta
	ImdbForText        string
	TmdbForText        string
	MovieTitleQueries  map[string][]string
	SeriesTitleQueries map[string][]string
	Metadata           *resolvedSearchMetadata
}

type resolvedSearchMetadata struct {
	KitsuDetails           *KitsuAnimeDetails
	MovieDetails           *tmdb.MovieDetails
	MovieTranslations      *tmdb.MovieTranslationsResponse
	MovieAlternativeTitles *tmdb.MovieAlternativeTitlesResponse
	TVDetails              *tmdb.TVDetails
	TVTranslations         *tmdb.TVTranslationsResponse
	TVAlternativeTitles    *tmdb.TVAlternativeTitlesResponse
	TVDBDetails            *tvdb.SeriesDetails
}

func metadataDisplayTitle(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil {
		if title := strings.TrimSpace(metadata.KitsuDetails.CanonicalTitle); title != "" {
			return title
		}
		if title := strings.TrimSpace(metadata.KitsuDetails.EnglishTitle); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.KitsuDetails.RomajiTitle)
	}
	if contentType == "movie" {
		if metadata.MovieDetails != nil {
			if title := strings.TrimSpace(metadata.MovieDetails.Title); title != "" {
				return title
			}
			return strings.TrimSpace(metadata.MovieDetails.OriginalTitle)
		}
		return ""
	}
	if metadata.TVDetails != nil {
		if title := strings.TrimSpace(metadata.TVDetails.Name); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.TVDetails.OriginalName)
	}
	if metadata.TVDBDetails != nil {
		if title := strings.TrimSpace(metadata.TVDBDetails.Name); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.TVDBDetails.OriginalName)
	}
	return ""
}

func metadataOriginalTitle(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails != nil {
			if title := strings.TrimSpace(metadata.MovieDetails.OriginalTitle); title != "" {
				return title
			}
			return strings.TrimSpace(metadata.MovieDetails.Title)
		}
		return ""
	}
	if metadata.TVDetails != nil {
		if title := strings.TrimSpace(metadata.TVDetails.OriginalName); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.TVDetails.Name)
	}
	if metadata.TVDBDetails != nil {
		if title := strings.TrimSpace(metadata.TVDBDetails.OriginalName); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.TVDBDetails.Name)
	}
	return ""
}

func metadataAlternativeTitle(metadata *resolvedSearchMetadata, contentType string) string {
	original := strings.TrimSpace(metadataOriginalTitle(metadata, contentType))
	if metadata == nil || original == "" {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails == nil || metadata.MovieAlternativeTitles == nil || !strings.EqualFold(strings.TrimSpace(metadata.MovieDetails.OriginalLanguage), "ja") {
			return ""
		}
		if alt := pickRomanizedAlternativeTitle(metadata.MovieAlternativeTitles.Titles); alt != "" && !strings.EqualFold(strings.TrimSpace(alt), original) {
			return strings.TrimSpace(alt)
		}
		return ""
	}
	if metadata.TVDetails == nil || metadata.TVAlternativeTitles == nil || !strings.EqualFold(strings.TrimSpace(metadata.TVDetails.OriginalLanguage), "ja") {
		return ""
	}
	if alt := pickRomanizedAlternativeTitle(metadata.TVAlternativeTitles.Results); alt != "" && !strings.EqualFold(strings.TrimSpace(alt), original) {
		return strings.TrimSpace(alt)
	}
	return ""
}

func metadataFallbackTitle(metadata *resolvedSearchMetadata, contentType string) string {
	original := strings.TrimSpace(metadataOriginalTitle(metadata, contentType))
	if metadata == nil || original == "" || hasLatinLetter(original) {
		return ""
	}
	if metadataAlternativeTitle(metadata, contentType) != "" {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(metadataOriginalLanguage(metadata, contentType)), "ja") {
		return ""
	}
	if contentType == "movie" {
		if fallback := strings.TrimSpace(preferredMovieEnglishTitle(metadata)); fallback != "" && !strings.EqualFold(fallback, original) {
			return fallback
		}
		return ""
	}
	if fallback := strings.TrimSpace(preferredSeriesEnglishTitle(metadata)); fallback != "" && !strings.EqualFold(fallback, original) {
		return fallback
	}
	return ""
}

func metadataDisplayYear(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails != nil && len(metadata.MovieDetails.ReleaseDate) >= 4 {
			return metadata.MovieDetails.ReleaseDate[:4]
		}
		return ""
	}
	if metadata.TVDetails != nil && len(metadata.TVDetails.FirstAirDate) >= 4 {
		return metadata.TVDetails.FirstAirDate[:4]
	}
	if metadata.TVDBDetails != nil && len(metadata.TVDBDetails.FirstAired) >= 4 {
		return metadata.TVDBDetails.FirstAired[:4]
	}
	return ""
}

func metadataLanguageCount(metadata *resolvedSearchMetadata, contentType string) int {
	if metadata == nil {
		return 0
	}
	if contentType == "movie" {
		if metadata.MovieTranslations != nil {
			return len(metadata.MovieTranslations.Translations)
		}
		return 0
	}
	if metadata.TVTranslations != nil {
		return len(metadata.TVTranslations.Translations)
	}
	return 0
}

func hasUsableResolvedMetadata(params *SearchParams, contentType string) bool {
	if params == nil {
		return false
	}
	if strings.TrimSpace(metadataDisplayTitle(params.Metadata, contentType)) != "" {
		return true
	}
	return len(params.PreparedQueries) > 0 || len(params.SeriesTitleQueries) > 0 || len(params.MovieTitleQueries) > 0
}

func useOriginalTitleLanguage(language string) bool {
	trimmed := strings.TrimSpace(language)
	return trimmed == "" || strings.EqualFold(trimmed, "original")
}

func localizedMovieTitleForLanguage(translations *tmdb.MovieTranslationsResponse, language string) string {
	if translations == nil || language == "" {
		return ""
	}
	langCode, countryCode := splitLanguageTagLocal(language)
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Title) == "" {
			continue
		}
		if countryCode != "" && strings.EqualFold(entry.ISO639_1, langCode) && strings.EqualFold(entry.ISO3166_1, countryCode) {
			return strings.TrimSpace(entry.Data.Title)
		}
	}
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Title) != "" && strings.EqualFold(entry.ISO639_1, langCode) {
			return strings.TrimSpace(entry.Data.Title)
		}
	}
	return ""
}

func hasLatinLetter(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Latin) && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func pickRomanizedAlternativeTitle(alternatives []tmdb.AlternativeTitle) string {
	for _, alt := range alternatives {
		title := strings.TrimSpace(alt.Title)
		if title == "" || !hasLatinLetter(title) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(alt.Type), "Romaji") {
			return title
		}
	}
	return ""
}

func preferredMovieEnglishTitle(metadata *resolvedSearchMetadata) string {
	if metadata == nil || metadata.MovieDetails == nil {
		return ""
	}
	if localized := localizedMovieTitleForLanguage(metadata.MovieTranslations, "en-US"); localized != "" {
		return localized
	}
	return strings.TrimSpace(metadata.MovieDetails.Title)
}

func preferredMovieOriginalTitle(metadata *resolvedSearchMetadata) string {
	if metadata == nil || metadata.MovieDetails == nil {
		return ""
	}
	title := strings.TrimSpace(metadata.MovieDetails.OriginalTitle)
	if title == "" {
		title = strings.TrimSpace(metadata.MovieDetails.Title)
	}
	if title == "" {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.MovieDetails.OriginalLanguage), "ja") || hasLatinLetter(title) {
		return title
	}
	if metadata.MovieAlternativeTitles != nil {
		if alt := pickRomanizedAlternativeTitle(metadata.MovieAlternativeTitles.Titles); alt != "" {
			return alt
		}
	}
	if english := preferredMovieEnglishTitle(metadata); english != "" {
		return english
	}
	return title
}

func preferredSeriesEnglishTitle(metadata *resolvedSearchMetadata) string {
	if metadata == nil || metadata.TVDetails == nil {
		return ""
	}
	if localized := localizedTVTitleForLanguage(metadata.TVTranslations, "en-US"); localized != "" {
		return localized
	}
	return strings.TrimSpace(metadata.TVDetails.Name)
}

func preferredSeriesOriginalTitle(metadata *resolvedSearchMetadata) string {
	if metadata == nil || metadata.TVDetails == nil {
		return ""
	}
	title := strings.TrimSpace(metadata.TVDetails.OriginalName)
	if title == "" {
		title = strings.TrimSpace(metadata.TVDetails.Name)
	}
	if title == "" {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.TVDetails.OriginalLanguage), "ja") || hasLatinLetter(title) {
		return title
	}
	if metadata.TVAlternativeTitles != nil {
		if alt := pickRomanizedAlternativeTitle(metadata.TVAlternativeTitles.Results); alt != "" {
			return alt
		}
	}
	if english := preferredSeriesEnglishTitle(metadata); english != "" {
		return english
	}
	return title
}

func localizedTVTitleForLanguage(translations *tmdb.TVTranslationsResponse, language string) string {
	if translations == nil || language == "" {
		return ""
	}
	langCode, countryCode := splitLanguageTagLocal(language)
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Name) == "" {
			continue
		}
		if countryCode != "" && strings.EqualFold(entry.ISO639_1, langCode) && strings.EqualFold(entry.ISO3166_1, countryCode) {
			return strings.TrimSpace(entry.Data.Name)
		}
	}
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Name) != "" && strings.EqualFold(entry.ISO639_1, langCode) {
			return strings.TrimSpace(entry.Data.Name)
		}
	}
	return ""
}

func splitLanguageTagLocal(tag string) (lang, country string) {
	tag = strings.TrimSpace(tag)
	if i := strings.Index(tag, "-"); i >= 0 {
		return tag[:i], tag[i+1:]
	}
	return tag, ""
}

func appendUniqueSearchQuery(queries []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return queries
	}
	for _, existing := range queries {
		if strings.EqualFold(strings.TrimSpace(existing), query) {
			return queries
		}
	}
	return append(queries, query)
}

func addSearchQueryVariants(queries []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return queries
	}
	normalized := strings.TrimSpace(release.NormalizeTitleForSearchQuery(query))
	if normalized != "" && !strings.EqualFold(normalized, query) {
		query = normalized
	}
	return appendUniqueSearchQuery(queries, query)
}

func metadataOriginalLanguage(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails != nil {
			return strings.TrimSpace(metadata.MovieDetails.OriginalLanguage)
		}
		return ""
	}
	if metadata.TVDetails != nil {
		return strings.TrimSpace(metadata.TVDetails.OriginalLanguage)
	}
	return ""
}

func buildMovieSearchQueryFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil {
		title := strings.TrimSpace(metadata.KitsuDetails.CanonicalTitle)
		if title == "" {
			title = strings.TrimSpace(metadata.KitsuDetails.EnglishTitle)
		}
		if includeYear && metadata.KitsuDetails.Year != "" {
			title = strings.TrimSpace(title + " " + metadata.KitsuDetails.Year)
		}
		return title
	}
	if metadata.MovieDetails == nil {
		return ""
	}
	title := strings.TrimSpace(metadata.MovieDetails.Title)
	if useOriginalTitleLanguage(language) {
		title = preferredMovieOriginalTitle(metadata)
	} else if localized := localizedMovieTitleForLanguage(metadata.MovieTranslations, language); localized != "" {
		title = localized
	}
	if title == "" {
		title = strings.TrimSpace(metadata.MovieDetails.Title)
	}
	if title == "" {
		return ""
	}
	if includeYear && len(metadata.MovieDetails.ReleaseDate) >= 4 {
		title = strings.TrimSpace(title + " " + metadata.MovieDetails.ReleaseDate[:4])
	}
	return title
}

func movieYearFromMetadata(metadata *resolvedSearchMetadata) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil && metadata.KitsuDetails.Year != "" {
		return metadata.KitsuDetails.Year
	}
	if metadata.MovieDetails == nil || len(metadata.MovieDetails.ReleaseDate) < 4 {
		return ""
	}
	return metadata.MovieDetails.ReleaseDate[:4]
}

func appendYearQuery(query, year string) string {
	query = strings.TrimSpace(query)
	year = strings.TrimSpace(year)
	if query == "" {
		return ""
	}
	if year == "" {
		return query
	}
	return query + " " + year
}

func buildMovieValidationQueryFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool) string {
	title := strings.TrimSpace(release.NormalizeTitleForSearchQuery(buildMovieSearchQueryFromMetadata(metadata, language, false)))
	if title == "" {
		return ""
	}
	if includeYear {
		title = appendYearQuery(title, movieYearFromMetadata(metadata))
	}
	return title
}

func buildMovieValidationQueriesFromMetadata(metadata *resolvedSearchMetadata, languages []string, includeYear bool) []string {
	queries := make([]string, 0, len(languages))
	seen := make(map[string]bool, len(languages))
	for _, language := range languages {
		query := strings.TrimSpace(buildMovieValidationQueryFromMetadata(metadata, language, includeYear))
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if seen[key] {
			continue
		}
		seen[key] = true
		queries = append(queries, query)
	}
	return queries
}

func buildMovieOriginalQueryFromMetadata(metadata *resolvedSearchMetadata) string {
	return strings.TrimSpace(preferredMovieOriginalTitle(metadata))
}

func appendSeasonQuery(query, season string) string {
	if season == "" {
		return strings.TrimSpace(query)
	}
	seasonNum, seasonErr := strconv.Atoi(season)
	suffix := fmt.Sprintf("S%s", season)
	if seasonErr == nil {
		suffix = fmt.Sprintf("S%02d", seasonNum)
	}
	if strings.TrimSpace(query) == "" {
		return suffix
	}
	return strings.TrimSpace(query) + " " + suffix
}

func buildSeriesPrimaryQueryFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool, season, episode, scope string) string {
	title := buildSeriesSearchTitleFromMetadata(metadata, language)
	if title == "" {
		return ""
	}
	if includeYear && metadata != nil && metadata.TVDetails != nil && len(metadata.TVDetails.FirstAirDate) >= 4 {
		title = strings.TrimSpace(title + " " + metadata.TVDetails.FirstAirDate[:4])
	}
	switch config.NormalizeSeriesSearchScope(scope) {
	case config.SeriesSearchScopeSeasonEpisode:
		title = appendSeasonEpisodeQuery(title, season, episode)
	case config.SeriesSearchScopeSeason:
		title = appendSeasonQuery(title, season)
	}
	return title
}

func buildSeriesSearchTitleFromMetadata(metadata *resolvedSearchMetadata, language string) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil {
		if useOriginalTitleLanguage(language) {
			if title := strings.TrimSpace(metadata.KitsuDetails.CanonicalTitle); title != "" {
				return title
			}
			return strings.TrimSpace(metadata.KitsuDetails.RomajiTitle)
		}
		if title := strings.TrimSpace(metadata.KitsuDetails.EnglishTitle); title != "" {
			return title
		}
		if title := strings.TrimSpace(metadata.KitsuDetails.CanonicalTitle); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.KitsuDetails.RomajiTitle)
	}
	if metadata.TVDetails != nil {
		title := strings.TrimSpace(metadata.TVDetails.Name)
		if useOriginalTitleLanguage(language) {
			title = preferredSeriesOriginalTitle(metadata)
		} else if localized := localizedTVTitleForLanguage(metadata.TVTranslations, language); localized != "" {
			title = localized
		}
		if title == "" {
			title = strings.TrimSpace(metadata.TVDetails.Name)
		}
		if title != "" {
			return title
		}
	}
	if metadata.TVDBDetails != nil {
		if title := strings.TrimSpace(metadata.TVDBDetails.Name); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.TVDBDetails.OriginalName)
	}
	return ""
}

func seriesYearFromMetadata(metadata *resolvedSearchMetadata) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil && metadata.KitsuDetails.Year != "" {
		return metadata.KitsuDetails.Year
	}
	if metadata.TVDetails != nil && len(metadata.TVDetails.FirstAirDate) >= 4 {
		return metadata.TVDetails.FirstAirDate[:4]
	}
	if metadata.TVDBDetails != nil && len(metadata.TVDBDetails.FirstAired) >= 4 {
		return metadata.TVDBDetails.FirstAired[:4]
	}
	return ""
}

func buildSeriesValidationQueryFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool) string {
	title := strings.TrimSpace(release.NormalizeTitleForSearchQuery(buildSeriesSearchTitleFromMetadata(metadata, language)))
	if title == "" {
		return ""
	}
	if includeYear {
		title = appendYearQuery(title, seriesYearFromMetadata(metadata))
	}
	return title
}

func buildSeriesValidationQueriesFromMetadata(metadata *resolvedSearchMetadata, languages []string, includeYear bool) []string {
	queries := make([]string, 0, len(languages))
	seen := make(map[string]bool, len(languages))
	for _, language := range languages {
		query := strings.TrimSpace(buildSeriesValidationQueryFromMetadata(metadata, language, includeYear))
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if seen[key] {
			continue
		}
		seen[key] = true
		queries = append(queries, query)
	}
	return queries
}

func buildSeriesOriginalQueryFromMetadata(metadata *resolvedSearchMetadata) string {
	return strings.TrimSpace(preferredSeriesOriginalTitle(metadata))
}

func uniqueTitleLogValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

func validationTitleLanguages(searchMode, language string, languages []string) []string {
	if strings.EqualFold(strings.TrimSpace(searchMode), "id") {
		normalized := config.NormalizeSearchTitleLanguages(languages)
		if len(normalized) > 0 {
			return normalized
		}
		single := config.NormalizeSearchTitleLanguage(language)
		if single != "" {
			return []string{single}
		}
		return config.DefaultIDSearchTitleLanguages()
	}
	single := config.NormalizeSearchTitleLanguage(language)
	if single == "" {
		return []string{""}
	}
	return []string{single}
}

type titleLogEntry struct {
	Languages       []string
	InputTitle      string
	NormalizedTitle string
}

func searchRequestNormalisationLogEntries(metadata *resolvedSearchMetadata, contentType string, languages []string) ([]titleLogEntry, bool) {
	grouped := make(map[string]*titleLogEntry, len(languages))
	order := make([]string, 0, len(languages))
	changed := false
	for _, language := range languages {
		var selectedTitle string
		if contentType == "movie" {
			selectedTitle = buildMovieSearchQueryFromMetadata(metadata, language, false)
		} else {
			selectedTitle = buildSeriesSearchTitleFromMetadata(metadata, language)
		}
		selectedTitle = strings.TrimSpace(selectedTitle)
		if selectedTitle == "" {
			continue
		}
		normalized := strings.TrimSpace(release.NormalizeTitleForSearchQuery(selectedTitle))
		if normalized == "" {
			continue
		}
		logLanguage := searchTitleLanguageForLog(language)
		key := strings.ToLower(selectedTitle) + "\x00" + strings.ToLower(normalized)
		entry, ok := grouped[key]
		if !ok {
			entry = &titleLogEntry{
				Languages:       []string{logLanguage},
				InputTitle:      selectedTitle,
				NormalizedTitle: normalized,
			}
			grouped[key] = entry
			order = append(order, key)
		} else {
			exists := false
			for _, existing := range entry.Languages {
				if strings.EqualFold(existing, logLanguage) {
					exists = true
					break
				}
			}
			if !exists {
				entry.Languages = append(entry.Languages, logLanguage)
			}
		}
		if !strings.EqualFold(strings.TrimSpace(normalized), strings.TrimSpace(selectedTitle)) {
			changed = true
		}
	}
	entries := make([]titleLogEntry, 0, len(order))
	for _, key := range order {
		entry := grouped[key]
		if entry == nil {
			continue
		}
		entries = append(entries, *entry)
	}
	if len(entries) == 0 {
		return nil, false
	}
	if len(entries) > 1 {
		changed = true
	}
	for _, entry := range entries {
		if len(entry.Languages) > 1 {
			changed = true
			break
		}
	}
	if !changed {
		return nil, false
	}
	return entries, true
}

func validationQueryProfilesFromMetadata(metadata *resolvedSearchMetadata, contentType string, languages []string, includeYear bool) []indexer.ValidationQueryProfile {
	grouped := make(map[string]*indexer.ValidationQueryProfile, len(languages))
	order := make([]string, 0, len(languages))

	if metadata != nil && metadata.KitsuDetails != nil {
		rawTitles := []string{
			metadata.KitsuDetails.CanonicalTitle,
			metadata.KitsuDetails.EnglishTitle,
			metadata.KitsuDetails.RomajiTitle,
		}
		rawTitles = append(rawTitles, metadata.KitsuDetails.Synonyms...)

		var kitsuQueries []string
		seenKitsu := make(map[string]bool)
		for _, raw := range rawTitles {
			t := strings.TrimSpace(release.NormalizeTitleForSearchQuery(raw))
			if t == "" {
				continue
			}
			if !seenKitsu[strings.ToLower(t)] {
				seenKitsu[strings.ToLower(t)] = true
				if includeYear && metadata.KitsuDetails.Year != "" {
					kitsuQueries = append(kitsuQueries, appendYearQuery(t, metadata.KitsuDetails.Year))
				}
				kitsuQueries = append(kitsuQueries, t)
			}
			norm := strings.Map(func(r rune) rune {
				switch r {
				case 'é', 'è', 'ê', 'ë':
					return 'e'
				case 'á', 'à', 'â', 'ä':
					return 'a'
				case 'ó', 'ò', 'ô', 'ö':
					return 'o'
				case 'ú', 'ù', 'û', 'ü':
					return 'u'
				case 'í', 'ì', 'î', 'ï':
					return 'i'
				default:
					return r
				}
			}, raw)
			asciiNorm := strings.TrimSpace(release.NormalizeTitleForSearchQuery(norm))
			if asciiNorm != "" && !seenKitsu[strings.ToLower(asciiNorm)] {
				seenKitsu[strings.ToLower(asciiNorm)] = true
				if includeYear && metadata.KitsuDetails.Year != "" {
					kitsuQueries = append(kitsuQueries, appendYearQuery(asciiNorm, metadata.KitsuDetails.Year))
				}
				kitsuQueries = append(kitsuQueries, asciiNorm)
			}
		}

		for _, q := range kitsuQueries {
			key := strings.ToLower(q)
			if _, ok := grouped[key]; !ok {
				profile := &indexer.ValidationQueryProfile{
					Languages: []string{"en-US"},
					Query:     q,
				}
				grouped[key] = profile
				order = append(order, key)
			}
		}
	}

	for _, language := range languages {
		var query string
		if contentType == "movie" {
			query = strings.TrimSpace(buildMovieValidationQueryFromMetadata(metadata, language, includeYear))
		} else {
			query = strings.TrimSpace(buildSeriesValidationQueryFromMetadata(metadata, language, includeYear))
		}
		if query == "" {
			continue
		}
		logLanguage := searchTitleLanguageForLog(language)
		key := strings.ToLower(query)
		profile, ok := grouped[key]
		if !ok {
			profile = &indexer.ValidationQueryProfile{
				Languages: []string{logLanguage},
				Query:     query,
			}
			grouped[key] = profile
			order = append(order, key)
			continue
		}
		exists := false
		for _, existing := range profile.Languages {
			if strings.EqualFold(existing, logLanguage) {
				exists = true
				break
			}
		}
		if !exists {
			profile.Languages = append(profile.Languages, logLanguage)
		}
	}
	profiles := make([]indexer.ValidationQueryProfile, 0, len(order))
	for _, key := range order {
		if profile := grouped[key]; profile != nil {
			profiles = append(profiles, *profile)
		}
	}
	return profiles
}

func validationQueriesFromProfiles(profiles []indexer.ValidationQueryProfile) []string {
	queries := make([]string, 0, len(profiles))
	seen := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		query := strings.TrimSpace(profile.Query)
		if query == "" {
			continue
		}
		key := strings.ToLower(query)
		if seen[key] {
			continue
		}
		seen[key] = true
		queries = append(queries, query)
	}
	return queries
}

func buildMovieQueriesFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool) []string {
	if metadata == nil || metadata.MovieDetails == nil {
		return nil
	}
	primary := strings.TrimSpace(release.NormalizeTitleForSearchQuery(buildMovieSearchQueryFromMetadata(metadata, language, false)))
	if primary == "" {
		return nil
	}
	if includeYear {
		primary = appendYearQuery(primary, movieYearFromMetadata(metadata))
	}
	return appendUniqueSearchQuery(nil, primary)
}

func buildSeriesQueriesFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool, season, episode, scope string) []string {
	if metadata == nil || (metadata.TVDetails == nil && metadata.TVDBDetails == nil && metadata.KitsuDetails == nil) {
		return nil
	}
	primary := strings.TrimSpace(release.NormalizeTitleForSearchQuery(buildSeriesSearchTitleFromMetadata(metadata, language)))
	if primary == "" {
		return nil
	}
	if includeYear {
		primary = appendYearQuery(primary, seriesYearFromMetadata(metadata))
	}
	queries := appendUniqueSearchQuery(nil, primary)

	switch config.NormalizeSeriesSearchScope(scope) {
	case config.SeriesSearchScopeSeasonEpisode:
		withEpisode := make([]string, 0, len(queries)*2)
		for _, query := range queries {
			if query == "" {
				continue
			}
			withEpisode = appendUniqueSearchQuery(withEpisode, appendSeasonEpisodeQuery(query, season, episode))
		}
		queries = withEpisode
	case config.SeriesSearchScopeSeason:
		withSeason := make([]string, 0, len(queries)*2)
		for _, query := range queries {
			if query == "" {
				continue
			}
			withSeason = appendUniqueSearchQuery(withSeason, appendSeasonQuery(query, season))
		}
		queries = withSeason
	}
	return queries
}

func logMetadataLookup(streamLabel, contentType, id string) {
	logger.Debug("Get metadata",
		"stream", streamLabel,
		"type", contentType,
		"id", id,
	)
}

func logMetadataLookupFinished(streamLabel, contentType, id string, params *SearchParams) {
	originalTitle := metadataOriginalTitle(params.Metadata, contentType)
	attrs := []any{
		"stream", streamLabel,
		"type", contentType,
		"id", id,
		"imdb_id", params.ContentIDs.ImdbID,
		"tmdb_id", params.Req.TMDBID,
		"original_title", originalTitle,
		"year", metadataDisplayYear(params.Metadata, contentType),
		"languages", metadataLanguageCount(params.Metadata, contentType),
	}
	if alternativeTitle := metadataAlternativeTitle(params.Metadata, contentType); alternativeTitle != "" {
		attrs = append(attrs, "alternative_title", alternativeTitle)
	} else if fallbackTitle := metadataFallbackTitle(params.Metadata, contentType); fallbackTitle != "" {
		attrs = append(attrs,
			"fallback_reason", "no_romaji",
			"fallback_title", fallbackTitle,
		)
	}
	if contentType == "series" {
		attrs = append(attrs,
			"tvdb_id", params.ContentIDs.TvdbID,
			"season", params.ContentIDs.Season,
			"episode", params.ContentIDs.Episode,
		)
	}
	logger.Debug("Get metadata finished", attrs...)
}

func logStreamConfiguration(streamLabel, contentType string, stream *auth.Stream, selectedQueries []string) {
	logger.Debug("Stream configuration",
		"stream", streamLabel,
		"type", contentType,
		"filter_sorting", func() string {
			if stream == nil || strings.TrimSpace(stream.FilterSortingMode) == "" {
				return "none"
			}
			return strings.ToLower(strings.TrimSpace(stream.FilterSortingMode))
		}(),
		"indexer_mode", streamIndexerMode(stream),
		"search_requests_mode", func() string {
			if streamCombinesResults(stream) {
				return "combine"
			}
			return "first_hit"
		}(),
		"results_mode", streamResultsMode(stream),
		"failover", streamFailoverEnabled(stream),
		"availnzb", streamUsesAvailNZB(stream),
		"providers", append([]string(nil), stream.ProviderSelections...),
		"indexers", append([]string(nil), stream.IndexerSelections...),
		"requests", append([]string(nil), selectedQueries...),
	)
}

func searchTitleLanguageForLog(language string) string {
	trimmed := config.NormalizeSearchTitleLanguage(language)
	if trimmed == "" {
		return "original"
	}
	return trimmed
}

func searchTitleLanguagesForLog(languages []string) []string {
	normalized := config.NormalizeSearchTitleLanguages(languages)
	if len(normalized) == 0 {
		return []string{"original"}
	}
	values := make([]string, 0, len(normalized))
	for _, language := range normalized {
		values = append(values, searchTitleLanguageForLog(language))
	}
	return values
}

func searchLimitForLog(limit int) any {
	if limit <= 0 {
		return "max"
	}
	return limit
}

func newAvailContext(result *availnzb.ReleasesResult, inputResults int) *AvailContext {
	ctx := &AvailContext{
		Result:                  result,
		InputResults:            inputResults,
		ByDetailsURL:            make(map[string]*availnzb.ReleaseWithStatus),
		AvailableByDetailsURL:   make(map[string]bool),
		UnavailableByDetailsURL: make(map[string]bool),
	}
	if result == nil {
		return ctx
	}
	for _, rws := range result.Releases {
		if rws == nil || rws.Release == nil || rws.Release.DetailsURL == "" {
			continue
		}
		ctx.ByDetailsURL[rws.Release.DetailsURL] = rws
		if rws.Available && rws.Release.Link != "" {
			ctx.AvailableByDetailsURL[rws.Release.DetailsURL] = true
			continue
		}
		if !rws.Available {
			ctx.UnavailableByDetailsURL[rws.Release.DetailsURL] = true
		}
	}
	return ctx
}

func (s *Server) loadAvailContext(params *SearchParams, stream *auth.Stream) *AvailContext {
	if params == nil || params.ContentIDs == nil {
		return newAvailContext(nil, 0)
	}
	contentIDs := params.ContentIDs
	if !streamUsesAvailNZB(stream) || s.availClient == nil || s.availClient.BaseURL == "" {
		return newAvailContext(nil, 0)
	}
	if strings.TrimSpace(params.Req.TMDBID) == "" && contentIDs.ImdbID == "" && contentIDs.TvdbID == "" {
		return newAvailContext(nil, 0)
	}
	availSeason := contentIDs.Season
	availEpisode := contentIDs.Episode
	if availSeason <= 0 && availEpisode > 0 {
		availSeason = 1
	}
	availResult, _ := s.availClient.GetReleases(contentIDs.ImdbID, params.Req.TMDBID, contentIDs.TvdbID, availSeason, availEpisode, s.indexerHostsForStream(stream), s.providerHostsForStream(stream))
	inputResults := 0
	if availResult != nil {
		inputResults = len(availResult.Releases)
	}
	return newAvailContext(availResult, inputResults)
}

func (s *Server) runConfiguredSearchRequests(contentType, id, streamLabel string, stream *auth.Stream, selectedQueries []string, params *SearchParams) ([]*release.Release, int, error) {
	indexerReleases := make([]*release.Release, 0)
	executedRequests := 0
	for _, name := range selectedQueries {
		searchQuery := s.config.GetSearchQueryByName(contentType, name)
		if searchQuery == nil {
			logger.Debug("Stream search query missing", "stream", streamLabel, "content_type", contentType, "id", id, "query", name)
			continue
		}
		params.Req.StreamLabel = streamLabel
		params.Req.RequestLabel = searchQuery.Name
		profileParams, profileErr := s.buildSearchParamsFromBase(params, searchQuery)
		if profileErr != nil {
			return nil, executedRequests, profileErr
		}
		profileParams.Req.StreamLabel = streamLabel
		profileParams.Req.RequestLabel = searchQuery.Name
		applyStreamIndexerSelection(&profileParams.Req, stream)
		profileParams.Req.DisableResultFiltering = stream == nil || strings.TrimSpace(stream.FilterSortingMode) == "" || strings.EqualFold(strings.TrimSpace(stream.FilterSortingMode), "none") || streamUsesAIOStreamsProfile(stream)
		searchMode := strings.ToLower(strings.TrimSpace(searchQuery.SearchMode))
		validationQueries := append([]string(nil), profileParams.Req.ValidationQueries...)
		if len(validationQueries) == 0 && strings.TrimSpace(profileParams.Req.ValidationQuery) != "" {
			validationQueries = []string{profileParams.Req.ValidationQuery}
		}
		if len(validationQueries) == 0 {
			logger.Debug("Skipping search request without validation basis",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"type", contentType,
				"id", id,
			)
			continue
		}
		titleLanguages := validationTitleLanguages(searchQuery.SearchMode, searchQuery.SearchTitleLanguage, searchQuery.SearchTitleLanguages)
		if searchMode == "id" && !hasUsableIDSearchIdentifier(profileParams.Req, contentType) {
			logger.Debug("Skipping search request without resolved metadata identifiers",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"type", contentType,
				"id", id,
			)
			continue
		}
		if searchMode != "id" && !hasPreparedTextQueries(profileParams.Req) {
			logger.Debug("Skipping search request without prepared text queries",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"type", contentType,
				"id", id,
			)
			continue
		}
		effectiveLimit := profileParams.Req.Limit
		if searchQuery.SearchResultLimit >= 0 {
			effectiveLimit = searchQuery.SearchResultLimit
		}
		scopeForLog := ""
		if contentType == "series" {
			scopeForLog = config.NormalizeSeriesSearchScope(profileParams.Req.SeriesSearchScope)
		}
		configAttrs := []any{
			"stream", streamLabel,
			"request", searchQuery.Name,
			"search_mode", searchQuery.SearchMode,
			"type", contentType,
			"id", id,
			"year", profileParams.Req.EnableYearValidation,
			"limit", searchLimitForLog(effectiveLimit),
		}
		if searchMode == "id" {
			configAttrs = append(configAttrs, "title_languages", searchTitleLanguagesForLog(titleLanguages))
		} else {
			configAttrs = append(configAttrs, "title_language", searchTitleLanguageForLog(searchQuery.SearchTitleLanguage))
		}
		if contentType == "series" {
			configAttrs = append(configAttrs,
				"tv_categories", searchQuery.TVCategories,
				"scope", scopeForLog,
			)
		} else {
			configAttrs = append(configAttrs,
				"movie_categories", searchQuery.MovieCategories,
			)
		}
		logger.Debug("Search request config", configAttrs...)
		if entries, ok := searchRequestNormalisationLogEntries(
			profileParams.Metadata,
			contentType,
			titleLanguages,
		); ok {
			for _, entry := range entries {
				attrs := []any{
					"stream", streamLabel,
					"request", searchQuery.Name,
					"input_title", entry.InputTitle,
					"normalised_title", entry.NormalizedTitle,
				}
				if len(entry.Languages) == 1 {
					attrs = append(attrs, "title_language", entry.Languages[0])
				} else {
					attrs = append(attrs, "title_languages", entry.Languages)
				}
				logger.Debug("Search request normalisation", attrs...)
			}
		}
		queryVariants := profileParams.PreparedQueries
		if searchMode == "id" || len(queryVariants) == 0 {
			queryVariants = []string{profileParams.Req.Query}
		}
		requestReleases := make([]*release.Release, 0)
		for _, queryVariant := range queryVariants {
			reqVariant := profileParams.Req
			reqVariant.Limit = effectiveLimit
			if searchMode != "id" {
				reqVariant.Query = queryVariant
			}
			executedRequests++
			releases, runErr := search.RunIndexerSearches(s.indexer, reqVariant, contentType)
			if runErr != nil {
				return nil, executedRequests, runErr
			}
			if len(releases) > 0 {
				requestReleases = append(requestReleases, releases...)
			}
			if streamCombinesResults(stream) {
				indexerReleases = append(indexerReleases, releases...)
				continue
			}
			if len(releases) > 0 {
				if idxName, ok := singleIndexerFromReleases(requestReleases); ok {
					s.addUniqueIndexerHits(map[string]int{idxName: 1})
				}
				return releases, executedRequests, nil
			}
		}
	}
	if idxName, ok := singleIndexerFromReleases(indexerReleases); ok {
		s.addUniqueIndexerHits(map[string]int{idxName: 1})
	}
	return indexerReleases, executedRequests, nil
}

func singleIndexerFromReleases(releases []*release.Release) (string, bool) {
	if len(releases) == 0 {
		return "", false
	}
	out := make(map[string]struct{})
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		name := strings.TrimSpace(rel.Indexer)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
		if len(out) > 1 {
			return "", false
		}
	}
	for name := range out {
		return name, true
	}
	return "", false
}

func dedupeCombinedSearchResults(streamLabel string, stream *auth.Stream, releases []*release.Release, executedRequests int) []*release.Release {
	if !streamCombinesResults(stream) {
		return releases
	}
	if executedRequests <= 1 {
		return releases
	}
	inputResults := len(releases)
	missingDetailsURL := 0
	for _, rel := range releases {
		if rel == nil || rel.DetailsURL != "" {
			continue
		}
		missingDetailsURL++
	}
	releases = search.MergeAndDedupeSearchResults(releases)
	logger.Debug("Stream deduplication",
		"stream", streamLabel,
		"search_requests_mode", "combine",
		"input_results", inputResults,
		"missing_details_url", missingDetailsURL,
		"final_results", len(releases),
	)
	return releases
}

func alignAvailContextWithSearch(availCtx *AvailContext, indexerReleases []*release.Release) *AvailContext {
	if availCtx == nil || availCtx.Result == nil {
		return newAvailContext(nil, 0)
	}
	indexerDetailsURLs := make(map[string]bool)
	for _, r := range indexerReleases {
		if r != nil && r.DetailsURL != "" {
			indexerDetailsURLs[r.DetailsURL] = true
		}
	}
	if len(indexerDetailsURLs) == 0 {
		return availCtx
	}
	filtered := availCtx.Result.Releases[:0]
	for _, rws := range availCtx.Result.Releases {
		if rws == nil || rws.Release == nil {
			continue
		}
		if !indexerDetailsURLs[rws.Release.DetailsURL] {
			continue
		}
		filtered = append(filtered, rws)
	}
	return newAvailContext(&availnzb.ReleasesResult{ImdbID: availCtx.Result.ImdbID, Count: availCtx.Result.Count, Releases: filtered}, availCtx.InputResults)
}

func enrichSearchResultsWithAvail(streamLabel string, indexerReleases []*release.Release, availCtx *AvailContext) {
	if availCtx == nil {
		availCtx = newAvailContext(nil, 0)
	}
	availableResults := 0
	matchedResults := 0
	missingDetailsURL := 0
	indexerDetailsURLs := make(map[string]bool, len(indexerReleases))
	for _, rel := range indexerReleases {
		if rel == nil {
			continue
		}
		if rel.DetailsURL == "" {
			missingDetailsURL++
			continue
		}
		indexerDetailsURLs[rel.DetailsURL] = true
	}
	for detailsURL := range availCtx.ByDetailsURL {
		if !indexerDetailsURLs[detailsURL] {
			continue
		}
		matchedResults++
		if availCtx.AvailableByDetailsURL[detailsURL] {
			availableResults++
		}
	}
	logger.Debug("AvailNZB enrichment",
		"stream", streamLabel,
		"AvailNZB_results", availCtx.InputResults,
		"search_results", len(indexerReleases),
		"matched_results", matchedResults,
		"available_results", availableResults,
		"missing_details_url", missingDetailsURL,
	)
}

// libraryContentID resolves the content id used to key the SQLite library, in a
// single fixed priority order. Both the write side (saveSessionToLibrary) and the
// read side (buildRawSearchResult) MUST use this so their keys can never drift —
// a past mismatch (write ended in TVDb, read ended in Kitsu) silently defeated the
// cache for Kitsu-only and TVDb-only content.
func libraryContentID(imdb, tmdb, tvdb, kitsu string) string {
	for _, id := range []string{imdb, tmdb, tvdb, kitsu} {
		if strings.TrimSpace(id) != "" {
			return id
		}
	}
	return ""
}

func (s *Server) buildRawSearchResult(ctx context.Context, contentType, id string, stream *auth.Stream) (*rawSearchResult, error) {
	selectedQueries := streamSearchQueryNames(stream, contentType)
	if len(selectedQueries) == 0 {
		return nil, fmt.Errorf("stream is missing at least one %s search request", contentType)
	}

	params, err := s.buildSearchParamsBase(contentType, id, nil)
	if err != nil {
		return nil, err
	}
	streamLabel := func() string {
		if stream != nil {
			return stream.Username
		}
		return "legacy"
	}()
	logger.Debug("Building playback candidates",
		"stream", streamLabel,
		"type", contentType,
		"id", id,
		"requests", len(selectedQueries),
	)
	logMetadataLookup(streamLabel, contentType, id)
	logMetadataLookupFinished(streamLabel, contentType, id, params)
	if !hasUsableResolvedMetadata(params, contentType) {
		logger.Debug("Skipping stream search because metadata could not be resolved",
			"stream", streamLabel,
			"type", contentType,
			"id", id,
		)
		return &rawSearchResult{
			Params:          params,
			IndexerReleases: nil,
			Avail: &AvailContext{
				ByDetailsURL:            make(map[string]*availnzb.ReleaseWithStatus),
				AvailableByDetailsURL:   make(map[string]bool),
				UnavailableByDetailsURL: make(map[string]bool),
			},
		}, nil
	}
	logStreamConfiguration(streamLabel, contentType, stream, selectedQueries)

	libMode := s.config.EffectiveLibrarySearchMode()
	var libraryReleases []*release.Release
	if libMode != "disabled" && s.attemptRecorder != nil && s.attemptRecorder.LibraryStore() != nil {
		season := 0
		episode := 0
		if params.ContentIDs != nil {
			season = params.ContentIDs.Season
			episode = params.ContentIDs.Episode
		}
		libItems, _ := s.attemptRecorder.LibraryStore().GetCandidatesByIDs(contentType, params.Req.IMDbID, params.Req.TMDBID, params.Req.TVDBID, params.Req.KitsuID, season, episode)
		for _, item := range libItems {
			rel := convertLibraryItemToRelease(item)
			if rel != nil {
				libraryReleases = append(libraryReleases, rel)
			}
		}
		libraryReleases = s.filterBadReleases(streamLabel, libraryReleases)
		if len(libraryReleases) > 0 {
			logger.Info("SQLite library hit: retrieved cached releases",
				"stream", streamLabel,
				"type", contentType,
				"id", id,
				"mode", libMode,
				"releases", len(libraryReleases),
			)
			if libMode == "library_first" {
				return &rawSearchResult{
					Params:          params,
					IndexerReleases: libraryReleases,
					Avail: &AvailContext{
						ByDetailsURL:            make(map[string]*availnzb.ReleaseWithStatus),
						AvailableByDetailsURL:   make(map[string]bool),
						UnavailableByDetailsURL: make(map[string]bool),
					},
				}, nil
			}
		}
	}

	availChan := make(chan *AvailContext, 1)
	go func() {
		availChan <- s.loadAvailContext(params, stream)
	}()
	indexerReleases, executedRequests, err := s.runConfiguredSearchRequests(contentType, id, streamLabel, stream, selectedQueries, params)
	availCtx := <-availChan
	if err != nil {
		return nil, err
	}
	if len(libraryReleases) > 0 && libMode == "combine" {
		indexerReleases = append(libraryReleases, indexerReleases...)
	} else if len(indexerReleases) == 0 && len(libraryReleases) > 0 && libMode == "fallback_only" {
		indexerReleases = libraryReleases
	}
	indexerReleases = dedupeCombinedSearchResults(streamLabel, stream, indexerReleases, executedRequests)
	indexerReleases = s.filterBadReleases(streamLabel, indexerReleases)
	availCtx = alignAvailContextWithSearch(availCtx, indexerReleases)
	enrichSearchResultsWithAvail(streamLabel, indexerReleases, availCtx)
	logger.Debug("Playback candidate build finished",
		"stream", streamLabel,
		"type", contentType,
		"id", id,
		"executed_requests", executedRequests,
		"releases", len(indexerReleases),
		"avail_matches", len(availCtx.AvailableByDetailsURL)+len(availCtx.UnavailableByDetailsURL),
	)

	return &rawSearchResult{
		Params:          params,
		IndexerReleases: indexerReleases,
		Avail:           availCtx,
	}, nil
}

// filterBadReleases drops releases with an unexpired persistent bad verdict
// (article hole / corruption recorded by purgeFailedRelease). One batched SQLite
// lookup; keeps known-broken releases from being re-offered after a restart.
func (s *Server) filterBadReleases(streamLabel string, releases []*release.Release) []*release.Release {
	if s == nil || s.attemptRecorder == nil || len(releases) == 0 {
		return releases
	}
	badStore := s.attemptRecorder.BadReleaseStore()
	if badStore == nil {
		return releases
	}
	urls := make([]string, 0, len(releases))
	for _, r := range releases {
		if r != nil && r.DetailsURL != "" {
			urls = append(urls, r.DetailsURL)
		}
	}
	bad := badStore.BadSet(urls)
	if len(bad) == 0 {
		return releases
	}
	kept := releases[:0]
	dropped := 0
	for _, r := range releases {
		if r != nil && r.DetailsURL != "" {
			if _, isBad := bad[r.DetailsURL]; isBad {
				dropped++
				logger.Debug("Filtered known-bad release from results", "stream", streamLabel, "title", r.Title, "url", r.DetailsURL)
				continue
			}
		}
		kept = append(kept, r)
	}
	if dropped > 0 {
		logger.Info("Filtered known-bad releases from search results", "stream", streamLabel, "dropped", dropped, "remaining", len(kept))
	}
	return kept
}

func convertLibraryItemToRelease(item *persistence.LibraryItem) *release.Release {
	if item == nil {
		return nil
	}
	indexerName := strings.TrimSpace(item.IndexerName)
	if indexerName == "" || indexerName == "Library" || indexerName == "StreamNZB Library" {
		indexerName = "StreamNZB Library"
	} else if !strings.HasPrefix(indexerName, "StreamNZB Library - ") {
		indexerName = "StreamNZB Library - " + indexerName
	}

	return &release.Release{
		Title:         item.ReleaseTitle,
		Link:          item.DetailsURL,
		DetailsURL:    item.DetailsURL,
		Indexer:       indexerName,
		Size:          item.SizeBytes,
		SourceIndexer: item,
		IsLibrary:     true,
	}
}

func buildSeriesQueries(showName string) []string {
	return buildSeriesQueriesWithOptions(showName, "", false)
}

func logMetadataResolutionState(contentType, requestID, resolver string, attrs ...any) {
	base := []any{
		"type", contentType,
		"id", requestID,
		"resolver", resolver,
	}
	logger.Debug("Metadata resolution", append(base, attrs...)...)
}

func buildSeriesQueriesWithOptions(showName, year string, includeYear bool) []string {
	showName = strings.TrimSpace(showName)
	if includeYear && strings.TrimSpace(year) != "" {
		showName = strings.TrimSpace(showName + " " + year)
	}
	if showName == "" {
		return nil
	}
	return []string{showName}
}

func (s *Server) buildSearchParamsBase(contentType, id string, searchQuery *config.SearchQueryConfig) (*SearchParams, error) {
	const searchLimit = 0
	params := &SearchParams{
		ContentType:        contentType,
		ID:                 id,
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		Metadata:           &resolvedSearchMetadata{},
	}
	req := indexer.SearchRequest{Limit: searchLimit}
	scope := config.SeriesSearchScopeSeasonEpisode
	if searchQuery != nil {
		scope = config.NormalizeSeriesSearchScope(searchQuery.SeriesSearchScope)
	}
	req.SeriesSearchScope = scope

	var kitsuID string
	var kitsuEpisode string

	searchID := id
	if strings.HasPrefix(id, "kitsu:") {
		parts := strings.Split(id, ":")
		if len(parts) >= 3 {
			kitsuID = parts[1]
			kitsuEpisode = parts[2]
			req.Episode = kitsuEpisode
		} else if len(parts) >= 2 {
			kitsuID = parts[1]
		}
		req.KitsuID = kitsuID
	} else if strings.HasPrefix(id, "tvdb:") {
		parts := strings.Split(id, ":")
		if len(parts) >= 4 {
			req.TVDBID = parts[1]
			searchID = parts[1]
			req.Season, req.Episode = parts[2], parts[3]
		} else if len(parts) >= 2 {
			req.TVDBID = parts[1]
			searchID = parts[1]
		}
	} else if strings.HasPrefix(id, "tmdb:") {
		parts := strings.Split(id, ":")
		if len(parts) >= 4 {
			req.TMDBID = parts[1]
			searchID = parts[1]
			req.Season, req.Episode = parts[2], parts[3]
		} else if len(parts) >= 2 {
			req.TMDBID = parts[1]
			searchID = parts[1]
		}
	} else if strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if len(parts) >= 3 {
			searchID = parts[0]
			req.Season, req.Episode = parts[1], parts[2]
		} else if len(parts) > 0 {
			searchID = parts[0]
		}
	}
	if strings.HasPrefix(searchID, "tt") {
		req.IMDbID = searchID
	} else if looksLikeTMDBID(searchID) && req.TVDBID == "" {
		req.TMDBID = searchID
	}
	imdbForText := req.IMDbID
	tmdbForText := req.TMDBID
	if strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if parts[0] == "tmdb" && len(parts) >= 2 {
			tmdbForText = parts[1]
		}
	}
	if contentType == "movie" {
		req.Cat = "2000"
	} else {
		req.Cat = "5000"
	}

	if kitsuID != "" && s.kitsuClient != nil {
		if details, err := s.kitsuClient.GetAnimeDetails(context.Background(), kitsuID); err == nil && details != nil {
			params.Metadata.KitsuDetails = details
			logMetadataResolutionState(contentType, id, "kitsu_details", "kitsu_id", kitsuID, "status", "success", "title", details.CanonicalTitle)

			if req.TVDBID == "" && details.TVDBID != "" {
				req.TVDBID = details.TVDBID
			}
			if req.IMDbID == "" && details.IMDbID != "" {
				req.IMDbID = details.IMDbID
			}
			if req.TMDBID == "" && details.TMDBID != "" {
				req.TMDBID = details.TMDBID
			}

			primaryTitle := details.EnglishTitle
			if primaryTitle == "" {
				primaryTitle = details.CanonicalTitle
			}
			if primaryTitle == "" {
				primaryTitle = details.RomajiTitle
			}
			asciiTitle := strings.Map(func(r rune) rune {
				switch r {
				case 'é', 'è', 'ê', 'ë':
					return 'e'
				case 'á', 'à', 'â', 'ä':
					return 'a'
				case 'ó', 'ò', 'ô', 'ö':
					return 'o'
				case 'ú', 'ù', 'û', 'ü':
					return 'u'
				case 'í', 'ì', 'î', 'ï':
					return 'i'
				default:
					return r
				}
			}, primaryTitle)

			searchTitle := asciiTitle
			if searchTitle == "" {
				searchTitle = primaryTitle
			}

			if kitsuEpisode != "" {
				epNum, _ := strconv.Atoi(kitsuEpisode)
				var epQueries []string
				if epNum > 0 {
					epQueries = append(epQueries, fmt.Sprintf("%s S01E%02d", searchTitle, epNum))
					epQueries = append(epQueries, fmt.Sprintf("%s %02d", searchTitle, epNum))
					if epNum >= 100 {
						epQueries = append(epQueries, fmt.Sprintf("%s %d", searchTitle, epNum))
					}
				} else {
					epQueries = append(epQueries, fmt.Sprintf("%s %s", searchTitle, kitsuEpisode))
				}
				params.SeriesTitleQueries[searchTitle] = epQueries
			} else {
				params.MovieTitleQueries[searchTitle] = []string{searchTitle}
			}
		} else if err != nil {
			logMetadataResolutionState(contentType, id, "kitsu_details", "kitsu_id", kitsuID, "status", "failed", "err", err)
		}
	}

	if req.TMDBID == "" && req.IMDbID != "" {
		if s.tmdbClient == nil {
			logMetadataResolutionState(contentType, id, "tmdb_find", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tmdb_client_unconfigured")
		} else {
			findResp, findErr := s.tmdbClient.Find(req.IMDbID, "imdb_id")
			if findErr != nil {
				logMetadataResolutionState(contentType, id, "tmdb_find", "imdb_id", req.IMDbID, "status", "failed", "err", findErr)
			} else {
				resolved := ""
				if contentType == "movie" && len(findResp.MovieResults) > 0 {
					resolved = strconv.Itoa(findResp.MovieResults[0].ID)
				}
				if (contentType == "series" || contentType == "anime" || contentType == "tv" || contentType == "documentary" || contentType == "other") && len(findResp.TVResults) > 0 {
					resolved = strconv.Itoa(findResp.TVResults[0].ID)
				}
				if resolved == "" && len(findResp.MovieResults) > 0 {
					resolved = strconv.Itoa(findResp.MovieResults[0].ID)
				}
				if resolved != "" {
					req.TMDBID = resolved
					tmdbForText = req.TMDBID
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_find", "imdb_id", req.IMDbID, "status", "empty")
				}
			}
		}
	}

	if req.TMDBID == "" && req.TVDBID != "" {
		if s.tmdbClient == nil {
			logMetadataResolutionState(contentType, id, "tmdb_find_tvdb", "tvdb_id", req.TVDBID, "status", "skipped", "reason", "tmdb_client_unconfigured")
		} else {
			findResp, findErr := s.tmdbClient.Find(req.TVDBID, "tvdb_id")
			if findErr != nil {
				logMetadataResolutionState(contentType, id, "tmdb_find_tvdb", "tvdb_id", req.TVDBID, "status", "failed", "err", findErr)
			} else {
				resolved := ""
				if len(findResp.TVResults) > 0 {
					resolved = strconv.Itoa(findResp.TVResults[0].ID)
				} else if len(findResp.MovieResults) > 0 {
					resolved = strconv.Itoa(findResp.MovieResults[0].ID)
				}
				if resolved != "" {
					req.TMDBID = resolved
					tmdbForText = req.TMDBID
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_find_tvdb", "tvdb_id", req.TVDBID, "status", "empty")
				}
			}
		}
	}

	isSeriesLike := contentType == "series" || contentType == "anime" || contentType == "tv" || (req.Season != "" && req.Episode != "")
	if isSeriesLike {
		if req.TMDBID != "" {
			if s.tmdbClient == nil {
				logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "skipped", "reason", "tmdb_client_unconfigured")
			} else if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
				var wg sync.WaitGroup
				var tvDetails *tmdb.TVDetails
				var tvTranslations *tmdb.TVTranslationsResponse
				var tvAlternatives *tmdb.TVAlternativeTitlesResponse
				var tvExtIDs *tmdb.ExternalIDsResponse

				wg.Add(4)
				go func() {
					defer wg.Done()
					if details, err := s.tmdbClient.GetTVDetails(tmdbIDNum); err == nil {
						tvDetails = details
					} else {
						logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
					}
				}()
				go func() {
					defer wg.Done()
					if translations, err := s.tmdbClient.GetTVTranslations(tmdbIDNum); err == nil {
						tvTranslations = translations
					} else {
						logMetadataResolutionState(contentType, id, "tmdb_series_translations", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
					}
				}()
				go func() {
					defer wg.Done()
					if alternatives, err := s.tmdbClient.GetTVAlternativeTitles(tmdbIDNum); err == nil {
						tvAlternatives = alternatives
					}
				}()
				go func() {
					defer wg.Done()
					if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "tv"); err == nil {
						tvExtIDs = extIDs
					} else {
						logMetadataResolutionState(contentType, id, "tmdb_series_external_ids", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
					}
				}()
				wg.Wait()

				params.Metadata.TVDetails = tvDetails
				params.Metadata.TVTranslations = tvTranslations
				params.Metadata.TVAlternativeTitles = tvAlternatives

				if tvExtIDs != nil {
					if tvExtIDs.TVDBID != 0 {
						req.TVDBID = strconv.Itoa(tvExtIDs.TVDBID)
					}
					if tvExtIDs.IMDbID != "" && req.IMDbID == "" {
						req.IMDbID = tvExtIDs.IMDbID
						imdbForText = tvExtIDs.IMDbID
					}
					if req.TVDBID == "" {
						logMetadataResolutionState(contentType, id, "tmdb_series_external_ids", "tmdb_id", req.TMDBID, "status", "empty")
					}
				}
			} else {
				logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
			}
		}
		if req.IMDbID != "" && req.TVDBID == "" {
			if s.tvdbClient == nil {
				logMetadataResolutionState(contentType, id, "tvdb_resolve", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tvdb_client_unconfigured")
			}
			if s.tvdbClient != nil {
				if tvdbID, err := s.tvdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID = tvdbID
				} else if err != nil {
					logMetadataResolutionState(contentType, id, "tvdb_resolve", "imdb_id", req.IMDbID, "status", "failed", "err", err)
				} else {
					logMetadataResolutionState(contentType, id, "tvdb_resolve", "imdb_id", req.IMDbID, "status", "empty")
				}
			}
			if req.TVDBID == "" && s.tmdbClient != nil {
				if tvdbID, err := s.tmdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID = tvdbID
				} else if err != nil {
					logMetadataResolutionState(contentType, id, "tmdb_resolve_tvdb", "imdb_id", req.IMDbID, "status", "failed", "err", err)
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_resolve_tvdb", "imdb_id", req.IMDbID, "status", "empty")
				}
			} else if req.TVDBID == "" && s.tmdbClient == nil {
				logMetadataResolutionState(contentType, id, "tmdb_resolve_tvdb", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tmdb_client_unconfigured")
			}
		}
		if req.TMDBID == "" && req.TVDBID != "" && s.tvdbClient != nil {
			if tvdbDetails, err := s.tvdbClient.GetSeriesDetails(req.TVDBID); err == nil && tvdbDetails != nil {
				params.Metadata.TVDBDetails = tvdbDetails
				logMetadataResolutionState(contentType, id, "tvdb_series_details", "tvdb_id", req.TVDBID, "status", "success", "title", tvdbDetails.Name)
			} else if err != nil {
				logMetadataResolutionState(contentType, id, "tvdb_series_details", "tvdb_id", req.TVDBID, "status", "failed", "err", err)
			}
		}
	}
	seasonNum, _ := strconv.Atoi(req.Season)
	episodeNum, _ := strconv.Atoi(req.Episode)
	contentIDs := &session.AvailReportMeta{ImdbID: req.IMDbID, TmdbID: req.TMDBID, TvdbID: req.TVDBID, KitsuID: req.KitsuID, Season: seasonNum, Episode: episodeNum}
	if contentType == "movie" && req.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
			var wg sync.WaitGroup
			var movieDetails *tmdb.MovieDetails
			var movieTranslations *tmdb.MovieTranslationsResponse
			var movieAlternatives *tmdb.MovieAlternativeTitlesResponse
			var movieExtIDs *tmdb.ExternalIDsResponse

			wg.Add(4)
			go func() {
				defer wg.Done()
				if details, err := s.tmdbClient.GetMovieDetails(tmdbIDNum); err == nil {
					movieDetails = details
				}
			}()
			go func() {
				defer wg.Done()
				if translations, err := s.tmdbClient.GetMovieTranslations(tmdbIDNum); err == nil {
					movieTranslations = translations
				}
			}()
			go func() {
				defer wg.Done()
				if alternatives, err := s.tmdbClient.GetMovieAlternativeTitles(tmdbIDNum); err == nil {
					movieAlternatives = alternatives
				}
			}()
			go func() {
				defer wg.Done()
				if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil {
					movieExtIDs = extIDs
				}
			}()
			wg.Wait()

			params.Metadata.MovieDetails = movieDetails
			params.Metadata.MovieTranslations = movieTranslations
			params.Metadata.MovieAlternativeTitles = movieAlternatives

			if movieExtIDs != nil && movieExtIDs.IMDbID != "" && contentIDs.ImdbID == "" {
				contentIDs.ImdbID = movieExtIDs.IMDbID
				req.IMDbID = contentIDs.ImdbID
				imdbForText = contentIDs.ImdbID
			}
		}
	}
	contentIDs.ImdbID = req.IMDbID
	contentIDs.TmdbID = req.TMDBID
	contentIDs.TvdbID = req.TVDBID
	params.Req = req
	params.ContentIDs = contentIDs
	params.ImdbForText = imdbForText
	params.TmdbForText = tmdbForText
	params.ContentTitle = metadataDisplayTitle(params.Metadata, contentType)
	return params, nil
}

func cloneSearchParams(base *SearchParams) *SearchParams {
	if base == nil {
		return nil
	}
	next := *base
	nextReq := base.Req
	next.Req = nextReq
	if base.ContentIDs != nil {
		contentIDs := *base.ContentIDs
		next.ContentIDs = &contentIDs
	}
	next.PreparedQueries = append([]string(nil), base.PreparedQueries...)
	next.MovieTitleQueries = make(map[string][]string, len(base.MovieTitleQueries))
	for k, v := range base.MovieTitleQueries {
		next.MovieTitleQueries[k] = append([]string(nil), v...)
	}
	next.SeriesTitleQueries = make(map[string][]string, len(base.SeriesTitleQueries))
	for k, v := range base.SeriesTitleQueries {
		next.SeriesTitleQueries[k] = append([]string(nil), v...)
	}
	next.Metadata = base.Metadata
	return &next
}

func (s *Server) buildSearchParamsFromBase(base *SearchParams, searchQuery *config.SearchQueryConfig) (*SearchParams, error) {
	params := cloneSearchParams(base)
	if params == nil {
		return nil, fmt.Errorf("base search params are required")
	}
	contentType := params.ContentType
	req := &params.Req
	searchMode := ""
	searchTitleLanguage := ""
	searchTitleLanguages := []string(nil)
	includeYear := true
	scope := config.NormalizeSeriesSearchScope(req.SeriesSearchScope)
	var queryIndexerConfig *config.IndexerSearchConfig
	if searchQuery != nil {
		searchMode = strings.ToLower(strings.TrimSpace(searchQuery.SearchMode))
		searchTitleLanguage = config.NormalizeSearchTitleLanguage(searchQuery.SearchTitleLanguage)
		searchTitleLanguages = config.NormalizeSearchTitleLanguages(searchQuery.SearchTitleLanguages)
		queryIndexerConfig = searchQuery.AsIndexerSearchConfig()
		if searchQuery.IncludeYear != nil {
			includeYear = *searchQuery.IncludeYear
		} else if searchMode == "id" {
			includeYear = false
		}
		scope = config.NormalizeSeriesSearchScope(searchQuery.SeriesSearchScope)
	}
	req.SeriesSearchScope = scope
	req.EnableYearValidation = includeYear
	req.SearchMode = "text"
	req.Query = ""
	req.ValidationQueryProfiles = nil
	req.ValidationQueries = nil
	validationLanguages := validationTitleLanguages(searchMode, searchTitleLanguage, searchTitleLanguages)
	req.ValidationQueryProfiles = validationQueryProfilesFromMetadata(params.Metadata, contentType, validationLanguages, includeYear)
	req.ValidationQueries = validationQueriesFromProfiles(req.ValidationQueryProfiles)
	req.ValidationQuery = ""
	if len(req.ValidationQueries) > 0 {
		req.ValidationQuery = req.ValidationQueries[0]
	}
	if searchMode == "id" {
		req.SearchMode = "id"
		if contentType == "series" {
			switch scope {
			case config.SeriesSearchScopeSeasonEpisode:
				if req.Season != "" && req.Episode != "" {
					if seasonNum, err1 := strconv.Atoi(req.Season); err1 == nil {
						if episodeNum, err2 := strconv.Atoi(req.Episode); err2 == nil {
							req.Query = fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
						}
					}
					if req.Query == "" {
						req.Query = fmt.Sprintf("S%sE%s", req.Season, req.Episode)
					}
				}
			case config.SeriesSearchScopeSeason:
				req.Query = appendSeasonQuery("", req.Season)
			}
		}
	}

	if len(s.config.Indexers) > 0 {
		req.EffectiveByIndexer = make(map[string]*config.IndexerSearchConfig)
		indexerTypeByName := make(map[string]string, len(s.config.Indexers))
		for i := range s.config.Indexers {
			ic := &s.config.Indexers[i]
			if ic.Enabled != nil && !*ic.Enabled {
				continue
			}
			eff := config.MergeIndexerSearch(ic, queryIndexerConfig, s.config)
			if strings.EqualFold(ic.Type, "easynews") {
				t := true
				eff.DisableIdSearch = &t
			}
			req.EffectiveByIndexer[ic.Name] = eff
			indexerTypeByName[ic.Name] = ic.Type
		}
		if searchMode != "id" {
		}
	}
	if searchMode != "id" {
		var queries []string
		cacheKey := fmt.Sprintf("%s|%t|%s", searchTitleLanguage, includeYear, scope)
		if contentType == "movie" {
			if cached, ok := params.MovieTitleQueries[cacheKey]; ok {
				queries = cached
			} else {
				queries = buildMovieQueriesFromMetadata(params.Metadata, searchTitleLanguage, includeYear)
				if len(queries) > 0 {
					params.MovieTitleQueries[cacheKey] = queries
				}
			}
		} else if req.Episode != "" || req.Season != "" {
			if cached, ok := params.SeriesTitleQueries[cacheKey]; ok {
				queries = cached
			} else {
				queries = buildSeriesQueriesFromMetadata(params.Metadata, searchTitleLanguage, includeYear, req.Season, req.Episode, scope)
				if len(queries) > 0 {
					params.SeriesTitleQueries[cacheKey] = queries
				}
			}
		} else {
			queries = buildSeriesQueriesFromMetadata(params.Metadata, searchTitleLanguage, includeYear, "", "", config.SeriesSearchScopeNone)
		}

		if len(queries) == 0 {
			for _, qList := range params.SeriesTitleQueries {
				queries = append(queries, qList...)
			}
			for _, qList := range params.MovieTitleQueries {
				queries = append(queries, qList...)
			}
		}

		if len(queries) > 0 {
			params.PreparedQueries = append([]string(nil), queries...)
			req.Query = queries[0]
		}
	}
	return params, nil
}

func (s *Server) buildSearchParams(contentType, id string, searchQuery *config.SearchQueryConfig) (*SearchParams, error) {
	base, err := s.buildSearchParamsBase(contentType, id, nil)
	if err != nil {
		return nil, err
	}
	return s.buildSearchParamsFromBase(base, searchQuery)
}

func appendSeasonEpisodeQuery(query, season, episode string) string {
	if episode == "" {
		return strings.TrimSpace(query)
	}
	if season == "" {
		season = "1"
	}
	seasonNum, seasonErr := strconv.Atoi(season)
	episodeNum, episodeErr := strconv.Atoi(episode)
	suffix := fmt.Sprintf("S%sE%s", season, episode)
	if seasonErr == nil && episodeErr == nil {
		suffix = fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
	}
	if strings.TrimSpace(query) == "" {
		return suffix
	}
	return strings.TrimSpace(query) + " " + suffix
}

func sliceContainsIC(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}
