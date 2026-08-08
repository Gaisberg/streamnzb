package tvdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"sync"
	"time"
)

const (
	baseURL        = "https://api4.thetvdb.com/v4"
	stateKey       = "tvdb_token"
	tokenKey       = "token"
	createdAtKey   = "created_at"
	statusKey      = "status"
	successVal     = "success"
	tokenValidDays = 25
)

// metadataCacheTTL bounds the in-memory response caches. Content metadata is
// effectively immutable, so a generous TTL only guards against unbounded growth
// of rarely-repeated keys.
const metadataCacheTTL = 24 * time.Hour

type cacheEntry struct {
	value   interface{}
	expires time.Time
}

func cacheGet(m *sync.Map, key string) (interface{}, bool) {
	v, ok := m.Load(key)
	if !ok {
		return nil, false
	}
	entry := v.(cacheEntry)
	if time.Now().After(entry.expires) {
		m.Delete(key)
		return nil, false
	}
	return entry.value, true
}

func cachePut(m *sync.Map, key string, value interface{}) {
	m.Store(key, cacheEntry{value: value, expires: time.Now().Add(metadataCacheTTL)})
}

type Client struct {
	apiKey  string
	dataDir string
	client  *http.Client
	BaseURL string

	// tokenMu guards tokenCache; the client is shared across concurrent
	// requests and a 401 storm must not interleave invalidate/refresh.
	tokenMu    sync.Mutex
	tokenCache string

	resolveCache sync.Map // remoteID -> string (TVDB id)
	seriesCache  sync.Map // seriesID -> *SeriesDetails
}

func NewClient(apiKey, dataDir string) *Client {
	baseURL := "https://api4.thetvdb.com/v4"
	if envURL := os.Getenv("STREAMNZB_TVDB_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		apiKey:  apiKey,
		dataDir: dataDir,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		BaseURL: baseURL,
	}
}

func (c *Client) Ping() error {
	if c.apiKey == "" {
		return fmt.Errorf("TVDB API key not configured")
	}
	_, err := c.login()
	return err
}

type loginResponse struct {
	Status string `json:"status"`
	Data   struct {
		Token string `json:"token"`
	} `json:"data"`
}

type searchRemoteIDResponse struct {
	Status string `json:"status"`
	Data   []struct {
		Episode *struct {
			SeriesID int `json:"seriesId"`
		} `json:"episode"`
		Movie *struct {
			ID int `json:"id"`
		} `json:"movie"`
		Series *struct {
			ID int `json:"id"`
		} `json:"series"`
	} `json:"data"`
}

type tokenState struct {
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

func (c *Client) ensureToken() (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("TVDB API key not configured")
	}

	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.tokenCache != "" {
		return c.tokenCache, nil
	}

	manager, err := persistence.GetManager(c.dataDir)
	if err != nil {
		return "", fmt.Errorf("failed to get state manager: %w", err)
	}
	var stored tokenState
	if found, _ := manager.Get(stateKey, &stored); found && stored.Token != "" {
		if created, err := time.Parse(time.RFC3339, stored.CreatedAt); err == nil {
			age := time.Since(created)
			if age < tokenValidDays*24*time.Hour {
				c.tokenCache = stored.Token
				return c.tokenCache, nil
			}
			logger.Debug("TVDB token expired, refreshing", "age_days", int(age.Hours()/24))
		}
	}

	token, err := c.login()
	if err != nil {
		return "", err
	}

	state := tokenState{
		Token:     token,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := manager.Set(stateKey, state); err != nil {
		logger.Warn("Failed to save TVDB token to state", "err", err)
	}
	c.tokenCache = token
	return token, nil
}

func (c *Client) login() (string, error) {
	body := map[string]string{"apikey": c.apiKey}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", c.BaseURL+"/login", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("TVDB login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("TVDB login returned status: %d", resp.StatusCode)
	}

	var out loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to decode TVDB login response: %w", err)
	}
	if out.Status != successVal || out.Data.Token == "" {
		return "", fmt.Errorf("TVDB login failed: status=%s", out.Status)
	}
	logger.Debug("TVDB login successful")
	return out.Data.Token, nil
}

