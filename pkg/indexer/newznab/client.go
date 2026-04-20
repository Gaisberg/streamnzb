package newznab

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL string
	apiPath string
	apiKey  string
	name    string
	client  *http.Client
	cfg     config.IndexerConfig
	caps    *indexer.Caps

	apiLimit          int
	apiUsed           int
	apiRemaining      int
	downloadLimit     int
	downloadUsed      int
	downloadRemaining int
	usageManager      *indexer.UsageManager
	requestLimiter    *indexer.RequestLimiter
	mu                sync.RWMutex
}

var orderedSearchQueryKeys = []string{
	"apikey",
	"t",
	"cat",
	"imdbid",
	"tmdbid",
	"tvdbid",
	"rid",
	"season",
	"ep",
	"q",
	"offset",
	"limit",
	"o",
}

func encodeOrderedQuery(params url.Values, orderedKeys []string) string {
	if len(params) == 0 {
		return ""
	}

	pairs := make([]string, 0, len(params))
	seen := make(map[string]struct{}, len(params))
	appendKey := func(key string) {
		values, ok := params[key]
		if !ok {
			return
		}
		seen[key] = struct{}{}
		escapedKey := url.QueryEscape(key)
		if len(values) == 0 {
			pairs = append(pairs, escapedKey+"=")
			return
		}
		for _, value := range values {
			pairs = append(pairs, escapedKey+"="+url.QueryEscape(value))
		}
	}

	for _, key := range orderedKeys {
		appendKey(key)
	}

	extraKeys := make([]string, 0, len(params))
	for key := range params {
		if _, ok := seen[key]; ok {
			continue
		}
		extraKeys = append(extraKeys, key)
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		appendKey(key)
	}

	return strings.Join(pairs, "&")
}

var _ indexer.Indexer = (*Client)(nil)
var _ indexer.IndexerWithCaps = (*Client)(nil)

type APIError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Newznab"
}

func (c *Client) Type() string {
	if c.cfg.Type != "" {
		return c.cfg.Type
	}
	return "newznab"
}

func (c *Client) GetUsage() indexer.Usage {
	usageData := c.refreshUsageFromManager()

	c.mu.RLock()
	u := indexer.Usage{
		APIHitsLimit:       c.apiLimit,
		APIHitsUsed:        c.apiUsed,
		APIHitsRemaining:   c.apiRemaining,
		DownloadsLimit:     c.downloadLimit,
		DownloadsUsed:      c.downloadUsed,
		DownloadsRemaining: c.downloadRemaining,
	}
	c.mu.RUnlock()
	if usageData != nil {
		u.AllTimeAPIHitsUsed = usageData.AllTimeAPIHitsUsed
		u.AllTimeDownloadsUsed = usageData.AllTimeDownloadsUsed
	}
	return u
}

func (c *Client) refreshUsageFromManager() *indexer.UsageData {
	if c.usageManager == nil {
		return nil
	}

	ud := c.usageManager.GetIndexerUsage(c.name)
	c.mu.Lock()
	c.apiUsed = ud.APIHitsUsed
	c.downloadUsed = ud.DownloadsUsed
	if c.apiLimit > 0 {
		c.apiRemaining = c.apiLimit - c.apiUsed
		if c.apiRemaining < 0 {
			c.apiRemaining = 0
		}
	}
	if c.downloadLimit > 0 {
		c.downloadRemaining = c.downloadLimit - c.downloadUsed
		if c.downloadRemaining < 0 {
			c.downloadRemaining = 0
		}
	}
	c.mu.Unlock()

	return ud
}

