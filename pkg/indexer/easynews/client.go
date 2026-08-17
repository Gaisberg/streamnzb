package easynews

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/indexer/httpproxy"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/release"
)

const (
	easynewsBaseURL = "https://members.easynews.com"
	// pby is a request, not a promise: 3.0 caps a page at 100 rows regardless.
	// Pages past the first are therefore the only way to see a full result set,
	// so walk them up to a bound that keeps one search's latency sane.
	maxResultsPerPage = 250
	maxSearchPages    = 5

	// The search endpoint must be requested WITHOUT a trailing slash — with one,
	// Easynews serves the web app's HTML instead of JSON. NZB creation has no
	// 3.0 equivalent, so it is the one call still made against 2.0.
	easynewsSearchPath = "/3.0/api/search"
	easynewsNZBPath    = "/2.0/api/dl-nzb"
)

// Containers we will hand to playback, and the ones that are never a playable
// video file. The allow-list doubles as the fex whitelist sent to Easynews, so
// what the server filters and what we filter cannot drift apart.
var (
	allowedVideoExts = map[string]bool{
		".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true,
		".mov": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".flv": true, ".webm": true,
	}
	disallowedExts = map[string]bool{
		".rar": true, ".zip": true, ".exe": true, ".jpg": true, ".png": true,
	}
	// defaultFileExtensions is allowedVideoExts as Easynews wants it: no dots,
	// comma-separated. Sorted so the request URL is stable.
	defaultFileExtensions = buildFileExtensionList(allowedVideoExts)
)

func buildFileExtensionList(exts map[string]bool) string {
	list := make([]string, 0, len(exts))
	for ext := range exts {
		list = append(list, strings.TrimPrefix(ext, "."))
	}
	sort.Strings(list)
	return strings.Join(list, ",")
}

// errCredentialsRejected is a 401/403 from Easynews.
var errCredentialsRejected = errors.New("easynews rejected credentials")

type Client struct {
	username        string
	password        string
	name            string
	queryHeader     string
	grabHeader      string
	client          *http.Client
	downloadClient  *http.Client
	downloadBase    string
	searchTimeout   time.Duration
	downloadTimeout time.Duration
	advancedSearch  bool
	spamFilter      bool
	fileExtensions  string

	core *indexer.ClientCore
}

var _ indexer.Indexer = (*Client)(nil)

func NewClient(username, password, name string, downloadBase string, apiLimit, downloadLimit, rateLimitRPS, timeoutSeconds int, proxyURL, queryHeader, grabHeader string, um *indexer.UsageManager) (*Client, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("easynews username and password are required")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = config.DefaultEasynewsIndexerTimeoutSeconds
	}
	searchTimeout := time.Duration(timeoutSeconds) * time.Second
	downloadTimeout := searchTimeout * 2

	searchProxy := httpproxy.IndexerProxy(proxyURL)
	searchTransport := &http.Transport{
		Proxy:               searchProxy,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}
	downloadTransport := &http.Transport{
		Proxy:               httpproxy.WithEasynewsDownloadNoProxy(searchProxy),
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	// Easynews hands out a session cookie and redirects between its own hosts.
	// Go drops the Authorization header on a cross-host redirect and keeps no
	// cookies by default, so a redirected search would arrive unauthenticated —
	// re-attach the credentials per hop and let the jar carry the session.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("easynews cookie jar: %w", err)
	}

	c := &Client{
		username:        username,
		password:        password,
		name:            name,
		queryHeader:     queryHeader,
		grabHeader:      grabHeader,
		downloadBase:    downloadBase,
		searchTimeout:   searchTimeout,
		downloadTimeout: downloadTimeout,
		advancedSearch:  env.EasynewsAdvancedSearch(),
		spamFilter:      env.EasynewsSpamFilter(),
		fileExtensions:  fileExtensionList(),
		core:            indexer.NewClientCore(name, apiLimit, downloadLimit, rateLimitRPS, um),
		client: &http.Client{
			Timeout:   searchTimeout,
			Transport: searchTransport,
			Jar:       jar,
		},
		downloadClient: &http.Client{
			Timeout:   downloadTimeout,
			Transport: downloadTransport,
			Jar:       jar,
		},
	}
	c.client.CheckRedirect = c.reauthenticateRedirect
	c.downloadClient.CheckRedirect = c.reauthenticateRedirect

	return c, nil
}

