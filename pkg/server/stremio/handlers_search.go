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
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/session"
)

// searchFacts is what one request is, as the plans need to read it: the
// content kind's own shape, resolved once and shared by every plan.
type searchFacts struct {
	IsSeries   bool
	IsAnime    bool
	HasSeason  bool
	HasEpisode bool
	Absolute   int
	Class      string
}

// PlanContext hands the facts to the plan compiler, with the one fact the
// compiler asks for lazily.
func (f searchFacts) PlanContext(seasonCompleted func() bool) config.SearchPlanContext {
	ctx := config.SearchPlanContext{
		IsSeries:    f.IsSeries,
		HasSeason:   f.HasSeason,
		HasEpisode:  f.HasEpisode,
		IsAnime:     f.IsAnime,
		HasAbsolute: f.Absolute > 0,
	}
	if seasonCompleted != nil {
		ctx.SeasonCompleted = seasonCompleted()
	}
	return ctx
}

// searchFacts resolves them. The absolute episode number is the only one that
// costs anything (a walk of the prior seasons' episode counts), and it is what
// an absolute-numbered attempt asks by — and what lets acceptance recognise an
// absolute-numbered release whichever attempt surfaced it.
//
// Series-ness is decided the way the metadata layer decides it, not by the
// Stremio type alone: "anime" is a series type unless the Kitsu entry is a
// film. Keying on type == "series" here once sent every anime episode out as
// a movie search.
func (s *Server) searchFacts(contentType, streamLabel string, params *query.SearchParams) searchFacts {
	facts := searchFacts{IsSeries: contentType != "movie"}
	if params == nil {
		facts.Class = config.SearchClassFor(facts.IsSeries, false)
		return facts
	}
	facts.IsSeries = !query.MovieLike(params.Metadata, contentType)
	facts.IsAnime = query.RequestLooksLikeAnime(params)
	facts.HasSeason = strings.TrimSpace(params.Req.Season) != ""
	facts.HasEpisode = strings.TrimSpace(params.Req.Episode) != ""
	facts.Class = config.SearchClassFor(facts.IsSeries, facts.IsAnime)
	if !facts.IsSeries {
		return facts
	}
	// A Kitsu entry spanning a whole series resolves its absolute number up
	// front, where deriving one from season/episode would not work.
	absolute, _ := strconv.Atoi(params.Req.AbsoluteEpisode)
	if absolute <= 0 && facts.IsAnime {
		absolute = query.AbsoluteEpisodeFromMetadata(params.Metadata, params.Req.Season, params.Req.Episode)
	}
	if absolute > 0 {
		facts.Absolute = absolute
		logger.Debug("Absolute episode resolved",
			"stream", streamLabel,
			"season", params.Req.Season,
			"episode", params.Req.Episode,
			"absolute_episode", absolute,
		)
	}
	return facts
}

