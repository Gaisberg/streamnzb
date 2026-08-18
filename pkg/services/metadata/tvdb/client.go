package tvdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/metacache"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"
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

	// lang3 is the display language as the ISO 639-3 code TVDB's translation
	// endpoints address ("deu", "fin"); empty means the default (English)
	// record. See SetLanguage.
	lang3 string

	resolveCache sync.Map // remoteID -> string (TVDB id)
	seriesCache  sync.Map // seriesID -> *SeriesDetails

	// cache backs the meta-source endpoints (extended details, episodes) with
	// the shared persistent response cache. The auth token lives in a header,
	// never in the key.
	cache *metacache.Cache
}

func NewClient(apiKey, dataDir string) *Client {
	return NewClientWithCache(apiKey, dataDir, nil)
}

// NewClientWithCache builds a client backed by the shared persistent response
// cache. A nil cache degrades to in-memory-only caching.
func NewClientWithCache(apiKey, dataDir string, cache *metacache.Cache) *Client {
	baseURL := "https://api4.thetvdb.com/v4"
	if envURL := os.Getenv("STREAMNZB_TVDB_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	if cache == nil {
		cache = metacache.New(nil, "tvdb")
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
		cache:   cache,
	}
}

func (c *Client) Ping() error {
	if c.apiKey == "" {
		return fmt.Errorf("TVDB API key not configured")
	}
	_, err := c.login()
	return err
}

// SetLanguage sets the display language from a TMDB-style tag ("de-DE").
// TVDB's v4 API addresses translations by ISO 639-3 code, which x/text
// derives from the tag; an unparseable tag or English keeps translations off.
func (c *Client) SetLanguage(tag string) {
	c.lang3 = ""
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return
	}
	parsed, err := language.Parse(tag)
	if err != nil {
		logger.Debug("TVDB display language tag not parseable, staying English", "tag", tag, "err", err)
		return
	}
	base, _ := parsed.Base()
	if iso3 := base.ISO3(); iso3 != "" && iso3 != "eng" {
		c.lang3 = iso3
	}
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

// episodesCacheTTL bounds the episode-list cache: air dates and late episode
// additions of running shows change, unlike the rest of TVDB's metadata.
const episodesCacheTTL = 6 * time.Hour

// getBodyCached GETs path through the shared response cache. Only 200 bodies
// are cached; the auth token lives in a header and never reaches the key.
func (c *Client) getBodyCached(path string, ttl time.Duration) ([]byte, error) {
	body, ok, err := c.getBodyCachedOptional(path, ttl, false)
	if err == nil && !ok {
		return nil, fmt.Errorf("TVDB %s returned status: %d", path, http.StatusNotFound)
	}
	return body, err
}

// notFoundSentinel marks a cached 404 so known-absent records (translations
// in niche languages, mostly) do not re-hit the API on every meta request.
var notFoundSentinel = []byte(`{"status":"notfound"}`)

// getBodyCachedOptional fetches like getBodyCached, but with allowNotFound a
// 404 is a cacheable "record does not exist" answer — ok=false, no error —
// instead of a failure. TVDB answers 404 for translations that simply were
// never written, which is the normal case, not an incident.
func (c *Client) getBodyCachedOptional(path string, ttl time.Duration, allowNotFound bool) ([]byte, bool, error) {
	if body, ok := c.cache.Get(path); ok {
		if bytes.Equal(body, notFoundSentinel) {
			return nil, false, nil
		}
		return body, true, nil
	}
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		if allowNotFound {
			c.cache.Put(path, notFoundSentinel, ttl)
		}
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("TVDB %s returned status: %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	c.cache.Put(path, body, ttl)
	return body, true, nil
}

// SeriesExtended carries the display fields of /series/{id}/extended — the
// meta-source record, unlike the resolution-only SeriesDetails.
type SeriesExtended struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Overview   string `json:"overview"`
	Image      string `json:"image"` // poster; TVDB returns absolute URLs
	FirstAired string `json:"firstAired"`
	LastAired  string `json:"lastAired"`
	Year       string `json:"year"`
	Status     struct {
		Name string `json:"name"`
	} `json:"status"`
	AverageRuntime int `json:"averageRuntime"`
	Genres         []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Artworks []struct {
		Image string `json:"image"`
		Type  int    `json:"type"`
		Score int64  `json:"score"`
	} `json:"artworks"`
	RemoteIDs []struct {
		ID         string `json:"id"`
		SourceName string `json:"sourceName"`
	} `json:"remoteIds"`
	// Characters carries the cast (and other people) in TVDB's sort order.
	Characters []struct {
		Name         string `json:"name"` // character name
		PersonName   string `json:"personName"`
		PeopleType   string `json:"peopleType"`
		PersonImgURL string `json:"personImgURL"` // actor headshot
	} `json:"characters"`
	Trailers []struct {
		URL string `json:"url"`
	} `json:"trailers"`
}

