// Package query builds indexer search queries and validation profiles from
// resolved content metadata. Everything here is pure: it turns TMDB / TVDB /
// Kitsu metadata plus the requested season/episode into query strings and
// validation profiles, with no HTTP, no network clients and no server state,
// so the query-shaping rules can be tested on their own.
package query

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
)

type SearchParams struct {
	ContentType     string
	ID              string
	ContentTitle    string
	Req             indexer.SearchRequest
	PreparedQueries []string
	// AbsoluteQueries are supplementary absolute-numbered anime queries
	// ("One Piece 63") executed as text searches after PreparedQueries.
	AbsoluteQueries    []string
	ContentIDs         *session.AvailReportMeta
	ImdbForText        string
	TmdbForText        string
	MovieTitleQueries  map[string][]string
	SeriesTitleQueries map[string][]string
	Metadata           *ResolvedSearchMetadata
}

type ResolvedSearchMetadata struct {
	KitsuDetails           *kitsu.AnimeDetails
	MovieDetails           *tmdb.MovieDetails
	MovieTranslations      *tmdb.MovieTranslationsResponse
	MovieAlternativeTitles *tmdb.MovieAlternativeTitlesResponse
	TVDetails              *tmdb.TVDetails
	TVTranslations         *tmdb.TVTranslationsResponse
	TVAlternativeTitles    *tmdb.TVAlternativeTitlesResponse
	TVDBDetails            *tvdb.SeriesDetails
}

