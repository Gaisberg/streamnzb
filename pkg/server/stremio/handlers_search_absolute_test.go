package stremio

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/services/metadata/tmdb"
)

func animeTVMetadata(seasons []tmdb.TVSeasonInfo) *resolvedSearchMetadata {
	return &resolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{
			Name:             "One Piece",
			OriginalLanguage: "ja",
			Genres:           []tmdb.Genre{{Name: "Animation"}},
			Seasons:          seasons,
		},
	}
}

func TestAbsoluteEpisodeFromMetadata(t *testing.T) {
	seasons := []tmdb.TVSeasonInfo{
		{SeasonNumber: 0, EpisodeCount: 10}, // specials must not count
		{SeasonNumber: 1, EpisodeCount: 61},
		{SeasonNumber: 2, EpisodeCount: 16},
		{SeasonNumber: 3, EpisodeCount: 14},
	}
	meta := animeTVMetadata(seasons)

	cases := []struct {
		name    string
		season  string
		episode string
		want    int
	}{
		{"season 1 maps directly", "1", "5", 5},
		{"season 2 sums prior seasons", "2", "2", 63},
		{"season 3 sums two seasons", "3", "1", 78},
		{"missing episode", "2", "", 0},
		{"missing season", "", "2", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := absoluteEpisodeFromMetadata(meta, tc.season, tc.episode); got != tc.want {
				t.Fatalf("absoluteEpisodeFromMetadata(%q, %q) = %d, want %d", tc.season, tc.episode, got, tc.want)
			}
		})
	}

	// A gap in the season list makes the sum unreliable — refuse to guess.
	gappy := animeTVMetadata([]tmdb.TVSeasonInfo{
		{SeasonNumber: 1, EpisodeCount: 61},
		{SeasonNumber: 3, EpisodeCount: 14},
	})
	if got := absoluteEpisodeFromMetadata(gappy, "3", "1"); got != 0 {
		t.Fatalf("expected 0 for gapped season list, got %d", got)
	}
	if got := absoluteEpisodeFromMetadata(nil, "2", "2"); got != 0 {
		t.Fatalf("expected 0 without metadata, got %d", got)
	}
}

func TestPrepareAbsoluteScopeRequestBuildsQueriesAndMarksRequest(t *testing.T) {
	s := &Server{}
	params := &SearchParams{
		ContentType: "series",
		Req: indexer.SearchRequest{
			Season:  "2",
			Episode: "2",
		},
		PreparedQueries:    []string{"One Piece S02E02"},
		MovieTitleQueries:  map[string][]string{},
		SeriesTitleQueries: map[string][]string{},
		Metadata: animeTVMetadata([]tmdb.TVSeasonInfo{
			{SeasonNumber: 1, EpisodeCount: 61},
			{SeasonNumber: 2, EpisodeCount: 16},
		}),
	}

	if !s.prepareAbsoluteScopeRequest(params, "text", "default", &config.SearchQueryConfig{SearchMode: "text"}) {
		t.Fatalf("expected absolute-scope request to be prepared")
	}
	if params.Req.AbsoluteEpisode != "63" {
		t.Fatalf("AbsoluteEpisode = %q, want 63", params.Req.AbsoluteEpisode)
	}
	found := false
	for _, q := range params.PreparedQueries {
		if q == "One Piece 63" {
			found = true
		}
		if q == "One Piece S02E02" {
			t.Fatalf("absolute scope must replace season/episode queries, got %v", params.PreparedQueries)
		}
	}
	if !found {
		t.Fatalf("expected absolute query variant, got %v", params.PreparedQueries)
	}
}

func TestPrepareAbsoluteScopeRequestSkipsUnsupportedRequests(t *testing.T) {
	s := &Server{}
	animeSeasons := []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 61}, {SeasonNumber: 2, EpisodeCount: 16}}

	cases := []struct {
		name       string
		params     *SearchParams
		searchMode string
	}{
		{
			name: "id search mode",
			params: &SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{Season: "2", Episode: "2"},
				Metadata:    animeTVMetadata(animeSeasons),
			},
			searchMode: "id",
		},
		{
			name: "kitsu already absolute",
			params: &SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{KitsuID: "486", Season: "2", Episode: "2"},
				Metadata:    animeTVMetadata(animeSeasons),
			},
			searchMode: "text",
		},
		{
			name: "non-anime content",
			params: &SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{Season: "2", Episode: "2"},
				Metadata: &resolvedSearchMetadata{
					TVDetails: &tmdb.TVDetails{
						Name:             "Breaking Bad",
						OriginalLanguage: "en",
						Seasons:          []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 7}},
					},
				},
			},
			searchMode: "text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s.prepareAbsoluteScopeRequest(tc.params, tc.searchMode, "default", nil) {
				t.Fatalf("expected request to be skipped")
			}
			if tc.params.Req.AbsoluteEpisode != "" {
				t.Fatalf("skipped request must not carry an absolute episode, got %q", tc.params.Req.AbsoluteEpisode)
			}
		})
	}
}
