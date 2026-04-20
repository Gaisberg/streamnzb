package stremio

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
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
	MovieDetails           *tmdb.MovieDetails
	MovieTranslations      *tmdb.MovieTranslationsResponse
	MovieAlternativeTitles *tmdb.MovieAlternativeTitlesResponse
	TVDetails              *tmdb.TVDetails
	TVTranslations         *tmdb.TVTranslationsResponse
	TVAlternativeTitles    *tmdb.TVAlternativeTitlesResponse
}

func metadataDisplayTitle(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
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
	return strings.TrimSpace(metadataDisplayTitle(params.Metadata, contentType)) != ""
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
	return title
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
	if metadata == nil || metadata.MovieDetails == nil {
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
	if metadata == nil || metadata.MovieDetails == nil || len(metadata.MovieDetails.ReleaseDate) < 4 {
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
	if metadata == nil || metadata.TVDetails == nil {
		return ""
	}
	title := strings.TrimSpace(metadata.TVDetails.Name)
	if useOriginalTitleLanguage(language) {
		title = preferredSeriesOriginalTitle(metadata)
	} else if localized := localizedTVTitleForLanguage(metadata.TVTranslations, language); localized != "" {
		title = localized
	}
	if title == "" {
		title = strings.TrimSpace(metadata.TVDetails.Name)
	}
	if title == "" {
		return ""
	}
	return title
}

func seriesYearFromMetadata(metadata *resolvedSearchMetadata) string {
	if metadata == nil || metadata.TVDetails == nil || len(metadata.TVDetails.FirstAirDate) < 4 {
		return ""
	}
	return metadata.TVDetails.FirstAirDate[:4]
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

func buildSeriesOriginalQueryFromMetadata(metadata *resolvedSearchMetadata) string {
	return strings.TrimSpace(preferredSeriesOriginalTitle(metadata))
}

func searchRequestNormalisationLogValues(metadata *resolvedSearchMetadata, contentType, language string, includeYear bool, season, episode, scope string) (string, string, string, bool) {
	var originalTitle string
	var selectedTitle string
	if contentType == "movie" {
		originalTitle = buildMovieOriginalQueryFromMetadata(metadata)
		selectedTitle = buildMovieSearchQueryFromMetadata(metadata, language, false)
	} else {
		originalTitle = buildSeriesOriginalQueryFromMetadata(metadata)
		selectedTitle = buildSeriesSearchTitleFromMetadata(metadata, language)
	}
	if selectedTitle == "" {
		return "", "", "", false
	}

	normalized := strings.TrimSpace(release.NormalizeTitleForSearchQuery(selectedTitle))
	if normalized == "" || strings.EqualFold(normalized, selectedTitle) {
		return "", "", "", false
	}
	return strings.TrimSpace(originalTitle), selectedTitle, normalized, true
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
	if metadata == nil || metadata.TVDetails == nil {
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
	trimmed := strings.TrimSpace(language)
	if trimmed == "" {
		return "original"
	}
	return trimmed
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
	availResult, _ := s.availClient.GetReleases(contentIDs.ImdbID, params.Req.TMDBID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, s.indexerHostsForStream(stream), s.providerHostsForStream(stream))
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
		if strings.TrimSpace(profileParams.Req.ValidationQuery) == "" {
			logger.Debug("Skipping search request without validation basis",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"type", contentType,
				"id", id,
			)
			continue
		}
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
			"title_language", searchTitleLanguageForLog(searchQuery.SearchTitleLanguage),
			"year", profileParams.Req.EnableYearValidation,
			"extra_terms", searchQuery.ExtraSearchTerms,
			"limit", searchLimitForLog(effectiveLimit),
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
		logIncludeYear := true
		if searchQuery.IncludeYear != nil {
			logIncludeYear = *searchQuery.IncludeYear
		}
		if originalTitle, inputTitle, normalizedTitle, ok := searchRequestNormalisationLogValues(
			profileParams.Metadata,
			contentType,
			searchQuery.SearchTitleLanguage,
			logIncludeYear,
			profileParams.Req.Season,
			profileParams.Req.Episode,
			profileParams.Req.SeriesSearchScope,
		); ok {
			logger.Debug("Search request normalisation",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"original_title", originalTitle,
				"input_title", inputTitle,
				"normalised_title", normalizedTitle,
			)
		}
		queryVariants := profileParams.PreparedQueries
		if searchMode == "id" || len(queryVariants) == 0 {
			queryVariants = []string{profileParams.Req.Query}
		}
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
			if streamCombinesResults(stream) {
				indexerReleases = append(indexerReleases, releases...)
				continue
			}
			if len(releases) > 0 {
				return releases, executedRequests, nil
			}
		}
	}
	return indexerReleases, executedRequests, nil
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
	availCtx := s.loadAvailContext(params, stream)
	indexerReleases, executedRequests, err := s.runConfiguredSearchRequests(contentType, id, streamLabel, stream, selectedQueries, params)
	if err != nil {
		return nil, err
	}
	indexerReleases = dedupeCombinedSearchResults(streamLabel, stream, indexerReleases, executedRequests)
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

	searchID := id
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if parts[0] == "tmdb" && len(parts) >= 4 {
			searchID = parts[1]
			req.Season, req.Episode = parts[2], parts[3]
		} else if len(parts) >= 3 {
			searchID = parts[0]
			req.Season, req.Episode = parts[1], parts[2]
		} else if len(parts) > 0 {
			searchID = parts[0]
		}
	} else if strings.HasPrefix(id, "tmdb:") {
		searchID = strings.TrimPrefix(id, "tmdb:")
	}
	if strings.HasPrefix(searchID, "tt") {
		req.IMDbID = searchID
	} else if looksLikeTMDBID(searchID) {
		req.TMDBID = searchID
	}
	imdbForText := req.IMDbID
	tmdbForText := req.TMDBID
	if contentType == "series" && strings.Contains(id, ":") {
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
				if contentType == "series" && len(findResp.TVResults) > 0 {
					resolved = strconv.Itoa(findResp.TVResults[0].ID)
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

	if contentType == "series" {
		if req.TMDBID != "" {
			if s.tmdbClient == nil {
				logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "skipped", "reason", "tmdb_client_unconfigured")
			} else if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
				if details, err := s.tmdbClient.GetTVDetails(tmdbIDNum); err == nil {
					params.Metadata.TVDetails = details
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
				}
				if translations, err := s.tmdbClient.GetTVTranslations(tmdbIDNum); err == nil {
					params.Metadata.TVTranslations = translations
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_series_translations", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
				}
				if alternatives, err := s.tmdbClient.GetTVAlternativeTitles(tmdbIDNum); err == nil {
					params.Metadata.TVAlternativeTitles = alternatives
				}
				if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "tv"); err == nil {
					if extIDs.TVDBID != 0 {
						req.TVDBID = strconv.Itoa(extIDs.TVDBID)
					}
					if extIDs.IMDbID != "" && req.IMDbID == "" {
						req.IMDbID = extIDs.IMDbID
						imdbForText = extIDs.IMDbID
					}
					if req.TVDBID == "" {
						logMetadataResolutionState(contentType, id, "tmdb_series_external_ids", "tmdb_id", req.TMDBID, "status", "empty")
					}
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_series_external_ids", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
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
	}
	seasonNum, _ := strconv.Atoi(req.Season)
	episodeNum, _ := strconv.Atoi(req.Episode)
	contentIDs := &session.AvailReportMeta{ImdbID: req.IMDbID, TmdbID: req.TMDBID, TvdbID: req.TVDBID, Season: seasonNum, Episode: episodeNum}
	if contentType == "movie" && req.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
			if details, err := s.tmdbClient.GetMovieDetails(tmdbIDNum); err == nil {
				params.Metadata.MovieDetails = details
			}
			if translations, err := s.tmdbClient.GetMovieTranslations(tmdbIDNum); err == nil {
				params.Metadata.MovieTranslations = translations
			}
			if alternatives, err := s.tmdbClient.GetMovieAlternativeTitles(tmdbIDNum); err == nil {
				params.Metadata.MovieAlternativeTitles = alternatives
			}
			if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil && extIDs.IMDbID != "" && contentIDs.ImdbID == "" {
				contentIDs.ImdbID = extIDs.IMDbID
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
	includeYear := true
	scope := config.NormalizeSeriesSearchScope(req.SeriesSearchScope)
	var queryIndexerConfig *config.IndexerSearchConfig
	if searchQuery != nil {
		searchMode = strings.ToLower(strings.TrimSpace(searchQuery.SearchMode))
		searchTitleLanguage = strings.TrimSpace(searchQuery.SearchTitleLanguage)
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
	if contentType == "movie" {
		req.ValidationQuery = buildMovieValidationQueryFromMetadata(params.Metadata, searchTitleLanguage, includeYear)
	} else {
		req.ValidationQuery = buildSeriesValidationQueryFromMetadata(params.Metadata, searchTitleLanguage, includeYear)
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
		} else if req.Season != "" && req.Episode != "" {
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
	if season == "" || episode == "" {
		return strings.TrimSpace(query)
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
