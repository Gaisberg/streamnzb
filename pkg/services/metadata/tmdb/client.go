package tmdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"strings"
	"sync"
	"time"
)

// responseCacheTTL bounds the in-memory response cache. TMDB metadata is
// effectively immutable for our purposes; the TTL only caps growth of
// rarely-repeated keys.
const responseCacheTTL = 24 * time.Hour

type cachedResponse struct {
	status  int
	body    []byte
	expires time.Time
}

type Client struct {
	apiKey  string
	client  *http.Client
	BaseURL string

	responseCache sync.Map // full request URL -> cachedResponse
}

func NewClient(apiKey string) *Client {
	baseURL := "https://api.themoviedb.org/3"
	if envURL := os.Getenv("STREAMNZB_TMDB_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		apiKey: apiKey,
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
		BaseURL: baseURL,
	}
}

func (c *Client) Ping() error {
	if c.apiKey == "" {
		return fmt.Errorf("TMDB Read Access Token not configured")
	}

	// Uncached: Ping exists to verify live connectivity and the token.
	resp, err := c.fetch(c.BaseURL + "/configuration?")
	if err != nil {
		return fmt.Errorf("TMDB ping request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TMDB returned status: %d (verify your Read Access Token)", resp.StatusCode)
	}

	return nil
}

type FindResponse struct {
	MovieResults     []Result `json:"movie_results"`
	PersonResults    []Result `json:"person_results"`
	TVResults        []Result `json:"tv_results"`
	TVEpisodeResults []Result `json:"tv_episode_results"`
	TVSeasonResults  []Result `json:"tv_season_results"`
}

type Result struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	Title            string `json:"title"`
	OriginalName     string `json:"original_name"`
	OriginalTitle    string `json:"original_title"`
	OriginalLanguage string `json:"original_language"`
	MediaType        string `json:"media_type"`
	Overview         string `json:"overview"`
	ReleaseDate      string `json:"release_date"`
	FirstAirDate     string `json:"first_air_date"`
}

type SearchMultiResponse struct {
	Page         int                 `json:"page"`
	Results      []SearchMultiResult `json:"results"`
	TotalPages   int                 `json:"total_pages"`
	TotalResults int                 `json:"total_results"`
}

type SearchMultiResult struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	MediaType     string `json:"media_type"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	PosterPath    string `json:"poster_path"`
	Overview      string `json:"overview"`
}

type ExternalIDsResponse struct {
	ID          int    `json:"id"`
	IMDbID      string `json:"imdb_id"`
	TVDBID      int    `json:"tvdb_id"`
	FreebaseID  string `json:"freebase_id"`
	WikidataID  string `json:"wikidata_id"`
	FacebookID  string `json:"facebook_id"`
	InstagramID string `json:"instagram_id"`
	TwitterID   string `json:"twitter_id"`
}

type MovieTranslationsResponse struct {
	ID           int                     `json:"id"`
	Translations []MovieTranslationEntry `json:"translations"`
}

type TVTranslationsResponse struct {
	ID           int                  `json:"id"`
	Translations []TVTranslationEntry `json:"translations"`
}

type AlternativeTitle struct {
	ISO3166_1 string `json:"iso_3166_1"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}

type MovieAlternativeTitlesResponse struct {
	ID     int                `json:"id"`
	Titles []AlternativeTitle `json:"titles"`
}

type TVAlternativeTitlesResponse struct {
	ID      int                `json:"id"`
	Results []AlternativeTitle `json:"results"`
}

type MovieTranslationEntry struct {
	ISO639_1    string               `json:"iso_639_1"`
	ISO3166_1   string               `json:"iso_3166_1"`
	Name        string               `json:"name"`
	EnglishName string               `json:"english_name"`
	Data        MovieTranslationData `json:"data"`
}

type MovieTranslationData struct {
	Title    string `json:"title"`
	Overview string `json:"overview"`
}

type TVTranslationEntry struct {
	ISO639_1    string            `json:"iso_639_1"`
	ISO3166_1   string            `json:"iso_3166_1"`
	Name        string            `json:"name"`
	EnglishName string            `json:"english_name"`
	Data        TVTranslationData `json:"data"`
}