func NewClient(cfg config.IndexerConfig, um *indexer.UsageManager) *Client {

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	apiPath := cfg.APIPath
	if apiPath == "" {
		apiPath = "/api"
	}

	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}

	c := &Client{
		name:    cfg.Name,
		baseURL: strings.TrimRight(cfg.URL, "/"),
		apiPath: apiPath,
		apiKey:  cfg.APIKey,
		cfg:     cfg,
		client: &http.Client{
			Timeout:   cfg.EffectiveTimeout(),
			Transport: transport,
		},
		apiLimit:          cfg.APIHitsDay,
		apiUsed:           0,
		apiRemaining:      cfg.APIHitsDay,
		downloadLimit:     cfg.DownloadsDay,
		downloadUsed:      0,
		downloadRemaining: cfg.DownloadsDay,
		usageManager:      um,
		requestLimiter:    indexer.NewRequestLimiter(cfg.RateLimitRPS),
	}

	if um != nil {
		usage := um.GetIndexerUsage(cfg.Name)
		c.apiUsed = usage.APIHitsUsed
		c.downloadUsed = usage.DownloadsUsed

		c.apiRemaining = cfg.APIHitsDay - usage.APIHitsUsed
		c.downloadRemaining = cfg.DownloadsDay - usage.DownloadsUsed

		if c.apiRemaining < 0 && cfg.APIHitsDay > 0 {
			c.apiRemaining = 0
		}
		if c.downloadRemaining < 0 && cfg.DownloadsDay > 0 {
			c.downloadRemaining = 0
		}
	}

	return c
}

func (c *Client) checkAPILimit() error {
	c.refreshUsageFromManager()

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.apiLimit > 0 && c.apiRemaining <= 0 {
		return fmt.Errorf("API limit reached for %s", c.Name())
	}
	return nil
}

func (c *Client) checkDownloadLimit() error {
	c.refreshUsageFromManager()

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.downloadLimit > 0 && c.downloadRemaining <= 0 {
		return fmt.Errorf("download limit reached for %s", c.Name())
	}
	return nil
}

func (c *Client) updateUsageFromHeaders(h http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if val := h.Get("X-RateLimit-Daily-Limit"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			c.apiLimit = limit
		}
	}
	if val := h.Get("X-RateLimit-Daily-Remaining"); val != "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.apiRemaining = remaining
		}
	}

	if val := h.Get("X-DNZBLimit-Daily-Limit"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			c.downloadLimit = limit
		}
	}
	if val := h.Get("X-DNZBLimit-Daily-Remaining"); val != "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.downloadRemaining = remaining
		}
	}

	if val := h.Get("x-api-remaining"); val != "" && h.Get("X-RateLimit-Daily-Remaining") == "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.apiRemaining = remaining
		}
	}
	if val := h.Get("x-grab-remaining"); val != "" && h.Get("X-DNZBLimit-Daily-Remaining") == "" {
		if remaining, err := strconv.Atoi(val); err == nil {
			c.downloadRemaining = remaining
		}
	}

	if c.usageManager != nil && (c.apiLimit > 0 || c.downloadLimit > 0) {
		if c.apiLimit > 0 {
			c.apiUsed = c.apiLimit - c.apiRemaining
		}
		if c.downloadLimit > 0 {
			c.downloadUsed = c.downloadLimit - c.downloadRemaining
		}
		c.usageManager.UpdateUsage(c.name, c.apiUsed, c.downloadUsed)
	}
}

func (c *Client) requestContext() (context.Context, context.CancelFunc) {
	if timeout := c.client.Timeout; timeout > 0 {
		return context.WithTimeout(context.Background(), timeout)
	}
	return context.Background(), func() {}
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	return c.requestLimiter.Wait(ctx)
}

func (c *Client) Ping() error {
	ctx, cancel := c.requestContext()
	defer cancel()
	if err := c.waitForRateLimit(ctx); err != nil {
		return err
	}
	apiURL := fmt.Sprintf("%s%s?t=caps&apikey=%s", c.baseURL, c.apiPath, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", env.IndexerQueryHeader())
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s indexer returned error status: %d", c.Name(), resp.StatusCode)
	}
	return nil
}

func (c *Client) GetCaps() (*indexer.Caps, error) {
	ctx, cancel := c.requestContext()
	defer cancel()
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, err
	}
	apiURL := fmt.Sprintf("%s%s?t=caps", c.baseURL, c.apiPath)
	if c.apiKey != "" {
		apiURL += "&apikey=" + url.QueryEscape(c.apiKey)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create caps request: %w", err)
	}
	req.Header.Set("User-Agent", env.IndexerQueryHeader())

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch caps from %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s caps returned status %d", c.Name(), resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read caps from %s: %w", c.Name(), err)
	}

	if err := c.checkNewznabError(body); err != nil {
		return nil, err
	}

	caps, err := indexer.ParseCapsXML(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse caps from %s: %w", c.Name(), err)
	}

	c.mu.Lock()
	c.caps = caps
	c.mu.Unlock()

	logger.Debug("Fetched capabilities", "indexer", c.Name(),
		"categories", len(caps.Categories),
		"movie_search", caps.Searching.MovieSearch,
		"tv_search", caps.Searching.TVSearch,
		"retention", caps.RetentionDays)

	return caps, nil
}

