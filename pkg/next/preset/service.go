package preset

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/session"
)

var ErrInvalidRequest = errors.New("invalid preset match request")

type providerHostSource interface {
	GetProviderHosts() []string
}

type availabilityLookup interface {
	GetReleases(imdbID string, tvdbID string, season, episode int, indexers []string, providers []string) (*availnzb.ReleasesResult, error)
}

type SearchConfig interface {
	GetIncludeYearInSearch() bool
	GetSearchTitleLanguage() string
	GetSearchTitleNormalize() bool
}

type TMDBSearchResolver interface {
	GetMovieTitle(imdbID, tmdbID string) (string, error)
	GetMovieTitleAndYear(imdbID, tmdbID string) (title, year string, err error)
	GetMovieTitleForSearch(imdbID, tmdbID, language string, includeYear, normalize bool) (string, error)
	GetTVShowName(tmdbID, imdbID string) (string, error)
}

type TMDBResolver interface {
	TMDBSearchResolver
	GetExternalIDs(tmdbID int, mediaType string) (*tmdb.ExternalIDsResponse, error)
	ResolveTVDBID(imdbID string) (string, error)
}

type tvdbResolver interface {
	ResolveTVDBID(remoteID string) (string, error)
}

type Options struct {
	AvailNZBMode         string
	SearchConfig         SearchConfig
	Indexer              indexer.Indexer
	Validator            providerHostSource
	AvailClient          availabilityLookup
	AvailNZBIndexerHosts []string
	TMDBClient           TMDBResolver
	TVDBClient           tvdbResolver
}

type MatchRequest struct {
	Type       string `json:"type"`
	MetadataID string `json:"metadata_id"`
	Title      string `json:"title,omitempty"`
}

type Candidate struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Link         string `json:"link,omitempty"`
	DetailsURL   string `json:"details_url,omitempty"`
	Size         int64  `json:"size,omitempty"`
	Indexer      string `json:"indexer,omitempty"`
	QuerySource  string `json:"query_source,omitempty"`
	Availability string `json:"availability"`
	Match        string `json:"match"`
}

type MatchResponse struct {
	Role       string      `json:"role"`
	Status     string      `json:"status"`
	Candidates []Candidate `json:"candidates"`
}

type Service struct {
	availNZBMode  string
	searchConfig  SearchConfig
	indexer       indexer.Indexer
	validator     providerHostSource
	availClient   availabilityLookup
	tmdbClient    TMDBResolver
	tvdbClient    tvdbResolver
	availIndexers []string
}

func NewService(availNZBMode string) *Service {
	return NewServiceWithOptions(Options{AvailNZBMode: availNZBMode})
}

func NewServiceWithOptions(opts Options) *Service {
	return &Service{
		availNZBMode:  opts.AvailNZBMode,
		searchConfig:  opts.SearchConfig,
		indexer:       opts.Indexer,
		validator:     opts.Validator,
		availClient:   opts.AvailClient,
		tmdbClient:    opts.TMDBClient,
		tvdbClient:    opts.TVDBClient,
		availIndexers: append([]string(nil), opts.AvailNZBIndexerHosts...),
	}
}

func (s *Service) Status() map[string]any {
	return map[string]any{
		"role":          "preset",
		"ready":         s.indexer != nil,
		"availability":  s.availabilityEnabled(),
		"metadata_ids":  s.tmdbClient != nil || s.tvdbClient != nil,
		"availnzb_mode": s.availNZBMode,
		"responsible":   []string{"indexer_fetch", "availability_lookup", "metadata_matching"},
	}
}

type searchParams struct {
	ContentType string
	ID          string
	Req         indexer.SearchRequest
	ContentIDs  *session.AvailReportMeta
	ImdbForText string
	TmdbForText string
}

func (s *Service) Match(_ context.Context, req MatchRequest) (MatchResponse, error) {
	params, err := s.buildSearchParams(req)
	if err != nil {
		return MatchResponse{}, err
	}
	if s.indexer == nil {
		return MatchResponse{
			Role:       "preset",
			Status:     "not_ready",
			Candidates: []Candidate{},
		}, nil
	}

	indexerReleases, err := search.RunIndexerSearches(s.indexer, s.tmdbClient, params.Req, params.ContentType, params.ContentIDs, params.ImdbForText, params.TmdbForText, s.searchConfig)
	if err != nil {
		return MatchResponse{}, err
	}

	availResult := s.lookupAvailability(params)

	return MatchResponse{
		Role:       "preset",
		Status:     "ok",
		Candidates: s.buildCandidates(indexerReleases, availResult),
	}, nil
}

