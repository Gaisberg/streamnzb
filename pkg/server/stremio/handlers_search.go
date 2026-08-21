package stremio

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/session"
)

// prepareAbsoluteEpisodeSearch supplements an anime series request with
// absolute-numbered queries ("One Piece 63" for S02E02) — the naming anime
// indexers and fansub groups use — and marks the request so validation accepts
// absolute-numbered releases. The queries land in params.AbsoluteQueries and
// run as trailing text searches after the request's own queries. Returns false
// when the supplement does not apply: non-anime content, or an absolute number
// that is neither already resolved nor derivable from metadata.
func (s *Server) prepareAbsoluteEpisodeSearch(params *query.SearchParams, streamLabel string, searchQuery *config.SearchQueryConfig) bool {
	if params == nil || params.ContentType != "series" {
		return false
	}
	req := &params.Req
	skip := func(reason string) bool {
		logger.Debug("Skipping absolute-episode search supplement",
			"stream", streamLabel,
			"request", req.RequestLabel,
			"reason", reason,
		)
		return false
	}
	// A Kitsu entry spanning a whole series resolves its absolute number up
	// front, where deriving one from season/episode would not work.
	absolute, _ := strconv.Atoi(req.AbsoluteEpisode)
	if absolute <= 0 {
		if !query.RequestLooksLikeAnime(params) {
			return skip("content not detected as anime")
		}
		absolute = query.AbsoluteEpisodeFromMetadata(params.Metadata, req.Season, req.Episode)
		if absolute <= 0 {
			return skip("absolute episode not derivable from metadata")
		}
		// The absolute number lets validation accept absolute-numbered
		// releases regardless of which query surfaced them.
		req.AbsoluteEpisode = strconv.Itoa(absolute)
	}

	language := ""
	if searchQuery != nil {
		language = config.NormalizeSearchTitleLanguage(searchQuery.SearchTitleLanguage)
	}
	var queries []string
	for _, title := range query.BuildSeriesQueriesFromMetadata(params.Metadata, language, false, "", "", config.SeriesSearchScopeNone) {
		if strings.TrimSpace(title) == "" {
			continue
		}
		queries = query.AppendUniqueSearchQuery(queries, fmt.Sprintf("%s %02d", title, absolute))
	}
	if len(queries) == 0 {
		return skip("no titles available for absolute query")
	}

	params.AbsoluteQueries = queries
	logger.Debug("Absolute-episode search supplement prepared",
		"stream", streamLabel,
		"request", req.RequestLabel,
		"season", req.Season,
		"episode", req.Episode,
		"absolute_episode", absolute,
		"queries", len(queries),
	)
	return true
}

func logMetadataLookup(streamLabel, contentType, id string) {
	logger.Debug("Get metadata",
		"stream", streamLabel,
		"type", contentType,
		"id", id,
	)
}

func logMetadataLookupFinished(streamLabel, contentType, id string, params *query.SearchParams) {
	originalTitle := query.MetadataOriginalTitle(params.Metadata, contentType)
	attrs := []any{
		"stream", streamLabel,
		"type", contentType,
		"id", id,
		"imdb_id", params.ContentIDs.ImdbID,
		"tmdb_id", params.Req.TMDBID,
		"original_title", originalTitle,
		"year", query.MetadataDisplayYear(params.Metadata, contentType),
		"languages", query.MetadataLanguageCount(params.Metadata, contentType),
	}
	if alternativeTitle := query.MetadataAlternativeTitle(params.Metadata, contentType); alternativeTitle != "" {
		attrs = append(attrs, "alternative_title", alternativeTitle)
	} else if fallbackTitle := query.MetadataFallbackTitle(params.Metadata, contentType); fallbackTitle != "" {
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
		"providers", stream.ActiveProviderSelections(),
		"indexers", append([]string(nil), stream.IndexerSelections...),
		"requests", append([]string(nil), selectedQueries...),
	)
}

