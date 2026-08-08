package kitsu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
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
}

type cacheEntry struct {
	details *AnimeDetails
	until   time.Time
}

type Client struct {
	httpClient *http.Client
	cache      sync.Map
	// BaseURL is the Kitsu API root; exported so tests can point at a stub.
	BaseURL string
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	return &Client{
		httpClient: httpClient,
		BaseURL:    "https://kitsu.app/api/edge",
	}
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

	if v, ok := c.cache.Load(kitsuID); ok {
		if ent, _ := v.(*cacheEntry); ent != nil && time.Now().Before(ent.until) {
			return ent.details, nil
		}
	}

	url := fmt.Sprintf("%s/anime/%s?include=mappings", c.BaseURL, kitsuID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "StreamNZB/1.0")
	req.Header.Set("Accept", "application/vnd.api+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kitsu API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kitsu API returned status %d", resp.StatusCode)
	}

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
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

	logger.Debug("Resolved Kitsu anime details",
		"kitsu_id", kitsuID,
		"canonical", details.CanonicalTitle,
		"english", details.EnglishTitle,
		"romaji", details.RomajiTitle,
		"year", details.Year,
		"imdb_id", details.IMDbID,
		"tvdb_id", details.TVDBID,
		"tmdb_id", details.TMDBID)

	c.cache.Store(kitsuID, &cacheEntry{
		details: details,
		until:   time.Now().Add(24 * time.Hour),
	})

	return details, nil
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