// CastMember is one credited actor, with the character and headshot when TVDB
// publishes them.
type CastMember struct {
	Name      string
	Character string
	Photo     string
}

// CastMembers returns the actors in TVDB's order, capped at limit.
func (s *SeriesExtended) CastMembers(limit int) []CastMember {
	var cast []CastMember
	seen := make(map[string]bool)
	for _, ch := range s.Characters {
		if !strings.EqualFold(ch.PeopleType, "Actor") || ch.PersonName == "" || seen[ch.PersonName] {
			continue
		}
		seen[ch.PersonName] = true
		cast = append(cast, CastMember{Name: ch.PersonName, Character: ch.Name, Photo: ch.PersonImgURL})
		if len(cast) >= limit {
			break
		}
	}
	return cast
}

// Cast returns the actor names in TVDB's order, capped at limit.
func (s *SeriesExtended) Cast(limit int) []string {
	members := s.CastMembers(limit)
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	return names
}

// IMDbID returns the IMDb remote id ("tt..."), or "".
func (s *SeriesExtended) IMDbID() string {
	for _, remote := range s.RemoteIDs {
		if strings.EqualFold(remote.SourceName, "IMDB") && strings.HasPrefix(remote.ID, "tt") {
			return remote.ID
		}
	}
	return ""
}

// TVDB artwork type ids for series records.
const (
	artworkTypeSeriesBackground = 3  // 1920x1080 fanart
	artworkTypeSeriesClearLogo  = 23 // transparent PNG title logo
)

// bestArtwork returns the highest-scored artwork of the given type, or "".
// Artworks arrive in no useful order, and the first one is frequently a bad
// one; score is TVDB's community ranking.
func (s *SeriesExtended) bestArtwork(artworkType int) string {
	best, bestScore := "", int64(-1)
	for _, art := range s.Artworks {
		if art.Type == artworkType && art.Image != "" && art.Score > bestScore {
			best, bestScore = art.Image, art.Score
		}
	}
	return best
}

// Background returns the highest-scored background artwork, or "".
func (s *SeriesExtended) Background() string {
	return s.bestArtwork(artworkTypeSeriesBackground)
}

// ClearLogo returns the highest-scored transparent title logo, or "".
func (s *SeriesExtended) ClearLogo() string {
	return s.bestArtwork(artworkTypeSeriesClearLogo)
}

type seriesExtendedResponse struct {
	Status string         `json:"status"`
	Data   SeriesExtended `json:"data"`
}

// GetSeriesExtended fetches the extended series record (artwork, overview,
// genres, status).
func (c *Client) GetSeriesExtended(seriesID string) (*SeriesExtended, error) {
	if c == nil {
		return nil, fmt.Errorf("TVDB client not configured")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("TVDB API key not configured")
	}
	body, err := c.getBodyCached("/series/"+seriesID+"/extended", metadataCacheTTL)
	if err != nil {
		return nil, err
	}
	var out seriesExtendedResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to decode TVDB response: %w", err)
	}
	if out.Status != successVal {
		return nil, fmt.Errorf("TVDB series extended failed: status=%s", out.Status)
	}
	c.applySeriesTranslation(seriesID, &out.Data)
	return &out.Data, nil
}

type seriesTranslationResponse struct {
	Status string `json:"status"`
	Data   struct {
		Name     string `json:"name"`
		Overview string `json:"overview"`
	} `json:"data"`
}

// SeriesTranslation returns the display language's name and overview for the
// series, or empty strings when translations are off (English) or TVDB has
// none. Public because some callers need to know whether a real translation
// exists — the anime path keeps Kitsu's English synopsis unless one does.
func (c *Client) SeriesTranslation(seriesID string) (name, overview string) {
	if c == nil || c.lang3 == "" {
		return "", ""
	}
	body, ok, err := c.getBodyCachedOptional(fmt.Sprintf("/series/%s/translations/%s", seriesID, c.lang3), metadataCacheTTL, true)
	if err != nil {
		logger.Debug("TVDB series translation fetch failed", "series_id", seriesID, "lang", c.lang3, "err", err)
		return "", ""
	}
	if !ok {
		// No translation record exists — the expected case for niche
		// languages, answered from the cached 404 after the first look.
		logger.Trace("TVDB series translation absent", "series_id", seriesID, "lang", c.lang3)
		return "", ""
	}
	var out seriesTranslationResponse
	if err := json.Unmarshal(body, &out); err != nil || out.Status != successVal {
		return "", ""
	}
	return out.Data.Name, out.Data.Overview
}

// applySeriesTranslation overlays the display language's name and overview
// onto the extended record. Missing translations (404, empty fields) keep the
// default-language record — localization never loses data.
func (c *Client) applySeriesTranslation(seriesID string, ext *SeriesExtended) {
	name, overview := c.SeriesTranslation(seriesID)
	if name != "" {
		ext.Name = name
	}
	if overview != "" {
		ext.Overview = overview
	}
}