// reauthenticateRedirect re-applies Basic auth on every redirect hop, matching
// how a session-based HTTP client behaves. Without it Go strips Authorization
// when Easynews bounces a request to another host and the hop comes back 401.
func (c *Client) reauthenticateRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	req.SetBasicAuth(c.username, c.password)
	return nil
}

func (c *Client) effectiveQueryHeader() string {
	if h := strings.TrimSpace(c.queryHeader); h != "" {
		return h
	}
	return env.IndexerQueryHeader()
}

func (c *Client) effectiveGrabHeader() string {
	if h := strings.TrimSpace(c.grabHeader); h != "" {
		return h
	}
	return env.IndexerGrabHeader()
}

func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Easynews"
}

func (c *Client) GetUsage() indexer.Usage {
	return c.core.Usage()
}

func (c *Client) Ping(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()
	if err := c.core.Limiter.Wait(ctx); err != nil {
		return err
	}

	// Easynews exposes no cheap auth-check endpoint; a minimal real search is
	// the only reliable credential probe.
	testQuery := "dune"
	_, _, err := c.searchInternal(ctx, testQuery, "", "", config.SeriesSearchScopeNone, "", false)
	if err != nil {
		return fmt.Errorf("easynews credentials invalid: %w", err)
	}
	return nil
}

func (c *Client) Search(ctx context.Context, req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()
	if err := c.core.Limiter.Wait(ctx); err != nil {
		return nil, err
	}

	query := prepareEasynewsQuery(req.Query)

	season := req.Season
	episode := req.Episode
	searchURL := buildEasynewsSearchURL(query, season, episode, req.SeriesSearchScope, req.Cat, c.searchOptions())

	logger.Debug("Search request",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", indexer.SearchModeLabel(req.SearchMode),
		"indexer", c.name,
		"type", "easynews",
		"advanced", c.advancedSearch,
		"url", searchURL,
		"gps", buildEasynewsGPSQuery(query, season, episode, req.SeriesSearchScope, req.Cat),
	)

	startedAt := time.Now()
	results, stats, err := c.searchInternal(ctx, query, season, episode, req.SeriesSearchScope, req.Cat, false)
	if err != nil {
		return nil, fmt.Errorf("easynews search failed: %w", err)
	}

	c.core.RecordAPIHit(nil)

	items := make([]indexer.Item, 0, len(results))
	for _, result := range results {
		item := indexer.Item{
			Title:         result.Title,
			Link:          result.DownloadURL,
			GUID:          result.GUID,
			PubDate:       result.PubDate,
			Size:          result.Size,
			SourceIndexer: c,
			Duration:      result.DurationSeconds,
		}
		items = append(items, item)
	}
	totalResults := stats.Total
	if totalResults <= 0 {
		totalResults = len(items)
	}
	logger.Debug("Search request result",
		"stream", req.StreamLabel,
		"request", req.RequestLabel,
		"mode", indexer.SearchModeLabel(req.SearchMode),
		"indexer", c.name,
		"type", "easynews",
		"filtered_results", len(items),
		"result_offset", 0,
		"total_results", totalResults,
		"rows_returned", stats.Returned,
		"unfiltered_results", stats.Unfiltered,
		"pages_fetched", stats.Pages,
		"pages_available", stats.NumPages,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
	c.core.RecordSearchDuration(time.Since(startedAt))

	return &indexer.SearchResponse{
		Channel: indexer.Channel{
			Items: items,
		},
	}, nil
}

func prepareEasynewsQuery(baseQuery string) string {
	return release.NormalizeTitleForSearchQuery(baseQuery)
}

func (c *Client) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, c.downloadTimeout)
	defer cancel()
	if err := c.core.Limiter.Wait(ctx); err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(nzbURL)
	if err != nil {
		return nil, fmt.Errorf("invalid NZB URL: %w", err)
	}

	payloadToken := parsedURL.Query().Get("payload")
	if payloadToken == "" {
		return nil, fmt.Errorf("missing payload token in URL")
	}

	payload, err := decodePayload(payloadToken)
	if err != nil {
		return nil, fmt.Errorf("invalid payload token: %w", err)
	}

	nzbData, err := c.downloadNZBInternal(ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB: %w", err)
	}

	c.core.RecordGrab(nil)

	return nzbData, nil
}

