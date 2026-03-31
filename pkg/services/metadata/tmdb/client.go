package tmdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"strings"
	"time"
)

type Client struct {
	apiKey string
	client *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
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

func (c *Client) doRequest(endpoint string, params url.Values) (*http.Response, error) {
	reqURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("accept", "application/json")

	return c.client.Do(req)
}

func (c *Client) Find(externalID, source string) (*FindResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}

	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/find/%s", externalID)
	params := url.Values{}
	params.Set("external_source", source)

	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB find request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}

	var result FindResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode TMDB response: %w", err)
	}

	return &result, nil
}

func (c *Client) SearchMulti(query string) (*SearchMultiResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	if query == "" {
		return &SearchMultiResponse{Results: []SearchMultiResult{}}, nil
	}
	endpoint := "https://api.themoviedb.org/3/search/multi"
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", "1")
	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB search request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var result SearchMultiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode TMDB search response: %w", err)
	}

	if len(result.Results) > 20 {
		result.Results = result.Results[:20]
	}

	filtered := result.Results[:0]
	for _, r := range result.Results {
		if r.MediaType == "movie" || r.MediaType == "tv" {
			filtered = append(filtered, r)
		}
	}
	result.Results = filtered
	return &result, nil
}

func (c *Client) GetExternalIDs(tmdbID int, mediaType string) (*ExternalIDsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}

	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/%s/%d/external_ids", mediaType, tmdbID)
	params := url.Values{}

	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB external_ids request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}

	var result ExternalIDsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode TMDB response: %w", err)
	}

	return &result, nil
}

type MovieDetails struct {
	ID               int    `json:"id"`
	Title            string `json:"title"`
	ReleaseDate      string `json:"release_date"`
	OriginalTitle    string `json:"original_title"`
	OriginalLanguage string `json:"original_language"`
}

type TVDetails struct {
	ID               int            `json:"id"`
	Name             string         `json:"name"`
	OriginalName     string         `json:"original_name"`
	OriginalLanguage string         `json:"original_language"`
	FirstAirDate     string         `json:"first_air_date"`
	NumberOfSeasons  int            `json:"number_of_seasons"`
	Seasons          []TVSeasonInfo `json:"seasons"`
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
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/movie/%d", tmdbID)
	params := url.Values{}
	if language != "" {
		params.Set("language", language)
	}
	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB movie details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var d MovieDetails
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("TMDB movie decode: %w", err)
	}
	return &d, nil
}

