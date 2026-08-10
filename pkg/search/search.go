package search

import (
	"context"
	"fmt"
	"strings"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/diag"
)

func validationQueriesForRequest(req indexer.SearchRequest) []string {
	profiles := validationProfilesForRequest(req)
	if len(profiles) == 0 {
		return nil
	}
	queries := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		queries = append(queries, profile.Query)
	}
	return queries
}

func validationProfilesForRequest(req indexer.SearchRequest) []indexer.ValidationQueryProfile {
	if len(req.ValidationQueryProfiles) > 0 {
		profiles := make([]indexer.ValidationQueryProfile, 0, len(req.ValidationQueryProfiles))
		for _, profile := range req.ValidationQueryProfiles {
			query := strings.TrimSpace(profile.Query)
			if query == "" {
				continue
			}
			languages := make([]string, 0, len(profile.Languages))
			for _, language := range profile.Languages {
				trimmedLanguage := strings.TrimSpace(language)
				if trimmedLanguage == "" {
					continue
				}
				languages = append(languages, trimmedLanguage)
			}
			profiles = append(profiles, indexer.ValidationQueryProfile{
				Languages: languages,
				Query:     query,
			})
		}
		if len(profiles) > 0 {
			return profiles
		}
	}
	if len(req.ValidationQueries) > 0 {
		profiles := make([]indexer.ValidationQueryProfile, 0, len(req.ValidationQueries))
		for _, query := range req.ValidationQueries {
			trimmed := strings.TrimSpace(query)
			if trimmed == "" {
				continue
			}
			profiles = append(profiles, indexer.ValidationQueryProfile{Query: trimmed})
		}
		if len(profiles) > 0 {
			return profiles
		}
	}
	if trimmed := strings.TrimSpace(req.ValidationQuery); trimmed != "" {
		return []indexer.ValidationQueryProfile{{Query: trimmed}}
	}
	return nil
}

func RunIndexerSearches(ctx context.Context, idx indexer.Indexer, req indexer.SearchRequest, contentType string) ([]*release.Release, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if idx == nil {
		return nil, nil
	}

	runIDSearch := strings.EqualFold(strings.TrimSpace(req.SearchMode), "id")
	searchReq := req

	validationQueries := validationQueriesForRequest(req)
	if len(validationQueries) == 0 {
		mode := "text"
		if runIDSearch {
			mode = "id"
		}
		logger.Debug("Skipping search request without validation basis",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", mode,
		)
		return nil, nil
	}

	if !runIDSearch && strings.TrimSpace(req.Query) != "" {
		searchReq.SearchMode = "text"
	}
	if !runIDSearch && strings.TrimSpace(searchReq.Query) == "" {
		logger.Debug("Skipping search request without prepared text query",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
		)
		return nil, nil
	}

	filterAggregator := func(base indexer.Indexer, request indexer.SearchRequest, textMode bool) indexer.Indexer {
		agg, ok := base.(*indexer.Aggregator)
		if !ok {
			return base
		}
		filtered := make([]indexer.Indexer, 0, len(agg.GetIndexers()))
		for _, idxr := range agg.GetIndexers() {
			var overrides *config.IndexerSearchConfig
			if request.EffectiveByIndexer != nil {
				overrides = request.EffectiveByIndexer[idxr.Name()]
			}
			reqCopy := request
			reqCopy.EffectiveByIndexer = nil
			reqCopy.OptionalOverrides = overrides
			if textMode && reqCopy.Query != "" {
				reqCopy.SearchMode = "text"
			}
			if indexer.ShouldSkipIndexerForRequest(reqCopy, overrides) {
				continue
			}
			filtered = append(filtered, idxr)
		}
		if len(filtered) == 0 {
			return nil
		}
		return indexer.NewAggregator(filtered...)
	}

	// NOTE: Per-indexer DisableIdSearch / DisableStringSearch flags are enforced
	// inside the Aggregator.Search method as a fallback, but we prefilter here so only
	// relevant indexers participate in each request path.

	idxForMode := filterAggregator(idx, searchReq, !runIDSearch)
	if idxForMode == nil {
		return nil, nil
	}

	resp, err := idxForMode.Search(ctx, searchReq)
	if err != nil {
		mode := "text"
		if runIDSearch {
			mode = "id"
		}
		logger.Warn("Stream search failed",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", mode,
			"err", err,
		)
		return nil, fmt.Errorf("%s search failed for stream=%s request=%s: %w", mode, req.StreamLabel, req.RequestLabel, err)
	}
	indexer.NormalizeSearchResponse(resp)

	rawReleases := resp.Releases
	var releases []*release.Release
	for _, rel := range rawReleases {
		if rel != nil {
			if runIDSearch {
				rel.QuerySource = "id"
			} else {
				rel.QuerySource = "text"
			}
			releases = append(releases, rel)
		}
	}

	var valStats ValidationStats
	releases, valStats = ValidateSearchResultsWithStatsForQueries(releases, contentType, validationQueries, req.Season, req.Episode, req.AbsoluteEpisode, true, req.EnableYearValidation)
	diag.From(ctx).AddValidation(diag.ValidationStat{
		Request:      req.RequestLabel,
		Mode:         indexer.SearchModeLabel(searchReq.SearchMode),
		Raw:          valStats.RawResults,
		Kept:         valStats.FinalResults,
		DroppedTitle: valStats.DroppedTitle,
		DroppedYear:  valStats.DroppedYear,
	})
	// The per-profile pass re-validates ALL raw releases once per profile (a
	// full title parse each) purely to produce per-profile debug stats — skip
	// the whole sweep unless debug logging is actually on.
	if logger.DebugEnabled() {
		logPerProfileValidationStats(req, contentType, rawReleases, runIDSearch)
	}

	logger.Debug("Search request finished",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", indexer.SearchModeLabel(req.SearchMode),
		"raw_results", len(rawReleases),
		"final_results", len(releases),
	)
	return releases, nil
}

