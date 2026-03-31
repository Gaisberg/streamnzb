package search

import (
	"fmt"
	"strconv"
	"sync"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
)

type TMDBResolver interface {
	GetMovieTitle(imdbID, tmdbID string) (string, error)
	GetMovieTitleAndYear(imdbID, tmdbID string) (title, year string, err error)
	GetMovieTitleForSearch(imdbID, tmdbID, language string, includeYear, normalize bool) (string, error)
	GetTVShowName(tmdbID, imdbID string) (string, error)
}

type SearchConfig interface {
	GetSearchTitleLanguage() string
}

// BuildFilterQuery resolves the expected title (and year for movies) from TMDB
// metadata so that FilterResults can reject releases whose title doesn't match.
// The returned string is empty when TMDB metadata is unavailable.
func BuildFilterQuery(tmdbClient TMDBResolver, req indexer.SearchRequest, contentType string, contentIDs *session.AvailReportMeta, imdbForText, tmdbForText string) string {
	if tmdbClient == nil {
		return ""
	}
	if contentType == "movie" && contentIDs != nil {
		if t, y, err := tmdbClient.GetMovieTitleAndYear(contentIDs.ImdbID, req.TMDBID); err == nil && t != "" {
			if y != "" {
				return t + " " + y
			}
			return t
		}
	} else if contentType == "series" && req.Season != "" && req.Episode != "" {
		if name, err := tmdbClient.GetTVShowName(tmdbForText, imdbForText); err == nil && name != "" {
			seasonNum, _ := strconv.Atoi(req.Season)
			epNum, _ := strconv.Atoi(req.Episode)
			if seasonNum > 0 || epNum > 0 {
				return fmt.Sprintf("%s S%02dE%02d", name, seasonNum, epNum)
			}
			return fmt.Sprintf("%s S%sE%s", name, req.Season, req.Episode)
		}
	}
	return ""
}

