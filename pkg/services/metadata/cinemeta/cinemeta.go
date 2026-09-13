// Package cinemeta talks to the public Cinemeta addon API
// (https://v3-cinemeta.strem.io) — the same source Stremio's own Cinemeta
// addon serves from. It needs no API key and keys everything by IMDb id.
// Cinemeta's meta object is already shaped like the Stremio addon spec, so
// mapping it onto our own MetaObject is close to a field rename rather than
// the heavier TMDB/TVDB transforms.
package cinemeta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"streamnzb/pkg/services/metadata/metacache"
)

// metaCacheTTL matches the other metadata clients' response cache lifetime.
const metaCacheTTL = 24 * time.Hour

type Client struct {
	httpClient *http.Client
	cache      *metacache.Cache
	// BaseURL is the Cinemeta API root; exported so tests can point at a stub.
	BaseURL string
}

func NewClient(httpClient *http.Client) *Client {
	return NewClientWithCache(httpClient, nil)
}

// NewClientWithCache builds a client backed by the shared persistent response
// cache. A nil cache degrades to in-memory-only caching.
func NewClientWithCache(httpClient *http.Client, cache *metacache.Cache) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	if cache == nil {
		cache = metacache.New(nil, "cinemeta")
	}
	return &Client{
		httpClient: httpClient,
		cache:      cache,
		BaseURL:    "https://v3-cinemeta.strem.io",
	}
}

// Video is one episode entry from a series meta object. ID arrives from
// Cinemeta already formatted "tt...:season:episode", matching the id scheme
// our own MetaVideo expects.
type Video struct {
	ID        string
	Title     string
	Season    int
	Episode   int
	Released  string
	Overview  string
	Thumbnail string
}

// Trailer is a YouTube trailer reference.
type Trailer struct {
	Source string
	Type   string
}

// Meta is the subset of Cinemeta's meta object our meta builders need. Cast
// carries names only — Cinemeta has no per-actor character or photo, unlike
// TMDB's credits payload.
type Meta struct {
	IMDbID      string
	Name        string
	Description string
	Poster      string
	Background  string
	Logo        string
	ReleaseInfo string
	Released    string
	IMDBRating  string
	Runtime     string
	Genres      []string
	Cast        []string
	Director    []string
	Writer      []string
	Trailers    []Trailer
	Videos      []Video
}

type rawMeta struct {
	IMDbID      string   `json:"imdb_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Poster      string   `json:"poster"`
	Background  string   `json:"background"`
	Logo        string   `json:"logo"`
	ReleaseInfo string   `json:"releaseInfo"`
	Released    string   `json:"released"`
	IMDBRating  string   `json:"imdbRating"`
	Runtime     string   `json:"runtime"`
	Genres      []string `json:"genres"`
	Cast        []string `json:"cast"`
	Director    []string `json:"director"`
	Writer      []string `json:"writer"`
	Trailers    []struct {
		Source string `json:"source"`
		Type   string `json:"type"`
	} `json:"trailers"`
	Videos []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Season      int    `json:"season"`
		Episode     int    `json:"episode"`
		Number      int    `json:"number"`
		Released    string `json:"released"`
		FirstAired  string `json:"firstAired"`
		Overview    string `json:"overview"`
		Description string `json:"description"`
		Thumbnail   string `json:"thumbnail"`
	} `json:"videos"`
}

type metaEnvelope struct {
	Meta *rawMeta `json:"meta"`
}

// GetMeta fetches the Cinemeta meta object for one title. contentType is
// Cinemeta's own resource path ("movie" or "series" — anything else
// normalizes to "series"); imdbID must be a "tt" id, since Cinemeta keys
// everything by IMDb id and has no other identity scheme.
func (c *Client) GetMeta(ctx context.Context, contentType, imdbID string) (*Meta, error) {
	imdbID = strings.TrimSpace(imdbID)
	if !strings.HasPrefix(imdbID, "tt") {
		return nil, fmt.Errorf("cinemeta: not an imdb id: %q", imdbID)
	}
	if contentType != "movie" {
		contentType = "series"
	}
	path := fmt.Sprintf("/meta/%s/%s.json", contentType, imdbID)

	body, fromCache, err := c.fetchBody(ctx, path)
	if err != nil {
		return nil, err
	}
	var env metaEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("cinemeta: decode response: %w", err)
	}
	if env.Meta == nil || env.Meta.Name == "" {
		return nil, fmt.Errorf("cinemeta: no meta for %s %s", contentType, imdbID)
	}
	// Cache only a body that decoded into a usable meta object — caching
	// before validation would let a malformed or empty response (Cinemeta has
	// no entry for this title yet, a transient bad response, ...) sit in the
	// cache for the full TTL and turn every later request for the same title
	// into the same failure.
	if !fromCache {
		c.cache.Put(path, body, metaCacheTTL)
	}
	return mapMeta(env.Meta), nil
}

// normalizeReleaseInfoDash rewrites Cinemeta's en dash ("2011–2019",
// "2023–") to the plain ASCII hyphen every other source in this codebase
// uses for the same "firstYear-lastYear" / "firstYear-" shape (see
// seriesReleaseInfo in pkg/server/stremio/handlers_meta.go). Nothing else
// here fixes this: our own Jellyfin-compatibility layer's seriesStatus()
// parser (and, plausibly, some Stremio client's own parsing) matches an
// ASCII "-" specifically, so left as Cinemeta serves it, a series sourced
// from Cinemeta would never register as "Continuing" or "Ended" there.
func normalizeReleaseInfoDash(releaseInfo string) string {
	return strings.ReplaceAll(releaseInfo, "–", "-")
}

func mapMeta(raw *rawMeta) *Meta {
	m := &Meta{
		IMDbID:      raw.IMDbID,
		Name:        raw.Name,
		Description: raw.Description,
		Poster:      raw.Poster,
		Background:  raw.Background,
		Logo:        raw.Logo,
		ReleaseInfo: normalizeReleaseInfoDash(raw.ReleaseInfo),
		Released:    raw.Released,
		IMDBRating:  raw.IMDBRating,
		Runtime:     raw.Runtime,
		Genres:      raw.Genres,
		Cast:        raw.Cast,
		Director:    raw.Director,
		Writer:      raw.Writer,
	}
	for _, t := range raw.Trailers {
		if t.Source == "" {
			continue
		}
		m.Trailers = append(m.Trailers, Trailer{Source: t.Source, Type: t.Type})
	}
	for _, v := range raw.Videos {
		episode := v.Episode
		if episode == 0 {
			episode = v.Number
		}
		title := v.Name
		if title == "" {
			title = v.Title
		}
		released := v.Released
		if released == "" {
			released = v.FirstAired
		}
		overview := v.Overview
		if overview == "" {
			overview = v.Description
		}
		m.Videos = append(m.Videos, Video{
			ID:        v.ID,
			Title:     title,
			Season:    v.Season,
			Episode:   episode,
			Released:  released,
			Overview:  overview,
			Thumbnail: v.Thumbnail,
		})
	}
	return m
}

// fetchBody returns the cached body for path, or fetches it fresh. It does
// NOT write a freshly fetched body back to the cache — the caller does that
// only once it has confirmed the body decodes into a usable response, so a
// malformed or empty upstream reply never gets cached.
func (c *Client) fetchBody(ctx context.Context, path string) (body []byte, fromCache bool, err error) {
	if body, ok := c.cache.Get(path); ok {
		return body, true, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", "StreamNZB/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("cinemeta API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("cinemeta API returned status %d", resp.StatusCode)
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	return body, false, nil
}