// logPerProfileValidationStats emits the per-validation-profile debug stats
// that used to run unconditionally on the hot path.
func logPerProfileValidationStats(req indexer.SearchRequest, contentType string, rawReleases []*release.Release, runIDSearch bool) {
	for _, profile := range validationProfilesForRequest(req) {
		_, profileStats := ValidateSearchResultsWithStatsForQueries(rawReleases, contentType, []string{profile.Query}, req.Season, req.Episode, req.AbsoluteEpisode, true, req.EnableYearValidation)
		validationAttrs := []any{
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", indexer.SearchModeLabel(req.SearchMode),
			"type", contentType,
			"raw_results", profileStats.RawResults,
			"final_results", profileStats.FinalResults,
			"rejected_results", profileStats.RejectedResults,
			"dropped_title", profileStats.DroppedTitle,
			"dropped_year", profileStats.DroppedYear,
			"validation_query", profile.Query,
		}
		if len(profile.Languages) == 1 {
			validationAttrs = append(validationAttrs, "title_language", profile.Languages[0])
		} else if len(profile.Languages) > 1 {
			validationAttrs = append(validationAttrs, "title_languages", profile.Languages)
		}
		if contentType == "series" {
			if req.AbsoluteEpisode != "" {
				validationAttrs = append(validationAttrs, "expected_absolute_episode", req.AbsoluteEpisode)
			}
			validationAttrs = append(validationAttrs,
				"scope", config.NormalizeSeriesSearchScope(req.SeriesSearchScope),
				"expected_season", profileStats.ExpectedSeason,
				"expected_episode", profileStats.ExpectedEpisode,
				"dropped_episode_request", profileStats.DroppedEpisodeRequest,
				"dropped_season", profileStats.DroppedSeason,
				"accepted_exact_episode", profileStats.AcceptedExactEpisode,
				"accepted_multi_episode", profileStats.AcceptedMultiEpisode,
				"accepted_season_pack", profileStats.AcceptedSeasonPack,
				"accepted_complete_pack", profileStats.AcceptedCompletePack,
				"accepted_season_match", profileStats.AcceptedSeasonMatch,
			)
		}
		logger.Debug("Search request validation", validationAttrs...)
	}
}
