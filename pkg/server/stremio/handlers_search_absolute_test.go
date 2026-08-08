package stremio

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/tmdb"
)

func animeTVMetadata(seasons []tmdb.TVSeasonInfo) *query.ResolvedSearchMetadata {
	return &query.ResolvedSearchMetadata{
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
			if got := query.AbsoluteEpisodeFromMetadata(meta, tc.season, tc.episode); got != tc.want {
				t.Fatalf("query.AbsoluteEpisodeFromMetadata(%q, %q) = %d, want %d", tc.season, tc.episode, got, tc.want)
			}
		})
	}

	// A gap in the season list makes the sum unreliable — refuse to guess.
	gappy := animeTVMetadata([]tmdb.TVSeasonInfo{
		{SeasonNumber: 1, EpisodeCount: 61},
		{SeasonNumber: 3, EpisodeCount: 14},
	})
	if got := query.AbsoluteEpisodeFromMetadata(gappy, "3", "1"); got != 0 {
		t.Fatalf("expected 0 for gapped season list, got %d", got)
	}
	if got := query.AbsoluteEpisodeFromMetadata(nil, "2", "2"); got != 0 {
		t.Fatalf("expected 0 without metadata, got %d", got)
	}
}

func TestPrepareAbsoluteEpisodeSearchSupplementsRequest(t *testing.T) {
	s := &Server{}
	params := &query.SearchParams{
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

	if !s.prepareAbsoluteEpisodeSearch(params, "default", &config.SearchQueryConfig{SearchMode: "text"}) {
		t.Fatalf("expected absolute-episode supplement to be prepared")
	}
	if params.Req.AbsoluteEpisode != "63" {
		t.Fatalf("AbsoluteEpisode = %q, want 63", params.Req.AbsoluteEpisode)
	}
	found := false
	for _, q := range params.AbsoluteQueries {
		if q == "One Piece 63" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected absolute query variant, got %v", params.AbsoluteQueries)
	}
	// The supplement must not disturb the request's own queries.
	if len(params.PreparedQueries) != 1 || params.PreparedQueries[0] != "One Piece S02E02" {
		t.Fatalf("prepared queries must stay untouched, got %v", params.PreparedQueries)
	}
}

func TestAppendSearchCategory(t *testing.T) {
	cases := []struct {
		categories string
		want       string
	}{
		{"5000", "5000,5070"},
		{"5000,5070", "5000,5070"},
		{"5070", "5070"},
		{"5000, 5070", "5000, 5070"},
		{"5000,5060", "5000,5060,5070"},
	}
	for _, tc := range cases {
		if got := query.AppendSearchCategory(tc.categories, "5070"); got != tc.want {
			t.Fatalf("query.AppendSearchCategory(%q) = %q, want %q", tc.categories, got, tc.want)
		}
	}
}

func TestAppendAnimeTVCategoryToEffective(t *testing.T) {
	tvCats := "5000"
	emptyCats := ""
	effective := map[string]*config.IndexerSearchConfig{
		"AnimeTosho": {TVCategories: &tvCats},
		"NoFilter":   {TVCategories: &emptyCats},
		"NoOverride": {},
	}

	query.AppendAnimeTVCategoryToEffective(effective)

	if got := *effective["AnimeTosho"].TVCategories; got != "5000,5070" {
		t.Fatalf("expected widened categories 5000,5070, got %q", got)
	}
	// An empty filter already matches everything and must stay empty.
	if got := *effective["NoFilter"].TVCategories; got != "" {
		t.Fatalf("expected empty categories to stay empty, got %q", got)
	}
	if effective["NoOverride"].TVCategories != nil {
		t.Fatalf("expected nil categories to stay nil, got %q", *effective["NoOverride"].TVCategories)
	}
}

func TestRequestLooksLikeAnime(t *testing.T) {
	animeSeasons := []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 61}}
	cases := []struct {
		name   string
		params *query.SearchParams
		want   bool
	}{
		{
			name: "kitsu is anime by definition",
			params: &query.SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{KitsuID: "486", Episode: "63"},
			},
			want: true,
		},
		{
			name: "anime metadata",
			params: &query.SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{Season: "2", Episode: "2"},
				Metadata:    animeTVMetadata(animeSeasons),
			},
			want: true,
		},
		{
			name: "non-anime content",
			params: &query.SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{Season: "2", Episode: "2"},
				Metadata: &query.ResolvedSearchMetadata{
					TVDetails: &tmdb.TVDetails{
						Name:             "Breaking Bad",
						OriginalLanguage: "en",
						Seasons:          []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 7}},
					},
				},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := query.RequestLooksLikeAnime(tc.params); got != tc.want {
				t.Fatalf("query.RequestLooksLikeAnime() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAbsoluteEpisodeForContentKitsuUsesEpisodeDirectly(t *testing.T) {
	if got := query.AbsoluteEpisodeForContent("series", "486", nil, "", "63"); got != 63 {
		t.Fatalf("query.AbsoluteEpisodeForContent(kitsu) = %d, want 63", got)
	}
	if got := query.AbsoluteEpisodeForContent("movie", "486", nil, "", "63"); got != 0 {
		t.Fatalf("query.AbsoluteEpisodeForContent(movie) = %d, want 0", got)
	}
}

func TestPrepareAbsoluteEpisodeSearchSkipsUnsupportedRequests(t *testing.T) {
	s := &Server{}
	animeSeasons := []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 61}, {SeasonNumber: 2, EpisodeCount: 16}}

	cases := []struct {
		name   string
		params *query.SearchParams
	}{
		{
			name: "kitsu already absolute",
			params: &query.SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{KitsuID: "486", Season: "2", Episode: "2"},
				Metadata:    animeTVMetadata(animeSeasons),
			},
		},
		{
			name: "non-anime content",
			params: &query.SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{Season: "2", Episode: "2"},
				Metadata: &query.ResolvedSearchMetadata{
					TVDetails: &tmdb.TVDetails{
						Name:             "Breaking Bad",
						OriginalLanguage: "en",
						Seasons:          []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 7}},
					},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s.prepareAbsoluteEpisodeSearch(tc.params, "default", nil) {
				t.Fatalf("expected supplement to be skipped")
			}
			if tc.params.Req.AbsoluteEpisode != "" {
				t.Fatalf("skipped request must not carry an absolute episode, got %q", tc.params.Req.AbsoluteEpisode)
			}
			if len(tc.params.AbsoluteQueries) != 0 {
				t.Fatalf("skipped request must not carry absolute queries, got %v", tc.params.AbsoluteQueries)
			}
		})
	}
}
