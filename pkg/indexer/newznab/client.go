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
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/httpproxy"
)

type Client struct {
	baseURL string
	apiPath string
	apiKey  string
	name    string
	client  *http.Client
	cfg     config.IndexerConfig
	caps    *indexer.Caps
	core    *indexer.ClientCore
	mu      sync.RWMutex // guards caps
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
	"cachetime",
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
	return c.core.Usage()
}

func NewClient(cfg config.IndexerConfig, um *indexer.UsageManager) *Client {

	transport := &http.Transport{
		Proxy: httpproxy.IndexerProxy(cfg.ProxyURL),
		TLSClientConfig: &tls.Config{
			// Historical default: certificates are NOT verified unless the
			// indexer opts in via verify_tls (self-signed private indexers
			// are common). Flipping the default would break existing setups.
			InsecureSkipVerify: !cfg.VerifyTLS,
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

	return &Client{
		name:    cfg.Name,
		baseURL: strings.TrimRight(cfg.URL, "/"),
		apiPath: apiPath,
		apiKey:  cfg.APIKey,
		cfg:     cfg,
		client: &http.Client{
			Timeout:   cfg.EffectiveTimeout(),
			Transport: transport,
		},
		core: indexer.NewClientCore(cfg.Name, cfg.APIHitsDay, cfg.DownloadsDay, cfg.RateLimitRPS, um),
	}
}

func (c *Client) effectiveQueryHeader() string {
	if h := strings.TrimSpace(c.cfg.QueryHeader); h != "" {
		return h
	}
	return env.IndexerQueryHeader()
}

func (c *Client) effectiveGrabHeader() string {
	if h := strings.TrimSpace(c.cfg.GrabHeader); h != "" {
		return h
	}
	return env.IndexerGrabHeader()
}

func (c *Client) checkAPILimit() error {
	return c.core.CheckAPILimit(c.Name())
}

func (c *Client) checkDownloadLimit() error {
	return c.core.CheckDownloadLimit(c.Name())
}

// requestContext bounds parent with the client timeout so a caller
// cancellation aborts the request while an absent deadline still cannot hang
// past the configured indexer timeout.
func (c *Client) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout := c.client.Timeout; timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return context.WithCancel(parent)
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	return c.core.Limiter.Wait(ctx)
}

func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	if err := c.waitForRateLimit(ctx); err != nil {
		return err
	}
	apiURL := fmt.Sprintf("%s%s?t=caps&apikey=%s", c.baseURL, c.apiPath, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.effectiveQueryHeader())
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
	ctx, cancel := c.requestContext(context.Background())
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
	req.Header.Set("User-Agent", c.effectiveQueryHeader())

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

// idParamOrder lists the id parameters to try, most specific first. Movie and
// TV searches probe different ids, and the defaults differ when an indexer
// publishes no supported-params caps at all.
var (
	movieIDParamOrder   = []string{"kitsu", "kitsu_id", "imdbid", "tmdbid"}
	movieIDParamDefault = map[string]bool{"imdbid": true, "tmdbid": true}
	tvIDParamOrder      = []string{"kitsu", "kitsu_id", "tvdbid", "tmdbid", "imdbid"}
	tvIDParamDefault    = map[string]bool{"tvdbid": true, "tmdbid": true, "imdbid": true}
)

// supportsIDParam reports whether the indexer accepts param, falling back to a
// conservative default set when it publishes no caps.
func supportsIDParam(supported map[string]bool, fallback map[string]bool, param string) bool {
	if len(supported) == 0 {
		return fallback[param]
	}
	return supported[param]
}

// selectIDSearchParam picks the first id parameter in order that the indexer
// supports AND the request actually carries.
func selectIDSearchParam(req indexer.SearchRequest, order []string, supported, fallback map[string]bool) (string, string) {
	valueFor := func(param string) string {
		switch param {
		case "kitsu", "kitsu_id":
			return strings.TrimSpace(req.KitsuID)
		case "imdbid":
			return normalizeIMDbID(req.IMDbID)
		case "tmdbid":
			return strings.TrimSpace(req.TMDBID)
		case "tvdbid":
			return strings.TrimSpace(req.TVDBID)
		}
		return ""
	}
	for _, param := range order {
		value := valueFor(param)
		if value == "" || !supportsIDParam(supported, fallback, param) {
			continue
		}
		return param, value
	}
	return "", ""
}

func movieSupportedParams(caps *indexer.Caps) map[string]bool {
	if caps == nil {
		return nil
	}
	return caps.Searching.MovieSearchSupportedParams
}

func tvSupportedParams(caps *indexer.Caps) map[string]bool {
	if caps == nil {
		return nil
	}
	return caps.Searching.TVSearchSupportedParams
}

func selectMovieIDSearchParam(caps *indexer.Caps, req indexer.SearchRequest) (string, string) {
	return selectIDSearchParam(req, movieIDParamOrder, movieSupportedParams(caps), movieIDParamDefault)
}

func selectTVIDSearchParam(caps *indexer.Caps, req indexer.SearchRequest) (string, string) {
	return selectIDSearchParam(req, tvIDParamOrder, tvSupportedParams(caps), tvIDParamDefault)
}

func (c *Client) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}
	ctx, cancel := c.requestContext(ctx)
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

	if query != "" && isTextMode {
		params.Set("q", query)
	}
	if c.cfg.SearchResultsCacheTime > 0 && config.IsAggregatorIndexerType(c.cfg.Type) {
		params.Set("cachetime", strconv.Itoa(c.cfg.SearchResultsCacheTime))
	}

	apiURL := fmt.Sprintf("%s%s?%s", c.baseURL, c.apiPath, encodeOrderedQuery(params, orderedSearchQueryKeys))
	logger.Debug("Search request",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", indexer.SearchModeLabel(req.SearchMode),
		"indexer", c.Name(),
		"type", "newznab",
		"url", apiURL,
		"limit", limit,
	)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("User-Agent", c.effectiveQueryHeader())
	startedAt := time.Now()
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	c.core.RecordAPIHit(resp.Header)

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
		"mode", indexer.SearchModeLabel(req.SearchMode),
		"indexer", c.Name(),
		"type", "newznab",
		"raw_results", len(result.Channel.Items),
		"result_offset", result.Channel.Response.Offset,
		"total_results", totalResults,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
	c.core.RecordSearchDuration(time.Since(startedAt))
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
	if err := c.core.Limiter.Wait(ctx); err != nil {
		return nil, err
	}
	nzbURL = c.normalizeDownloadURL(nzbURL)

	req, err := http.NewRequestWithContext(ctx, "GET", nzbURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", c.effectiveGrabHeader())
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB from %s: %w", c.Name(), err)
	}
	defer resp.Body.Close()

	c.core.RecordGrab(resp.Header)

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
