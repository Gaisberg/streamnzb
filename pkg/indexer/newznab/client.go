package newznab

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
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
	baseURL    string
	apiPath    string
	baseParams url.Values // query params carried by the configured URL/api_path (e.g. NZBHydra2's ?indexers=...)
	apiKey     string
	name       string
	client     *http.Client
	cfg        config.IndexerConfig
	caps       *indexer.Caps
	core       *indexer.ClientCore
	mu         sync.RWMutex // guards caps
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

// maxPingReadBytes bounds how much of a ping response is read. An error
// document is a few hundred bytes; 64 KB leaves room for any indexer's
// framing while keeping a misbehaving one from streaming a result dump
// into the credential check.
const maxPingReadBytes = 64 << 10

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

	// Users paste full API endpoints copied from other tools, e.g. NZBHydra2's
	// "http://host:5076/api?indexers=abc". Split any query params off the URL
	// and api_path so they ride along on every request instead of corrupting
	// the request path, and don't append api_path again when the URL already
	// ends with it.
	baseParams := url.Values{}
	if idx := strings.Index(apiPath, "?"); idx >= 0 {
		if vals, err := url.ParseQuery(apiPath[idx+1:]); err == nil {
			for key, values := range vals {
				baseParams[key] = values
			}
		}
		apiPath = apiPath[:idx]
	}
	baseURL := strings.TrimRight(cfg.URL, "/")
	if u, err := url.Parse(strings.TrimSpace(cfg.URL)); err == nil && u.Host != "" {
		if u.RawQuery != "" {
			for key, values := range u.Query() {
				baseParams[key] = values
			}
			u.RawQuery = ""
		}
		u.Fragment = ""
		u.Path = strings.TrimRight(u.Path, "/")
		if u.Path != "" && strings.HasSuffix(u.Path, apiPath) {
			apiPath = ""
		}
		baseURL = u.String()
	}

	return &Client{
		name:       cfg.Name,
		baseURL:    baseURL,
		apiPath:    apiPath,
		baseParams: baseParams,
		apiKey:     cfg.APIKey,
		cfg:        cfg,
		client: &http.Client{
			Timeout:   cfg.EffectiveTimeout(),
			Transport: transport,
		},
		core: indexer.NewClientCore(cfg.Name, cfg.APIHitsDay, cfg.DownloadsDay, cfg.RateLimitRPS, um),
	}
}

// buildAPIURL merges the query params carried by the configured URL/api_path
// into params (request params win) and returns the full request URL.
func (c *Client) buildAPIURL(params url.Values) string {
	for key, values := range c.baseParams {
		if _, ok := params[key]; !ok {
			params[key] = values
		}
	}
	return fmt.Sprintf("%s%s?%s", c.baseURL, c.apiPath, encodeOrderedQuery(params, orderedSearchQueryKeys))
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
	return c.core.CheckSearchAllowed(c.Name(), time.Now())
}

func (c *Client) checkDownloadLimit() error {
	return c.core.CheckGrabAllowed(c.Name(), time.Now())
}

// noteThrottled opens the shared cooldown and logs it once, so a burst of
// concurrent refusals does not produce one warning per candidate.
func (c *Client) noteThrottled(h http.Header, status int) {
	remaining := c.core.NoteThrottled(h, time.Now())
	logger.Warn("Indexer rate limited; pausing requests",
		"indexer", c.Name(),
		"status", status,
		"retry_after", h.Get("Retry-After"),
		"cooldown", remaining.Round(time.Second))
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

// Ping verifies the indexer is reachable AND that our API key works.
//
// It deliberately issues t=search rather than t=caps: many indexers serve caps
// publicly, so a caps request succeeds with any key at all — which is how a
// bogus key once sailed through save-time validation and the health probes
// while every real search failed. A bare t=search (no query) is the newznab
// "latest releases" listing, the cheapest request that actually exercises the
// key; limit=1 keeps the answer small. It costs one API hit, which is the
// price of an honest answer.
//
// The body read is capped as well: the only thing worth reading is a possible
// error document (always tiny), so an indexer that ignores limit=1 and streams
// a full result page cannot slow the check down past the cap. A truncated
// success body decodes to "no error", which is the same verdict its full form
// would have produced.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	if err := c.waitForRateLimit(ctx); err != nil {
		return err
	}
	params := url.Values{}
	params.Set("t", "search")
	params.Set("limit", "1")
	params.Set("apikey", c.apiKey)
	apiURL := c.buildAPIURL(params)
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPingReadBytes))
	if err != nil {
		return fmt.Errorf("failed to read %s ping response: %w", c.Name(), err)
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%s indexer returned error status %d: %w", c.Name(), resp.StatusCode, indexer.ErrAuthFailed)
		}
		return fmt.Errorf("%s indexer returned error status: %d", c.Name(), resp.StatusCode)
	}
	// Newznab answers a rejected API key with an error document under HTTP 200,
	// so a status check alone reports a working indexer for a key the server
	// just refused — which is precisely the case a ping exists to catch.
	return c.checkNewznabError(body)
}