func (c *Client) GetMovieTranslations(movieID int) (*MovieTranslationsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/movie/%d/translations", movieID)
	resp, err := c.doRequest(endpoint, url.Values{})
	if err != nil {
		return nil, fmt.Errorf("TMDB movie translations: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var out MovieTranslationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("TMDB translations decode: %w", err)
	}
	return &out, nil
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

func (c *Client) GetMovieTitlesForSearch(imdbID, tmdbID, language string, includeYear, normalize bool) (primary, original string, err error) {
	var movieID int
	var title, year string
	var origTitle, origYear string
	var origLang string

	if tmdbID != "" {
		if id, parseErr := strconv.Atoi(tmdbID); parseErr == nil {
			d, getErr := c.GetMovieDetails(id)
			if getErr != nil {
				return "", "", getErr
			}
			movieID = d.ID
			title = d.Title
			if includeYear && len(d.ReleaseDate) >= 4 {
				year = d.ReleaseDate[:4]
			}
			origTitle = strings.TrimSpace(d.OriginalTitle)
			origLang = d.OriginalLanguage
			if includeYear && len(d.ReleaseDate) >= 4 {
				origYear = d.ReleaseDate[:4]
			}
			logger.Debug("TMDB movie title from details", "tmdb_id", movieID, "title", title, "language", language)
		}
	}
	if movieID == 0 && imdbID != "" {
		find, findErr := c.Find(imdbID, "imdb_id")
		if findErr != nil {
			return "", "", findErr
		}
		if len(find.MovieResults) == 0 {
			return "", "", fmt.Errorf("could not resolve movie title")
		}
		r := find.MovieResults[0]
		movieID = r.ID
		title = r.Title
		if includeYear && r.ReleaseDate != "" && len(r.ReleaseDate) >= 4 {
			year = r.ReleaseDate[:4]
		}
		origTitle = strings.TrimSpace(r.OriginalTitle)
		origLang = r.OriginalLanguage
		if includeYear && r.ReleaseDate != "" && len(r.ReleaseDate) >= 4 {
			origYear = r.ReleaseDate[:4]
		}
		logger.Debug("TMDB movie resolved from IMDb Find", "imdb_id", imdbID, "tmdb_id", movieID, "default_title", title)
	}

	if language != "" && movieID != 0 {
		tr, trErr := c.GetMovieTranslations(movieID)
		if trErr != nil {
			logger.Debug("TMDB translations not used, falling back to default title", "movie_id", movieID, "language", language, "err", trErr)
		} else if t := movieTitleFromTranslations(tr, language); t != "" {
			title = t
			logger.Debug("TMDB movie title for search (translated)", "language", language, "title", title)
		}
	}

	if title == "" {
		return "", "", fmt.Errorf("could not resolve movie title")
	}
	primary = strings.TrimSpace(title)
	if year != "" {
		primary = primary + " " + year
	}
	if normalize {
		primary = release.NormalizeTitleForFilename(primary)
	}
	primary = strings.TrimSpace(primary)

	if origTitle == "" || origLang == "" || strings.ToLower(origLang) == "en" {
		return primary, "", nil
	}
	if release.NormalizeTitle(origTitle) == release.NormalizeTitle(title) {
		return primary, "", nil
	}
	out := origTitle
	if origYear != "" {
		out = out + " " + origYear
	}
	if normalize {
		out = release.NormalizeTitleForFilename(out)
	}
	out = strings.TrimSpace(out)
	if out == primary {
		return primary, "", nil
	}
	logger.Debug("TMDB movie original title for search", "original_language", origLang, "query", out)
	return primary, out, nil
}

func (c *Client) GetTVDetails(tmdbID int) (*TVDetails, error) {
	return c.GetTVDetailsWithLanguage(tmdbID, "")
}

func (c *Client) GetTVDetailsWithLanguage(tmdbID int, language string) (*TVDetails, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/tv/%d", tmdbID)
	params := url.Values{}
	if language != "" {
		params.Set("language", language)
	}
	resp, err := c.doRequest(endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("TMDB TV details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var d TVDetails
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("TMDB TV decode: %w", err)
	}
	return &d, nil
}

func (c *Client) GetTVTranslations(tmdbID int) (*TVTranslationsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/tv/%d/translations", tmdbID)
	resp, err := c.doRequest(endpoint, url.Values{})
	if err != nil {
		return nil, fmt.Errorf("TMDB TV translations: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var out TVTranslationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("TMDB TV translations decode: %w", err)
	}
	return &out, nil
}

func (c *Client) GetTVShowTitlesForSearch(tmdbID, imdbID, language string, includeYear, normalize bool) (primary, original string, err error) {
	var showID int
	var title, year string
	var originalTitle, originalYear string

	if tmdbID != "" {
		if id, parseErr := strconv.Atoi(tmdbID); parseErr == nil {
			showID = id
			details, detailsErr := c.GetTVDetails(id)
			if detailsErr != nil {
				return "", "", detailsErr
			}
			title = details.Name
			originalTitle = details.OriginalName
			if originalTitle == "" {
				originalTitle = details.Name
			}
			if includeYear && len(details.FirstAirDate) >= 4 {
				year = details.FirstAirDate[:4]
				originalYear = year
			}
			if language != "" {
				if localized, localizedErr := c.GetTVDetailsWithLanguage(id, language); localizedErr == nil && strings.TrimSpace(localized.Name) != "" {
					title = localized.Name
				}
			}
		}
	}

	if showID == 0 && imdbID != "" {
		find, findErr := c.Find(imdbID, "imdb_id")
		if findErr != nil {
			return "", "", findErr
		}
		if len(find.TVResults) == 0 {
			return "", "", fmt.Errorf("could not resolve TV show name")
		}
		r := find.TVResults[0]
		showID = r.ID
		title = r.Name
		originalTitle = r.OriginalName
		if originalTitle == "" {
			originalTitle = r.Name
		}
		if includeYear && len(r.FirstAirDate) >= 4 {
			year = r.FirstAirDate[:4]
			originalYear = year
		}
		if language != "" && showID != 0 {
			if localized, localizedErr := c.GetTVDetailsWithLanguage(showID, language); localizedErr == nil && strings.TrimSpace(localized.Name) != "" {
				title = localized.Name
			}
		}
	}

	if title == "" {
		return "", "", fmt.Errorf("could not resolve TV show name")
	}

	primary = strings.TrimSpace(title)
	if includeYear && year != "" {
		primary += " " + year
	}
	if normalize {
		primary = release.NormalizeTitleForFilename(primary)
	}

	if release.NormalizeTitle(originalTitle) == release.NormalizeTitle(title) {
		return primary, "", nil
	}

	original = strings.TrimSpace(originalTitle)
	if includeYear && originalYear != "" {
		original += " " + originalYear
	}
	if normalize {
		original = release.NormalizeTitleForFilename(original)
	}
	if strings.TrimSpace(original) == "" || original == primary {
		return primary, "", nil
	}
	return primary, original, nil
}

func (c *Client) GetTVSeasonDetails(seriesID, seasonNumber int) (*TVSeasonDetails, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("TMDB API key not configured")
	}
	endpoint := fmt.Sprintf("https://api.themoviedb.org/3/tv/%d/season/%d", seriesID, seasonNumber)
	resp, err := c.doRequest(endpoint, url.Values{})
	if err != nil {
		return nil, fmt.Errorf("TMDB TV season details: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB returned status: %d", resp.StatusCode)
	}
	var d TVSeasonDetails
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("TMDB TV season decode: %w", err)
	}
	return &d, nil
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