func buildEasynewsGPSQuery(query, season, episode, scope, category string) string {
	query = release.NormalizeTitleForSearchQuery(query)
	if !strings.HasPrefix(strings.TrimSpace(category), "5") {
		return query
	}
	switch config.NormalizeSeriesSearchScope(scope) {
	case config.SeriesSearchScopeSeasonEpisode:
		if season == "" || episode == "" {
			return query
		}
		seasonNum, seasonErr := strconv.Atoi(season)
		episodeNum, episodeErr := strconv.Atoi(episode)
		suffix := fmt.Sprintf("S%sE%s", season, episode)
		if seasonErr == nil && episodeErr == nil {
			suffix = fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
		}
		if easynewsQueryContainsToken(query, suffix) {
			return query
		}
		if query == "" {
			return suffix
		}
		return fmt.Sprintf("%s %s", query, suffix)
	case config.SeriesSearchScopeSeason:
		if season == "" {
			return query
		}
		seasonNum, seasonErr := strconv.Atoi(season)
		suffix := fmt.Sprintf("S%s", season)
		if seasonErr == nil {
			suffix = fmt.Sprintf("S%02d", seasonNum)
		}
		if easynewsQueryContainsToken(query, suffix) {
			return query
		}
		if query == "" {
			return suffix
		}
		return fmt.Sprintf("%s %s", query, suffix)
	default:
		return query
	}
}

func easynewsQueryContainsToken(query, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	for _, part := range strings.Fields(query) {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

// easynewsSearchStats are the response's own counts, kept alongside the mapped
// rows so callers can see how much Easynews filtered before we did.
type easynewsSearchStats struct {
	Total      int
	Returned   int
	Unfiltered int
	NumPages   int
	Pages      int
}

func (c *Client) searchInternal(ctx context.Context, query, season, episode, scope, category string, strictMode bool) ([]easynewsResult, easynewsSearchStats, error) {
	var (
		rows  []interface{}
		stats easynewsSearchStats
	)

	for page := 1; page <= maxSearchPages; page++ {
		data, err := c.fetchSearchPage(ctx, query, season, episode, scope, category, page)
		if err != nil {
			// Page one failing is the search failing; a later page failing just
			// means we return what we already have rather than nothing.
			if page == 1 {
				return nil, easynewsSearchStats{}, err
			}
			logger.Debug("Easynews page fetch failed; keeping earlier pages",
				"indexer", c.Name(), "page", page, "err", err)
			break
		}

		rows = append(rows, data.Data...)
		if page == 1 {
			stats.Total = data.Results
			stats.Unfiltered = data.UnfilteredResults
			stats.NumPages = data.NumPages
		}
		stats.Returned = len(rows)
		stats.Pages = page

		if len(data.Data) == 0 || page >= data.NumPages {
			break
		}
	}

	results := c.filterAndMapResults(easynewsSearchResponse{Data: rows}, query, season, episode, strictMode)

	if stats.Total <= 0 {
		stats.Total = stats.Returned
	}

	return results, stats, nil
}

func (c *Client) fetchSearchPage(ctx context.Context, query, season, episode, scope, category string, page int) (*easynewsSearchResponse, error) {
	opts := c.searchOptions()
	opts.page = page
	searchURL := buildEasynewsSearchURL(query, season, episode, scope, category, opts)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.effectiveQueryHeader())
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("easynews search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("easynews search: %w (status %d)", errCredentialsRejected, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if isThrottleStatus(resp.StatusCode) {
			c.noteThrottled(resp.Header, resp.StatusCode)
			return nil, fmt.Errorf("easynews search failed with status %d: %s: %w", resp.StatusCode, string(body), indexer.ErrRateLimited)
		}
		return nil, fmt.Errorf("easynews search failed with status %d: %s", resp.StatusCode, string(body))
	}

	var data easynewsSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse Easynews response: %w", err)
	}

	return &data, nil
}