func (c *Client) GetCaps() (*indexer.Caps, error) {
	ctx, cancel := c.requestContext(context.Background())
	defer cancel()
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("t", "caps")
	if c.apiKey != "" {
		params.Set("apikey", c.apiKey)
	}
	apiURL := c.buildAPIURL(params)
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
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%s caps returned status %d: %w", c.Name(), resp.StatusCode, indexer.ErrAuthFailed)
		}
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
			// The 1xx block is newznab's credential range: wrong api key, wrong
			// user, account suspended. It is a verdict on the account, so it
			// carries the sentinel that lets the health layer park the indexer.
			return fmt.Errorf("%s authentication error (code %d): %s: %w", c.Name(), apiErr.Code, apiErr.Description, indexer.ErrAuthFailed)
		case apiErr.Code == 201:
			return fmt.Errorf("%s request limit reached (code %d): %s: %w", c.Name(), apiErr.Code, apiErr.Description, indexer.ErrRateLimited)
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
	// The per-indexer result limit is a stream-search knob: a proxied query
	// carries the caller's own limit and must not be shrunk by it.
	if o := req.OptionalOverrides; o != nil && o.SearchResultLimit > 0 && req.Passthrough == nil {
		limit = o.SearchResultLimit
	}
	maxLimit := 2000
	if caps != nil && caps.Limits.Max > 0 {
		maxLimit = caps.Limits.Max
	}
	if limit <= 0 {
		limit = maxLimit
	}

	if req.Passthrough != nil {
		if reason := passthroughSkipReason(caps, req); reason != "" {
			logger.Debug("Indexer skipped for request",
				"stream", req.StreamLabel,
				"request", req.RequestLabel,
				"indexer", c.Name(),
				"reason", reason,
			)
			return emptySearchResponse(), nil
		}
		return c.executeSearch(ctx, req, c.passthroughParams(req, limit), limit)
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

	return c.executeSearch(ctx, req, params, limit)
}

// passthroughIDParams are the identifier parameters a proxied query can carry,
// beyond the three StreamNZB models itself. They are forwarded untouched — no
// id is ever translated into another — so an indexer only sees one it asked
// for in its caps.
var passthroughIDParams = []string{"imdbid", "tmdbid", "tvdbid", "tvmazeid", "traktid", "rid"}

// passthroughSkipReason declines a proxied query this indexer cannot answer,
// returning a log-ready reason or "" to proceed. It matters more here than for
// a stream search: an indexer sent an id it does not understand does not fail,
// it ignores the parameter and answers with its latest listing, and that would
// land in the merged feed as results the caller never asked for.
//
// A query carrying text is never declined — the id is then a refinement the
// indexer may ignore, and the text search still means what it says.
func passthroughSkipReason(caps *indexer.Caps, req indexer.SearchRequest) string {
	p := req.Passthrough
	if p == nil || strings.TrimSpace(p.Params.Get("q")) != "" {
		return ""
	}

	var supported, fallback map[string]bool
	switch p.Function {
	case "movie":
		if caps != nil && !caps.Searching.MovieSearch {
			return "movie search unsupported by caps"
		}
		supported, fallback = movieSupportedParams(caps), movieIDParamDefault
	case "tvsearch":
		if caps != nil && !caps.Searching.TVSearch {
			return "tv search unsupported by caps"
		}
		supported, fallback = tvSupportedParams(caps), tvIDParamDefault
	default:
		// Ids are not part of any other function's contract, so there is
		// nothing to check them against.
		return ""
	}

	requested := make([]string, 0, len(passthroughIDParams))
	for _, param := range passthroughIDParams {
		if strings.TrimSpace(p.Params.Get(param)) != "" {
			requested = append(requested, param)
		}
	}
	if len(requested) == 0 {
		return ""
	}
	for _, param := range requested {
		if supportsIDParam(supported, fallback, param) {
			return ""
		}
	}
	return "no supported id parameter for caps: " + strings.Join(requested, ",")
}