func searchTitleLanguagesForLog(languages []string) []string {
	normalized := config.NormalizeSearchTitleLanguages(languages)
	if len(normalized) == 0 {
		return []string{"original"}
	}
	values := make([]string, 0, len(normalized))
	for _, language := range normalized {
		values = append(values, query.SearchTitleLanguageForLog(language))
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

func (s *Server) loadAvailContext(params *query.SearchParams, stream *auth.Stream) *AvailContext {
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

func (s *Server) runConfiguredSearchRequests(ctx context.Context, contentType, id, streamLabel string, stream *auth.Stream, selectedQueries []string, params *query.SearchParams) ([]*release.Release, int, error) {
	indexerReleases := make([]*release.Release, 0)
	executedRequests := 0
	// runOne executes a single configured request. It returns the releases that
	// request produced, how many indexer round trips it took, and any hard
	// error. An empty result with no error means "this request matched nothing"
	// — for a fallback chain that is the cue to try the next one.
	runOne := func(searchQuery *config.SearchQueryConfig) ([]*release.Release, int, error) {
		executedRequests := 0
		var collected []*release.Release
		// Clone before stamping the labels: the base params are shared across
		// requests, and in combine mode these run concurrently.
		base := cloneSearchParams(params)
		base.Req.StreamLabel = streamLabel
		base.Req.RequestLabel = searchQuery.Name
		profileParams, profileErr := s.buildSearchParamsFromBase(base, searchQuery)
		if profileErr != nil {
			return nil, executedRequests, profileErr
		}
		profileParams.Req.StreamLabel = streamLabel
		profileParams.Req.RequestLabel = searchQuery.Name
		// Per-indexer content_scope routes on this flag; stamped here so every
		// query variant of the request carries it.
		profileParams.Req.ContentIsAnime = query.RequestLooksLikeAnime(profileParams)
		applyStreamIndexerSelection(&profileParams.Req, stream)
		profileParams.Req.DisableResultFiltering = stream == nil || strings.TrimSpace(stream.FilterSortingMode) == "" || strings.EqualFold(strings.TrimSpace(stream.FilterSortingMode), "none") || streamUsesAIOStreamsProfile(stream)
		searchMode := strings.ToLower(strings.TrimSpace(searchQuery.SearchMode))
		if contentType == "series" && searchQuery.TryAbsoluteEpisodeEnabled() {
			if profileParams.Req.ContentIsAnime {
				// Widen every variant of this anime request to also match the
				// anime subcategory on indexers that don't expand the 5000
				// parent. Kitsu requests are anime even though they skip the
				// absolute query supplement below (already absolute).
				query.AppendAnimeTVCategoryToEffective(profileParams.Req.EffectiveByIndexer)
			}
			s.prepareAbsoluteEpisodeSearch(profileParams, streamLabel, searchQuery)
		}
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
			return nil, executedRequests, nil
		}
		titleLanguages := query.ValidationTitleLanguages(searchQuery.SearchMode, searchQuery.SearchTitleLanguage, searchQuery.SearchTitleLanguages)
		if searchMode == "id" && !hasUsableIDSearchIdentifier(profileParams.Req, contentType) {
			logger.Debug("Skipping search request without resolved metadata identifiers",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"type", contentType,
				"id", id,
			)
			return nil, executedRequests, nil
		}
		if searchMode != "id" && !hasPreparedTextQueries(profileParams.Req) {
			logger.Debug("Skipping search request without prepared text queries",
				"stream", streamLabel,
				"request", searchQuery.Name,
				"type", contentType,
				"id", id,
			)
			return nil, executedRequests, nil
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
			configAttrs = append(configAttrs, "title_language", query.SearchTitleLanguageForLog(searchQuery.SearchTitleLanguage))
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
		if entries, ok := query.SearchRequestNormalisationLogEntries(
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
		type searchVariant struct {
			query    string
			absolute bool
		}
		variants := make([]searchVariant, 0, len(profileParams.PreparedQueries)+len(profileParams.AbsoluteQueries)+1)
		if searchMode == "id" || len(profileParams.PreparedQueries) == 0 {
			variants = append(variants, searchVariant{query: profileParams.Req.Query})
		} else {
			for _, q := range profileParams.PreparedQueries {
				variants = append(variants, searchVariant{query: q})
			}
		}
		for _, q := range profileParams.AbsoluteQueries {
			variants = append(variants, searchVariant{query: q, absolute: true})
		}
		// An id-mode query's per-indexer config disables text search, which
		// would skip every indexer for the text-mode absolute variants; those
		// variants run with the query's text-mode equivalent instead.
		var absoluteEffective map[string]*config.IndexerSearchConfig
		if searchMode == "id" && len(profileParams.AbsoluteQueries) > 0 {
			textQuery := *searchQuery
			textQuery.SearchMode = "text"
			absoluteEffective = s.effectiveIndexerConfigs(textQuery.AsIndexerSearchConfig())
			query.AppendAnimeTVCategoryToEffective(absoluteEffective)
		}
		requestReleases := make([]*release.Release, 0)
		for _, variant := range variants {
			reqVariant := profileParams.Req
			reqVariant.Limit = effectiveLimit
			if searchMode != "id" || variant.absolute {
				reqVariant.Query = variant.query
			}
			if variant.absolute && searchMode == "id" {
				reqVariant.SearchMode = "text"
				reqVariant.EffectiveByIndexer = absoluteEffective
			}
			executedRequests++
			releases, runErr := search.RunIndexerSearches(ctx, s.indexer, reqVariant, validationContentType(profileParams, contentType))
			if runErr != nil {
				return nil, executedRequests, runErr
			}
			if len(releases) > 0 {
				requestReleases = append(requestReleases, releases...)
			}
			if streamCombinesResults(stream) {
				collected = append(collected, releases...)
				continue
			}
			if len(releases) > 0 {
				if idxName, ok := singleIndexerFromReleases(requestReleases); ok {
					s.addUniqueIndexerHits(map[string]int{idxName: 1})
				}
				return releases, executedRequests, nil
			}
		}
		return collected, executedRequests, nil
	}

	queries := make([]*config.SearchQueryConfig, 0, len(selectedQueries))
	for _, name := range selectedQueries {
		searchQuery := s.config.GetSearchQueryByName(contentType, name)
		if searchQuery == nil {
			logger.Debug("Stream search query missing", "stream", streamLabel, "content_type", contentType, "id", id, "query", name)
			continue
		}
		queries = append(queries, searchQuery)
	}

	if !streamCombinesResults(stream) {
		// Fallback chain: the requests are ordered by preference and the first
		// one that matches anything wins, so running later requests would be
		// work whose result is thrown away.
		for _, searchQuery := range queries {
			releases, executed, err := runOne(searchQuery)
			executedRequests += executed
			if err != nil {
				return nil, executedRequests, err
			}
			if len(releases) > 0 {
				return releases, executedRequests, nil
			}
		}
		return indexerReleases, executedRequests, nil
	}

	// Combine mode runs every request regardless, and they share no state, so
	// there is nothing to gain from waiting for one before starting the next.
	type queryOutcome struct {
		releases []*release.Release
		executed int
		err      error
	}
	outcomes := make([]queryOutcome, len(queries))
	var wg sync.WaitGroup
	for i, searchQuery := range queries {
		wg.Add(1)
		go func(i int, searchQuery *config.SearchQueryConfig) {
			defer wg.Done()
			releases, executed, err := runOne(searchQuery)
			outcomes[i] = queryOutcome{releases: releases, executed: executed, err: err}
		}(i, searchQuery)
	}
	wg.Wait()

	// Merged in configured order so results stay stable regardless of which
	// request happened to finish first.
	for _, outcome := range outcomes {
		executedRequests += outcome.executed
		if outcome.err != nil {
			return nil, executedRequests, outcome.err
		}
		indexerReleases = append(indexerReleases, outcome.releases...)
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
	streamLabel := streamLogName(stream)
	logger.Debug("Building playback candidates",
		"stream", streamLabel,
		"type", contentType,
		"id", id,
		"requests", len(selectedQueries),
	)
	logMetadataLookup(streamLabel, contentType, id)
	logMetadataLookupFinished(streamLabel, contentType, id, params)
	if !query.HasUsableResolvedMetadata(params, contentType) {
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

	// Air-date gate: a positively-unaired episode gets an instant empty result
	// instead of a full indexer fan-out that cannot find anything. Strictly
	// failure-open — only a trusted source saying "airs in the future" gates.
	if aired, window, known := s.episodeAiredState(ctx, stream, contentType, params.ContentIDs); known && !aired {
		reportAt, timeKnown := window.reportAt()
		logger.Info("Episode has not aired yet; skipping search",
			"stream", streamLabel,
			"type", contentType,
			"id", id,
			"airs_at", reportAt.Format(time.RFC3339),
			"air_time_known", timeKnown,
			"gate_opens_at", window.opensAt.Format(time.RFC3339),
		)
		diag.From(ctx).SetUnaired(reportAt.UTC().Format(time.RFC3339), timeKnown)
		return &rawSearchResult{
			Params:  params,
			Unaired: true,
			Air:     window,
			Avail: &AvailContext{
				ByDetailsURL:            make(map[string]*availnzb.ReleaseWithStatus),
				AvailableByDetailsURL:   make(map[string]bool),
				UnavailableByDetailsURL: make(map[string]bool),
			},
		}, nil
	}

	// Certification gate: content over the stream profile's parental cap gets
	// an instant empty result, same shape as the unaired gate. Deliberately
	// fail-closed on unknown certifications (unless the profile allows
	// unrated) — this is a parental control, unlike the failure-open checks
	// around it. A stream with no metadata profile bound is uncapped.
	if cap, capped := capForProfile(s.metadataProfileFor(stream)); capped {
		certAge, certKnown := s.resolveSearchCertification(contentType, params)
		if params.Metadata != nil {
			params.Metadata.Certification = &query.ResolvedCertification{Age: certAge, Known: certKnown}
		}
		if !cap.Allows(certAge, certKnown) {
			reason := fmt.Sprintf("age %d over cap %d", certAge, cap.MaxAge)
			if !certKnown {
				reason = "certification unknown (cap fails closed)"
			}
			logger.Info("Content blocked by certification cap; skipping search",
				"stream", streamLabel,
				"type", contentType,
				"id", id,
				"reason", reason,
			)
			diag.From(ctx).SetCertificationBlocked(reason)
			return &rawSearchResult{
				Params: params,
				Avail: &AvailContext{
					ByDetailsURL:            make(map[string]*availnzb.ReleaseWithStatus),
					AvailableByDetailsURL:   make(map[string]bool),
					UnavailableByDetailsURL: make(map[string]bool),
				},
			}, nil
		}
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
	indexerReleases, executedRequests, err := s.runConfiguredSearchRequests(ctx, contentType, id, streamLabel, stream, selectedQueries, params)
	availCtx := <-availChan
	if err != nil {
		return nil, err
	}
	if len(libraryReleases) > 0 && libMode == "combine" {
		indexerReleases = append(libraryReleases, indexerReleases...)
	} else if len(indexerReleases) == 0 && len(libraryReleases) > 0 && libMode == "fallback_only" {
		indexerReleases = libraryReleases
	}
	dedupInput := len(indexerReleases)
	indexerReleases = dedupeCombinedSearchResults(streamLabel, stream, indexerReleases, executedRequests)
	diag.From(ctx).SetDedup(dedupInput, len(indexerReleases))
	beforeBad := len(indexerReleases)
	indexerReleases = s.filterBadReleases(streamLabel, indexerReleases)
	diag.From(ctx).SetBadFiltered(beforeBad - len(indexerReleases))
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

// validationContentType maps a request's catalog type onto the validator's
// movie/series split. The validator skips types it does not know, and "anime"
// covers films and episodic entries alike — a film must validate as a movie,
// or episode-titled series releases pass ("Avatar ... S01E05 Spirited Away"
// matches a "Spirited Away" text query). Episodic anime deliberately keeps
// its unvalidated behaviour: fansub naming (romaji titles, absolute
// numbering) does not reliably match the resolved metadata titles.
func validationContentType(params *query.SearchParams, contentType string) string {
	if params != nil && query.MovieLike(params.Metadata, contentType) {
		return "movie"
	}
	return contentType
}

func logMetadataResolutionState(contentType, requestID, resolver string, attrs ...any) {
	base := []any{
		"type", contentType,
		"id", requestID,
		"resolver", resolver,
	}
	logger.Debug("Metadata resolution", append(base, attrs...)...)
}

func (s *Server) buildSearchParamsBase(contentType, id string, searchQuery *config.SearchQueryConfig) (*query.SearchParams, error) {
	const searchLimit = 0
	params := &query.SearchParams{
		ContentType:        contentType,
		ID:                 id,
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		Metadata:           &query.ResolvedSearchMetadata{},
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

	// Kitsu addresses anime per entry — usually one season, often one cour —
	// and numbers episodes within that entry, while Kitsu's own mappings carry
	// neither the season nor the offset (most modern entries map only to
	// AniList/MAL). anime-lists supplies both, turning kitsu:49016:3 into the
	// S03E15 that releases are actually named by. A mapped request is an
	// ordinary series request from here on, so it takes the same TMDB/TVDB
	// path as the equivalent tt id instead of the Kitsu-title fallback below.
	kitsuMapped := false
	if kitsuID != "" && s.animeLists != nil {
		if mapping, ok := s.animeLists.LookupKitsu(kitsuID); ok {
			if req.IMDbID == "" && mapping.IMDbID != "" {
				req.IMDbID = mapping.IMDbID
			}
			if req.TVDBID == "" && mapping.TVDBID != "" {
				req.TVDBID = mapping.TVDBID
			}
			if req.TMDBID == "" && mapping.TMDBID != "" {
				req.TMDBID = mapping.TMDBID
			}
			kitsuMapped = req.IMDbID != "" || req.TVDBID != "" || req.TMDBID != ""

			entryEpisode, _ := strconv.Atoi(kitsuEpisode)
			if season, episode, resolved := mapping.ResolveEpisode(entryEpisode); resolved {
				req.Season = strconv.Itoa(season)
				req.Episode = strconv.Itoa(episode)
			} else if mapping.SpansSeries() && entryEpisode > 0 {
				// The entry covers the whole series, so the requested number
				// is already the series' absolute episode.
				req.AbsoluteEpisode = kitsuEpisode
			}
			logMetadataResolutionState(contentType, id, "anime_lists", "kitsu_id", kitsuID,
				"status", "success",
				"imdb_id", req.IMDbID, "tvdb_id", req.TVDBID, "tmdb_id", req.TMDBID,
				"season", req.Season, "episode", req.Episode,
				"absolute_episode", req.AbsoluteEpisode)
		} else {
			logMetadataResolutionState(contentType, id, "anime_lists", "kitsu_id", kitsuID,
				"status", "empty", "ready", s.animeLists.Ready())
		}
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

	// Every Kitsu request fetches the entry details: the entry's subtype is the
	// only signal that tells a film apart from episodic anime — the Stremio
	// type is "anime" for both — and the movie/series split below depends on
	// it. The response is cached, so mapped requests pay one lookup at most.
	if kitsuID != "" && s.kitsuClient != nil {
		if details, err := s.kitsuClient.GetAnimeDetails(context.Background(), kitsuID); err == nil && details != nil {
			logMetadataResolutionState(contentType, id, "kitsu_details", "kitsu_id", kitsuID, "status", "success", "title", details.CanonicalTitle, "show_type", details.ShowType)
			// A film's Kitsu titles are the film's actual titles, so they may
			// drive queries and validation. Mapped episodic entries keep Kitsu
			// titles out of the metadata: per-entry titles ("Fire Force Season
			// 3 Part 2") match release names far less often than the series
			// title does, so those are only the last resort when anime-lists
			// could not place the entry.
			if strings.EqualFold(details.ShowType, "movie") || !kitsuMapped {
				params.Metadata.KitsuDetails = details
			}
			if !kitsuMapped {
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
			}
		} else if err != nil {
			logMetadataResolutionState(contentType, id, "kitsu_details", "kitsu_id", kitsuID, "status", "failed", "err", err)
		}
	}

	// movieLike folds the Kitsu subtype into the movie/series split, and must
	// gate every id resolution below: TMDB and TVDB movie ids live in
	// namespaces separate from their TV ids, so resolving a film through the
	// series endpoints lands on whatever unrelated show shares the number.
	movieLike := query.MovieLike(params.Metadata, contentType)

	if req.TMDBID == "" && req.IMDbID != "" {
		if s.tmdbClient == nil {
			logMetadataResolutionState(contentType, id, "tmdb_find", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tmdb_client_unconfigured")
		} else {
			findResp, findErr := s.tmdbClient.Find(req.IMDbID, "imdb_id")
			if findErr != nil {
				logMetadataResolutionState(contentType, id, "tmdb_find", "imdb_id", req.IMDbID, "status", "failed", "err", findErr)
			} else {
				resolved := ""
				if movieLike && len(findResp.MovieResults) > 0 {
					resolved = strconv.Itoa(findResp.MovieResults[0].ID)
				}
				if !movieLike && (contentType == "series" || contentType == "anime" || contentType == "tv" || contentType == "documentary" || contentType == "other") && len(findResp.TVResults) > 0 {
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

	isSeriesLike := !movieLike && (contentType == "series" || contentType == "anime" || contentType == "tv" || (req.Season != "" && req.Episode != ""))
	if isSeriesLike {
		if req.TMDBID != "" {
			if s.tmdbClient == nil {
				logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "skipped", "reason", "tmdb_client_unconfigured")
			} else if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
				var tvDetails *tmdb.TVDetails
				var tvTranslations *tmdb.TVTranslationsResponse
				var tvAlternatives *tmdb.TVAlternativeTitlesResponse
				var tvExtIDs *tmdb.ExternalIDsResponse

				query.RunParallel(
					func() {
						if details, err := s.tmdbClient.GetTVDetails(tmdbIDNum); err == nil {
							tvDetails = details
						} else {
							logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
						}
					},
					func() {
						if translations, err := s.tmdbClient.GetTVTranslations(tmdbIDNum); err == nil {
							tvTranslations = translations
						} else {
							logMetadataResolutionState(contentType, id, "tmdb_series_translations", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
						}
					},
					func() {
						if alternatives, err := s.tmdbClient.GetTVAlternativeTitles(tmdbIDNum); err == nil {
							tvAlternatives = alternatives
						}
					},
					func() {
						if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "tv"); err == nil {
							tvExtIDs = extIDs
						} else {
							logMetadataResolutionState(contentType, id, "tmdb_series_external_ids", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
						}
					},
				)

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
	contentIDs.AbsoluteEpisode = query.AbsoluteEpisodeForContent(contentType, req.AbsoluteEpisode, params.Metadata, req.Season, req.Episode)
	if movieLike && req.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
			var movieDetails *tmdb.MovieDetails
			var movieTranslations *tmdb.MovieTranslationsResponse
			var movieAlternatives *tmdb.MovieAlternativeTitlesResponse
			var movieExtIDs *tmdb.ExternalIDsResponse

			query.RunParallel(
				func() {
					if details, err := s.tmdbClient.GetMovieDetails(tmdbIDNum); err == nil {
						movieDetails = details
					}
				},
				func() {
					if translations, err := s.tmdbClient.GetMovieTranslations(tmdbIDNum); err == nil {
						movieTranslations = translations
					}
				},
				func() {
					if alternatives, err := s.tmdbClient.GetMovieAlternativeTitles(tmdbIDNum); err == nil {
						movieAlternatives = alternatives
					}
				},
				func() {
					if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil {
						movieExtIDs = extIDs
					}
				},
			)

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
	params.ContentTitle = query.MetadataDisplayTitle(params.Metadata, contentType)
	return params, nil
}

func cloneSearchParams(base *query.SearchParams) *query.SearchParams {
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
	next.AbsoluteQueries = append([]string(nil), base.AbsoluteQueries...)
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

// effectiveIndexerConfigs merges each enabled indexer's search settings with a
// search query's overrides. Nil when no indexers are configured.
func (s *Server) effectiveIndexerConfigs(queryIndexerConfig *config.IndexerSearchConfig) map[string]*config.IndexerSearchConfig {
	if len(s.config.Indexers) == 0 {
		return nil
	}
	out := make(map[string]*config.IndexerSearchConfig, len(s.config.Indexers))
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
		out[ic.Name] = eff
	}
	return out
}

func (s *Server) buildSearchParamsFromBase(base *query.SearchParams, searchQuery *config.SearchQueryConfig) (*query.SearchParams, error) {
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
	validationLanguages := query.ValidationTitleLanguages(searchMode, searchTitleLanguage, searchTitleLanguages)
	req.ValidationQueryProfiles = query.ValidationQueryProfilesFromMetadata(params.Metadata, contentType, validationLanguages, includeYear)
	req.ValidationQueries = query.ValidationQueriesFromProfiles(req.ValidationQueryProfiles)
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
				req.Query = query.AppendSeasonQuery("", req.Season)
			}
		}
	}

	req.EffectiveByIndexer = s.effectiveIndexerConfigs(queryIndexerConfig)
	if searchMode != "id" {
		var queries []string
		cacheKey := fmt.Sprintf("%s|%t|%s", searchTitleLanguage, includeYear, scope)
		if query.MovieLike(params.Metadata, contentType) {
			if cached, ok := params.MovieTitleQueries[cacheKey]; ok {
				queries = cached
			} else {
				queries = query.BuildMovieQueriesFromMetadata(params.Metadata, searchTitleLanguage, includeYear)
				if len(queries) > 0 {
					params.MovieTitleQueries[cacheKey] = queries
				}
			}
		} else if req.Episode != "" || req.Season != "" {
			if cached, ok := params.SeriesTitleQueries[cacheKey]; ok {
				queries = cached
			} else {
				queries = query.BuildSeriesQueriesFromMetadata(params.Metadata, searchTitleLanguage, includeYear, req.Season, req.Episode, scope)
				if len(queries) > 0 {
					params.SeriesTitleQueries[cacheKey] = queries
				}
			}
		} else {
			queries = query.BuildSeriesQueriesFromMetadata(params.Metadata, searchTitleLanguage, includeYear, "", "", config.SeriesSearchScopeNone)
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
