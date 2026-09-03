package newznab

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
)

// searchTimeout bounds one fan-out. Each indexer already has its own timeout;
// this is the backstop that keeps a client's request from outliving the
// client's patience when several of them stall at once.
const searchTimeout = 60 * time.Second

// defaultCategories are the categories a function implies when the client
// named none. They decide which of an indexer's configured category lists
// applies, so they matter even though they are not forwarded as-is.
var defaultCategories = map[string]string{
	"movie":    "2000",
	"tvsearch": "5000",
	"music":    "3000",
	"book":     "7000",
}

// animeCategory is the standard TV/Anime id. A query aimed at it is the only
// signal a proxied request carries about anime, and per-indexer content scopes
// are decided from it.
const animeCategory = "5070"

// handleSearch serves every search function. The client's query is forwarded
// verbatim to each indexer and the merged results come back unranked and
// unfiltered — a Newznab client parses release titles itself and wants to see
// everything the indexers offered.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, function string) {
	query := r.URL.Query()
	merged := s.mergedCaps()
	limit := clampLimit(query.Get("limit"), merged.Limits)
	offset := parseInt(query.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	effectiveCat := strings.TrimSpace(query.Get("cat"))
	if effectiveCat == "" {
		effectiveCat = defaultCategories[function]
	}

	forwarded := forwardedParams(query)
	if offset > 0 {
		forwarded.Set("offset", strconv.Itoa(offset))
	}

	text := strings.TrimSpace(query.Get("q"))
	season := strings.TrimSpace(query.Get("season"))
	episode := strings.TrimSpace(query.Get("ep"))
	// Only a query with ids and no text is an id search. A query with neither
	// is a plain listing (an RSS sync), and calling that an id search would
	// drop every indexer whose id search is switched off.
	searchMode := "text"
	if text == "" && carriesSearchID(query) {
		searchMode = "id"
	}

	req := indexer.SearchRequest{
		Query:              text,
		IMDbID:             strings.TrimSpace(query.Get("imdbid")),
		TMDBID:             strings.TrimSpace(query.Get("tmdbid")),
		TVDBID:             strings.TrimSpace(query.Get("tvdbid")),
		Cat:                effectiveCat,
		Limit:              limit,
		Season:             season,
		Episode:            episode,
		ContentIsAnime:     categoriesIncludeAnime(effectiveCat),
		Class:              newznabClassFor(function, categoriesIncludeAnime(effectiveCat)),
		SeriesSearchScope:  seriesScopeFor(function, season, episode),
		SearchMode:         searchMode,
		IndexerMode:        "combine",
		EffectiveByIndexer: s.effectiveByIndexer(),
		StreamLabel:        "newznab",
		RequestLabel:       "newznab " + function,
		Passthrough:        &indexer.PassthroughQuery{Function: function, Params: forwarded},
	}

	idx := s.currentIndexer()
	startedAt := time.Now()
	var items []indexer.Item
	if idx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
		defer cancel()
		resp, err := idx.Search(ctx, req)
		if err != nil {
			logger.Warn("Newznab search failed", "function", function, "err", err)
			s.writeError(w, r, http.StatusBadGateway, errUnknownError, "Search failed")
			return
		}
		if resp != nil {
			items = resp.Channel.Items
		}
	}

	sortItemsByDate(items)
	if len(items) > limit {
		items = items[:limit]
	}

	base := s.baseURL(r)
	result := newFeed(s.selfURL(r), base, offset)
	result.Channel.Items = buildFeedItems(items, linkerFor(s.grabSecret(), base, s.apiKey()))
	// Newznab's total is the size of the whole result set. A fan-out cannot
	// know that, and overstating it makes clients page into nothing, so it
	// reports what has actually been served up to this page.
	result.Channel.Response.Total = offset + len(result.Channel.Items)

	logger.Info("Newznab search",
		"function", function,
		"mode", indexer.SearchModeLabel(searchMode),
		"query", text,
		"cat", effectiveCat,
		"season", season,
		"episode", episode,
		"limit", limit,
		"offset", offset,
		"results", len(result.Channel.Items),
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, result.toJSON())
		return
	}
	writeXML(w, http.StatusOK, rssMediaType, result)
}

// forwardedParams copies the search parameters this endpoint proxies. The
// caller's credentials, output format and paging are deliberately left behind:
// each indexer gets its own.
func forwardedParams(query url.Values) url.Values {
	forwarded := url.Values{}
	for key, values := range query {
		name := strings.ToLower(strings.TrimSpace(key))
		if !forwardableParams[name] {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				continue
			}
			forwarded.Add(name, value)
		}
	}
	return forwarded
}

// searchIDParams are the identifier parameters that make a query an id search.
var searchIDParams = []string{"imdbid", "tmdbid", "tvdbid", "tvmazeid", "traktid", "rid"}

// carriesSearchID reports whether the query names any content identifier.
func carriesSearchID(query url.Values) bool {
	for _, name := range searchIDParams {
		if strings.TrimSpace(query.Get(name)) != "" {
			return true
		}
	}
	return false
}

// newznabClassFor is the content class a proxied query is after. A passthrough
// query carries its own cat and keeps it; the class is what the non-Newznab
// backends read instead.
func newznabClassFor(function string, anime bool) string {
	if function != "tvsearch" {
		if function == "movie" {
			return config.SearchClassMovie
		}
		return ""
	}
	return config.SearchClassFor(true, anime)
}

// seriesScopeFor describes how much of a series a TV query is after, which is
// what non-Newznab backends (Easynews) read instead of the season/ep params.
func seriesScopeFor(function, season, episode string) string {
	if function != "tvsearch" || season == "" {
		return config.SeriesSearchScopeNone
	}
	if episode == "" {
		return config.SeriesSearchScopeSeason
	}
	return config.SeriesSearchScopeSeasonEpisode
}

// categoriesIncludeAnime reports whether a category list names TV/Anime, the
// one hint a proxied query gives about anime content.
func categoriesIncludeAnime(cat string) bool {
	for _, part := range strings.Split(cat, ",") {
		if strings.TrimSpace(part) == animeCategory {
			return true
		}
	}
	return false
}

// clampLimit resolves the requested page size against the merged limits: the
// advertised default when the client asks for nothing, never more than the
// advertised maximum.
func clampLimit(raw string, limits indexer.CapsLimits) int {
	limit := parseInt(raw, 0)
	if limit <= 0 {
		limit = limits.Default
	}
	if limit <= 0 {
		limit = 100
	}
	if limits.Max > 0 && limit > limits.Max {
		limit = limits.Max
	}
	return limit
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}