func RunIndexerSearches(idx indexer.Indexer, tmdbClient TMDBResolver, req indexer.SearchRequest, contentType string, contentIDs *session.AvailReportMeta, imdbForText, tmdbForText string, cfg SearchConfig) ([]*release.Release, error) {
	if idx == nil {
		return nil, nil
	}

	runIDSearch := req.ForceIDSearch || (req.Query == "" && len(req.PerIndexerQuery) == 0 && (req.IMDbID != "" || req.TVDBID != "" || req.TMDBID != ""))
	var idReq indexer.SearchRequest
	if runIDSearch {
		idReq = req
		if !idReq.ForceIDSearch {
			idReq.Query = ""
		}
		idReq.PerIndexerQuery = nil
	}

	var textReq *indexer.SearchRequest
	filterQuery := req.FilterQuery
	if filterQuery == "" {
		filterQuery = BuildFilterQuery(tmdbClient, req, contentType, contentIDs, imdbForText, tmdbForText)
	}

	usePerIndexerQuery := len(req.PerIndexerQuery) > 0

	if usePerIndexerQuery {
		textReq = &indexer.SearchRequest{
			Cat:                    req.Cat,
			Limit:                  req.Limit,
			IMDbID:                 req.IMDbID,
			TMDBID:                 req.TMDBID,
			TVDBID:                 req.TVDBID,
			EffectiveByIndexer:     req.EffectiveByIndexer,
			PerIndexerQuery:        req.PerIndexerQuery,
			UseSeasonEpisodeParams: req.UseSeasonEpisodeParams,
			ForceIDSearch:          false,
			IndexerMode:            req.IndexerMode,
			StreamLabel:            req.StreamLabel,
			RequestLabel:           req.RequestLabel,
		}
		if req.UseSeasonEpisodeParams {
			textReq.Season = req.Season
			textReq.Episode = req.Episode
		}
	} else if !req.ForceIDSearch && tmdbClient != nil && cfg != nil {
		var textQuery string
		includeYear := true
		searchTitleLanguage := cfg.GetSearchTitleLanguage()
		if contentType == "movie" {
			if searchTitleLanguage != "" {
				if q, err := tmdbClient.GetMovieTitleForSearch(contentIDs.ImdbID, req.TMDBID, searchTitleLanguage, includeYear, false); err == nil {
					textQuery = q
				}
			} else if includeYear {
				if t, y, err := tmdbClient.GetMovieTitleAndYear(contentIDs.ImdbID, req.TMDBID); err == nil {
					if y != "" {
						textQuery = t + " " + y
					} else {
						textQuery = t
					}
				}
			} else {
				if t, err := tmdbClient.GetMovieTitle(contentIDs.ImdbID, req.TMDBID); err == nil {
					textQuery = t
				}
			}
		} else if req.Season != "" && req.Episode != "" {
			if name, err := tmdbClient.GetTVShowName(tmdbForText, imdbForText); err == nil {
				textQuery = name
			}
		}
		if textQuery != "" {
			textReq = &indexer.SearchRequest{
				Query:                  textQuery,
				Cat:                    req.Cat,
				Limit:                  req.Limit,
				IMDbID:                 req.IMDbID,
				TMDBID:                 req.TMDBID,
				TVDBID:                 req.TVDBID,
				UseSeasonEpisodeParams: req.UseSeasonEpisodeParams,
				ForceIDSearch:          false,
				IndexerMode:            req.IndexerMode,
				StreamLabel:            req.StreamLabel,
				RequestLabel:           req.RequestLabel,
			}
			if req.UseSeasonEpisodeParams {
				textReq.Season = req.Season
				textReq.Episode = req.Episode
			}
		}
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
			if textMode && request.PerIndexerQuery != nil && len(request.PerIndexerQuery[idxr.Name()]) == 0 {
				continue
			}
			reqCopy := request
			reqCopy.EffectiveByIndexer = nil
			reqCopy.PerIndexerQuery = nil
			reqCopy.OptionalOverrides = overrides
			if textMode {
				if queries := request.PerIndexerQuery[idxr.Name()]; len(queries) > 0 {
					reqCopy.Query = queries[0]
				} else if reqCopy.Query == "" {
					reqCopy.Query = "__prepared_text_query__"
				}
				reqCopy.ForceIDSearch = false
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

	var idResp *indexer.SearchResponse
	var idErr error
	var textReleases []*release.Release
	var textErr error
	var wg sync.WaitGroup

	if runIDSearch {
		idIdx := filterAggregator(idx, idReq, false)
		if idIdx != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				idResp, idErr = idIdx.Search(idReq)
			}()
		} else {
			idResp = &indexer.SearchResponse{}
		}
	}

	if textReq != nil {
		textIdx := filterAggregator(idx, *textReq, true)
		if textIdx != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if resp, err := textIdx.Search(*textReq); err == nil {
					indexer.NormalizeSearchResponse(resp)
					textReleases = resp.Releases
				} else {
					textErr = err
				}
			}()
		} else {
			textReleases = nil
		}
	}

	wg.Wait()

	if idErr != nil {
		return nil, fmt.Errorf("indexer search failed: %w", idErr)
	}
	if textErr != nil {
		logger.Warn("Stream text search failed",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"err", textErr,
		)
	}
	if idResp != nil {
		indexer.NormalizeSearchResponse(idResp)
	}

	var idReleases []*release.Release
	if idResp != nil {
		idReleases = idResp.Releases
	}

	combined := make([]*release.Release, 0, len(idReleases)+len(textReleases))
	for _, rel := range idReleases {
		if rel != nil {
			rel.QuerySource = "id"
			combined = append(combined, rel)
		}
	}
	for _, rel := range textReleases {
		if rel != nil {
			rel.QuerySource = "text"
			combined = append(combined, rel)
		}
	}

	merged := MergeAndDedupeSearchResults(combined)
	switch {
	case len(textReleases) > 0 && len(idReleases) > 0:
		logger.Debug("Search request finished",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", "id+text",
			"final_results", len(merged),
			"id_results", len(idReleases),
			"text_results", len(textReleases),
		)
	case len(textReleases) > 0:
		logger.Debug("Search request finished",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", "text",
			"final_results", len(merged),
			"raw_results", len(textReleases),
		)
	case len(idReleases) > 0:
		logger.Debug("Search request finished",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", "id",
			"final_results", len(merged),
			"raw_results", len(idReleases),
		)
	default:
		logger.Debug("Search request finished",
			"stream", req.StreamLabel,
			"request", req.RequestLabel,
			"mode", func() string {
				if req.ForceIDSearch {
					return "id"
				}
				return "text"
			}(),
			"final_results", 0,
		)
	}

	if filterQuery != "" {
		merged = FilterResults(merged, contentType, filterQuery, req.Season, req.Episode)
	}
	return merged, nil
}