// searchOptions is how much filtering to push onto Easynews for one request.
type searchOptions struct {
	advanced       bool
	spamFilter     bool
	fileExtensions string
	page           int
}

func (c *Client) searchOptions() searchOptions {
	return searchOptions{
		advanced:       c.advancedSearch,
		spamFilter:     c.spamFilter,
		fileExtensions: c.fileExtensions,
	}
}

// fileExtensionList is the container whitelist sent as fex, honouring an env
// override but defaulting to the same set results are filtered by.
func fileExtensionList() string {
	if v := env.EasynewsFileExtensions(); v != "" {
		return v
	}
	return defaultFileExtensions
}

func buildEasynewsSearchURL(query, season, episode, scope, category string, opts searchOptions) string {
	params := url.Values{}
	params.Set("fly", "2")
	params.Set("sb", "1")
	page := opts.page
	if page < 1 {
		page = 1
	}
	params.Set("pno", strconv.Itoa(page))
	params.Set("pby", strconv.Itoa(maxResultsPerPage))
	params.Set("u", "1")
	params.Set("chxu", "1")
	params.Set("chxgx", "1")
	params.Set("st", "basic")
	params.Set("gps", buildEasynewsGPSQuery(query, season, episode, scope, category))
	params.Set("vv", "1")
	params.Set("safeO", "0")
	params.Set("s1", "relevance")
	params.Set("s1d", "-")
	params.Add("fty[]", "VIDEO")

	if opts.advanced {
		// st=adv turns on server-side filtering; gx/sS are what the web app
		// sends alongside it. spamf drops Easynews-flagged spam and fex limits
		// results to containers we would accept anyway, so both cut junk before
		// it costs us a row to map.
		params.Set("st", "adv")
		params.Set("gx", "1")
		params.Set("sS", "3")
		if opts.spamFilter {
			params.Set("spamf", "1")
		}
		if opts.fileExtensions != "" {
			params.Set("fex", opts.fileExtensions)
		}
	}

	return fmt.Sprintf("%s%s?%s", easynewsBaseURL, easynewsSearchPath, params.Encode())
}

func (c *Client) downloadNZBInternal(ctx context.Context, payload map[string]interface{}) ([]byte, error) {
	hash, _ := payload["hash"].(string)
	filename, _ := payload["filename"].(string)
	ext, _ := payload["ext"].(string)
	sig, _ := payload["sig"].(string)
	title, _ := payload["title"].(string)

	if hash == "" {
		return nil, fmt.Errorf("missing hash in payload")
	}

	nzbEntries := buildNZBPayload([]easynewsItem{
		{Hash: hash, Filename: filename, Ext: ext, Sig: sig},
	}, title)

	form := url.Values{}
	for key, value := range nzbEntries {
		form.Set(key, value)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", easynewsBaseURL+easynewsNZBPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", c.effectiveGrabHeader())
	resp, err := c.downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("easynews NZB download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if isThrottleStatus(resp.StatusCode) {
			c.noteThrottled(resp.Header, resp.StatusCode)
			return nil, fmt.Errorf("easynews NZB download failed with status %d: %s: %w", resp.StatusCode, string(body), indexer.ErrRateLimited)
		}
		return nil, fmt.Errorf("easynews NZB download failed with status %d: %s", resp.StatusCode, string(body))
	}

	nzbData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read NZB data: %w", err)
	}

	nzbData = injectEasynewsSubject(nzbData, filename, ext)

	return nzbData, nil
}

func (c *Client) checkAPILimit() error {
	if err := c.core.CheckThrottled(c.Name(), time.Now()); err != nil {
		return err
	}
	return c.core.CheckAPILimit(c.Name())
}

func (c *Client) checkDownloadLimit() error {
	if err := c.core.CheckThrottled(c.Name(), time.Now()); err != nil {
		return err
	}
	return c.core.CheckDownloadLimit(c.Name())
}