type TVTranslationData struct {
	Name     string `json:"name"`
	Overview string `json:"overview"`
}

// fetch performs an uncached GET against the TMDB API.
func (c *Client) fetch(reqURL string) (*http.Response, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("accept", "application/json")
	return c.client.Do(req)
}

// doRequest performs a GET against the TMDB API with a 24h response cache.
// Caching sits here so every endpoint method benefits without per-method
// bookkeeping; only 200 responses are cached.
func (c *Client) doRequest(endpoint string, params url.Values) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	if v, ok := c.responseCache.Load(reqURL); ok {
		cached := v.(cachedResponse)
		if time.Now().Before(cached.expires) {
			return &http.Response{
				StatusCode: cached.status,
				Body:       io.NopCloser(bytes.NewReader(cached.body)),
				Header:     make(http.Header),
			}, nil
		}
		c.responseCache.Delete(reqURL)
	}

	resp, err := c.fetch(reqURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	c.responseCache.Store(reqURL, cachedResponse{
		status:  resp.StatusCode,
		body:    body,
		expires: time.Now().Add(responseCacheTTL),
	})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// getJSON performs a cached GET against endpoint and decodes the JSON body
// into a fresh T. It is a free function because Go does not allow type
// parameters on methods. label appears in errors, e.g. "movie details".
func getJSON[T any](c *Client, endpoint string, params url.Values, label string) (*T, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB Read Access Token not configured")
	}
	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB %s request failed: %w", label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode TMDB %s response: %w", label, err)
	}
	return &out, nil
}

func (c *Client) Find(externalID, source string) (*FindResponse, error) {
	params := url.Values{}
	params.Set("external_source", source)
	return getJSON[FindResponse](c, fmt.Sprintf(c.BaseURL+"/find/%s", externalID), params, "find")
}

func (c *Client) SearchMulti(query string) (*SearchMultiResponse, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", "1")
	return getJSON[SearchMultiResponse](c, c.BaseURL+"/search/multi", params, "search")
}

func (c *Client) GetExternalIDs(tmdbID int, mediaType string) (*ExternalIDsResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/%s/%d/external_ids", mediaType, tmdbID)
	return getJSON[ExternalIDsResponse](c, endpoint, url.Values{}, "external_ids")
}

// Genre is a TMDB genre. Only the name is used.
type Genre struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type MovieDetails struct {
	ID               int     `json:"id"`
	Title            string  `json:"title"`
	ReleaseDate      string  `json:"release_date"`
	OriginalTitle    string  `json:"original_title"`
	OriginalLanguage string  `json:"original_language"`
	Genres           []Genre `json:"genres"`
}

type TVDetails struct {
	ID               int            `json:"id"`
	Name             string         `json:"name"`
	OriginalName     string         `json:"original_name"`
	OriginalLanguage string         `json:"original_language"`
	FirstAirDate     string         `json:"first_air_date"`
	NumberOfSeasons  int            `json:"number_of_seasons"`
	Seasons          []TVSeasonInfo `json:"seasons"`
	Genres           []Genre        `json:"genres"`
}

type TVSeasonInfo struct {
	SeasonNumber int    `json:"season_number"`
	EpisodeCount int    `json:"episode_count"`
	Name         string `json:"name"`
}

type TVSeasonDetails struct {
	SeasonNumber int             `json:"season_number"`
	Episodes     []TVEpisodeInfo `json:"episodes"`
}

type TVEpisodeInfo struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	AirDate       string `json:"air_date"`
}

func (c *Client) GetMovieTitle(imdbID string, tmdbID string) (string, error) {
	title, _, err := c.GetMovieTitleAndYear(imdbID, tmdbID)
	return title, err
}

func (c *Client) GetMovieTitleAndYear(imdbID string, tmdbID string) (title string, year string, err error) {
	if tmdbID != "" {
		if id, parseErr := strconv.Atoi(tmdbID); parseErr == nil {
			d, getErr := c.GetMovieDetails(id)
			if getErr != nil {
				return "", "", getErr
			}
			year = ""
			if len(d.ReleaseDate) >= 4 {
				year = d.ReleaseDate[:4]
			}
			return d.Title, year, nil
		}
	}
	if imdbID != "" {
		find, findErr := c.Find(imdbID, "imdb_id")
		if findErr != nil {
			return "", "", findErr
		}
		if len(find.MovieResults) > 0 {
			return find.MovieResults[0].Title, "", nil
		}
	}
	return "", "", fmt.Errorf("could not resolve movie title")
}

