// Package seadex looks up SeaDex (releases.moe) release recommendations for an
// anime. SeaDex curates, per AniList entry, which release groups produced the
// best and the notable alternative releases of that specific title — a
// per-title judgment that no static group tier can reproduce.
//
// SeaDex catalogs torrents, so a usenet release is matched by release-group
// name, not by hash: the recommendation transfers whenever the same group's
// release circulates on usenet under its group tag.
package seadex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"streamnzb/pkg/services/metadata/metacache"
)

// entryCacheTTL is the response TTL. SeaDex entries change rarely — a day-old
// recommendation is still the recommendation — and misses ("no entry for this
// anime") are cached the same way, since a 200 with zero items is a valid body.
const entryCacheTTL = 24 * time.Hour

// Torrent is one recommended release of an entry.
type Torrent struct {
	ReleaseGroup string
	IsBest       bool
	DualAudio    bool
	Tracker      string
}

// Entry is the SeaDex record for one anime: the curated set of recommended
// releases. A nil Entry means SeaDex has no record for the title.
type Entry struct {
	AniListID int
	Torrents  []Torrent
}

// GroupSets projects the entry onto normalized release-group names: best is
// every group with at least one release marked best for this anime, alt every
// recommended group without a best mark. A group with both a best and a lesser
// release lands only in best — the stronger claim wins.
func (e *Entry) GroupSets() (best, alt map[string]bool) {
	best, alt = make(map[string]bool), make(map[string]bool)
	if e == nil {
		return best, alt
	}
	for _, t := range e.Torrents {
		g := NormalizeGroup(t.ReleaseGroup)
		if g == "" {
			continue
		}
		if t.IsBest {
			best[g] = true
			delete(alt, g)
		} else if !best[g] {
			alt[g] = true
		}
	}
	return best, alt
}

// DualAudioGroups projects the entry onto the normalized release-group names
// whose recommended release for this title is dual audio. It follows the same
// "stronger claim wins" rule as GroupSets: when a group has a release marked
// best, that release decides, and its alternatives do not — so a group whose
// best is sub-only stays false even if it also has a dual-audio alternative,
// and `seadex.best and not seadex.dualAudio` means what it says. A group with
// no best mark is judged by its recommended alternatives. Per title, like the
// rest of the namespace.
func (e *Entry) DualAudioGroups() map[string]bool {
	out := make(map[string]bool)
	if e == nil {
		return out
	}
	type claim struct{ best, bestDual, altDual bool }
	claims := make(map[string]*claim)
	for _, t := range e.Torrents {
		g := NormalizeGroup(t.ReleaseGroup)
		if g == "" {
			continue
		}
		c := claims[g]
		if c == nil {
			c = &claim{}
			claims[g] = c
		}
		if t.IsBest {
			c.best = true
			c.bestDual = c.bestDual || t.DualAudio
		} else {
			c.altDual = c.altDual || t.DualAudio
		}
	}
	for g, c := range claims {
		if (c.best && c.bestDual) || (!c.best && c.altDual) {
			out[g] = true
		}
	}
	return out
}

// NormalizeGroup canonicalizes a release-group name for matching: SeaDex spells
// groups as they name themselves ("SubsPlease"), release parsers report what
// the filename carried, and case is the only difference that occurs in
// practice.
func NormalizeGroup(group string) string {
	return strings.ToLower(strings.TrimSpace(group))
}

// Client queries the SeaDex PocketBase API.
type Client struct {
	httpClient *http.Client
	cache      *metacache.Cache // request path -> body; L1 + persistent L2
	// BaseURL is the SeaDex API root; exported so tests can point at a stub.
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
		cache = metacache.New(nil, "seadex")
	}
	baseURL := "https://releases.moe/api"
	if envURL := strings.TrimSpace(os.Getenv("STREAMNZB_SEADEX_BASE_URL")); envURL != "" {
		baseURL = envURL
	}
	return &Client{
		httpClient: httpClient,
		cache:      cache,
		BaseURL:    baseURL,
	}
}

// getBody GETs path (relative to BaseURL, cache key) through the response
// cache. Only 200 bodies are cached.
func (c *Client) getBody(ctx context.Context, path string, ttl time.Duration) ([]byte, error) {
	if body, ok := c.cache.Get(path); ok {
		return body, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "StreamNZB/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("seadex API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seadex API returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	c.cache.Put(path, body, ttl)
	return body, nil
}

// apiResponse mirrors the PocketBase list envelope with the torrent relation
// expanded in place.
type apiResponse struct {
	Items []struct {
		AniListID int `json:"alID"`
		Expand    struct {
			Torrents []struct {
				ReleaseGroup string `json:"releaseGroup"`
				IsBest       bool   `json:"isBest"`
				DualAudio    bool   `json:"dualAudio"`
				Tracker      string `json:"tracker"`
			} `json:"trs"`
		} `json:"expand"`
	} `json:"items"`
}

// GetEntry fetches the SeaDex entry for an AniList id. A title SeaDex has not
// cataloged returns (nil, nil): absence is an answer, not an error — the
// distinction is what lets rule evaluation treat lookup failure as unknown but
// an uncataloged title as a plain false.
func (c *Client) GetEntry(ctx context.Context, anilistID int) (*Entry, error) {
	if c == nil {
		return nil, fmt.Errorf("seadex client not configured")
	}
	if anilistID <= 0 {
		return nil, fmt.Errorf("invalid anilist id %d", anilistID)
	}

	params := url.Values{}
	params.Set("page", "1")
	params.Set("perPage", "1")
	params.Set("filter", fmt.Sprintf("alID=%d", anilistID))
	params.Set("expand", "trs")
	path := "/collections/entries/records?" + params.Encode()

	body, err := c.getBody(ctx, path, entryCacheTTL)
	if err != nil {
		return nil, err
	}

	var resp apiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode seadex API response: %w", err)
	}
	if len(resp.Items) == 0 {
		return nil, nil
	}

	item := resp.Items[0]
	entry := &Entry{AniListID: item.AniListID}
	for _, t := range item.Expand.Torrents {
		entry.Torrents = append(entry.Torrents, Torrent{
			ReleaseGroup: strings.TrimSpace(t.ReleaseGroup),
			IsBest:       t.IsBest,
			DualAudio:    t.DualAudio,
			Tracker:      t.Tracker,
		})
	}
	return entry, nil
}