func (s *Service) buildSearchParams(req MatchRequest) (*searchParams, error) {
	contentType := strings.ToLower(strings.TrimSpace(req.Type))
	metadataID := strings.TrimSpace(req.MetadataID)
	if contentType != "movie" && contentType != "series" {
		return nil, fmt.Errorf("%w: unsupported type %q", ErrInvalidRequest, req.Type)
	}
	if metadataID == "" {
		return nil, fmt.Errorf("%w: metadata_id is required", ErrInvalidRequest)
	}

	params := &searchParams{ContentType: contentType, ID: metadataID}
	searchReq := indexer.SearchRequest{Limit: 1000}
	searchID := metadataID

	if strings.Contains(metadataID, ":") {
		parts := strings.Split(metadataID, ":")
		if len(parts) > 0 && parts[0] == "tmdb" {
			if len(parts) >= 2 {
				searchID = parts[1]
			}
			if len(parts) >= 3 {
				searchReq.Season = parts[2]
			}
			if len(parts) >= 4 {
				searchReq.Episode = parts[3]
			}
		} else {
			searchID = parts[0]
			if len(parts) >= 2 {
				searchReq.Season = parts[1]
			}
			if len(parts) >= 3 {
				searchReq.Episode = parts[2]
			}
		}
	}

	if strings.HasPrefix(searchID, "tt") {
		searchReq.IMDbID = searchID
	} else {
		searchReq.TMDBID = searchID
	}

	if contentType == "movie" {
		searchReq.Cat = "2000"
	} else {
		searchReq.Cat = "5000"
	}

	imdbForText := searchReq.IMDbID
	tmdbForText := searchReq.TMDBID

	if contentType == "series" && searchReq.IMDbID != "" && searchReq.TVDBID == "" {
		if s.tvdbClient != nil {
			if tvdbID, err := s.tvdbClient.ResolveTVDBID(searchReq.IMDbID); err == nil && tvdbID != "" {
				searchReq.TVDBID = tvdbID
			}
		}
		if searchReq.TVDBID == "" && s.tmdbClient != nil {
			if tvdbID, err := s.tmdbClient.ResolveTVDBID(searchReq.IMDbID); err == nil && tvdbID != "" {
				searchReq.TVDBID = tvdbID
			}
		}
	}

	if contentType == "series" && searchReq.TVDBID == "" && searchReq.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(searchReq.TMDBID); err == nil {
			if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "tv"); err == nil {
				if extIDs != nil && extIDs.TVDBID != 0 {
					searchReq.TVDBID = strconv.Itoa(extIDs.TVDBID)
				}
				if extIDs != nil && extIDs.IMDbID != "" && searchReq.IMDbID == "" {
					searchReq.IMDbID = extIDs.IMDbID
					imdbForText = extIDs.IMDbID
				}
			}
		}
	}

	if contentType == "movie" && searchReq.IMDbID == "" && searchReq.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(searchReq.TMDBID); err == nil {
			if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil && extIDs != nil && extIDs.IMDbID != "" {
				searchReq.IMDbID = extIDs.IMDbID
				imdbForText = extIDs.IMDbID
			}
		}
	}

	seasonNum, _ := strconv.Atoi(searchReq.Season)
	episodeNum, _ := strconv.Atoi(searchReq.Episode)
	params.Req = searchReq
	params.ContentIDs = &session.AvailReportMeta{
		ImdbID:  searchReq.IMDbID,
		TvdbID:  searchReq.TVDBID,
		Season:  seasonNum,
		Episode: episodeNum,
	}
	params.ImdbForText = imdbForText
	params.TmdbForText = tmdbForText
	return params, nil
}

func (s *Service) lookupAvailability(params *searchParams) *availnzb.ReleasesResult {
	if params == nil || params.ContentIDs == nil || !s.availabilityEnabled() {
		return nil
	}
	contentIDs := params.ContentIDs
	if contentIDs.ImdbID == "" && contentIDs.TvdbID == "" {
		return nil
	}

	var providers []string
	if s.validator != nil {
		providers = s.validator.GetProviderHosts()
	}

	result, err := s.availClient.GetReleases(contentIDs.ImdbID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, s.availIndexers, providers)
	if err != nil {
		return nil
	}
	return result
}

func (s *Service) buildCandidates(indexerReleases []*release.Release, availResult *availnzb.ReleasesResult) []Candidate {
	seen := make(map[string]bool)
	availByKey := make(map[string]string)
	var candidates []Candidate

	appendCandidate := func(rel *release.Release, availability, fallbackSource string) {
		if rel == nil {
			return
		}
		key := release.Key(rel)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		querySource := rel.QuerySource
		if querySource == "" {
			querySource = fallbackSource
		}
		candidates = append(candidates, Candidate{
			ID:           key,
			Title:        rel.Title,
			Link:         rel.Link,
			DetailsURL:   rel.DetailsURL,
			Size:         rel.Size,
			Indexer:      rel.Indexer,
			QuerySource:  querySource,
			Availability: availability,
			Match:        "metadata",
		})
	}

	if availResult != nil {
		for _, relWithStatus := range availResult.Releases {
			if relWithStatus == nil || relWithStatus.Release == nil {
				continue
			}
			availability := "Unavailable"
			if relWithStatus.Available {
				availability = "Available"
			}
			availByKey[release.Key(relWithStatus.Release)] = availability
			appendCandidate(relWithStatus.Release, availability, "availability")
		}
	}

	for _, rel := range indexerReleases {
		availability := availByKey[release.Key(rel)]
		if availability == "" {
			availability = "Unknown"
		}
		appendCandidate(rel, availability, "indexer")
	}

	return candidates
}

func (s *Service) availabilityEnabled() bool {
	return s.availNZBMode != "disabled" && s.availClient != nil
}