func (c *Client) GetTVShowName(tmdbID string, imdbID string) (string, error) {
	name, _, err := c.GetTVShowTitleAndYear(tmdbID, imdbID)
	return name, err
}

func (c *Client) GetTVShowTitleAndYear(tmdbID string, imdbID string) (title string, year string, err error) {
	if tmdbID != "" {
		if id, err := strconv.Atoi(tmdbID); err == nil {
			d, err := c.GetTVDetails(id)
			if err != nil {
				return "", "", err
			}
			if len(d.FirstAirDate) >= 4 {
				year = d.FirstAirDate[:4]
			}
			return d.Name, year, nil
		}
	}
	if imdbID != "" {
		find, err := c.Find(imdbID, "imdb_id")
		if err != nil {
			return "", "", err
		}
		if len(find.TVResults) > 0 {
			if len(find.TVResults[0].FirstAirDate) >= 4 {
				year = find.TVResults[0].FirstAirDate[:4]
			}
			return find.TVResults[0].Name, year, nil
		}
	}
	return "", "", fmt.Errorf("could not resolve TV show name")
}

func (c *Client) GetMovieDetails(tmdbID int) (*MovieDetails, error) {
	return c.GetMovieDetailsWithLanguage(tmdbID, "")
}

func (c *Client) GetMovieDetailsWithLanguage(tmdbID int, language string) (*MovieDetails, error) {
	params := url.Values{}
	if language != "" {
		params.Set("language", language)
	}
	return getJSON[MovieDetails](c, fmt.Sprintf(c.BaseURL+"/movie/%d", tmdbID), params, "movie details")
}

func (c *Client) GetMovieTranslations(movieID int) (*MovieTranslationsResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/movie/%d/translations", movieID)
	return getJSON[MovieTranslationsResponse](c, endpoint, url.Values{}, "movie translations")
}

func (c *Client) GetMovieAlternativeTitles(movieID int) (*MovieAlternativeTitlesResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/movie/%d/alternative_titles", movieID)
	return getJSON[MovieAlternativeTitlesResponse](c, endpoint, url.Values{}, "movie alternative titles")
}

func movieTitleFromTranslations(translations *MovieTranslationsResponse, language string) string {
	if translations == nil || language == "" {
		return ""
	}
	langCode, countryCode := splitLanguageTag(language)
	for i := range translations.Translations {
		t := &translations.Translations[i]
		if t.Data.Title == "" {
			continue
		}

		if countryCode != "" {
			if strings.EqualFold(t.ISO639_1, langCode) && strings.EqualFold(t.ISO3166_1, countryCode) {
				logger.Debug("TMDB translation match", "requested", language, "iso_639_1", t.ISO639_1, "iso_3166_1", t.ISO3166_1, "title", t.Data.Title)
				return t.Data.Title
			}
		} else {
			if strings.EqualFold(t.ISO639_1, langCode) {
				logger.Debug("TMDB translation match (language only)", "requested", language, "iso_639_1", t.ISO639_1, "title", t.Data.Title)
				return t.Data.Title
			}
		}
	}

	for i := range translations.Translations {
		t := &translations.Translations[i]
		if t.Data.Title != "" && strings.EqualFold(t.ISO639_1, langCode) {
			logger.Debug("TMDB translation match (fallback)", "requested", language, "iso_639_1", t.ISO639_1, "iso_3166_1", t.ISO3166_1, "title", t.Data.Title)
			return t.Data.Title
		}
	}
	logger.Debug("TMDB no translation for language", "requested", language, "lang_code", langCode, "country_code", countryCode, "available", len(translations.Translations))
	return ""
}

func splitLanguageTag(tag string) (lang, country string) {
	tag = strings.TrimSpace(tag)
	if i := strings.Index(tag, "-"); i >= 0 {
		return tag[:i], tag[i+1:]
	}
	return tag, ""
}