func (c *Client) invalidateToken() {
	c.tokenMu.Lock()
	c.tokenCache = ""
	c.tokenMu.Unlock()
	// Clear the persisted copy too, or ensureToken would reload the same
	// server-rejected token and the retry could never succeed.
	if manager, err := persistence.GetManager(c.dataDir); err == nil {
		_ = manager.Set(stateKey, tokenState{})
	}
}

func (c *Client) doRequest(method, path string, body []byte) (*http.Response, error) {
	// One retry: the persisted token can be expired (e.g. past day 25); a 401
	// invalidates it and the second attempt logs in fresh instead of failing
	// the first request after every expiry.
	for attempt := 0; ; attempt++ {
		token, err := c.ensureToken()
		if err != nil {
			return nil, err
		}
		var req *http.Request
		if body != nil {
			req, err = http.NewRequest(method, c.BaseURL+path, bytes.NewReader(body))
		} else {
			req, err = http.NewRequest(method, c.BaseURL+path, nil)
		}
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			c.invalidateToken()
			if attempt == 0 {
				continue
			}
			return nil, fmt.Errorf("TVDB token invalid or expired")
		}
		return resp, nil
	}
}

func (c *Client) ResolveTVDBID(remoteID string) (string, error) {
	if c.apiKey == "" {
		return "", fmt.Errorf("TVDB API key not configured")
	}
	if cached, ok := cacheGet(&c.resolveCache, remoteID); ok {
		return cached.(string), nil
	}
	resp, err := c.doRequest("GET", "/search/remoteid/"+remoteID, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("TVDB search/remoteid returned status: %d", resp.StatusCode)
	}

	var out searchRemoteIDResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("failed to decode TVDB response: %w", err)
	}
	if out.Status != successVal {
		return "", fmt.Errorf("TVDB search failed: status=%s", out.Status)
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("no TVDB result for remote ID: %s", remoteID)
	}

	for _, item := range out.Data {
		if item.Episode != nil && item.Episode.SeriesID != 0 {
			logger.Debug("Resolved TVDB ID from remote ID", "remote", remoteID, "tvdb", item.Episode.SeriesID)
			id := strconv.Itoa(item.Episode.SeriesID)
			cachePut(&c.resolveCache, remoteID, id)
			return id, nil
		}
		if item.Series != nil && item.Series.ID != 0 {
			logger.Debug("Resolved TVDB ID from remote ID (series)", "remote", remoteID, "tvdb", item.Series.ID)
			id := strconv.Itoa(item.Series.ID)
			cachePut(&c.resolveCache, remoteID, id)
			return id, nil
		}
		if item.Movie != nil && item.Movie.ID != 0 {
			logger.Debug("Resolved TVDB ID from remote ID (movie)", "remote", remoteID, "tvdb", item.Movie.ID)
			id := strconv.Itoa(item.Movie.ID)
			cachePut(&c.resolveCache, remoteID, id)
			return id, nil
		}
	}
	return "", fmt.Errorf("no TVDB ID found for remote ID: %s", remoteID)
}

type SeriesDetails struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"originalName"`
	FirstAired   string `json:"firstAired"`
}

type seriesDetailsResponse struct {
	Status string        `json:"status"`
	Data   SeriesDetails `json:"data"`
}

func (c *Client) GetSeriesDetails(seriesID string) (*SeriesDetails, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TVDB API key not configured")
	}
	if cached, ok := cacheGet(&c.seriesCache, seriesID); ok {
		return cached.(*SeriesDetails), nil
	}
	resp, err := c.doRequest("GET", "/series/"+seriesID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TVDB /series/%s returned status: %d", seriesID, resp.StatusCode)
	}

	var out seriesDetailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode TVDB response: %w", err)
	}
	if out.Status != successVal {
		return nil, fmt.Errorf("TVDB get series failed: status=%s", out.Status)
	}
	cachePut(&c.seriesCache, seriesID, &out.Data)
	return &out.Data, nil
}