// passthroughParams renders a verbatim Newznab query for this indexer: the
// caller's function and parameters, with this client's credentials, paging and
// output format layered on. The configured movie/TV category lists only fill
// in when the caller named no categories at all — a client that asked for
// specific ones gets exactly those.
func (c *Client) passthroughParams(req indexer.SearchRequest, limit int) url.Values {
	params := url.Values{}
	for key, values := range req.Passthrough.Params {
		params[key] = append([]string(nil), values...)
	}
	params.Set("t", req.Passthrough.Function)
	params.Set("apikey", c.apiKey)
	params.Set("o", "xml")
	params.Set("limit", strconv.Itoa(limit))
	if params.Get("offset") == "" {
		params.Set("offset", "0")
	}
	if params.Get("cat") == "" {
		cat := ""
		switch {
		case strings.HasPrefix(req.Cat, "2"):
			cat = c.cfg.MovieCategories
			if o := req.OptionalOverrides; o != nil && o.MovieCategories != nil && *o.MovieCategories != "" {
				cat = *o.MovieCategories
			}
		case strings.HasPrefix(req.Cat, "5"):
			cat = c.cfg.TVCategories
			if o := req.OptionalOverrides; o != nil && o.TVCategories != nil && *o.TVCategories != "" {
				cat = *o.TVCategories
			}
		}
		if cat != "" {
			params.Set("cat", cat)
		}
	}
	if c.cfg.SearchResultsCacheTime > 0 && config.IsAggregatorIndexerType(c.cfg.Type) {
		params.Set("cachetime", strconv.Itoa(c.cfg.SearchResultsCacheTime))
	}
	return params
}

// executeSearch issues a built query, records usage and timing, and parses the
// result set. Shared by the stream-search mapping above and the Newznab
// endpoint's passthrough queries, which differ only in how params are built.
func (c *Client) executeSearch(ctx context.Context, req indexer.SearchRequest, params url.Values, limit int) (*indexer.SearchResponse, error) {
	apiURL := c.buildAPIURL(params)
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
		if isTransientDownloadStatus(resp.StatusCode) {
			c.noteThrottled(resp.Header, resp.StatusCode)
		}
		if err := c.checkNewznabError(bodyBytes); err != nil {
			return nil, err
		}
		if isTransientDownloadStatus(resp.StatusCode) {
			return nil, fmt.Errorf("%s returned status %d: %s: %w", c.Name(), resp.StatusCode, string(bodyBytes), indexer.ErrRateLimited)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%s returned status %d: %s: %w", c.Name(), resp.StatusCode, string(bodyBytes), indexer.ErrAuthFailed)
		}
		return nil, fmt.Errorf("%s returned status %d: %s", c.Name(), resp.StatusCode, string(bodyBytes))
	}

	if err := c.checkNewznabError(bodyBytes); err != nil {
		// Newznab reports quota exhaustion as an error document under HTTP 200,
		// so the status check above never sees it.
		if errors.Is(err, indexer.ErrRateLimited) {
			c.noteThrottled(resp.Header, resp.StatusCode)
		}
		return nil, err
	}

	var result indexer.SearchResponse
	if err := xml.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to parse %s response: %w", c.Name(), err)
	}

	for i := range result.Channel.Items {
		item := &result.Channel.Items[i]
		item.SourceIndexer = c

		// NZBHydra2 tags each result with the indexer it came from; surface it
		// so aggregated results stay attributable per sub-indexer.
		if actual := item.GetAttribute("hydraIndexerName"); actual != "" && !strings.EqualFold(actual, c.Name()) {
			item.ActualIndexer = c.Name() + " - " + actual
		}

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

	if resp.StatusCode != http.StatusOK {
		// A throttled or overloaded indexer says nothing about the release, so
		// tag those statuses as temporary. Without this the caller reads the
		// bare status text as a definitive failure and bans a good release for
		// the full bad-release TTL.
		//
		// Usage is deliberately not recorded here: a refused grab handed back
		// no NZB, so charging it against the daily download budget spends quota
		// on nothing. Only the headers are ingested, since a 429 is often the
		// most accurate statement of remaining quota we ever get.
		c.core.ApplyHeaderUsage(resp.Header)
		if isTransientDownloadStatus(resp.StatusCode) {
			c.noteThrottled(resp.Header, resp.StatusCode)
			return nil, fmt.Errorf("%s NZB download returned status %d: %w", c.Name(), resp.StatusCode, indexer.ErrRateLimited)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%s NZB download returned status %d: %w", c.Name(), resp.StatusCode, indexer.ErrAuthFailed)
		}
		return nil, fmt.Errorf("%s NZB download returned status %d", c.Name(), resp.StatusCode)
	}

	c.core.RecordGrab(resp.Header)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data from %s: %w", c.Name(), err)
	}

	return data, nil
}

// 403 is deliberately not treated as an authentication verdict. Private
// indexers sit behind WAFs that answer 403 to a request they disliked for
// reasons having nothing to do with the API key, and blocking the indexer on
// that would retire a working key over a user-agent check. A genuine newznab
// credential rejection arrives as a 1xx error document, which
// checkNewznabError classifies before this point.
//
// isTransientDownloadStatus reports whether an NZB download status means the
// indexer declined to serve us right now rather than the NZB being gone. 404
// and 410 are deliberately absent: those do implicate the release.
func isTransientDownloadStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
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
