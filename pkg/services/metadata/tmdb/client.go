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
	"streamnzb/pkg/services/metadata/metacache"
	"strings"
	"time"
)

// responseCacheTTL is the default response TTL. TMDB metadata is effectively
// immutable for our purposes; the TTL only caps growth of rarely-repeated keys.
const responseCacheTTL = 24 * time.Hour

// volatileCacheTTL covers endpoints whose results are expected to change
// between visits (trending, popular, search listings).
const volatileCacheTTL = 3 * time.Hour

// Client is shared by every stream; display language is a per-call parameter
// on the meta-path methods (metadata profiles differ per stream), never
// client state — a mutable language field here would be a data race.
type Client struct {
	apiKey  string
	client  *http.Client
	BaseURL string

	cache *metacache.Cache // request path -> body; L1 + persistent L2
}

func NewClient(apiKey string) *Client {
	return NewClientWithCache(apiKey, nil)
}

// NewClientWithCache builds a client backed by the shared persistent response
// cache. A nil cache degrades to in-memory-only caching, which is what the
// plain NewClient and tests get.
func NewClientWithCache(apiKey string, cache *metacache.Cache) *Client {
	baseURL := "https://api.themoviedb.org/3"
	if envURL := os.Getenv("STREAMNZB_TMDB_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
	if cache == nil {
		cache = metacache.New(nil, "tmdb")
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
		cache:   cache,
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
	BackdropPath  string `json:"backdrop_path"`
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

// doRequest performs a GET against the TMDB API with a response cache.
// Caching sits here so every endpoint method benefits without per-method
// bookkeeping; only 200 responses are cached. Cache keys are the request path
// relative to BaseURL so persisted entries are independent of base-URL
// overrides (tests, STREAMNZB_TMDB_BASE_URL).
func (c *Client) doRequest(endpoint string, params url.Values) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())
	cacheKey := strings.TrimPrefix(reqURL, c.BaseURL)

	if body, ok := c.cache.Get(cacheKey); ok {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
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
	c.cache.Put(cacheKey, body, ttlForEndpoint(cacheKey))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// ttlForEndpoint picks the cache TTL by endpoint class: listing endpoints whose
// contents drift (trending, popular, search, ...) get a short TTL, everything
// else the long default.
func ttlForEndpoint(cacheKey string) time.Duration {
	if strings.HasPrefix(cacheKey, "/trending/") || strings.HasPrefix(cacheKey, "/search/") || strings.HasPrefix(cacheKey, "/discover/") {
		return volatileCacheTTL
	}
	for _, listing := range []string{"/popular", "/top_rated", "/now_playing", "/upcoming", "/on_the_air"} {
		if strings.Contains(cacheKey, listing) {
			return volatileCacheTTL
		}
	}
	return responseCacheTTL
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

// ListingResponse is the shared shape of TMDB's paged listing endpoints
// (trending, popular, per-type search); results reuse the search result shape.
type ListingResponse struct {
	Page       int                 `json:"page"`
	Results    []SearchMultiResult `json:"results"`
	TotalPages int                 `json:"total_pages"`
}

// GetListing fetches one paged listing. mediaType is "movie" or "tv"; kind is
// "trending" (the weekly window) or one of TMDB's list endpoints (popular,
// top_rated, now_playing, upcoming, on_the_air). lang is the display language
// tag; "" keeps the request parameter-free (English, stable cache keys).
func (c *Client) GetListing(mediaType, kind string, page int, lang string) (*ListingResponse, error) {
	params := url.Values{}
	params.Set("page", strconv.Itoa(max(page, 1)))
	if lang != "" {
		params.Set("language", lang)
	}
	var endpoint string
	switch kind {
	case "trending":
		endpoint = fmt.Sprintf(c.BaseURL+"/trending/%s/week", mediaType)
	case "popular", "top_rated", "now_playing", "upcoming", "on_the_air":
		endpoint = fmt.Sprintf(c.BaseURL+"/%s/%s", mediaType, kind)
	default:
		return nil, fmt.Errorf("unknown TMDB listing kind %q", kind)
	}
	return getJSON[ListingResponse](c, endpoint, params, "listing "+kind)
}

// GetRecommendations fetches TMDB's recommendations for one title — the seed
// of "Because You Watched" catalog rows. mediaType is "movie" or "tv"; lang
// is the display language tag, "" for the parameter-free English default.
func (c *Client) GetRecommendations(mediaType string, tmdbID, page int, lang string) (*ListingResponse, error) {
	params := url.Values{}
	params.Set("page", strconv.Itoa(max(page, 1)))
	if lang != "" {
		params.Set("language", lang)
	}
	endpoint := fmt.Sprintf(c.BaseURL+"/%s/%d/recommendations", mediaType, tmdbID)
	return getJSON[ListingResponse](c, endpoint, params, "recommendations")
}

// DiscoverFilters are the server-side filters a discover listing applies —
// what makes purpose-built catalog rows (family, animated, kids) dense by
// construction instead of thinned-out general rows.
type DiscoverFilters struct {
	// Genres is a comma-separated TMDB genre id list (with_genres).
	Genres string
	// MaxCert caps movie results at a US certification ("PG", "PG-13").
	// Movies only — TMDB's TV discover has no certification filter.
	MaxCert string
}

// Discover fetches one page of /discover/{movie|tv} sorted by popularity.
// lang is the display language tag, "" for the parameter-free English
// default. Filters ride the query string, so every (filters, page, lang)
// combination caches under its own key.
func (c *Client) Discover(mediaType string, filters DiscoverFilters, page int, lang string) (*ListingResponse, error) {
	params := url.Values{}
	params.Set("page", strconv.Itoa(max(page, 1)))
	params.Set("sort_by", "popularity.desc")
	if lang != "" {
		params.Set("language", lang)
	}
	if filters.Genres != "" {
		params.Set("with_genres", filters.Genres)
	}
	if filters.MaxCert != "" && mediaType == "movie" {
		params.Set("certification_country", "US")
		params.Set("certification.lte", filters.MaxCert)
	}
	endpoint := fmt.Sprintf(c.BaseURL+"/discover/%s", mediaType)
	return getJSON[ListingResponse](c, endpoint, params, "discover")
}

// SearchByType searches one media type ("movie" or "tv") — unlike SearchMulti,
// results are homogeneous, which is what a typed catalog needs.
func (c *Client) SearchByType(mediaType, query string, page int) (*ListingResponse, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", strconv.Itoa(max(page, 1)))
	endpoint := fmt.Sprintf(c.BaseURL+"/search/%s", mediaType)
	return getJSON[ListingResponse](c, endpoint, params, "search")
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
	// Display fields, used by the Stremio meta resource.
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	Runtime      int     `json:"runtime"`
	VoteAverage  float64 `json:"vote_average"`
	IMDbID       string  `json:"imdb_id"`
	Tagline      string  `json:"tagline"`
	// Credits, Videos, Images and ReleaseDates are populated only when the
	// details request appended them (GetMovieDetailsFull).
	Credits      *Credits              `json:"credits"`
	Videos       *Videos               `json:"videos"`
	Images       *Images               `json:"images"`
	ReleaseDates *ReleaseDatesResponse `json:"release_dates"`
}

// ReleaseDatesResponse carries per-country movie certifications from
// /movie/{id}/release_dates (standalone or appended).
type ReleaseDatesResponse struct {
	Results []struct {
		ISO3166_1    string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
		} `json:"release_dates"`
	} `json:"results"`
}

// Certifications flattens the response into (country, label) pairs, skipping
// empty labels.
func (r *ReleaseDatesResponse) Certifications() [][2]string {
	if r == nil {
		return nil
	}
	var out [][2]string
	for _, res := range r.Results {
		for _, rd := range res.ReleaseDates {
			if rd.Certification != "" {
				out = append(out, [2]string{res.ISO3166_1, rd.Certification})
			}
		}
	}
	return out
}

// ContentRatingsResponse carries per-country TV certifications from
// /tv/{id}/content_ratings (standalone or appended).
type ContentRatingsResponse struct {
	Results []struct {
		ISO3166_1 string `json:"iso_3166_1"`
		Rating    string `json:"rating"`
	} `json:"results"`
}

// Certifications flattens the response into (country, label) pairs, skipping
// empty labels.
func (r *ContentRatingsResponse) Certifications() [][2]string {
	if r == nil {
		return nil
	}
	var out [][2]string
	for _, res := range r.Results {
		if res.Rating != "" {
			out = append(out, [2]string{res.ISO3166_1, res.Rating})
		}
	}
	return out
}

// Credits is the appended credits payload shared by movie and TV details.
type Credits struct {
	Cast []struct {
		Name        string `json:"name"`
		Character   string `json:"character"`
		ProfilePath string `json:"profile_path"`
		Order       int    `json:"order"`
	} `json:"cast"`
	Crew []struct {
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"crew"`
}

// appendedLanguages lists the languages appended images and videos are
// filtered to: the display language plus the English and textless/undubbed
// entries the pickers fall back to. lang is a TMDB-style tag ("de-DE").
func appendedLanguages(lang string) string {
	base, _, _ := strings.Cut(lang, "-")
	if base != "" && base != "en" {
		return base + ",en,null"
	}
	return "en,null"
}

// applyLanguage adds the display-language parameters to a details request;
// "" adds nothing, keeping the default path parameter-free (stable cache
// keys). include_video_language exists because a bare language param makes
// TMDB return only trailers dubbed in that language — usually none.
func applyLanguage(params url.Values, lang string) {
	if lang == "" {
		return
	}
	params.Set("language", lang)
	params.Set("include_video_language", appendedLanguages(lang))
}

// Images is the appended images payload; only logos are consumed (posters and
// backdrops already arrive as poster_path/backdrop_path on the details).
type Images struct {
	Logos []struct {
		FilePath    string  `json:"file_path"`
		ISO639_1    string  `json:"iso_639_1"`
		VoteAverage float64 `json:"vote_average"`
	} `json:"logos"`
}

// BestLogo returns the file path of the highest-voted logo, preferring the
// given ISO 639-1 language, then English, then textless entries, or "".
func (im *Images) BestLogo(preferred string) string {
	if im == nil {
		return ""
	}
	langs := []string{"en", ""}
	if preferred != "" && preferred != "en" {
		langs = []string{preferred, "en", ""}
	}
	for _, lang := range langs {
		best, bestVotes := "", -1.0
		for _, logo := range im.Logos {
			if logo.ISO639_1 == lang && logo.FilePath != "" && logo.VoteAverage > bestVotes {
				best, bestVotes = logo.FilePath, logo.VoteAverage
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// Videos is the appended videos payload; Key is a YouTube id for Site YouTube.
type Videos struct {
	Results []struct {
		Key      string `json:"key"`
		Site     string `json:"site"`
		Type     string `json:"type"`
		Official bool   `json:"official"`
	} `json:"results"`
}

// GetMovieDetailsFull fetches movie details with credits, videos, images and
// release dates appended — one request, everything the meta resource renders.
// lang is the display language tag; "" keeps the English default.
func (c *Client) GetMovieDetailsFull(tmdbID int, lang string) (*MovieDetails, error) {
	params := url.Values{}
	params.Set("append_to_response", "credits,videos,images,release_dates")
	params.Set("include_image_language", appendedLanguages(lang))
	applyLanguage(params, lang)
	return getJSON[MovieDetails](c, fmt.Sprintf(c.BaseURL+"/movie/%d", tmdbID), params, "movie details")
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
	// Display fields, used by the Stremio meta resource.
	Overview       string  `json:"overview"`
	PosterPath     string  `json:"poster_path"`
	BackdropPath   string  `json:"backdrop_path"`
	VoteAverage    float64 `json:"vote_average"`
	EpisodeRunTime []int   `json:"episode_run_time"`
	Status         string  `json:"status"`
	LastAirDate    string  `json:"last_air_date"`
	// ExternalIDs, Credits, Videos, Images and ContentRatings are populated
	// only when the details request appended them (see GetTVDetailsWithSeasons).
	ExternalIDs    *ExternalIDsResponse    `json:"external_ids"`
	Credits        *Credits                `json:"credits"`
	Videos         *Videos                 `json:"videos"`
	Images         *Images                 `json:"images"`
	ContentRatings *ContentRatingsResponse `json:"content_ratings"`
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
	SeasonNumber  int    `json:"season_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	AirDate       string `json:"air_date"`
	StillPath     string `json:"still_path"`
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

// GetMovieReleaseDates fetches just the certification payload for one movie —
// the small cached lookup catalog filtering and the playback gate use.
func (c *Client) GetMovieReleaseDates(movieID int) (*ReleaseDatesResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/movie/%d/release_dates", movieID)
	return getJSON[ReleaseDatesResponse](c, endpoint, url.Values{}, "movie release dates")
}

// GetTVContentRatings fetches just the certification payload for one series —
// the small cached lookup catalog filtering and the playback gate use.
func (c *Client) GetTVContentRatings(tmdbID int) (*ContentRatingsResponse, error) {
	endpoint := fmt.Sprintf(c.BaseURL+"/tv/%d/content_ratings", tmdbID)
	return getJSON[ContentRatingsResponse](c, endpoint, url.Values{}, "TV content ratings")
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

// maxAppendedSeasons caps the seasons batched into one details request.
// append_to_response accepts at most 20 appended resources; the first batch
// also carries external_ids, credits and videos.
const maxAppendedSeasons = 17

// GetTVDetailsWithSeasons fetches TV details plus the given seasons' episode
// lists using append_to_response, so a whole series costs ceil(n/19) requests
// instead of one per season. The first batch also appends external_ids into
// TVDetails.ExternalIDs. Appended seasons arrive as top-level "season/N" keys,
// which is why the body is decoded twice: once into TVDetails, once into a raw
// map the season payloads are picked from. lang is the display language tag;
// "" keeps the English default.
func (c *Client) GetTVDetailsWithSeasons(tmdbID int, seasonNumbers []int, lang string) (*TVDetails, map[int]*TVSeasonDetails, error) {
	if c.apiKey == "" {
		return nil, nil, fmt.Errorf("TMDB Read Access Token not configured")
	}
	var details *TVDetails
	seasons := make(map[int]*TVSeasonDetails, len(seasonNumbers))
	for start := 0; start == 0 || start < len(seasonNumbers); start += maxAppendedSeasons {
		batch := seasonNumbers[min(start, len(seasonNumbers)):min(start+maxAppendedSeasons, len(seasonNumbers))]
		appends := make([]string, 0, len(batch)+4)
		params := url.Values{}
		if start == 0 {
			appends = append(appends, "external_ids", "credits", "videos", "images", "content_ratings")
			params.Set("include_image_language", appendedLanguages(lang))
		}
		for _, n := range batch {
			appends = append(appends, fmt.Sprintf("season/%d", n))
		}
		params.Set("append_to_response", strings.Join(appends, ","))
		// Every batch localizes: appended season episode names follow the
		// language param of their own request.
		applyLanguage(params, lang)

		resp, err := c.doRequest(fmt.Sprintf(c.BaseURL+"/tv/%d", tmdbID), params)
		if err != nil {
			return nil, nil, fmt.Errorf("TMDB TV details request failed: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
		}
		if start == 0 {
			details = &TVDetails{}
			if err := json.Unmarshal(body, details); err != nil {
				return nil, nil, fmt.Errorf("failed to decode TMDB TV details response: %w", err)
			}
		}
		var sidecar map[string]json.RawMessage
		if err := json.Unmarshal(body, &sidecar); err != nil {
			return nil, nil, fmt.Errorf("failed to decode TMDB appended seasons: %w", err)
		}
		for _, n := range batch {
			raw, ok := sidecar[fmt.Sprintf("season/%d", n)]
			if !ok {
				continue
			}
			var sd TVSeasonDetails
			if err := json.Unmarshal(raw, &sd); err == nil {
				seasons[n] = &sd
			}
		}
	}
	return details, seasons, nil
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
