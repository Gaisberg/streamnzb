package kitsu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/metacache"
)

type AnimeDetails struct {
	ID             string   `json:"id"`
	CanonicalTitle string   `json:"canonical_title"`
	EnglishTitle   string   `json:"english_title,omitempty"`
	RomajiTitle    string   `json:"romaji_title,omitempty"`
	Synonyms       []string `json:"synonyms,omitempty"`
	Year           string   `json:"year,omitempty"`
	ShowType       string   `json:"show_type,omitempty"`
	IMDbID         string   `json:"imdb_id,omitempty"`
	TVDBID         string   `json:"tvdb_id,omitempty"`
	TMDBID         string   `json:"tmdb_id,omitempty"`
	AgeRating      string   `json:"age_rating,omitempty"` // Kitsu enum: G / PG / R / R18
	Nsfw           bool     `json:"nsfw,omitempty"`
}

// detailsCacheTTL is the response TTL for anime detail lookups.
const detailsCacheTTL = 24 * time.Hour

type Client struct {
	httpClient *http.Client
	cache      *metacache.Cache // request path -> body; L1 + persistent L2
	// BaseURL is the Kitsu API root; exported so tests can point at a stub.
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
		cache = metacache.New(nil, "kitsu")
	}
	return &Client{
		httpClient: httpClient,
		cache:      cache,
		BaseURL:    "https://kitsu.app/api/edge",
	}
}

// getBody GETs path (relative to BaseURL, cache key) through the response
// cache. Only 200 bodies are cached.
func (c *Client) getBody(ctx context.Context, path string, ttl time.Duration) (body []byte, fromCache bool, err error) {
	if body, ok := c.cache.Get(path); ok {
		return body, true, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", "StreamNZB/1.0")
	req.Header.Set("Accept", "application/vnd.api+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("kitsu API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("kitsu API returned status %d", resp.StatusCode)
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	c.cache.Put(path, body, ttl)
	return body, false, nil
}

type apiResponse struct {
	Data struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			CanonicalTitle string `json:"canonicalTitle"`
			Titles         struct {
				EN   string `json:"en"`
				ENJP string `json:"en_jp"`
				JAJP string `json:"ja_jp"`
			} `json:"titles"`
			AbbreviatedTitles []string `json:"abbreviatedTitles"`
			StartDate         string   `json:"startDate"`
			ShowType          string   `json:"showType"`
			AgeRating         string   `json:"ageRating"`
			Nsfw              bool     `json:"nsfw"`
		} `json:"attributes"`
	} `json:"data"`
	Included []struct {
		Type       string `json:"type"`
		Attributes struct {
			ExternalSite string `json:"externalSite"`
			ExternalID   string `json:"externalId"`
		} `json:"attributes"`
	} `json:"included"`
}

func (c *Client) GetAnimeDetails(ctx context.Context, kitsuID string) (*AnimeDetails, error) {
	kitsuID = strings.TrimSpace(kitsuID)
	if kitsuID == "" {
		return nil, fmt.Errorf("empty kitsu id")
	}

	body, fromCache, err := c.getBody(ctx, fmt.Sprintf("/anime/%s?include=mappings", kitsuID), detailsCacheTTL)
	if err != nil {
		return nil, err
	}

	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode kitsu API response: %w", err)
	}

	attr := apiResp.Data.Attributes
	details := &AnimeDetails{
		ID:             apiResp.Data.ID,
		CanonicalTitle: strings.TrimSpace(attr.CanonicalTitle),
		EnglishTitle:   strings.TrimSpace(attr.Titles.EN),
		RomajiTitle:    strings.TrimSpace(attr.Titles.ENJP),
		Synonyms:       cleanSynonyms(attr.AbbreviatedTitles),
		ShowType:       strings.TrimSpace(attr.ShowType),
		AgeRating:      strings.TrimSpace(attr.AgeRating),
		Nsfw:           attr.Nsfw,
	}

	if len(attr.StartDate) >= 4 {
		details.Year = attr.StartDate[:4]
	}

	for _, inc := range apiResp.Included {
		if inc.Type == "mappings" {
			site := strings.ToLower(inc.Attributes.ExternalSite)
			extID := strings.TrimSpace(inc.Attributes.ExternalID)
			if extID == "" {
				continue
			}
			switch {
			case strings.HasPrefix(site, "thetvdb") || site == "tvdb":
				if details.TVDBID == "" {
					details.TVDBID = extID
				}
			case strings.HasPrefix(site, "imdb"):
				if details.IMDbID == "" {
					if !strings.HasPrefix(extID, "tt") && len(extID) > 0 && extID[0] >= '0' && extID[0] <= '9' {
						extID = "tt" + extID
					}
					details.IMDbID = extID
				}
			case strings.HasPrefix(site, "themoviedb") || site == "tmdb":
				if details.TMDBID == "" {
					details.TMDBID = extID
				}
			}
		}
	}

	if !fromCache {
		logger.Debug("Resolved Kitsu anime details",
			"kitsu_id", kitsuID,
			"canonical", details.CanonicalTitle,
			"english", details.EnglishTitle,
			"romaji", details.RomajiTitle,
			"year", details.Year,
			"imdb_id", details.IMDbID,
			"tvdb_id", details.TVDBID,
			"tmdb_id", details.TMDBID)
	}

	return details, nil
}

