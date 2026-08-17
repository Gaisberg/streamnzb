// Package tvmaze looks up series and per-episode air times from the TVMaze
// API. TVMaze is keyless and its airstamps (full timestamps with timezone) are
// maintained aggressively for running shows, which makes it the air-date
// authority for non-anime series — TMDB's date-only air_date is the fallback.
package tvmaze

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"streamnzb/pkg/services/metadata/metacache"
)

const (
	// lookupCacheTTL covers external-id -> show resolution; the mapping is
	// effectively permanent.
	lookupCacheTTL = 7 * 24 * time.Hour
	// episodesCacheTTL covers episode lists: air dates of running shows change
	// (scheduling shifts, new episodes announced).
	episodesCacheTTL = 6 * time.Hour
	// rateLimitRetryDelay is the single-retry backoff for a 429. TVMaze allows
	// ~20 requests per 10 seconds; with caching we make at most two calls per
	// series, so one short retry is enough.
	rateLimitRetryDelay = 2 * time.Second
)

type Client struct {
	httpClient *http.Client
	cache      *metacache.Cache // request path -> body; L1 + persistent L2
	// BaseURL is the TVMaze API root; exported so tests can point at a stub.
	BaseURL string
}

func NewClient(httpClient *http.Client, cache *metacache.Cache) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	if cache == nil {
		cache = metacache.New(nil, "tvmaze")
	}
	return &Client{
		httpClient: httpClient,
		cache:      cache,
		BaseURL:    "https://api.tvmaze.com",
	}
}

// Show is a TVMaze show, optionally with embedded episodes.
type Show struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Premiered string   `json:"premiered"`
	Status    string   `json:"status"`
	Runtime   int      `json:"runtime"`
	Summary   string   `json:"summary"`
	Genres    []string `json:"genres"`
	Rating    struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Image struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
	Embedded struct {
		Episodes []Episode `json:"episodes"`
	} `json:"_embedded"`
}

// Episode is one TVMaze episode. Airstamp is the authoritative ISO 8601 air
// time; Airdate is the date-only fallback.
type Episode struct {
	ID       int    `json:"id"`
	Season   int    `json:"season"`
	Number   int    `json:"number"`
	Name     string `json:"name"`
	Airdate  string `json:"airdate"`
	Airstamp string `json:"airstamp"`
	Summary  string `json:"summary"`
	Image    struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
}

// LookupByIMDB resolves an IMDb id ("tt...") to a TVMaze show. The lookup
// endpoint redirects to the show resource; the final body is what gets cached.
func (c *Client) LookupByIMDB(ctx context.Context, imdbID string) (*Show, error) {
	imdbID = strings.TrimSpace(imdbID)
	if imdbID == "" {
		return nil, fmt.Errorf("empty imdb id")
	}
	return c.getShow(ctx, "/lookup/shows?imdb="+url.QueryEscape(imdbID), lookupCacheTTL)
}

// LookupByTVDB resolves a TVDB id to a TVMaze show.
func (c *Client) LookupByTVDB(ctx context.Context, tvdbID string) (*Show, error) {
	tvdbID = strings.TrimSpace(tvdbID)
	if tvdbID == "" {
		return nil, fmt.Errorf("empty tvdb id")
	}
	return c.getShow(ctx, "/lookup/shows?thetvdb="+url.QueryEscape(tvdbID), lookupCacheTTL)
}

// GetShowWithEpisodes fetches a show with its full episode list embedded.
func (c *Client) GetShowWithEpisodes(ctx context.Context, showID int) (*Show, error) {
	if showID <= 0 {
		return nil, fmt.Errorf("invalid tvmaze show id %d", showID)
	}
	return c.getShow(ctx, fmt.Sprintf("/shows/%d?embed=episodes", showID), episodesCacheTTL)
}

func (c *Client) getShow(ctx context.Context, path string, ttl time.Duration) (*Show, error) {
	body, err := c.getBody(ctx, path, ttl)
	if err != nil {
		return nil, err
	}
	var show Show
	if err := json.Unmarshal(body, &show); err != nil {
		return nil, fmt.Errorf("failed to decode tvmaze response: %w", err)
	}
	return &show, nil
}

// getBody GETs path (relative to BaseURL, cache key) through the response
// cache, retrying once after a rate-limit response. Only 200 bodies are cached.
func (c *Client) getBody(ctx context.Context, path string, ttl time.Duration) ([]byte, error) {
	if body, ok := c.cache.Get(path); ok {
		return body, nil
	}

	body, status, err := c.fetch(ctx, path)
	if err == nil && status == http.StatusTooManyRequests {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(rateLimitRetryDelay):
		}
		body, status, err = c.fetch(ctx, path)
	}
	if err != nil {
		return nil, fmt.Errorf("tvmaze API request failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("tvmaze API returned status %d", status)
	}
	c.cache.Put(path, body, ttl)
	return body, nil
}

func (c *Client) fetch(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "StreamNZB/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}
