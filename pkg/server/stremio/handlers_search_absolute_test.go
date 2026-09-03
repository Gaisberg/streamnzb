package stremio

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/kitsu"
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

// The absolute number is resolved once per request and the queries are built
// per attempt, because each title attempt has its own language.
func TestSearchFactsResolveTheAbsoluteEpisode(t *testing.T) {
	s := &Server{}
	params := &query.SearchParams{
		ContentType: "series",
		Req: indexer.SearchRequest{
			Season:  "2",
			Episode: "2",
		},
		MovieTitleQueries:  map[string][]string{},
		SeriesTitleQueries: map[string][]string{},
		Metadata: animeTVMetadata([]tmdb.TVSeasonInfo{
			{SeasonNumber: 1, EpisodeCount: 61},
			{SeasonNumber: 2, EpisodeCount: 16},
		}),
	}

	facts := s.searchFacts("series", "default", params)
	if !facts.IsAnime {
		t.Fatal("expected the request to be detected as anime")
	}
	if facts.Absolute != 63 {
		t.Fatalf("absolute = %d, want 63", facts.Absolute)
	}
	if facts.Class != config.SearchClassTVAnime {
		t.Fatalf("class = %q, want %q", facts.Class, config.SearchClassTVAnime)
	}
	if got := absoluteEpisodeQueries(params, "", facts.Absolute); len(got) == 0 || got[0] != "One Piece 63" {
		t.Fatalf("absolute queries = %v, want [One Piece 63]", got)
	}
}

// An absolute attempt dispatches the absolute-numbered query, and every
// attempt of the plan carries the number so acceptance recognises an
// absolute-numbered release whichever attempt surfaced it.
func TestBuildSearchParamsForAttemptDispatchesTheAbsoluteQuery(t *testing.T) {
	s := &Server{config: &config.Config{}}
	base := func() *query.SearchParams {
		return &query.SearchParams{
			ContentType:        "series",
			Req:                indexer.SearchRequest{Season: "2", Episode: "2"},
			MovieTitleQueries:  map[string][]string{},
			SeriesTitleQueries: map[string][]string{},
			Metadata: animeTVMetadata([]tmdb.TVSeasonInfo{
				{SeasonNumber: 1, EpisodeCount: 61},
				{SeasonNumber: 2, EpisodeCount: 16},
			}),
		}
	}
	plan := config.DefaultTVPlan("TV")
	facts := s.searchFacts("series", "default", base())

	absolute := config.SearchAttempt{Address: config.SearchAddressTitle, Target: config.SearchTargetAbsolute}
	params, err := s.buildSearchParamsForAttempt(base(), &plan, absolute, facts)
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}
	if params.Req.Query != "One Piece 63" {
		t.Fatalf("absolute attempt query = %q, want %q", params.Req.Query, "One Piece 63")
	}
	if params.Req.AbsoluteEpisode != "63" {
		t.Fatalf("AbsoluteEpisode = %q, want 63", params.Req.AbsoluteEpisode)
	}

	episode := config.SearchAttempt{Address: config.SearchAddressTitle, Target: config.SearchTargetEpisode}
	params, err = s.buildSearchParamsForAttempt(base(), &plan, episode, facts)
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}
	if params.Req.Query != "One Piece S02E02" {
		t.Fatalf("episode attempt query = %q, want %q", params.Req.Query, "One Piece S02E02")
	}
	if params.Req.AbsoluteEpisode != "63" {
		t.Fatalf("expected every attempt of the plan to carry the absolute number, got %q", params.Req.AbsoluteEpisode)
	}

	// A plan with no absolute attempt does not claim the number, so acceptance
	// stays strict about season and episode.
	noAbsolute := planOf("TV", nil, episode)
	params, err = s.buildSearchParamsForAttempt(base(), &noAbsolute, episode, facts)
	if err != nil {
		t.Fatalf("buildSearchParamsForAttempt() error = %v", err)
	}
	if params.Req.AbsoluteEpisode != "" {
		t.Fatalf("AbsoluteEpisode = %q, want empty", params.Req.AbsoluteEpisode)
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

func TestAbsoluteEpisodeForContentPrefersResolvedAbsolute(t *testing.T) {
	if got := query.AbsoluteEpisodeForContent("series", "63", nil, "", ""); got != 63 {
		t.Fatalf("query.AbsoluteEpisodeForContent(resolved) = %d, want 63", got)
	}
	if got := query.AbsoluteEpisodeForContent("movie", "63", nil, "", ""); got != 0 {
		t.Fatalf("query.AbsoluteEpisodeForContent(movie) = %d, want 0", got)
	}
	// Without a resolved number it still derives one from anime metadata.
	metadata := animeTVMetadata([]tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 61}, {SeasonNumber: 2, EpisodeCount: 16}})
	if got := query.AbsoluteEpisodeForContent("series", "", metadata, "2", "2"); got != 63 {
		t.Fatalf("query.AbsoluteEpisodeForContent(derived) = %d, want 63", got)
	}
}