// AnimeMeta carries the display attributes the Stremio meta resource needs,
// which the search-oriented AnimeDetails deliberately does not decode.
type AnimeMeta struct {
	ID             string
	CanonicalTitle string
	EnglishTitle   string
	Synopsis       string
	PosterImage    string
	CoverImage     string
	StartDate      string
	AverageRating  string
	EpisodeCount   int
	ShowType       string
	AgeRating      string // Kitsu enum: G / PG / R / R18
	Nsfw           bool
}

type metaAPIResponse struct {
	Data struct {
		ID         string `json:"id"`
		Attributes struct {
			CanonicalTitle string `json:"canonicalTitle"`
			Titles         struct {
				EN string `json:"en"`
			} `json:"titles"`
			Synopsis      string `json:"synopsis"`
			StartDate     string `json:"startDate"`
			AverageRating string `json:"averageRating"`
			EpisodeCount  int    `json:"episodeCount"`
			ShowType      string `json:"showType"`
			AgeRating     string `json:"ageRating"`
			Nsfw          bool   `json:"nsfw"`
			PosterImage   struct {
				Medium   string `json:"medium"`
				Original string `json:"original"`
			} `json:"posterImage"`
			CoverImage struct {
				Original string `json:"original"`
			} `json:"coverImage"`
		} `json:"attributes"`
	} `json:"data"`
}

// GetAnimeMeta fetches the display attributes for one anime.
func (c *Client) GetAnimeMeta(ctx context.Context, kitsuID string) (*AnimeMeta, error) {
	kitsuID = strings.TrimSpace(kitsuID)
	if kitsuID == "" {
		return nil, fmt.Errorf("empty kitsu id")
	}
	body, _, err := c.getBody(ctx, fmt.Sprintf("/anime/%s", kitsuID), detailsCacheTTL)
	if err != nil {
		return nil, err
	}
	var resp metaAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode kitsu API response: %w", err)
	}
	attr := resp.Data.Attributes
	poster := attr.PosterImage.Original
	if poster == "" {
		poster = attr.PosterImage.Medium
	}
	return &AnimeMeta{
		ID:             resp.Data.ID,
		CanonicalTitle: strings.TrimSpace(attr.CanonicalTitle),
		EnglishTitle:   strings.TrimSpace(attr.Titles.EN),
		Synopsis:       strings.TrimSpace(attr.Synopsis),
		PosterImage:    poster,
		CoverImage:     attr.CoverImage.Original,
		StartDate:      attr.StartDate,
		AverageRating:  attr.AverageRating,
		EpisodeCount:   attr.EpisodeCount,
		ShowType:       strings.TrimSpace(attr.ShowType),
		AgeRating:      strings.TrimSpace(attr.AgeRating),
		Nsfw:           attr.Nsfw,
	}, nil
}

// EpisodeInfo is one episode from the Kitsu episodes endpoint. Number is the
// entry-relative episode number — the same numbering kitsu:<id>:<ep> stream
// ids carry.
type EpisodeInfo struct {
	Number         int
	CanonicalTitle string
	Synopsis       string
	Airdate        string
	Thumbnail      string
}

const (
	episodesPageSize = 20
	// episodesMaxPages caps pagination. Kitsu entries are per-cour, so almost
	// every entry fits in a page or two; the cap only truncates extreme
	// long-runners.
	episodesMaxPages = 15
)

type episodesAPIResponse struct {
	Data []struct {
		Attributes struct {
			CanonicalTitle string `json:"canonicalTitle"`
			Synopsis       string `json:"synopsis"`
			Number         int    `json:"number"`
			Airdate        string `json:"airdate"`
			Thumbnail      struct {
				Original string `json:"original"`
			} `json:"thumbnail"`
		} `json:"attributes"`
	} `json:"data"`
}

