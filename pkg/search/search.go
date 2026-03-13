package search

import (
	"fmt"
	"strconv"
	"sync"

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

func RunIndexerSearches(idx indexer.Indexer, tmdbClient TMDBResolver, req indexer.SearchRequest, contentType string, contentIDs *session.AvailReportMeta, imdbForText, tmdbForText string) ([]*release.Release, error) {
	idReq := req
	idReq.Query = ""
	idReq.PerIndexerQuery = nil

	var textReq *indexer.SearchRequest
	var filterQuery string

	usePerIndexerQuery := len(req.PerIndexerQuery) > 0

	if tmdbClient != nil {
		if contentType == "movie" && contentIDs != nil {
			if t, y, err := tmdbClient.GetMovieTitleAndYear(contentIDs.ImdbID, req.TMDBID); err == nil && t != "" {
				if y != "" {
					filterQuery = t + " " + y
				} else {
					filterQuery = t
				}
			}
		} else if contentType == "series" && req.Season != "" && req.Episode != "" {
			if name, err := tmdbClient.GetTVShowName(tmdbForText, imdbForText); err == nil && name != "" {
				seasonNum, _ := strconv.Atoi(req.Season)
				epNum, _ := strconv.Atoi(req.Episode)
				if seasonNum > 0 || epNum > 0 {
					filterQuery = fmt.Sprintf("%s S%02dE%02d", name, seasonNum, epNum)
				} else {
					filterQuery = fmt.Sprintf("%s S%sE%s", name, req.Season, req.Episode)
				}
			}
		}
	}

	if usePerIndexerQuery {
		textReq = &indexer.SearchRequest{
			Cat:                req.Cat,
			Limit:              req.Limit,
			EffectiveByIndexer: req.EffectiveByIndexer,
			PerIndexerQuery:    req.PerIndexerQuery,
		}
	} else if tmdbClient != nil {
		var textQuery string
		includeYear := true
		searchTitleLanguage := ""
		searchTitleNormalize := true
		if contentType == "movie" {
			if searchTitleLanguage != "" || searchTitleNormalize {
				if q, err := tmdbClient.GetMovieTitleForSearch(contentIDs.ImdbID, req.TMDBID, searchTitleLanguage, includeYear, searchTitleNormalize); err == nil {
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
			textReq = &indexer.SearchRequest{Query: textQuery, Cat: req.Cat, Limit: req.Limit, Season: req.Season, Episode: req.Episode}
		}
	}

	var idResp *indexer.SearchResponse
	var idErr error
	var textReleases []*release.Release
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		idResp, idErr = idx.Search(idReq)
	}()

	if textReq != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp, err := idx.Search(*textReq); err == nil {
				indexer.NormalizeSearchResponse(resp)
				textReleases = resp.Releases
			}
		}()
	}

	wg.Wait()

	if idErr != nil {
		return nil, fmt.Errorf("indexer search failed: %w", idErr)
	}
	indexer.NormalizeSearchResponse(idResp)

	combined := make([]*release.Release, 0, len(idResp.Releases)+len(textReleases))
	for _, rel := range idResp.Releases {
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
	if len(textReleases) > 0 {
		logger.Debug("Indexer dual search", "id", len(idResp.Releases), "text", len(textReleases))
	}

	if filterQuery != "" {
		merged = FilterResults(merged, contentType, filterQuery, req.Season, req.Episode)
	}
	return merged, nil
}