func (c *Client) GetMovieTitleForSearch(imdbID, tmdbID, language string, includeYear, normalize bool) (string, error) {
	var movieID int
	var title, year string

	if tmdbID != "" {
		if id, err := strconv.Atoi(tmdbID); err == nil {
			movieID = id
			d, err := c.GetMovieDetails(id)
			if err == nil {
				title = d.Title
				if includeYear && len(d.ReleaseDate) >= 4 {
					year = d.ReleaseDate[:4]
				}
				logger.Debug("TMDB movie title from details", "tmdb_id", movieID, "title", title, "language", language)
			}
		}
	}
	if movieID == 0 && imdbID != "" {
		find, err := c.Find(imdbID, "imdb_id")
		if err != nil {
			return "", err
		}
		if len(find.MovieResults) > 0 {
			movieID = find.MovieResults[0].ID
			title = find.MovieResults[0].Title
			if includeYear && find.MovieResults[0].ReleaseDate != "" && len(find.MovieResults[0].ReleaseDate) >= 4 {
				year = find.MovieResults[0].ReleaseDate[:4]
			}
			logger.Debug("TMDB movie resolved from IMDb Find", "imdb_id", imdbID, "tmdb_id", movieID, "default_title", title)
		}
	}

	if language != "" && movieID != 0 {
		tr, err := c.GetMovieTranslations(movieID)
		if err != nil {
			logger.Debug("TMDB translations not used, falling back to default title", "movie_id", movieID, "language", language, "err", err)
		} else if t := movieTitleFromTranslations(tr, language); t != "" {
			title = t
			logger.Debug("TMDB movie title for search (translated)", "language", language, "title", title)
		}
	}

	if title == "" {
		return "", fmt.Errorf("could not resolve movie title")
	}
	out := strings.TrimSpace(title)
	if year != "" {
		out = out + " " + year
	}
	if normalize {
		out = release.NormalizeTitleForFilename(out)
	}
	return out, nil
}

func (c *Client) GetTVDetails(tmdbID int) (*TVDetails, error) {
	return c.GetTVDetailsWithLanguage(tmdbID, "")
}

func (c *Client) GetTVDetailsWithLanguage(tmdbID int, language string) (*TVDetails, error) {
	params := url.Values{}
	if language != "" {
		params.Set("language", language)
	}
	return getJSON[TVDetails](c, fmt.Sprintf(c.BaseURL+"/tv/%d", tmdbID), params, "TV details")
}

func (c *Client) GetTVTranslations(tmdbID int) (*TVTranslationsResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/tv/%d/translations", tmdbID)
	return getJSON[TVTranslationsResponse](c, endpoint, url.Values{}, "TV translations")
}

func (c *Client) GetTVAlternativeTitles(tmdbID int) (*TVAlternativeTitlesResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/tv/%d/alternative_titles", tmdbID)
	return getJSON[TVAlternativeTitlesResponse](c, endpoint, url.Values{}, "TV alternative titles")
}

func (c *Client) GetTVSeasonDetails(seriesID, seasonNumber int) (*TVSeasonDetails, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/tv/%d/season/%d", seriesID, seasonNumber)
	return getJSON[TVSeasonDetails](c, endpoint, url.Values{}, "TV season details")
}

func (c *Client) ResolveTVDBID(imdbID string) (string, error) {

	findResp, err := c.Find(imdbID, "imdb_id")
	if err != nil {
		return "", err
	}

	if len(findResp.TVResults) == 0 {
		return "", fmt.Errorf("no TV show found for IMDb ID: %s", imdbID)
	}

	tmdbID := findResp.TVResults[0].ID
	logger.Debug("Resolved TMDB ID from IMDb", "imdb", imdbID, "tmdb", tmdbID)

	extIDs, err := c.GetExternalIDs(tmdbID, "tv")
	if err != nil {
		return "", err
	}

	if extIDs.TVDBID == 0 {
		return "", fmt.Errorf("no TVDB ID found for TMDB ID: %d", tmdbID)
	}

	logger.Debug("Resolved TVDB ID", "tvdb", extIDs.TVDBID)
	return strconv.Itoa(extIDs.TVDBID), nil
}