// noteThrottled mirrors the newznab client: one cooldown per indexer, opened by
// whichever request first gets refused.
func (c *Client) noteThrottled(h http.Header, status int) {
	remaining := c.core.NoteThrottled(h, time.Now())
	logger.Warn("Indexer rate limited; pausing requests",
		"indexer", c.Name(),
		"status", status,
		"retry_after", h.Get("Retry-After"),
		"cooldown", remaining.Round(time.Second))
}

// isThrottleStatus reports whether a status means "not now" rather than
// "not ever". 404/410 are absent on purpose: those are about the content.
func isThrottleStatus(status int) bool {
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

type easynewsSearchResponse struct {
	Data     []interface{} `json:"data"`
	Results  int           `json:"results"`
	ThumbURL string        `json:"thumbURL"`

	// Returned is how many rows this page actually carries and
	// UnfilteredResults how many matched before Easynews' own server-side
	// filtering. The gap between them is what spamf/fex/sS removed, which is
	// otherwise invisible from our side.
	Returned          int `json:"returned"`
	UnfilteredResults int `json:"unfilteredResults"`
	NumPages          int `json:"numPages"`
}

type easynewsResult struct {
	Title           string
	DownloadURL     string
	GUID            string
	PubDate         string
	Size            int64
	DurationSeconds float64
}

type easynewsItem struct {
	Hash     string
	Filename string
	Ext      string
	Sig      string
	Size     int64
	Subject  string
	Poster   string
	Posted   string
	Duration interface{}
	// Easynews' own verdicts on the post, present on object rows only.
	PasswordProtected bool
	VirusFlagged      bool
}

func (c *Client) filterAndMapResults(data easynewsSearchResponse, query, season, episode string, strictMode bool) []easynewsResult {
	results := make([]easynewsResult, 0)

	logEasynewsRowShape(data.Data)

	for _, entry := range data.Data {
		var item easynewsItem

		if arr, ok := entry.([]interface{}); ok && len(arr) >= 12 {
			if hash, ok := arr[0].(string); ok {
				item.Hash = hash
			}
			if subject, ok := arr[6].(string); ok {
				item.Subject = subject
			}
			if filename, ok := arr[10].(string); ok {
				item.Filename = filename
			}
			if ext, ok := arr[11].(string); ok {
				item.Ext = ext
			}
			if poster, ok := arr[7].(string); ok {
				item.Poster = poster
			}
			if posted, ok := arr[8].(string); ok {
				item.Posted = posted
			}

			if len(arr) > 12 {
				if sizeVal, ok := arr[12].(float64); ok {
					item.Size = int64(sizeVal)
				} else if sizeVal, ok := arr[12].(int64); ok {
					item.Size = sizeVal
				} else if sizeVal, ok := arr[12].(int); ok {
					item.Size = int64(sizeVal)
				}
			}
			if len(arr) > 14 {
				item.Duration = arr[14]
			}
		} else if obj, ok := entry.(map[string]interface{}); ok {
			// Object rows are a hybrid: identity fields carry the positional
			// index as a string key, everything else is named. The indices are
			// NOT interchangeable with the array layout — "12" is the video
			// codec here and "14" is not the runtime — so only the three that
			// genuinely overlap get a positional fallback.
			item.Hash = objString(obj, "hash", "0")
			item.Subject = objString(obj, "subject", "6")
			item.Filename = objString(obj, "fn", "10")
			item.Ext = objString(obj, "extension", "11")
			item.Sig = objString(obj, "sig")
			item.Poster = objString(obj, "poster", "7")
			// rawSize is the byte count; size is the humanized string beside it.
			item.Size = objSize(obj, "rawSize", "size")
			item.Duration = objAny(obj, "runtime")
			item.PasswordProtected, _ = objAny(obj, "password", "passwd").(bool)
			item.VirusFlagged, _ = objAny(obj, "virus").(bool)
			if ts := objSize(obj, "timestamp", "ts"); ts > 0 {
				item.Posted = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
			} else {
				item.Posted = objString(obj, "8")
			}
		}

		if item.Hash == "" {
			continue
		}

		// Easynews already knows these are dead ends: a password-protected
		// archive can never be unpacked for playback, and a virus-flagged post
		// is not something to hand a player. Both are definitive, so dropping
		// them here costs nothing downstream.
		if item.PasswordProtected || item.VirusFlagged {
			continue
		}

		extLower := strings.ToLower(item.Ext)
		if !strings.HasPrefix(extLower, ".") {
			extLower = "." + extLower
		}
		if disallowedExts[extLower] {
			continue
		}
		if extLower != "" && !allowedVideoExts[extLower] {
			continue
		}

		durationSeconds := parseDuration(item.Duration)
		if durationSeconds != nil && *durationSeconds < 60 {
			continue
		}

		title := item.Filename
		if item.Ext != "" {
			if !strings.HasPrefix(item.Ext, ".") {
				title += "." + item.Ext
			} else {
				title += item.Ext
			}
		}
		if title == "" {
			title = item.Subject
		}
		if title == "" {
			title = item.Hash
		}

		titleLower := strings.ToLower(title)
		if strings.Contains(titleLower, "sample") {
			continue
		}

		finalTitle := title
		if finalTitle == "" {
			finalTitle = item.Subject
		}
		if finalTitle == "" {
			finalTitle = fmt.Sprintf("Easynews-%s", item.Hash[:min(8, len(item.Hash))])
		}

		payload := map[string]interface{}{
			"hash":     item.Hash,
			"filename": item.Filename,
			"ext":      item.Ext,
			"sig":      item.Sig,
			"title":    finalTitle,
		}
		payloadToken := encodePayload(payload)
		downloadURL := fmt.Sprintf("%s/easynews/nzb?payload=%s", c.downloadBase, url.QueryEscape(payloadToken))

		pubDate := time.Now().Format(time.RFC1123Z)
		if item.Posted != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", item.Posted); err == nil {
				pubDate = t.Format(time.RFC1123Z)
			}
		}

		var durSec float64
		if durationSeconds != nil && *durationSeconds > 0 {
			durSec = float64(*durationSeconds)
		}

		results = append(results, easynewsResult{
			Title:           finalTitle,
			DownloadURL:     downloadURL,
			GUID:            fmt.Sprintf("easynews-%s", item.Hash),
			PubDate:         pubDate,
			Size:            item.Size,
			DurationSeconds: durSec,
		})
	}

	return results
}

