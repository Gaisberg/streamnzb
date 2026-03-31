package newznab

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	mu                sync.RWMutex
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

func (c *Client) Ping() error {
	apiURL := fmt.Sprintf("%s%s?t=caps&apikey=%s", c.baseURL, c.apiPath, c.apiKey)
	req, err := http.NewRequest("GET", apiURL, nil)
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
	apiURL := fmt.Sprintf("%s%s?t=caps", c.baseURL, c.apiPath)
	if c.apiKey != "" {
		apiURL += "&apikey=" + url.QueryEscape(c.apiKey)
	}
	req, err := http.NewRequest("GET", apiURL, nil)
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

func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}

	limit := req.Limit
	if o := req.OptionalOverrides; o != nil && o.SearchResultLimit > 0 {
		limit = o.SearchResultLimit
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	params := url.Values{}
	params.Set("apikey", c.apiKey)
	params.Set("o", "xml")
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("offset", "0")

	c.mu.RLock()
	caps := c.caps
	c.mu.RUnlock()

	isMovieSearch := strings.HasPrefix(req.Cat, "2")
	isTVSearch := strings.HasPrefix(req.Cat, "5")

	query := req.Query
	isTextMode := !req.ForceIDSearch && query != ""
	extraTerms := c.cfg.ExtraSearchTerms
	if o := req.OptionalOverrides; o != nil && o.ExtraSearchTerms != nil {
		extraTerms = *o.ExtraSearchTerms
	}
	if extraTerms != "" {
		if req.ForceIDSearch {
			if query != "" {
				query = extraTerms + " " + query
			} else {
				query = extraTerms
			}
		} else {
			if query != "" {
				query = query + " " + extraTerms
			} else {
				query = extraTerms
			}
		}
	}

	useTVSearchParams := false
	if isMovieSearch && (caps == nil || caps.Searching.MovieSearch) {
		if !isTextMode && req.IMDbID != "" {
			params.Set("t", "movie")
		} else {
			params.Set("t", "search")
		}
	} else if isTVSearch && (caps == nil || caps.Searching.TVSearch) {
		if isTextMode {
			params.Set("t", "search")
		} else {
			params.Set("t", "tvsearch")
			useTVSearchParams = true
		}
	} else {
		params.Set("t", "search")
	}

	if query != "" {
		params.Set("q", query)
	}

	if !isTextMode && isMovieSearch && req.IMDbID != "" {
		imdbID := strings.TrimPrefix(req.IMDbID, "tt")
		params.Set("imdbid", imdbID)
	}
	if !isTextMode && req.TVDBID != "" {
		params.Set("tvdbid", req.TVDBID)
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
		if req.Season != "" {
			params.Set("season", req.Season)
		}
		if req.Episode != "" {
			params.Set("ep", req.Episode)
		}
	}

	apiURL := fmt.Sprintf("%s%s?%s", c.baseURL, c.apiPath, params.Encode())
	logger.Debug("Search request",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", func() string {
			if req.ForceIDSearch {
				return "id"
			}
			return "text"
		}(),
		"indexer", c.Name(),
		"type", "newznab",
		"url", apiURL,
		"limit", limit,
	)

	httpReq, err := http.NewRequestWithContext(context.Background(), "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("User-Agent", env.IndexerQueryHeader())
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