func (c *Client) checkNewznabError(bodyBytes []byte) error {
	var apiErr APIError
	if err := xml.Unmarshal(bodyBytes, &apiErr); err == nil && apiErr.Description != "" {

		switch {
		case apiErr.Code >= 100 && apiErr.Code <= 199:
			return fmt.Errorf("%s authentication error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		case apiErr.Code == 201:
			return fmt.Errorf("%s request limit reached (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		case apiErr.Code >= 200 && apiErr.Code <= 299:
			return fmt.Errorf("%s request error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		case apiErr.Code >= 300 && apiErr.Code <= 399:
			return fmt.Errorf("%s server error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		default:
			return fmt.Errorf("%s API error (code %d): %s", c.Name(), apiErr.Code, apiErr.Description)
		}
	}
	return nil
}

func emptySearchResponse() *indexer.SearchResponse {
	resp := &indexer.SearchResponse{
		XMLName: xml.Name{Local: "rss"},
		Channel: indexer.Channel{Items: []indexer.Item{}},
	}
	indexer.NormalizeSearchResponse(resp)
	return resp
}

func normalizeIMDbID(id string) string {
	return strings.TrimPrefix(strings.TrimSpace(id), "tt")
}

func supportsMovieIDParam(caps *indexer.Caps, param string) bool {
	if caps == nil || len(caps.Searching.MovieSearchSupportedParams) == 0 {
		return param == "imdbid" || param == "tmdbid"
	}
	return caps.Searching.MovieSearchSupportedParams[param]
}

func supportsTVIDParam(caps *indexer.Caps, param string) bool {
	if caps == nil || len(caps.Searching.TVSearchSupportedParams) == 0 {
		return param == "tvdbid" || param == "tmdbid" || param == "imdbid"
	}
	return caps.Searching.TVSearchSupportedParams[param]
}

func selectMovieIDSearchParam(caps *indexer.Caps, req indexer.SearchRequest) (string, string) {
	if imdbID := normalizeIMDbID(req.IMDbID); imdbID != "" && supportsMovieIDParam(caps, "imdbid") {
		return "imdbid", imdbID
	}
	if tmdbID := strings.TrimSpace(req.TMDBID); tmdbID != "" && supportsMovieIDParam(caps, "tmdbid") {
		return "tmdbid", tmdbID
	}
	return "", ""
}

func selectTVIDSearchParam(caps *indexer.Caps, req indexer.SearchRequest) (string, string) {
	if tvdbID := strings.TrimSpace(req.TVDBID); tvdbID != "" && supportsTVIDParam(caps, "tvdbid") {
		return "tvdbid", tvdbID
	}
	if tmdbID := strings.TrimSpace(req.TMDBID); tmdbID != "" && supportsTVIDParam(caps, "tmdbid") {
		return "tmdbid", tmdbID
	}
	if imdbID := normalizeIMDbID(req.IMDbID); imdbID != "" && supportsTVIDParam(caps, "imdbid") {
		return "imdbid", imdbID
	}
	return "", ""
}

func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}
	ctx, cancel := c.requestContext()
	defer cancel()
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, err
	}

	c.mu.RLock()
	caps := c.caps
	c.mu.RUnlock()

	limit := req.Limit
	if o := req.OptionalOverrides; o != nil && o.SearchResultLimit > 0 {
		limit = o.SearchResultLimit
	}
	maxLimit := 2000
	if caps != nil && caps.Limits.Max > 0 {
		maxLimit = caps.Limits.Max
	}
	if limit <= 0 {
		limit = maxLimit
	}

	params := url.Values{}
	params.Set("apikey", c.apiKey)
	params.Set("o", "xml")
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("offset", "0")

	isMovieSearch := strings.HasPrefix(req.Cat, "2")
	isTVSearch := strings.HasPrefix(req.Cat, "5")

	rawQuery := req.Query
	query := rawQuery
	isTextMode := !strings.EqualFold(strings.TrimSpace(req.SearchMode), "id") && query != ""
	extraTerms := c.cfg.ExtraSearchTerms
	if o := req.OptionalOverrides; o != nil && o.ExtraSearchTerms != nil {
		extraTerms = *o.ExtraSearchTerms
	}

	useTVSearchParams := false
	searchSeason, searchEpisode := "", ""
	idParamName, idParamValue := "", ""
	if isMovieSearch {
		if isTextMode {
			params.Set("t", "search")
		} else {
			if caps != nil && !caps.Searching.MovieSearch {
				logger.Debug("Indexer skipped for request",
					"stream", req.StreamLabel,
					"request", req.RequestLabel,
					"indexer", c.Name(),
					"reason", "movie id search unsupported by caps",
				)
				return emptySearchResponse(), nil
			}
			params.Set("t", "movie")
			idParamName, idParamValue = selectMovieIDSearchParam(caps, req)
			if idParamName == "" {
				logger.Debug("Indexer skipped for request",
					"stream", req.StreamLabel,
					"request", req.RequestLabel,
					"indexer", c.Name(),
					"reason", "no supported movie id for caps",
					"imdb_id", strings.TrimSpace(req.IMDbID) != "",
					"tmdb_id", strings.TrimSpace(req.TMDBID) != "",
				)
				return emptySearchResponse(), nil
			}
		}
	} else if isTVSearch {
		searchSeason, searchEpisode = config.SeriesSearchScopeSearchTarget(req.SeriesSearchScope, req.SearchMode, req.Season, req.Episode)
		useTVSearchParams = config.SeriesSearchScopeUsesSeasonParams(req.SeriesSearchScope, req.SearchMode) && (searchSeason != "" || searchEpisode != "")
		if isTextMode {
			params.Set("t", "search")
		} else {
			if caps != nil && !caps.Searching.TVSearch {
				logger.Debug("Indexer skipped for request",
					"stream", req.StreamLabel,
					"request", req.RequestLabel,
					"indexer", c.Name(),
					"reason", "tv id search unsupported by caps",
				)
				return emptySearchResponse(), nil
			}
			params.Set("t", "tvsearch")
			idParamName, idParamValue = selectTVIDSearchParam(caps, req)
			if idParamName == "" {
				logger.Debug("Indexer skipped for request",
					"stream", req.StreamLabel,
					"request", req.RequestLabel,
					"indexer", c.Name(),
					"reason", "no supported tv id for caps",
					"imdb_id", strings.TrimSpace(req.IMDbID) != "",
					"tmdb_id", strings.TrimSpace(req.TMDBID) != "",
					"tvdb_id", strings.TrimSpace(req.TVDBID) != "",
				)
				return emptySearchResponse(), nil
			}
		}
	} else {
		params.Set("t", "search")
	}

	if !isTextMode && idParamName != "" && idParamValue != "" {
		params.Set(idParamName, idParamValue)
	}

	cat := req.Cat
	if isMovieSearch && c.cfg.MovieCategories != "" {
		cat = c.cfg.MovieCategories
	} else if isTVSearch && c.cfg.TVCategories != "" {
		cat = c.cfg.TVCategories
	}
	if o := req.OptionalOverrides; o != nil {
		if isMovieSearch && o.MovieCategories != nil && *o.MovieCategories != "" {
			cat = *o.MovieCategories
		} else if isTVSearch && o.TVCategories != nil && *o.TVCategories != "" {
			cat = *o.TVCategories
		}
	}
	if cat != "" {
		params.Set("cat", cat)
	}

	if useTVSearchParams {
		if searchSeason != "" {
			params.Set("season", searchSeason)
		}
		if searchEpisode != "" {
			params.Set("ep", searchEpisode)
		}
	}

	if extraTerms != "" {
		switch {
		case !isTextMode && useTVSearchParams:
			query = extraTerms
		case strings.EqualFold(strings.TrimSpace(req.SearchMode), "id"):
			if query != "" {
				query = extraTerms + " " + query
			} else {
				query = extraTerms
			}
		default:
			if query != "" {
				query = query + " " + extraTerms
			} else {
				query = extraTerms
			}
		}
	}

	if query != "" && (isTextMode || !useTVSearchParams || (!isTextMode && useTVSearchParams && query != rawQuery)) {
		params.Set("q", query)
	}

	apiURL := fmt.Sprintf("%s%s?%s", c.baseURL, c.apiPath, encodeOrderedQuery(params, orderedSearchQueryKeys))
	logger.Debug("Search request",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", func() string {
			if strings.EqualFold(strings.TrimSpace(req.SearchMode), "id") {
				return "id"
			}
			return "text"
		}(),
		"indexer", c.Name(),
		"type", "newznab",
		"url", apiURL,
		"limit", limit,
	)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("User-Agent", env.IndexerQueryHeader())
	startedAt := time.Now()
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	c.updateUsageFromHeaders(resp.Header)

	if c.usageManager != nil && c.apiLimit == 0 {
		c.usageManager.IncrementUsed(c.name, 1, 0)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s response: %w", c.Name(), err)
	}

	if resp.StatusCode != http.StatusOK {

		if err := c.checkNewznabError(bodyBytes); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s returned status %d: %s", c.Name(), resp.StatusCode, string(bodyBytes))
	}

	if err := c.checkNewznabError(bodyBytes); err != nil {
		return nil, err
	}

	var result indexer.SearchResponse
	if err := xml.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse %s response: %w", c.Name(), err)
	}

	for i := range result.Channel.Items {
		item := &result.Channel.Items[i]
		item.SourceIndexer = c

		if item.Size <= 0 {
			if item.Enclosure.Length > 0 {
				item.Size = item.Enclosure.Length
			} else if sizeAttr := item.GetAttribute("size"); sizeAttr != "" {
				fmt.Sscanf(sizeAttr, "%d", &item.Size)
			}
		}
	}

	if len(result.Channel.Items) > limit {
		result.Channel.Items = result.Channel.Items[:limit]
	}
	totalResults := result.Channel.Response.Total
	if totalResults <= 0 {
		totalResults = len(result.Channel.Items)
	}
	logger.Debug("Search request result",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", func() string {
			if strings.EqualFold(strings.TrimSpace(req.SearchMode), "id") {
				return "id"
			}
			return "text"
		}(),
		"indexer", c.Name(),
		"type", "newznab",
		"raw_results", len(result.Channel.Items),
		"result_offset", result.Channel.Response.Offset,
		"total_results", totalResults,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
	return &result, nil
}