// Episode is one episode from /series/{id}/episodes/default, in TVDB's
// default (aired) season order.
type Episode struct {
	SeasonNumber int    `json:"seasonNumber"`
	Number       int    `json:"number"`
	Name         string `json:"name"`
	Aired        string `json:"aired"`
	Overview     string `json:"overview"`
	Image        string `json:"image"`
}

type seriesEpisodesResponse struct {
	Status string `json:"status"`
	Data   struct {
		Episodes []Episode `json:"episodes"`
	} `json:"data"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

// episodesMaxPages caps pagination; TVDB pages hold 500 episodes, so the cap
// only truncates extreme long-runners.
const episodesMaxPages = 6

// GetSeriesEpisodes fetches the full default-order episode list, with names
// and overviews translated to the display language where TVDB has them.
func (c *Client) GetSeriesEpisodes(seriesID string) ([]Episode, error) {
	if c == nil {
		return nil, fmt.Errorf("TVDB client not configured")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("TVDB API key not configured")
	}
	episodes, err := c.fetchEpisodePages(fmt.Sprintf("/series/%s/episodes/default", seriesID), false)
	if err != nil {
		return nil, err
	}
	c.applyEpisodeTranslations(seriesID, episodes)
	return episodes, nil
}

// fetchEpisodePages walks one paginated episodes endpoint to the end. With
// optional, a 404 on the first page means the endpoint has no record (an
// untranslated language variant) and yields an empty list with the absence
// cached, rather than an error.
func (c *Client) fetchEpisodePages(basePath string, optional bool) ([]Episode, error) {
	var episodes []Episode
	for page := 0; page < episodesMaxPages; page++ {
		body, ok, err := c.getBodyCachedOptional(fmt.Sprintf("%s?page=%d", basePath, page), episodesCacheTTL, optional)
		if err == nil && !ok {
			if !optional && page == 0 {
				return nil, fmt.Errorf("TVDB %s returned status: %d", basePath, http.StatusNotFound)
			}
			break
		}
		if err != nil {
			// A missing later page must not throw away what is already fetched.
			if page > 0 {
				break
			}
			return nil, err
		}
		var out seriesEpisodesResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("failed to decode TVDB episodes response: %w", err)
		}
		if out.Status != successVal {
			return nil, fmt.Errorf("TVDB series episodes failed: status=%s", out.Status)
		}
		episodes = append(episodes, out.Data.Episodes...)
		if out.Links.Next == nil || *out.Links.Next == "" {
			break
		}
	}
	return episodes, nil
}

// applyEpisodeTranslations overlays translated names and overviews onto the
// default-language episodes. The translated endpoint serves the same episode
// list; entries TVDB has no translation for carry empty text and keep their
// default-language fields.
func (c *Client) applyEpisodeTranslations(seriesID string, episodes []Episode) {
	if c.lang3 == "" || len(episodes) == 0 {
		return
	}
	translated, err := c.fetchEpisodePages(fmt.Sprintf("/series/%s/episodes/default/%s", seriesID, c.lang3), true)
	if err != nil {
		logger.Debug("TVDB episode translations fetch failed", "series_id", seriesID, "lang", c.lang3, "err", err)
		return
	}
	byNumber := make(map[[2]int]*Episode, len(episodes))
	for i := range episodes {
		byNumber[[2]int{episodes[i].SeasonNumber, episodes[i].Number}] = &episodes[i]
	}
	for _, tr := range translated {
		ep, ok := byNumber[[2]int{tr.SeasonNumber, tr.Number}]
		if !ok {
			continue
		}
		if tr.Name != "" {
			ep.Name = tr.Name
		}
		if tr.Overview != "" {
			ep.Overview = tr.Overview
		}
	}
}

// SeriesListing is one row of a /series/filter listing.
type SeriesListing struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Year     string `json:"year"`
	Overview string `json:"overview"`
}

type seriesFilterResponse struct {
	Status string          `json:"status"`
	Data   []SeriesListing `json:"data"`
}

// listingCacheTTL bounds filter listings, whose ordering drifts.
const listingCacheTTL = 3 * time.Hour

// FilterSeries fetches one page of TVDB's series filter listing. sort is
// "score" (TVDB's popularity ranking) or "firstAired". The endpoint requires
// a country and language; english-language titles are the pragmatic default.
func (c *Client) FilterSeries(sort string, page int) ([]SeriesListing, error) {
	if c == nil {
		return nil, fmt.Errorf("TVDB client not configured")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("TVDB API key not configured")
	}
	path := fmt.Sprintf("/series/filter?country=usa&lang=eng&sort=%s&sortType=desc&page=%d", sort, page)
	body, err := c.getBodyCached(path, listingCacheTTL)
	if err != nil {
		return nil, err
	}
	var out seriesFilterResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to decode TVDB filter response: %w", err)
	}
	if out.Status != successVal {
		return nil, fmt.Errorf("TVDB series filter failed: status=%s", out.Status)
	}
	return out.Data, nil
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
