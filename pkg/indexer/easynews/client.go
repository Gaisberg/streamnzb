package easynews

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/nzb"
)

const (
	easynewsBaseURL   = "https://members.easynews.com"
	maxResultsPerPage = 250
	searchTimeout     = 15 * time.Second
	downloadTimeout   = 30 * time.Second
)

type Client struct {
	username     string
	password     string
	name         string
	client       *http.Client
	downloadBase string

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

func NewClient(username, password, name string, downloadBase string, apiLimit, downloadLimit int, um *indexer.UsageManager) (*Client, error) {
	if username == "" || password == "" {
		return nil, fmt.Errorf("easynews username and password are required")
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}

	c := &Client{
		username:          username,
		password:          password,
		name:              name,
		downloadBase:      downloadBase,
		usageManager:      um,
		apiLimit:          apiLimit,
		apiUsed:           0,
		apiRemaining:      apiLimit,
		downloadLimit:     downloadLimit,
		downloadUsed:      0,
		downloadRemaining: downloadLimit,
		client: &http.Client{
			Timeout:   searchTimeout,
			Transport: transport,
		},
	}

	if um != nil && name != "" {
		usage := um.GetIndexerUsage(name)
		c.apiUsed = usage.APIHitsUsed
		c.downloadUsed = usage.DownloadsUsed

		c.apiRemaining = apiLimit - usage.APIHitsUsed
		c.downloadRemaining = downloadLimit - usage.DownloadsUsed

		if c.apiRemaining < 0 && apiLimit > 0 {
			c.apiRemaining = 0
		}
		if c.downloadRemaining < 0 && downloadLimit > 0 {
			c.downloadRemaining = 0
		}
	}

	return c, nil
}

func (c *Client) Name() string {
	if c.name != "" {
		return c.name
	}
	return "Easynews"
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
	if c.usageManager == nil || c.name == "" {
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

func (c *Client) Ping() error {

	testQuery := "dune"
	_, err := c.searchInternal(testQuery, "", "", "", false)
	if err != nil {
		return fmt.Errorf("easynews credentials invalid: %w", err)
	}
	return nil
}

func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	if err := c.checkAPILimit(); err != nil {
		return nil, err
	}

	query := strings.TrimSpace(req.Query)
	logger.Debug("Easynews search query", "indexer", c.name, "query", query, "cat", req.Cat, "season", req.Season, "episode", req.Episode)

	season := req.Season
	episode := req.Episode

	results, err := c.searchInternal(query, season, episode, req.Cat, false)
	if err != nil {
		return nil, fmt.Errorf("easynews search failed: %w", err)
	}

	c.mu.Lock()
	c.apiUsed++
	if c.apiRemaining > 0 {
		c.apiRemaining--
	}
	c.mu.Unlock()

	if c.usageManager != nil && c.name != "" {
		c.usageManager.IncrementUsed(c.name, 1, 0)
	}

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

	return &indexer.SearchResponse{
		Channel: indexer.Channel{
			Items: items,
		},
	}, nil
}

func (c *Client) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	if err := c.checkDownloadLimit(); err != nil {
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

	nzbData, err := c.downloadNZBInternal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to download NZB: %w", err)
	}

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

	if c.usageManager != nil && c.name != "" {
		c.usageManager.IncrementUsed(c.name, 1, 1)
	}

	return nzbData, nil
}

func (c *Client) searchInternal(query, season, episode, category string, strictMode bool) ([]easynewsResult, error) {
	params := url.Values{}
	params.Set("fly", "2")
	params.Set("sb", "1")
	params.Set("pno", "1")
	params.Set("pby", strconv.Itoa(maxResultsPerPage))
	params.Set("u", "1")
	params.Set("chxu", "1")
	params.Set("chxgx", "1")
	params.Set("st", "basic")
	params.Set("gps", query)
	params.Set("vv", "1")
	params.Set("safeO", "0")
	params.Set("s1", "relevance")
	params.Set("s1d", "-")
	params.Add("fty[]", "VIDEO")

	if category == "2000" {

	} else if category == "5000" {

		if season != "" && episode != "" {
			params.Set("gps", fmt.Sprintf("%s S%sE%s", query, season, episode))
		}
	}

	searchURL := fmt.Sprintf("%s/2.0/search/solr-search/?%s", easynewsBaseURL, params.Encode())
	logger.Debug("Easynews search request", "indexer", c.name, "url", searchURL, "gps", params.Get("gps"))

	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("easynews search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("easynews rejected credentials")
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("easynews search failed with status %d: %s", resp.StatusCode, string(body))
	}

	var data easynewsSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse Easynews response: %w", err)
	}

	results := c.filterAndMapResults(data, query, season, episode, strictMode)

	return results, nil
}

func (c *Client) downloadNZBInternal(payload map[string]interface{}) ([]byte, error) {
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

	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", easynewsBaseURL+"/2.0/api/dl-nzb", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", env.IndexerGrabHeader())
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("easynews NZB download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
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

type easynewsSearchResponse struct {
	Data     []interface{} `json:"data"`
	Results  int           `json:"results"`
	ThumbURL string        `json:"thumbURL"`
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
}

func (c *Client) filterAndMapResults(data easynewsSearchResponse, query, season, episode string, strictMode bool) []easynewsResult {
	results := make([]easynewsResult, 0)

	disallowedExts := map[string]bool{
		".rar": true, ".zip": true, ".exe": true, ".jpg": true, ".png": true,
	}
	allowedVideoExts := map[string]bool{
		".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true,
		".mov": true, ".wmv": true, ".mpg": true, ".mpeg": true, ".flv": true, ".webm": true,
	}

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

			if hash, ok := obj["hash"].(string); ok {
				item.Hash = hash
			}
			if subject, ok := obj["subject"].(string); ok {
				item.Subject = subject
			}
			if fn, ok := obj["fn"].(string); ok {
				item.Filename = fn
			}
			if ext, ok := obj["extension"].(string); ok {
				item.Ext = ext
			}
			if size, ok := obj["size"].(float64); ok {
				item.Size = int64(size)
			}
			if sig, ok := obj["sig"].(string); ok {
				item.Sig = sig
			}
			if poster, ok := obj["poster"].(string); ok {
				item.Poster = poster
			}
			if rt, ok := obj["runtime"].(float64); ok {
				item.Duration = rt
			}
			if ts, ok := obj["timestamp"].(float64); ok {
				item.Posted = time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")
			}
		}

		if item.Hash == "" {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