func (c *Client) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
		logger.Warn("Download limit reached for indexer", "indexer", c.Name())
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.requestLimiter.Wait(ctx); err != nil {
		return nil, err
	}
	nzbURL = c.normalizeDownloadURL(nzbURL)

	req, err := http.NewRequestWithContext(ctx, "GET", nzbURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", env.IndexerGrabHeader())
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB from %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	c.mu.Lock()
	c.apiUsed++
	c.downloadUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	if c.downloadRemaining > 0 {
		c.downloadRemaining--
	}
	c.mu.Unlock()

	c.updateUsageFromHeaders(resp.Header)

	if c.usageManager != nil && c.apiLimit == 0 && c.downloadLimit == 0 {
		c.usageManager.IncrementUsed(c.name, 1, 1)
	} else if c.usageManager != nil && c.apiLimit == 0 {
		c.usageManager.IncrementUsed(c.name, 1, 0)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s NZB download returned status %d", c.Name(), resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data from %s: %w", c.Name(), err)
	}

	return data, nil
}

func (c *Client) normalizeDownloadURL(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	baseURL, err := url.Parse(c.baseURL)
	if err != nil || baseURL.Hostname() == "" {
		return rawURL
	}

	changed := false
	if !parsedURL.IsAbs() || parsedURL.Hostname() == "" {
		parsedURL = baseURL.ResolveReference(parsedURL)
		changed = true
	}
	if !hostsMatch(baseURL.Hostname(), parsedURL.Hostname()) {
		return rawURL
	}

	q := parsedURL.Query()
	if q.Get("t") == "get" && q.Get("id") == "" && q.Get("guid") != "" {
		q.Set("id", q.Get("guid"))
		changed = true
	}
	if !queryHasAPIKey(q) && c.apiKey != "" {
		q.Set("apikey", c.apiKey)
		changed = true
	}
	if !changed {
		return rawURL
	}
	parsedURL.RawQuery = q.Encode()
	return parsedURL.String()
}

func hostsMatch(a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.TrimPrefix(a, "api.") == b || strings.TrimPrefix(b, "api.") == a
}

func queryHasAPIKey(q url.Values) bool {
	return q.Get("apikey") != "" || q.Get("api_key") != "" || q.Get("r") != ""
}
