package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/metadata/tmdb"
)

type tmdbSearchResult struct {
	ID        string `json:"id"`
	TMDBID    int    `json:"tmdb_id"`
	Title     string `json:"title"`
	Year      string `json:"year,omitempty"`
	MediaType string `json:"media_type"`
	IMDbID    string `json:"imdb_id,omitempty"`
	TVDBID    int    `json:"tvdb_id,omitempty"`
	PosterURL string `json:"poster_url,omitempty"`
	Overview  string `json:"overview,omitempty"`
}

func (s *Server) handleTMDBSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]tmdbSearchResult{})
		return
	}
	client := tmdb.NewClient(s.tmdbAPIKey)
	resp, err := client.SearchMulti(q)
	if err != nil {
		logger.Debug("TMDB search failed", "query", q, "err", err)
		http.Error(w, "Search failed", http.StatusBadGateway)
		return
	}
	results := make([]tmdbSearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		year := r.ReleaseDate
		if year == "" {
			year = r.FirstAirDate
		}
		if len(year) > 4 {
			year = year[:4]
		}
		title := r.Title
		if title == "" {
			title = r.Name
		}
		posterURL := ""
		if r.PosterPath != "" {
			posterURL = "https://image.tmdb.org/t/p/w92" + r.PosterPath
		}
		overview := ""
		if r.Overview != "" {
			overview = r.Overview
		}
		item := tmdbSearchResult{
			TMDBID:    r.ID,
			Title:     title,
			Year:      year,
			MediaType: r.MediaType,
			PosterURL: posterURL,
			Overview:  overview,
		}
		if r.MediaType == "movie" {
			item.ID = strconv.Itoa(r.ID)
			ext, err := client.GetExternalIDs(r.ID, "movie")
			if err == nil && ext.IMDbID != "" {
				item.IMDbID = ext.IMDbID
				item.ID = ext.IMDbID
			}
		} else {
			item.ID = "tmdb:" + strconv.Itoa(r.ID)
			ext, err := client.GetExternalIDs(r.ID, "tv")
			if err == nil {
				if ext.IMDbID != "" {
					item.IMDbID = ext.IMDbID
				}
				if ext.TVDBID != 0 {
					item.TVDBID = ext.TVDBID
				}
			}
		}
		results = append(results, item)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

type tmdbTVDetailsResponse struct {
	Name    string             `json:"name"`
	Seasons []tmdbTVSeasonInfo `json:"seasons"`
}

type tmdbTVSeasonInfo struct {
	SeasonNumber int    `json:"season_number"`
	EpisodeCount int    `json:"episode_count"`
	Name         string `json:"name"`
}

type tmdbTVSeasonResponse struct {
	SeasonNumber int                 `json:"season_number"`
	Episodes     []tmdbTVEpisodeInfo `json:"episodes"`
}

type tmdbTVEpisodeInfo struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview,omitempty"`
	AirDate       string `json:"air_date,omitempty"`
}

func (s *Server) handleTMDBTV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := auth.StreamFromContext(r); !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/tmdb/tv/")
	if path == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "tv id required", http.StatusBadRequest)
		return
	}
	tmdbID, err := strconv.Atoi(parts[0])
	if err != nil || tmdbID <= 0 {
		http.Error(w, "invalid tv id", http.StatusBadRequest)
		return
	}
	client := tmdb.NewClient(s.tmdbAPIKey)

	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "details") {

		details, err := client.GetTVDetails(tmdbID)
		if err != nil {
			logger.Debug("TMDB TV details failed", "id", tmdbID, "err", err)
			http.Error(w, "TV details failed", http.StatusBadGateway)
			return
		}
		seasons := make([]tmdbTVSeasonInfo, 0, len(details.Seasons))
		for _, se := range details.Seasons {
			seasons = append(seasons, tmdbTVSeasonInfo{
				SeasonNumber: se.SeasonNumber,
				EpisodeCount: se.EpisodeCount,
				Name:         se.Name,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tmdbTVDetailsResponse{Name: details.Name, Seasons: seasons})
		return
	}
	if len(parts) >= 3 && parts[1] == "seasons" {

		seasonNum, err := strconv.Atoi(parts[2])
		if err != nil || seasonNum < 0 {
			http.Error(w, "invalid season number", http.StatusBadRequest)
			return
		}
		season, err := client.GetTVSeasonDetails(tmdbID, seasonNum)
		if err != nil {
			logger.Debug("TMDB TV season failed", "id", tmdbID, "season", seasonNum, "err", err)
			http.Error(w, "Season details failed", http.StatusBadGateway)
			return
		}
		episodes := make([]tmdbTVEpisodeInfo, 0, len(season.Episodes))
		for _, ep := range season.Episodes {
			episodes = append(episodes, tmdbTVEpisodeInfo{
				EpisodeNumber: ep.EpisodeNumber,
				Name:          ep.Name,
				Overview:      ep.Overview,
				AirDate:       ep.AirDate,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tmdbTVSeasonResponse{SeasonNumber: season.SeasonNumber, Episodes: episodes})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) parseStreamRequest(w http.ResponseWriter, r *http.Request) (contentType, id string, stream *auth.Stream, ok bool) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return "", "", nil, false
	}
	stream, authOk := auth.StreamFromContext(r)
	if !authOk {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return "", "", nil, false
	}
	contentType = r.URL.Query().Get("type")
	id = r.URL.Query().Get("id")
	if contentType == "" || id == "" {
		http.Error(w, "type and id are required", http.StatusBadRequest)
		return "", "", nil, false
	}
	if contentType != "movie" && contentType != "series" {
		http.Error(w, "type must be movie or series", http.StatusBadRequest)
		return "", "", nil, false
	}
	return contentType, id, stream, true
}

func (s *Server) handleStreams(w http.ResponseWriter, r *http.Request) {
	contentType, id, stream, ok := s.parseStreamRequest(w, r)
	if !ok {
		return
	}
	streams, err := s.strmServer.GetStreams(r.Context(), contentType, id, stream)
	if err != nil {
		logger.Error("GetStreams failed", "type", contentType, "id", id, "err", err)
		http.Error(w, "Stream search failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"streams": streams})
}

func (s *Server) handleStreamsAvail(w http.ResponseWriter, r *http.Request) {
	contentType, id, stream, ok := s.parseStreamRequest(w, r)
	if !ok {
		return
	}
	streams, err := s.strmServer.GetAvailNZBStreams(r.Context(), contentType, id, stream)
	if err != nil {
		logger.Error("GetAvailNZBStreams failed", "type", contentType, "id", id, "err", err)
		http.Error(w, "AvailNZB streams failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"streams": streams})
}

func (s *Server) handleSearchReleases(w http.ResponseWriter, r *http.Request) {
	contentType, id, _, ok := s.parseStreamRequest(w, r)
	if !ok {
		return
	}
	result, err := s.strmServer.GetSearchReleases(r.Context(), contentType, id)
	if err != nil {
		logger.Error("GetSearchReleases failed", "type", contentType, "id", id, "err", err)
		http.Error(w, "Search releases failed", http.StatusInternalServerError)
		return
	}
	if result == nil {
		result = &stremio.SearchReleasesResponse{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		logger.Debug("Search releases encode failed", "err", err)
	}
}