// logEasynewsRowShape reports the first row's field names once per search. When
// results map badly the question is always "what did the endpoint actually
// return", and answering it otherwise needs a packet capture. Trace-level: it
// pairs with the per-release drop reasons validation logs at the same level.
func logEasynewsRowShape(rows []interface{}) {
	if len(rows) == 0 {
		return
	}
	switch row := rows[0].(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		logger.Trace("Easynews row shape",
			"form", "object",
			"keys", strings.Join(keys, ","),
			"fn", fmt.Sprintf("%v", row["fn"]),
			"10", fmt.Sprintf("%v", row["10"]),
			"11", fmt.Sprintf("%v", row["11"]))
	case []interface{}:
		logger.Trace("Easynews row shape", "form", "array", "len", len(row))
	default:
		logger.Trace("Easynews row shape", "form", fmt.Sprintf("%T", rows[0]))
	}
}

// objAny returns the first key that is present, so callers can list a named key
// and its positional-array fallback in one place.
func objAny(obj map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if v, ok := obj[key]; ok && v != nil {
			return v
		}
	}
	return nil
}

func objString(obj map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if s, ok := obj[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// objSize reads a byte count that may arrive as a number, a numeric string, or
// a humanized string ("1.4 GB").
func objSize(obj map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		switch v := obj[key].(type) {
		case float64:
			if v > 0 {
				return int64(v)
			}
		case string:
			if n := parseHumanSize(v); n > 0 {
				return n
			}
		}
	}
	return 0
}

var humanSizeUnits = map[string]float64{
	"":  1,
	"B": 1,
	"K": 1 << 10, "KB": 1 << 10, "KIB": 1 << 10,
	"M": 1 << 20, "MB": 1 << 20, "MIB": 1 << 20,
	"G": 1 << 30, "GB": 1 << 30, "GIB": 1 << 30,
	"T": 1 << 40, "TB": 1 << 40, "TIB": 1 << 40,
}

func parseHumanSize(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	cut := len(raw)
	for i, r := range raw {
		if (r < '0' || r > '9') && r != '.' {
			cut = i
			break
		}
	}

	value, err := strconv.ParseFloat(strings.TrimSpace(raw[:cut]), 64)
	if err != nil || value <= 0 {
		return 0
	}
	multiplier, ok := humanSizeUnits[strings.ToUpper(strings.TrimSpace(raw[cut:]))]
	if !ok {
		return 0
	}
	return int64(value * multiplier)
}

func parseDuration(raw interface{}) *int64 {
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case float64:
		if v > 0 {
			sec := int64(v)
			return &sec
		}
	case int64:
		if v > 0 {
			return &v
		}
	case int:
		if v > 0 {
			sec := int64(v)
			return &sec
		}
	case string:

		if num, err := strconv.ParseInt(v, 10, 64); err == nil && num > 0 {
			return &num
		}

		if strings.Contains(v, ":") {
			parts := strings.Split(v, ":")
			if len(parts) == 3 {
				h, _ := strconv.Atoi(parts[0])
				m, _ := strconv.Atoi(parts[1])
				s, _ := strconv.Atoi(parts[2])
				total := int64(h*3600 + m*60 + s)
				if total > 0 {
					return &total
				}
			} else if len(parts) == 2 {
				m, _ := strconv.Atoi(parts[0])
				s, _ := strconv.Atoi(parts[1])
				total := int64(m*60 + s)
				if total > 0 {
					return &total
				}
			}
		}
	}

	return nil
}