// GetAnimeEpisodes fetches the episode list for one anime, paginating with
// explicit offsets so every page has a stable cache key.
func (c *Client) GetAnimeEpisodes(ctx context.Context, kitsuID string) ([]EpisodeInfo, error) {
	kitsuID = strings.TrimSpace(kitsuID)
	if kitsuID == "" {
		return nil, fmt.Errorf("empty kitsu id")
	}
	var episodes []EpisodeInfo
	for page := 0; page < episodesMaxPages; page++ {
		path := fmt.Sprintf("/anime/%s/episodes?page[limit]=%d&page[offset]=%d&sort=number",
			kitsuID, episodesPageSize, page*episodesPageSize)
		body, _, err := c.getBody(ctx, path, detailsCacheTTL)
		if err != nil {
			// A missing later page must not throw away what is already fetched.
			if page > 0 {
				break
			}
			return nil, err
		}
		var resp episodesAPIResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to decode kitsu episodes response: %w", err)
		}
		for _, ep := range resp.Data {
			episodes = append(episodes, EpisodeInfo{
				Number:         ep.Attributes.Number,
				CanonicalTitle: strings.TrimSpace(ep.Attributes.CanonicalTitle),
				Synopsis:       strings.TrimSpace(ep.Attributes.Synopsis),
				Airdate:        ep.Attributes.Airdate,
				Thumbnail:      ep.Attributes.Thumbnail.Original,
			})
		}
		if len(resp.Data) < episodesPageSize {
			break
		}
	}
	return episodes, nil
}

// listingCacheTTL covers trending and search listings, whose contents drift.
const listingCacheTTL = 3 * time.Hour

// AnimeListing is one row of a trending or search listing.
type AnimeListing struct {
	ID             string
	CanonicalTitle string
	Synopsis       string
	PosterImage    string
	CoverImage     string
	AgeRating      string // Kitsu enum: G / PG / R / R18
	Nsfw           bool
}

type listingAPIResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			CanonicalTitle string `json:"canonicalTitle"`
			Synopsis       string `json:"synopsis"`
			AgeRating      string `json:"ageRating"`
			Nsfw           bool   `json:"nsfw"`
			PosterImage    struct {
				Medium   string `json:"medium"`
				Original string `json:"original"`
			} `json:"posterImage"`
			CoverImage struct {
				Original string `json:"original"`
			} `json:"coverImage"`
		} `json:"attributes"`
	} `json:"data"`
}

// GetAnimeListing fetches one listing by kind: "trending" (Kitsu's own
// trending feed, unpaged upstream), "top_rated" (by average rating) or
// "popular" (by user count).
func (c *Client) GetAnimeListing(ctx context.Context, kind string, offset int) ([]AnimeListing, error) {
	switch kind {
	case "trending":
		return c.getListing(ctx, "/trending/anime?limit=20")
	case "top_rated":
		return c.getListing(ctx, fmt.Sprintf("/anime?sort=-averageRating&page[limit]=20&page[offset]=%d", offset))
	case "popular":
		return c.getListing(ctx, fmt.Sprintf("/anime?sort=-userCount&page[limit]=20&page[offset]=%d", offset))
	}
	return nil, fmt.Errorf("unknown kitsu listing kind %q", kind)
}

// GetAnimeKidsListing fetches the most popular anime within the given Kitsu
// age ratings ("G" or "G,PG"), filtered server-side — a kids row that is
// dense by construction instead of a thinned-out general listing.
func (c *Client) GetAnimeKidsListing(ctx context.Context, offset int, ratings string) ([]AnimeListing, error) {
	return c.getListing(ctx, fmt.Sprintf("/anime?filter[ageRating]=%s&sort=-userCount&page[limit]=20&page[offset]=%d", ratings, offset))
}

// SearchAnime searches anime by text with offset pagination.
func (c *Client) SearchAnime(ctx context.Context, text string, offset int) ([]AnimeListing, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	path := fmt.Sprintf("/anime?filter[text]=%s&page[limit]=20&page[offset]=%d", url.QueryEscape(text), offset)
	return c.getListing(ctx, path)
}

func (c *Client) getListing(ctx context.Context, path string) ([]AnimeListing, error) {
	body, _, err := c.getBody(ctx, path, listingCacheTTL)
	if err != nil {
		return nil, err
	}
	var resp listingAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode kitsu listing response: %w", err)
	}
	listings := make([]AnimeListing, 0, len(resp.Data))
	for _, item := range resp.Data {
		poster := item.Attributes.PosterImage.Medium
		if poster == "" {
			poster = item.Attributes.PosterImage.Original
		}
		listings = append(listings, AnimeListing{
			ID:             item.ID,
			CanonicalTitle: strings.TrimSpace(item.Attributes.CanonicalTitle),
			Synopsis:       strings.TrimSpace(item.Attributes.Synopsis),
			PosterImage:    poster,
			CoverImage:     item.Attributes.CoverImage.Original,
			AgeRating:      strings.TrimSpace(item.Attributes.AgeRating),
			Nsfw:           item.Attributes.Nsfw,
		})
	}
	return listings, nil
}

func cleanSynonyms(raw []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, s := range raw {
		trimmed := strings.TrimSpace(s)
		if trimmed != "" && !seen[strings.ToLower(trimmed)] {
			seen[strings.ToLower(trimmed)] = true
			out = append(out, trimmed)
		}
	}
	return out
}