// A Kitsu entry spanning a whole series resolves its absolute number up front
// and has no season to derive one from, so the facts must use what the request
// already carries.
func TestSearchFactsUseAResolvedAbsoluteEpisode(t *testing.T) {
	s := &Server{}
	params := &query.SearchParams{
		ContentType: "series",
		Req:         indexer.SearchRequest{KitsuID: "486", AbsoluteEpisode: "63"},
		Metadata:    animeTVMetadata(nil),
	}
	facts := s.searchFacts("series", "default", params)
	if facts.Absolute != 63 {
		t.Fatalf("absolute = %d, want 63", facts.Absolute)
	}
	if got := absoluteEpisodeQueries(params, "", facts.Absolute); len(got) == 0 || got[0] != "One Piece 63" {
		t.Fatalf("absolute queries = %v, want [One Piece 63]", got)
	}
}

// Without a derivable number there is nothing to ask by, and the plan compiler
// drops the absolute attempt rather than dispatching a query that cannot match.
func TestSearchPlanDropsTheAbsoluteAttemptWhenThereIsNoNumber(t *testing.T) {
	s := &Server{}

	cases := []struct {
		name   string
		params *query.SearchParams
	}{
		{
			// A gap in the season list makes the sum unreliable.
			name: "absolute not derivable from metadata",
			params: &query.SearchParams{
				ContentType: "series",
				Req:         indexer.SearchRequest{Season: "3", Episode: "2"},
				Metadata: animeTVMetadata([]tmdb.TVSeasonInfo{
					{SeasonNumber: 1, EpisodeCount: 61},
					{SeasonNumber: 3, EpisodeCount: 14},
				}),
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
	plan := config.DefaultTVPlan("TV")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := s.searchFacts("series", "default", tc.params)
			if facts.Absolute != 0 {
				t.Fatalf("absolute = %d, want 0", facts.Absolute)
			}
			for _, attempt := range plan.SearchPlanAttempts(facts.PlanContext(nil)) {
				if attempt.Target == config.SearchTargetAbsolute {
					t.Fatal("expected the absolute attempt to be dropped")
				}
			}
		})
	}
}

// Stremio's "anime" type is a series type unless the Kitsu entry is a film.
// Deciding series-ness by type == "series" once sent every anime episode out
// as a movie search (class movie, cat 2000) and found nothing.
func TestSearchFactsTreatAnimeTypeAsSeries(t *testing.T) {
	s := &Server{}
	params := &query.SearchParams{
		ContentType:        "anime",
		Req:                indexer.SearchRequest{Season: "1", Episode: "1", KitsuID: "11469"},
		MovieTitleQueries:  map[string][]string{},
		SeriesTitleQueries: map[string][]string{},
		Metadata:           animeTVMetadata([]tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 13}}),
	}

	facts := s.searchFacts("anime", "default", params)
	if !facts.IsSeries {
		t.Fatal("expected an anime episode request to count as a series")
	}
	if facts.Class != config.SearchClassTVAnime {
		t.Fatalf("class = %q, want %q", facts.Class, config.SearchClassTVAnime)
	}
	if !facts.HasSeason || !facts.HasEpisode {
		t.Fatal("expected the season and episode to be carried into the plan context")
	}

	// A Kitsu film stays a movie even under the anime type.
	params.Metadata.KitsuDetails = &kitsu.AnimeDetails{ShowType: "movie", CanonicalTitle: "Spirited Away"}
	facts = s.searchFacts("anime", "default", params)
	if facts.IsSeries || facts.Class != config.SearchClassMovie {
		t.Fatalf("kitsu film: series=%v class=%q, want a movie", facts.IsSeries, facts.Class)
	}
}