// absoluteEpisodeQueries builds the absolute-numbered text queries for one
// title language ("One Piece 63" for S02E02) — the naming anime indexers and
// fansub groups use.
func absoluteEpisodeQueries(params *query.SearchParams, language string, absolute int) []string {
	if params == nil || absolute <= 0 {
		return nil
	}
	var queries []string
	for _, title := range query.BuildSeriesQueriesFromMetadata(params.Metadata, language, false, "", "", config.SeriesSearchScopeNone) {
		if strings.TrimSpace(title) == "" {
			continue
		}
		queries = query.AppendUniqueSearchQuery(queries, fmt.Sprintf("%s %02d", title, absolute))
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
	rt := s.runtime()
	if params == nil || params.ContentIDs == nil {
		return newAvailContext(nil, 0)
	}
	contentIDs := params.ContentIDs
	if !streamUsesAvailNZB(stream) || rt.availClient == nil || rt.availClient.BaseURL == "" {
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
	availResult, _ := rt.availClient.GetReleases(contentIDs.ImdbID, params.Req.TMDBID, contentIDs.TvdbID, availSeason, availEpisode, s.indexerHostsForStream(stream), s.providerHostsForStream(stream))
	inputResults := 0
	if availResult != nil {
		inputResults = len(availResult.Releases)
	}
	return newAvailContext(availResult, inputResults)
}

func (s *Server) runConfiguredSearchRequests(ctx context.Context, contentType, id, streamLabel string, stream *auth.Stream, selectedQueries []string, params *query.SearchParams) ([]*release.Release, int, error) {
	rt := s.runtime()
	indexerReleases := make([]*release.Release, 0)
	executedRequests := 0
	// planFacts is what the plans cannot know about this request: whether it
	// is anime, whether an absolute episode number exists, and what season and
	// episode there are to aim at. Resolved once — every plan asks about the
	// same content.
	planFacts := s.searchFacts(contentType, streamLabel, params)
	// seasonCompleted is the one fact a plan pays for only if it asks: the
	// adaptive ordering reads it, and nothing else does. Resolved at most once
	// per search, since the plans run concurrently and they all ask about the
	// same season.
	var (
		seasonStateOnce sync.Once
		seasonDone      bool
	)
	seasonCompleted := func() bool {
		seasonStateOnce.Do(func() {
			done, known := s.seasonCompletedState(ctx, contentType, params.ContentIDs)
			seasonDone = done && known
			logger.Debug("Adaptive plan season state",
				"stream", streamLabel,
				"type", contentType,
				"id", id,
				"season_completed", seasonDone,
				"known", known,
			)
		})
		return seasonDone
	}
	// runAttempt executes one attempt of one plan: the plan with this attempt's
	// address, target, title language and year resolved, dispatched once per
	// query the attempt produces. It returns what the attempt matched, how many
	// indexer round trips it took, and any hard error. Empty with no error
	// means "this attempt matched nothing" — the cue to try the next one.
	runAttempt := func(plan *config.SearchQueryConfig, attempt config.SearchAttempt) ([]*release.Release, int, error) {
		executed := 0
		// Clone before stamping the labels: the base params are shared across
		// plans, and the plans run concurrently.
		base := cloneSearchParams(params)
		base.Req.StreamLabel = streamLabel
		base.Req.RequestLabel = plan.Name
		attemptParams, err := s.buildSearchParamsForAttempt(base, plan, attempt, planFacts)
		if err != nil {
			return nil, executed, err
		}
		req := &attemptParams.Req
		req.StreamLabel = streamLabel
		req.RequestLabel = plan.Name
		req.AttemptLabel = attempt.Label()
		req.ContentIsAnime = planFacts.IsAnime
		applyStreamIndexerSelection(req, stream)
		req.DisableResultFiltering = stream == nil || strings.TrimSpace(stream.FilterSortingMode) == "" || strings.EqualFold(strings.TrimSpace(stream.FilterSortingMode), "none") || streamUsesAIOStreamsProfile(stream)

		skip := func(reason string) ([]*release.Release, int, error) {
			logger.Debug("Skipping search attempt",
				"stream", streamLabel,
				"request", plan.Name,
				"attempt", attempt.Label(),
				"type", contentType,
				"id", id,
				"reason", reason,
			)
			return nil, executed, nil
		}
		if len(req.ValidationQueries) == 0 && strings.TrimSpace(req.ValidationQuery) == "" {
			return skip("no validation basis")
		}
		byID := config.NormalizeSearchAddress(attempt.Address) == config.SearchAddressID
		if byID && !hasUsableIDSearchIdentifier(*req, contentType) {
			return skip("no resolved metadata identifiers")
		}
		if !byID && !hasPreparedTextQueries(*req) {
			return skip("no prepared text queries")
		}

		effectiveLimit := req.Limit
		if plan.SearchResultLimit >= 0 {
			effectiveLimit = plan.SearchResultLimit
		}
		logAttemptConfig(streamLabel, contentType, id, plan, attempt, attemptParams, effectiveLimit)

		// An id attempt names an id and carries no query text; a title attempt
		// runs the queries prepared for it, which is one per title the attempt
		// resolved to (several only for an absolute-numbered attempt).
		queries := []string{req.Query}
		if !byID && len(attemptParams.PreparedQueries) > 0 {
			queries = attemptParams.PreparedQueries
		}
		collected := make([]*release.Release, 0)
		for _, text := range queries {
			dispatch := *req
			dispatch.Limit = effectiveLimit
			if !byID {
				dispatch.Query = text
			}
			executed++
			releases, runErr := search.RunIndexerSearches(ctx, rt.indexer, dispatch, validationContentType(attemptParams, contentType))
			if runErr != nil {
				return nil, executed, runErr
			}
			collected = append(collected, releases...)
		}
		return collected, executed, nil
	}

	// runOne walks one plan's attempts in order. The plan's own stop rule
	// decides whether it ends at the first attempt that matched: the fallback
	// is deliberately sequential, because its whole point is that the broader
	// indexer query is only paid for when the narrower one came back empty.
	runOne := func(plan *config.SearchQueryConfig) ([]*release.Release, int, error) {
		executed := 0
		attempts := plan.SearchPlanAttempts(planFacts.PlanContext(seasonCompleted))
		if len(attempts) == 0 {
			logger.Debug("Search request has no runnable attempts",
				"stream", streamLabel,
				"request", plan.Name,
				"type", contentType,
				"id", id,
			)
			return nil, executed, nil
		}
		firstHit := plan.StopsAtFirstHit()
		collected := make([]*release.Release, 0)
		for i, attempt := range attempts {
			releases, attemptExecuted, err := runAttempt(plan, attempt)
			executed += attemptExecuted
			if err != nil {
				return nil, executed, err
			}
			if len(releases) > 0 {
				if firstHit {
					return releases, executed, nil
				}
				collected = append(collected, releases...)
				continue
			}
			if i+1 < len(attempts) {
				logger.Debug("Search attempt found nothing; falling back",
					"stream", streamLabel,
					"request", plan.Name,
					"type", contentType,
					"id", id,
					"attempt", attempt.Label(),
					"fallback_attempt", attempts[i+1].Label(),
				)
			}
		}
		return collected, executed, nil
	}

	queries := make([]*config.SearchQueryConfig, 0, len(selectedQueries))
	for _, name := range selectedQueries {
		searchQuery := rt.config.GetSearchQueryByName(contentType, name)
		if searchQuery == nil {
			logger.Debug("Stream search query missing", "stream", streamLabel, "content_type", contentType, "id", id, "query", name)
			continue
		}
		queries = append(queries, searchQuery)
	}

	// Every selected plan runs and their results are merged. Sequencing lives
	// inside a plan — its attempts and stop rule are the fallback chain — so
	// a stream that wants "try this, then that" says so in one plan rather
	// than by ordering several. The plans share no state, so there is nothing
	// to gain from waiting for one before starting the next.
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

	return indexerReleases, executedRequests, nil
}

// uniqueIndexerHitsFrom counts, per indexer, the deduplicated releases that no
// other indexer had a copy of — content this indexer alone contributed to the
// result. It runs on the merged list, so the question it answers is "did any
// other indexer carry this release", not "was this the only indexer that
// answered the search": the old whole-search version credited nobody as soon as
// a second indexer returned anything, which on a multi-indexer setup is always.
func uniqueIndexerHitsFrom(releases []*release.Release) map[string]int {
	hits := make(map[string]int)
	for _, rel := range releases {
		if name, ok := soleIndexerOf(rel); ok {
			hits[name]++
		}
	}
	return hits
}

// soleIndexerOf returns the one indexer behind every copy of a release, or
// false when the copies span several indexers or name none at all.
//
// Library copies are skipped: a cached result is the same content coming back
// from disk rather than a second indexer having it, so it must not cancel the
// hit its own indexer earned.
func soleIndexerOf(rel *release.Release) (string, bool) {
	sole := ""
	for _, c := range rel.Copies() {
		if c == nil || c.IsLibraryResult() {
			continue
		}
		name := strings.TrimSpace(c.Indexer)
		if name == "" {
			continue
		}
		if sole == "" {
			sole = name
			continue
		}
		if !strings.EqualFold(sole, name) {
			return "", false
		}
	}
	return sole, sole != ""
}

// dedupeSearchResults collapses copies of the same release into one result.
//
// It replaces plain deduplication and runs everywhere, including on a single
// request's results: the old path sat those out because collapsing a duplicate
// meant discarding it, and a discarded copy is redundancy lost. Nothing is
// discarded here — the losers ride along on the winner — so there is no reason
// left to keep a duplicate in the list.
func (s *Server) dedupeSearchResults(streamLabel string, stream *auth.Stream, releases []*release.Release, availCtx *AvailContext) []*release.Release {
	inputResults := len(releases)
	merged := search.MergeSameReleaseVariants(releases, search.VariantMergeOptions{Rank: s.variantRank(stream, availCtx)})
	variants := 0
	for _, rel := range merged {
		variants += rel.CopyCount() - 1
	}
	logger.Debug("Stream variant merge",
		"stream", streamLabel,
		"input_results", inputResults,
		"final_results", len(merged),
		"variants_retained", variants,
	)
	return merged
}

// variantRank orders the copies of one release as playback targets: what this
// stream already has, then what the availability database says is there, then
// the indexer this stream prefers, with grabs and age breaking the rest.
//
// It is the merge's only opinion about which copy leads. Everything after it —
// filtering, ranking, formatting — sees one release, so a bad ordering here
// costs a failover hop rather than a result.
func (s *Server) variantRank(stream *auth.Stream, availCtx *AvailContext) func(*release.Release) int {
	rt := s.runtime()
	var byDetailsURL map[string]*availnzb.ReleaseWithStatus
	if availCtx != nil {
		byDetailsURL = availCtx.ByDetailsURL
	}
	var ourBackbones map[string]bool
	if rt.availClient != nil && len(byDetailsURL) > 0 {
		ourBackbones, _ = rt.availClient.OurBackbones(s.providerHostsForStream(stream))
	}
	indexerPriority := make(map[string]int)
	names := streamIndexerSelections(stream)
	if len(names) == 0 {
		for _, idx := range rt.config.Indexers {
			names = append(names, idx.Name)
		}
	}
	for i, name := range names {
		indexerPriority[strings.ToLower(strings.TrimSpace(name))] = len(names) - i
	}

	return func(rel *release.Release) int {
		if rel == nil {
			return 0
		}
		score := 0
		if rel.IsLibraryResult() {
			score += 1 << 20
		}
		state := availStateFor(byDetailsURL[rel.DetailsURL], ourBackbones)
		switch state.Status {
		case triage.AvailAvailable:
			score += 1 << 16
			if days := state.CheckedDaysAgo(); days >= 0 && days <= availRecentDays {
				score += 1 << 14
			}
		case triage.AvailUnavailable:
			score -= 1 << 18
		}
		if state.OnMyBackbone {
			score += 1 << 18
		}
		score += indexerPriority[strings.ToLower(strings.TrimSpace(rel.Indexer))]
		return score
	}
}

// availRecentDays is how fresh an availability record has to be to count as a
// confirmation rather than a memory when copies of one release are compared.
const availRecentDays = 30

func alignAvailContextWithSearch(availCtx *AvailContext, indexerReleases []*release.Release) *AvailContext {
	if availCtx == nil || availCtx.Result == nil {
		return newAvailContext(nil, 0)
	}
	indexerDetailsURLs := make(map[string]bool)
	for _, r := range indexerReleases {
		// Every copy, not just the primary: a variant's availability record is
		// what tells the merge which copy to lead with and the filter which
		// copies are worth failing over to.
		for _, c := range r.Copies() {
			if c != nil && c.DetailsURL != "" {
				indexerDetailsURLs[c.DetailsURL] = true
			}
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
	rt := s.runtime()
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

	libMode := rt.config.EffectiveLibrarySearchMode()
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
	indexerReleases = s.dedupeSearchResults(streamLabel, stream, indexerReleases, availCtx)
	variantsKept := 0
	for _, rel := range indexerReleases {
		if rel == nil {
			continue
		}
		variantsKept += rel.CopyCount() - 1
	}
	diag.From(ctx).SetDedup(dedupInput, len(indexerReleases), variantsKept)
	beforeBad := len(indexerReleases)
	indexerReleases = s.filterBadReleases(streamLabel, indexerReleases)
	diag.From(ctx).SetBadFiltered(beforeBad - len(indexerReleases))
	// Credited here rather than at merge time so a release nobody can play
	// does not earn its indexer a hit.
	s.addUniqueIndexerHits(uniqueIndexerHitsFrom(indexerReleases))
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
		for _, c := range r.Copies() {
			if c != nil && c.DetailsURL != "" {
				urls = append(urls, c.DetailsURL)
			}
		}
	}
	bad := badStore.BadSet(urls)
	if len(bad) == 0 {
		return releases
	}
	badURLs := make(map[string]bool, len(bad))
	for url := range bad {
		badURLs[url] = true
	}
	kept := releases[:0]
	droppedCopies := 0
	dropped := 0
	for _, r := range releases {
		if r == nil {
			continue
		}
		// A verdict is about one NZB, so it retires one copy. A release only
		// leaves the results once every copy of it is known bad.
		before := r.CopyCount()
		next, removed := search.DropCopies(r, badURLs)
		if removed {
			droppedCopies += before - next.CopyCount()
			logger.Debug("Filtered known-bad release copy from results", "stream", streamLabel, "title", r.Title, "url", r.DetailsURL)
		}
		if next == nil {
			dropped++
			continue
		}
		kept = append(kept, next)
	}
	if droppedCopies > 0 {
		logger.Info("Filtered known-bad releases from search results", "stream", streamLabel, "dropped", dropped, "dropped_copies", droppedCopies, "remaining", len(kept))
	}
	return kept
}

// libraryIndexerName renders the display name a library-backed release carries,
// crediting the indexer the item originally came from. Shared with the rebind
// pass in the playlist builder so a cached release that turns out to be in the
// library ends up labelled exactly as a fresh search would have labelled it.
func libraryIndexerName(indexerName string) string {
	name := strings.TrimSpace(indexerName)
	if name == "" || name == "Library" || name == "StreamNZB Library" {
		return "StreamNZB Library"
	}
	if strings.HasPrefix(name, "StreamNZB Library - ") {
		return name
	}
	return "StreamNZB Library - " + name
}

func convertLibraryItemToRelease(item *persistence.LibraryItem) *release.Release {
	if item == nil {
		return nil
	}

	return &release.Release{
		Title:         item.ReleaseTitle,
		Link:          item.DetailsURL,
		DetailsURL:    item.DetailsURL,
		Indexer:       libraryIndexerName(item.IndexerName),
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
	rt := s.runtime()
	const searchLimit = 0
	params := &query.SearchParams{
		ContentType:        contentType,
		ID:                 id,
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		Metadata:           &query.ResolvedSearchMetadata{},
	}
	// The base request carries no plan settings: every attempt resolves its
	// own from the plan it belongs to (see buildSearchParamsForAttempt). The
	// episode scope is the narrowest thing a series request could be after,
	// and is what the base stands for until an attempt says otherwise.
	req := indexer.SearchRequest{Limit: searchLimit, SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode}

	// Reading the id apart is pure string work with no clients in it, so it
	// lives in pkg/search/query where its arities can be tested directly.
	parsed := query.ParseContentID(id)
	kitsuID, kitsuEpisode := parsed.KitsuID, parsed.KitsuEpisode
	req.KitsuID = parsed.KitsuID
	req.IMDbID = parsed.IMDbID
	req.TMDBID = parsed.TMDBID
	req.TVDBID = parsed.TVDBID
	req.Season, req.Episode = parsed.Season, parsed.Episode

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
		if rt.tmdbClient == nil {
			logMetadataResolutionState(contentType, id, "tmdb_find", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tmdb_client_unconfigured")
		} else {
			findResp, findErr := rt.tmdbClient.Find(req.IMDbID, "imdb_id")
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
		if rt.tmdbClient == nil {
			logMetadataResolutionState(contentType, id, "tmdb_find_tvdb", "tvdb_id", req.TVDBID, "status", "skipped", "reason", "tmdb_client_unconfigured")
		} else {
			findResp, findErr := rt.tmdbClient.Find(req.TVDBID, "tvdb_id")
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
			if rt.tmdbClient == nil {
				logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "skipped", "reason", "tmdb_client_unconfigured")
			} else if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
				var tvDetails *tmdb.TVDetails
				var tvTranslations *tmdb.TVTranslationsResponse
				var tvAlternatives *tmdb.TVAlternativeTitlesResponse
				var tvExtIDs *tmdb.ExternalIDsResponse

				query.RunParallel(
					func() {
						if details, err := rt.tmdbClient.GetTVDetails(tmdbIDNum); err == nil {
							tvDetails = details
						} else {
							logMetadataResolutionState(contentType, id, "tmdb_series_details", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
						}
					},
					func() {
						if translations, err := rt.tmdbClient.GetTVTranslations(tmdbIDNum); err == nil {
							tvTranslations = translations
						} else {
							logMetadataResolutionState(contentType, id, "tmdb_series_translations", "tmdb_id", req.TMDBID, "status", "failed", "err", err)
						}
					},
					func() {
						if alternatives, err := rt.tmdbClient.GetTVAlternativeTitles(tmdbIDNum); err == nil {
							tvAlternatives = alternatives
						}
					},
					func() {
						if extIDs, err := rt.tmdbClient.GetExternalIDs(tmdbIDNum, "tv"); err == nil {
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
			if rt.tvdbClient == nil {
				logMetadataResolutionState(contentType, id, "tvdb_resolve", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tvdb_client_unconfigured")
			}
			if rt.tvdbClient != nil {
				if tvdbID, err := rt.tvdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID = tvdbID
				} else if err != nil {
					logMetadataResolutionState(contentType, id, "tvdb_resolve", "imdb_id", req.IMDbID, "status", "failed", "err", err)
				} else {
					logMetadataResolutionState(contentType, id, "tvdb_resolve", "imdb_id", req.IMDbID, "status", "empty")
				}
			}
			if req.TVDBID == "" && rt.tmdbClient != nil {
				if tvdbID, err := rt.tmdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID = tvdbID
				} else if err != nil {
					logMetadataResolutionState(contentType, id, "tmdb_resolve_tvdb", "imdb_id", req.IMDbID, "status", "failed", "err", err)
				} else {
					logMetadataResolutionState(contentType, id, "tmdb_resolve_tvdb", "imdb_id", req.IMDbID, "status", "empty")
				}
			} else if req.TVDBID == "" && rt.tmdbClient == nil {
				logMetadataResolutionState(contentType, id, "tmdb_resolve_tvdb", "imdb_id", req.IMDbID, "status", "skipped", "reason", "tmdb_client_unconfigured")
			}
		}
		if req.TMDBID == "" && req.TVDBID != "" && rt.tvdbClient != nil {
			if tvdbDetails, err := rt.tvdbClient.GetSeriesDetails(req.TVDBID); err == nil && tvdbDetails != nil {
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
	if movieLike && req.TMDBID != "" && rt.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
			var movieDetails *tmdb.MovieDetails
			var movieTranslations *tmdb.MovieTranslationsResponse
			var movieAlternatives *tmdb.MovieAlternativeTitlesResponse
			var movieExtIDs *tmdb.ExternalIDsResponse

			query.RunParallel(
				func() {
					if details, err := rt.tmdbClient.GetMovieDetails(tmdbIDNum); err == nil {
						movieDetails = details
					}
				},
				func() {
					if translations, err := rt.tmdbClient.GetMovieTranslations(tmdbIDNum); err == nil {
						movieTranslations = translations
					}
				},
				func() {
					if alternatives, err := rt.tmdbClient.GetMovieAlternativeTitles(tmdbIDNum); err == nil {
						movieAlternatives = alternatives
					}
				},
				func() {
					if extIDs, err := rt.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil {
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
	rt := s.runtime()
	if len(rt.config.Indexers) == 0 {
		return nil
	}
	out := make(map[string]*config.IndexerSearchConfig, len(rt.config.Indexers))
	for i := range rt.config.Indexers {
		ic := &rt.config.Indexers[i]
		if ic.Enabled != nil && !*ic.Enabled {
			continue
		}
		eff := config.MergeIndexerSearch(ic, queryIndexerConfig, rt.config)
		if strings.EqualFold(ic.Type, "easynews") {
			t := true
			eff.DisableIdSearch = &t
		}
		out[ic.Name] = eff
	}
	return out
}

// scopeForTarget maps a plan target onto the season/episode vocabulary the
// indexer clients speak: how much of a series a request is asking for.
func scopeForTarget(target string) string {
	switch target {
	case config.SearchTargetSeason:
		return config.SeriesSearchScopeSeason
	case config.SearchTargetSeries, config.SearchTargetAbsolute:
		return config.SeriesSearchScopeNone
	default:
		return config.SeriesSearchScopeSeasonEpisode
	}
}

// buildSearchParamsForAttempt turns one plan attempt into a dispatchable
// request. It is the single place a plan meets the wire: the attempt's address
// becomes the search mode, its target becomes the series scope, its title
// language and year build the query text, and the plan's acceptance builds the
// validation profiles. Nothing downstream re-reads the plan.
func (s *Server) buildSearchParamsForAttempt(base *query.SearchParams, plan *config.SearchQueryConfig, attempt config.SearchAttempt, facts searchFacts) (*query.SearchParams, error) {
	params := cloneSearchParams(base)
	if params == nil {
		return nil, fmt.Errorf("base search params are required")
	}
	contentType := params.ContentType
	req := &params.Req
	byID := config.NormalizeSearchAddress(attempt.Address) == config.SearchAddressID
	target := ""
	if facts.IsSeries {
		target = config.NormalizeSearchTarget(attempt.Target)
	}
	language := attempt.TitleLanguage()
	yearInQuery := attempt.YearInQuery()
	accept := plan.Acceptance()

	req.SeriesSearchScope = scopeForTarget(target)
	req.Class = facts.Class
	req.EnableYearValidation = accept.YearEnforced()
	req.AcceptPacks = accept.PacksEnabled()
	req.Query = ""
	req.SearchMode = "text"
	if byID {
		req.SearchMode = "id"
	}
	// The absolute number is acceptance, not addressing: it lets an
	// absolute-numbered release be recognised whichever attempt found it, so
	// every attempt of a plan that asks by absolute number carries it.
	req.AbsoluteEpisode = ""
	if facts.Absolute > 0 && plan.RunsAbsoluteAttempt() {
		req.AbsoluteEpisode = strconv.Itoa(facts.Absolute)
	}

	// Acceptance titles are the spellings a release may match. They are the
	// plan's, not the attempt's — what goes out and what may come back are
	// different questions — and fall back to the attempt's own language.
	validationLanguages := accept.AcceptTitles()
	if len(validationLanguages) == 0 {
		validationLanguages = []string{language}
	}
	req.ValidationQueryProfiles = query.ValidationQueryProfilesFromMetadata(params.Metadata, contentType, validationLanguages, accept.YearEnforced())
	req.ValidationQueries = query.ValidationQueriesFromProfiles(req.ValidationQueryProfiles)
	req.ValidationQuery = ""
	if len(req.ValidationQueries) > 0 {
		req.ValidationQuery = req.ValidationQueries[0]
	}

	req.EffectiveByIndexer = s.effectiveIndexerConfigs(plan.AsIndexerSearchConfigFor(attempt))
	params.PreparedQueries = nil
	params.AbsoluteQueries = nil

	if byID {
		// An id request names an id. The season/episode text is carried for
		// the backends that read it instead of the params (Easynews) and for
		// the request label in logs.
		if facts.IsSeries {
			switch target {
			case config.SearchTargetEpisode:
				req.Query = seasonEpisodeToken(req.Season, req.Episode)
			case config.SearchTargetSeason:
				req.Query = query.AppendSeasonQuery("", req.Season)
			}
		}
		return params, nil
	}

	if target == config.SearchTargetAbsolute {
		params.PreparedQueries = absoluteEpisodeQueries(params, language, facts.Absolute)
		if len(params.PreparedQueries) > 0 {
			req.Query = params.PreparedQueries[0]
		}
		return params, nil
	}

	queries := s.titleQueriesFor(params, contentType, language, yearInQuery, req.SeriesSearchScope)
	if len(queries) > 0 {
		params.PreparedQueries = append([]string(nil), queries...)
		req.Query = queries[0]
	}
	return params, nil
}

// seasonEpisodeToken is the S01E04 form of a season and episode, and "" when
// either is missing.
func seasonEpisodeToken(season, episode string) string {
	if season == "" || episode == "" {
		return ""
	}
	if seasonNum, err := strconv.Atoi(season); err == nil {
		if episodeNum, err := strconv.Atoi(episode); err == nil {
			return fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
		}
	}
	return fmt.Sprintf("S%sE%s", season, episode)
}

// titleQueriesFor builds the query text for a title attempt, memoized per
// (language, year, scope) on the shared params — several attempts of several
// plans ask under the same title.
func (s *Server) titleQueriesFor(params *query.SearchParams, contentType, language string, includeYear bool, scope string) []string {
	req := &params.Req
	cacheKey := fmt.Sprintf("%s|%t|%s", language, includeYear, scope)
	var queries []string
	if query.MovieLike(params.Metadata, contentType) {
		if cached, ok := params.MovieTitleQueries[cacheKey]; ok {
			return cached
		}
		queries = query.BuildMovieQueriesFromMetadata(params.Metadata, language, includeYear)
		if len(queries) > 0 {
			params.MovieTitleQueries[cacheKey] = queries
		}
		return queries
	}
	if req.Episode != "" || req.Season != "" {
		if cached, ok := params.SeriesTitleQueries[cacheKey]; ok {
			return cached
		}
		queries = query.BuildSeriesQueriesFromMetadata(params.Metadata, language, includeYear, req.Season, req.Episode, scope)
		if len(queries) > 0 {
			params.SeriesTitleQueries[cacheKey] = queries
		}
		return queries
	}
	return query.BuildSeriesQueriesFromMetadata(params.Metadata, language, includeYear, "", "", config.SeriesSearchScopeNone)
}

// logAttemptConfig records what one attempt is about to ask for. It is the
// plan read back at dispatch time, which is what a debug log is for.
func logAttemptConfig(streamLabel, contentType, id string, plan *config.SearchQueryConfig, attempt config.SearchAttempt, params *query.SearchParams, limit int) {
	accept := plan.Acceptance()
	attrs := []any{
		"stream", streamLabel,
		"request", plan.Name,
		"attempt", attempt.Label(),
		"type", contentType,
		"id", id,
		"class", params.Req.Class,
		"limit", searchLimitForLog(limit),
		"accept_titles", searchTitleLanguagesForLog(accept.AcceptTitles()),
		"accept_year", accept.YearEnforced(),
	}
	if language := attempt.TitleLanguage(); config.NormalizeSearchAddress(attempt.Address) == config.SearchAddressTitle {
		attrs = append(attrs, "title_language", query.SearchTitleLanguageForLog(language), "query_year", attempt.YearInQuery())
	}
	if config.SearchClassIsSeries(params.Req.Class) {
		attrs = append(attrs, "accept_packs", accept.PacksEnabled())
		if params.Req.AbsoluteEpisode != "" {
			attrs = append(attrs, "absolute_episode", params.Req.AbsoluteEpisode)
		}
	}
	logger.Debug("Search attempt", attrs...)
	if entries, ok := query.SearchRequestNormalisationLogEntries(params.Metadata, contentType, accept.AcceptTitles()); ok {
		for _, entry := range entries {
			logger.Debug("Search request normalisation",
				"stream", streamLabel,
				"request", plan.Name,
				"input_title", entry.InputTitle,
				"normalised_title", entry.NormalizedTitle,
				"title_languages", entry.Languages,
			)
		}
	}
}