func MetadataDisplayTitle(metadata *ResolvedSearchMetadata, contentType string) string {
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

func MetadataOriginalTitle(metadata *ResolvedSearchMetadata, contentType string) string {
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

func MetadataAlternativeTitle(metadata *ResolvedSearchMetadata, contentType string) string {
	original := strings.TrimSpace(MetadataOriginalTitle(metadata, contentType))
	if metadata == nil || original == "" {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails == nil || metadata.MovieAlternativeTitles == nil || !strings.EqualFold(strings.TrimSpace(metadata.MovieDetails.OriginalLanguage), "ja") {
			return ""
		}
		if alt := PickRomanizedAlternativeTitle(metadata.MovieAlternativeTitles.Titles); alt != "" && !strings.EqualFold(strings.TrimSpace(alt), original) {
			return strings.TrimSpace(alt)
		}
		return ""
	}
	if metadata.TVDetails == nil || metadata.TVAlternativeTitles == nil || !strings.EqualFold(strings.TrimSpace(metadata.TVDetails.OriginalLanguage), "ja") {
		return ""
	}
	if alt := PickRomanizedAlternativeTitle(metadata.TVAlternativeTitles.Results); alt != "" && !strings.EqualFold(strings.TrimSpace(alt), original) {
		return strings.TrimSpace(alt)
	}
	return ""
}

func MetadataFallbackTitle(metadata *ResolvedSearchMetadata, contentType string) string {
	original := strings.TrimSpace(MetadataOriginalTitle(metadata, contentType))
	if metadata == nil || original == "" || HasLatinLetter(original) {
		return ""
	}
	if MetadataAlternativeTitle(metadata, contentType) != "" {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(MetadataOriginalLanguage(metadata, contentType)), "ja") {
		return ""
	}
	if contentType == "movie" {
		if fallback := strings.TrimSpace(PreferredMovieEnglishTitle(metadata)); fallback != "" && !strings.EqualFold(fallback, original) {
			return fallback
		}
		return ""
	}
	if fallback := strings.TrimSpace(PreferredSeriesEnglishTitle(metadata)); fallback != "" && !strings.EqualFold(fallback, original) {
		return fallback
	}
	return ""
}

func MetadataDisplayYear(metadata *ResolvedSearchMetadata, contentType string) string {
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

func MetadataLanguageCount(metadata *ResolvedSearchMetadata, contentType string) int {
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

func HasUsableResolvedMetadata(params *SearchParams, contentType string) bool {
	if params == nil {
		return false
	}
	if strings.TrimSpace(MetadataDisplayTitle(params.Metadata, contentType)) != "" {
		return true
	}
	return len(params.PreparedQueries) > 0 || len(params.SeriesTitleQueries) > 0 || len(params.MovieTitleQueries) > 0
}

// RunParallel runs every fn concurrently and returns once all have finished.
// Each fn writes to its own capture, so the WaitGroup boilerplate does not have
// to be repeated per metadata fan-out.
func RunParallel(fns ...func()) {
	var wg sync.WaitGroup
	wg.Add(len(fns))
	for _, fn := range fns {
		go func(f func()) {
			defer wg.Done()
			f()
		}(fn)
	}
	wg.Wait()
}

func UseOriginalTitleLanguage(language string) bool {
	trimmed := strings.TrimSpace(language)
	return trimmed == "" || strings.EqualFold(trimmed, "original")
}

// TitleSource is the shape title selection actually needs, normalized from
// either TMDB's movie or TV response types. It exists so the localized /
// English / preferred-original logic lives once instead of as mirrored movie
// and series helpers that had to be fixed twice per bug.
type TitleSource struct {
	present          bool
	title            string
	originalTitle    string
	originalLanguage string
	translations     []TranslatedTitle
	alternatives     []tmdb.AlternativeTitle
}

// TranslatedTitle is one TMDB translation flattened to the fields that matter,
// hiding that movies carry Data.Title while series carry Data.Name.
type TranslatedTitle struct {
	lang    string
	country string
	title   string
}

func MovieTitleSource(metadata *ResolvedSearchMetadata) TitleSource {
	if metadata == nil || metadata.MovieDetails == nil {
		return TitleSource{}
	}
	ts := TitleSource{
		present:          true,
		title:            strings.TrimSpace(metadata.MovieDetails.Title),
		originalTitle:    strings.TrimSpace(metadata.MovieDetails.OriginalTitle),
		originalLanguage: strings.TrimSpace(metadata.MovieDetails.OriginalLanguage),
	}
	if metadata.MovieTranslations != nil {
		for i := range metadata.MovieTranslations.Translations {
			entry := metadata.MovieTranslations.Translations[i]
			ts.translations = append(ts.translations, TranslatedTitle{
				lang:    entry.ISO639_1,
				country: entry.ISO3166_1,
				title:   strings.TrimSpace(entry.Data.Title),
			})
		}
	}
	if metadata.MovieAlternativeTitles != nil {
		ts.alternatives = metadata.MovieAlternativeTitles.Titles
	}
	return ts
}

func SeriesTitleSource(metadata *ResolvedSearchMetadata) TitleSource {
	if metadata == nil || metadata.TVDetails == nil {
		return TitleSource{}
	}
	ts := TitleSource{
		present:          true,
		title:            strings.TrimSpace(metadata.TVDetails.Name),
		originalTitle:    strings.TrimSpace(metadata.TVDetails.OriginalName),
		originalLanguage: strings.TrimSpace(metadata.TVDetails.OriginalLanguage),
	}
	if metadata.TVTranslations != nil {
		for i := range metadata.TVTranslations.Translations {
			entry := metadata.TVTranslations.Translations[i]
			ts.translations = append(ts.translations, TranslatedTitle{
				lang:    entry.ISO639_1,
				country: entry.ISO3166_1,
				title:   strings.TrimSpace(entry.Data.Name),
			})
		}
	}
	if metadata.TVAlternativeTitles != nil {
		ts.alternatives = metadata.TVAlternativeTitles.Results
	}
	return ts
}

// LocalizedFor returns the translated title for language, preferring an exact
// language+country match over a language-only one.
func (ts TitleSource) LocalizedFor(language string) string {
	if language == "" || len(ts.translations) == 0 {
		return ""
	}
	langCode, countryCode := SplitLanguageTagLocal(language)
	for _, entry := range ts.translations {
		if entry.title == "" {
			continue
		}
		if countryCode != "" && strings.EqualFold(entry.lang, langCode) && strings.EqualFold(entry.country, countryCode) {
			return entry.title
		}
	}
	for _, entry := range ts.translations {
		if entry.title != "" && strings.EqualFold(entry.lang, langCode) {
			return entry.title
		}
	}
	return ""
}

func (ts TitleSource) EnglishTitle() string {
	if !ts.present {
		return ""
	}
	if localized := ts.LocalizedFor("en-US"); localized != "" {
		return localized
	}
	return ts.title
}

// PreferredOriginal returns the original title, romanizing Japanese titles that
// carry no Latin letters (via a Romaji alternative title, else the English
// title) so text queries stay searchable on indexers.
func (ts TitleSource) PreferredOriginal() string {
	if !ts.present {
		return ""
	}
	title := ts.originalTitle
	if title == "" {
		title = ts.title
	}
	if title == "" {
		return ""
	}
	if !strings.EqualFold(ts.originalLanguage, "ja") || HasLatinLetter(title) {
		return title
	}
	if alt := PickRomanizedAlternativeTitle(ts.alternatives); alt != "" {
		return alt
	}
	if english := ts.EnglishTitle(); english != "" {
		return english
	}
	return title
}

func LocalizedMovieTitleForLanguage(translations *tmdb.MovieTranslationsResponse, language string) string {
	return MovieTitleSource(&ResolvedSearchMetadata{
		MovieDetails:      &tmdb.MovieDetails{},
		MovieTranslations: translations,
	}).LocalizedFor(language)
}

func HasLatinLetter(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Latin) && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func PickRomanizedAlternativeTitle(alternatives []tmdb.AlternativeTitle) string {
	for _, alt := range alternatives {
		title := strings.TrimSpace(alt.Title)
		if title == "" || !HasLatinLetter(title) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(alt.Type), "Romaji") {
			return title
		}
	}
	return ""
}

func PreferredMovieEnglishTitle(metadata *ResolvedSearchMetadata) string {
	return MovieTitleSource(metadata).EnglishTitle()
}

func PreferredMovieOriginalTitle(metadata *ResolvedSearchMetadata) string {
	return MovieTitleSource(metadata).PreferredOriginal()
}

func PreferredSeriesEnglishTitle(metadata *ResolvedSearchMetadata) string {
	return SeriesTitleSource(metadata).EnglishTitle()
}

func PreferredSeriesOriginalTitle(metadata *ResolvedSearchMetadata) string {
	return SeriesTitleSource(metadata).PreferredOriginal()
}

func LocalizedTVTitleForLanguage(translations *tmdb.TVTranslationsResponse, language string) string {
	return SeriesTitleSource(&ResolvedSearchMetadata{
		TVDetails:      &tmdb.TVDetails{},
		TVTranslations: translations,
	}).LocalizedFor(language)
}

func SplitLanguageTagLocal(tag string) (lang, country string) {
	tag = strings.TrimSpace(tag)
	if i := strings.Index(tag, "-"); i >= 0 {
		return tag[:i], tag[i+1:]
	}
	return tag, ""
}

func AppendUniqueSearchQuery(queries []string, query string) []string {
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

func MetadataOriginalLanguage(metadata *ResolvedSearchMetadata, contentType string) string {
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

func BuildMovieSearchQueryFromMetadata(metadata *ResolvedSearchMetadata, language string) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil {
		title := strings.TrimSpace(metadata.KitsuDetails.CanonicalTitle)
		if title == "" {
			title = strings.TrimSpace(metadata.KitsuDetails.EnglishTitle)
		}
		return title
	}
	if metadata.MovieDetails == nil {
		return ""
	}
	title := strings.TrimSpace(metadata.MovieDetails.Title)
	if UseOriginalTitleLanguage(language) {
		title = PreferredMovieOriginalTitle(metadata)
	} else if localized := LocalizedMovieTitleForLanguage(metadata.MovieTranslations, language); localized != "" {
		title = localized
	}
	if title == "" {
		title = strings.TrimSpace(metadata.MovieDetails.Title)
	}
	if title == "" {
		return ""
	}
	return title
}

func MovieYearFromMetadata(metadata *ResolvedSearchMetadata) string {
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

func AppendYearQuery(query, year string) string {
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

func BuildMovieValidationQueryFromMetadata(metadata *ResolvedSearchMetadata, language string, includeYear bool) string {
	title := strings.TrimSpace(release.NormalizeTitleForSearchQuery(BuildMovieSearchQueryFromMetadata(metadata, language)))
	if title == "" {
		return ""
	}
	if includeYear {
		title = AppendYearQuery(title, MovieYearFromMetadata(metadata))
	}
	return title
}

func AppendSeasonQuery(query, season string) string {
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

func BuildSeriesSearchTitleFromMetadata(metadata *ResolvedSearchMetadata, language string) string {
	if metadata == nil {
		return ""
	}
	if metadata.KitsuDetails != nil {
		if UseOriginalTitleLanguage(language) {
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
		if UseOriginalTitleLanguage(language) {
			title = PreferredSeriesOriginalTitle(metadata)
		} else if localized := LocalizedTVTitleForLanguage(metadata.TVTranslations, language); localized != "" {
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

func SeriesYearFromMetadata(metadata *ResolvedSearchMetadata) string {
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

func BuildSeriesValidationQueryFromMetadata(metadata *ResolvedSearchMetadata, language string, includeYear bool) string {
	title := strings.TrimSpace(release.NormalizeTitleForSearchQuery(BuildSeriesSearchTitleFromMetadata(metadata, language)))
	if title == "" {
		return ""
	}
	if includeYear {
		title = AppendYearQuery(title, SeriesYearFromMetadata(metadata))
	}
	return title
}

func ValidationTitleLanguages(searchMode, language string, languages []string) []string {
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

type TitleLogEntry struct {
	Languages       []string
	InputTitle      string
	NormalizedTitle string
}

func SearchRequestNormalisationLogEntries(metadata *ResolvedSearchMetadata, contentType string, languages []string) ([]TitleLogEntry, bool) {
	grouped := make(map[string]*TitleLogEntry, len(languages))
	order := make([]string, 0, len(languages))
	changed := false
	for _, language := range languages {
		var selectedTitle string
		if contentType == "movie" {
			selectedTitle = BuildMovieSearchQueryFromMetadata(metadata, language)
		} else {
			selectedTitle = BuildSeriesSearchTitleFromMetadata(metadata, language)
		}
		selectedTitle = strings.TrimSpace(selectedTitle)
		if selectedTitle == "" {
			continue
		}
		normalized := strings.TrimSpace(release.NormalizeTitleForSearchQuery(selectedTitle))
		if normalized == "" {
			continue
		}
		logLanguage := SearchTitleLanguageForLog(language)
		key := strings.ToLower(selectedTitle) + "\x00" + strings.ToLower(normalized)
		entry, ok := grouped[key]
		if !ok {
			entry = &TitleLogEntry{
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
	entries := make([]TitleLogEntry, 0, len(order))
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

func ValidationQueryProfilesFromMetadata(metadata *ResolvedSearchMetadata, contentType string, languages []string, includeYear bool) []indexer.ValidationQueryProfile {
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
					kitsuQueries = append(kitsuQueries, AppendYearQuery(t, metadata.KitsuDetails.Year))
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
					kitsuQueries = append(kitsuQueries, AppendYearQuery(asciiNorm, metadata.KitsuDetails.Year))
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
			query = strings.TrimSpace(BuildMovieValidationQueryFromMetadata(metadata, language, includeYear))
		} else {
			query = strings.TrimSpace(BuildSeriesValidationQueryFromMetadata(metadata, language, includeYear))
		}
		if query == "" {
			continue
		}
		logLanguage := SearchTitleLanguageForLog(language)
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

func ValidationQueriesFromProfiles(profiles []indexer.ValidationQueryProfile) []string {
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

func BuildMovieQueriesFromMetadata(metadata *ResolvedSearchMetadata, language string, includeYear bool) []string {
	if metadata == nil || metadata.MovieDetails == nil {
		return nil
	}
	primary := strings.TrimSpace(release.NormalizeTitleForSearchQuery(BuildMovieSearchQueryFromMetadata(metadata, language)))
	if primary == "" {
		return nil
	}
	if includeYear {
		primary = AppendYearQuery(primary, MovieYearFromMetadata(metadata))
	}
	return AppendUniqueSearchQuery(nil, primary)
}

func BuildSeriesQueriesFromMetadata(metadata *ResolvedSearchMetadata, language string, includeYear bool, season, episode, scope string) []string {
	if metadata == nil || (metadata.TVDetails == nil && metadata.TVDBDetails == nil && metadata.KitsuDetails == nil) {
		return nil
	}
	primary := strings.TrimSpace(release.NormalizeTitleForSearchQuery(BuildSeriesSearchTitleFromMetadata(metadata, language)))
	if primary == "" {
		return nil
	}
	if includeYear {
		primary = AppendYearQuery(primary, SeriesYearFromMetadata(metadata))
	}
	switch config.NormalizeSeriesSearchScope(scope) {
	case config.SeriesSearchScopeSeasonEpisode:
		primary = AppendSeasonEpisodeQuery(primary, season, episode)
	case config.SeriesSearchScopeSeason:
		primary = AppendSeasonQuery(primary, season)
	}
	return AppendUniqueSearchQuery(nil, primary)
}

// AbsoluteEpisodeFromMetadata converts a season/episode request into the anime
// absolute episode number (e.g. One Piece S02E02 -> 63) by summing the episode
// counts of all prior seasons from TMDB metadata. Season 1 maps directly.
// Returns 0 when the number cannot be derived (missing metadata or gaps in the
// season list).
func AbsoluteEpisodeFromMetadata(metadata *ResolvedSearchMetadata, seasonStr, episodeStr string) int {
	season, _ := strconv.Atoi(seasonStr)
	episode, _ := strconv.Atoi(episodeStr)
	if season <= 0 || episode <= 0 {
		return 0
	}
	if season == 1 {
		return episode
	}
	if metadata == nil || metadata.TVDetails == nil {
		return 0
	}
	counts := make(map[int]int, len(metadata.TVDetails.Seasons))
	for _, seasonInfo := range metadata.TVDetails.Seasons {
		counts[seasonInfo.SeasonNumber] = seasonInfo.EpisodeCount
	}
	total := 0
	for prior := 1; prior < season; prior++ {
		count, ok := counts[prior]
		if !ok || count <= 0 {
			return 0
		}
		total += count
	}
	return total + episode
}

// animeTVCategory is the Newznab TV/Anime subcategory. Anime-only indexers
// (e.g. AnimeTosho) expose it as a flat category without expanding the 5000
// parent, so anime requests widen their category filter with it.
const animeTVCategory = "5070"

// AppendAnimeTVCategoryToEffective widens each effective indexer config's TV
// categories with the anime subcategory (Newznab cat is a comma-separated
// list). Empty category lists stay empty — no filter already matches anime.
func AppendAnimeTVCategoryToEffective(effective map[string]*config.IndexerSearchConfig) {
	for _, eff := range effective {
		if eff == nil || eff.TVCategories == nil || strings.TrimSpace(*eff.TVCategories) == "" {
			continue
		}
		widened := AppendSearchCategory(*eff.TVCategories, animeTVCategory)
		eff.TVCategories = &widened
	}
}

// AppendSearchCategory appends cat to a comma-separated category list unless
// it is already listed.
func AppendSearchCategory(categories, cat string) string {
	trimmed := strings.TrimSpace(categories)
	for _, part := range strings.Split(trimmed, ",") {
		if strings.TrimSpace(part) == cat {
			return trimmed
		}
	}
	return trimmed + "," + cat
}

// AbsoluteEpisodeForContent returns the anime absolute episode number for a
// series request, or 0 when it does not apply: movie content, non-anime
// metadata, or a number that cannot be derived. Kitsu-addressed requests
// already carry the absolute number as their episode.
func AbsoluteEpisodeForContent(contentType, kitsuID string, metadata *ResolvedSearchMetadata, season, episode string) int {
	if contentType != "series" {
		return 0
	}
	if kitsuID != "" {
		absolute, _ := strconv.Atoi(episode)
		return absolute
	}
	if !MetadataLooksLikeAnime(metadata, contentType) {
		return 0
	}
	return AbsoluteEpisodeFromMetadata(metadata, season, episode)
}

// RequestLooksLikeAnime reports whether a series request targets anime:
// Kitsu-addressed content is anime by definition, otherwise TMDB metadata
// decides.
func RequestLooksLikeAnime(params *SearchParams) bool {
	if params == nil {
		return false
	}
	if params.Req.KitsuID != "" {
		return true
	}
	return MetadataLooksLikeAnime(params.Metadata, params.ContentType)
}

// MetadataLooksLikeAnime treats anime as animation that is not originally in
// English: an explicit anime genre, or an animation genre on something made in
// another language. Without metadata it reports false, so a request is only
// ever promoted to anime on evidence.
func MetadataLooksLikeAnime(meta *ResolvedSearchMetadata, contentType string) bool {
	if meta == nil {
		return false
	}
	var genres []tmdb.Genre
	var originalLanguage string
	if strings.EqualFold(strings.TrimSpace(contentType), "movie") {
		if meta.MovieDetails == nil {
			return false
		}
		genres, originalLanguage = meta.MovieDetails.Genres, meta.MovieDetails.OriginalLanguage
	} else {
		if meta.TVDetails == nil {
			return false
		}
		genres, originalLanguage = meta.TVDetails.Genres, meta.TVDetails.OriginalLanguage
	}

	animation := false
	for _, genre := range genres {
		switch strings.ToLower(strings.TrimSpace(genre.Name)) {
		case "anime":
			return true
		case "animation":
			animation = true
		}
	}
	// TMDB reports ISO 639-1, so English is "en".
	return animation && !strings.EqualFold(strings.TrimSpace(originalLanguage), "en")
}

func SearchTitleLanguageForLog(language string) string {
	trimmed := config.NormalizeSearchTitleLanguage(language)
	if trimmed == "" {
		return "original"
	}
	return trimmed
}

func AppendSeasonEpisodeQuery(query, season, episode string) string {
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