func encodePayload(payload map[string]interface{}) string {
	jsonData, _ := json.Marshal(payload)
	encoded := base64.URLEncoding.EncodeToString(jsonData)
	return strings.TrimRight(encoded, "=")
}

func decodePayload(token string) (map[string]interface{}, error) {

	padLen := (4 - len(token)%4) % 4
	token += strings.Repeat("=", padLen)

	decoded, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil, err
	}

	return payload, nil
}

func buildNZBPayload(items []easynewsItem, name string) map[string]string {
	result := map[string]string{
		"autoNZB": "1",
	}

	for i, item := range items {
		key := strconv.Itoa(i)
		if item.Sig != "" {
			key = fmt.Sprintf("%d&sig=%s", i, item.Sig)
		}
		value := buildValueToken(item)
		result[key] = value
	}

	if name != "" {
		result["nameZipQ0"] = name
	}

	return result
}

func buildValueToken(item easynewsItem) string {
	fnB64 := base64.StdEncoding.EncodeToString([]byte(item.Filename))
	fnB64 = strings.TrimRight(fnB64, "=")
	extB64 := base64.StdEncoding.EncodeToString([]byte(item.Ext))
	extB64 = strings.TrimRight(extB64, "=")
	return fmt.Sprintf("%s|%s:%s", item.Hash, fnB64, extB64)
}

func injectEasynewsSubject(data []byte, filename, ext string) []byte {
	if filename == "" && ext == "" {
		return data
	}
	subject := filename
	if ext != "" {
		normalizedExt := ext
		if !strings.HasPrefix(normalizedExt, ".") {
			normalizedExt = "." + normalizedExt
		}
		if !strings.HasSuffix(strings.ToLower(subject), strings.ToLower(normalizedExt)) {
			subject += normalizedExt
		}
	}
	if subject == "" {
		return data
	}

	parsed, err := nzb.Parse(bytes.NewReader(data))
	if err != nil {
		logger.Debug("injectEasynewsSubject: failed to parse NZB, returning raw data", "err", err)
		return data
	}

	for i := range parsed.Files {
		parsed.Files[i].Subject = subject
	}

	out, err := xml.MarshalIndent(parsed, "", "  ")
	if err != nil {
		logger.Debug("injectEasynewsSubject: failed to re-marshal NZB", "err", err)
		return data
	}
	return append([]byte(xml.Header), out...)
}
