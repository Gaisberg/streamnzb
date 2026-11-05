package stremio

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/kitsu"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
)

// Search and playback number an episode through one helper, and anime is
// whatever query.RequestLooksLikeAnime calls anime: the "anime" type, a Kitsu
// id even under "series", or anime metadata. A Kitsu request under "series"
// once took an absolute number in search and none at playback, which then
// refused the "Show - 19" search had accepted.
func TestAbsoluteEpisodeForRequest(t *testing.T) {
	animeMeta := animeTVMetadata([]tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 25}, {SeasonNumber: 2, EpisodeCount: 12}})
	// Western animation on Kitsu (RWBY-like): TMDB present, but not "anime".
	westernMeta := &query.ResolvedSearchMetadata{TVDetails: &tmdb.TVDetails{Name: "Show", OriginalLanguage: "en",
		Genres:  []tmdb.Genre{{Name: "Animation"}},
		Seasons: []tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 16}, {SeasonNumber: 2, EpisodeCount: 12}}}}
	for _, tc := range []struct {
		name                                     string
		contentType, kitsu, abs, season, episode string
		meta                                     *query.ResolvedSearchMetadata
		want                                     int
	}{
		{"anime type, derived", "anime", "", "", "2", "3", animeMeta, 28},
		{"anime type, resolved up front", "anime", "", "154", "", "", nil, 154},
		{"anime film: no episode", "anime", "", "", "", "", animeMeta, 0},
		{"anime type, TVDB-only metadata, season 1", "anime", "", "", "1", "4", nil, 4},
		{"Kitsu under series, TVDB-only metadata", "series", "1", "", "1", "5", nil, 5},
		{"Kitsu under series, western animation", "series", "1", "", "2", "3", westernMeta, 19},
		{"tt series, anime metadata", "series", "", "", "2", "3", animeMeta, 28},
		{"tt series, western animation", "series", "", "", "2", "3", westernMeta, 0},
		{"tt series, no metadata", "series", "", "", "1", "4", nil, 0},
		{"movie", "movie", "", "", "1", "4", animeMeta, 0},
		// A Kitsu film's mapping spans no season, which resolves its "episode"
		// up front; a film has no episode to number.
		{"Kitsu film resolved up front", "anime", "1", "1", "", "", &query.ResolvedSearchMetadata{KitsuDetails: &kitsu.AnimeDetails{ShowType: "movie"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := &query.SearchParams{ContentType: tc.contentType, Metadata: tc.meta,
				Req: indexer.SearchRequest{KitsuID: tc.kitsu, AbsoluteEpisode: tc.abs, Season: tc.season, Episode: tc.episode}}
			if got := query.AbsoluteEpisodeForRequest(params); got != tc.want {
				t.Fatalf("AbsoluteEpisodeForRequest = %d, want %d", got, tc.want)
			}
			// buildSearchParamsBase stores the number on the playback target,
			// and search reads it from there.
			params.ContentIDs = &session.AvailReportMeta{AbsoluteEpisode: query.AbsoluteEpisodeForRequest(params)}
			if got := (&Server{}).searchFacts(tc.contentType, "default", params).Absolute; got != tc.want {
				t.Fatalf("search absolute = %d, want playback's %d", got, tc.want)
			}
		})
	}

	// Playback matches the file search accepted.
	params := &query.SearchParams{ContentType: "series", Metadata: westernMeta,
		Req: indexer.SearchRequest{KitsuID: "1", Season: "2", Episode: "3"}}
	target := (&session.AvailReportMeta{Season: 2, Episode: 3, AbsoluteEpisode: query.AbsoluteEpisodeForRequest(params)}).EpisodeTarget()
	for name, want := range map[string]bool{
		"[Grp] Show - 19 [1080p].mkv": true,
		"Show.S02E03.1080p.mkv":       true,
		"[Grp] Show - 18 [1080p].mkv": false,
		"Show.S02E04.1080p.mkv":       false,
	} {
		if got := parser.ParseReleaseTitle(name).TargetMatchRank(target) > 0; got != want {
			t.Errorf("playback match %q = %v, want %v", name, got, want)
		}
	}
}

// Search takes the playback target's absolute number as it stands, including
// one TVDB has vetoed, rather than deriving its own.
func TestSearchFactsTakeThePlaybackAbsolute(t *testing.T) {
	params := &query.SearchParams{ContentType: "anime", Req: indexer.SearchRequest{Season: "2", Episode: "3"},
		Metadata:   animeTVMetadata([]tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 25}, {SeasonNumber: 2, EpisodeCount: 12}}),
		ContentIDs: &session.AvailReportMeta{Season: 2, Episode: 3}}
	if got := (&Server{}).searchFacts("anime", "default", params).Absolute; got != 0 {
		t.Fatalf("search absolute = %d, want the target's 0", got)
	}
}

// The validator's rules follow the same anime test, not the Stremio type: a
// Kitsu request under "series" is anime, a film is a movie, and a show that
// is not anime keeps the series rules.
func TestValidationContentTypeFollowsAnimeClassification(t *testing.T) {
	drama := &query.ResolvedSearchMetadata{TVDetails: &tmdb.TVDetails{Name: "Show", OriginalLanguage: "en", Genres: []tmdb.Genre{{Name: "Drama"}}}}
	for _, tc := range []struct {
		name, contentType, kitsu string
		meta                     *query.ResolvedSearchMetadata
		want                     string
	}{
		{"Kitsu under series", "series", "1", nil, "anime"},
		{"anime type", "anime", "", nil, "anime"},
		// Anime by metadata alone is anime to plans and numbering, but its
		// releases are validated as a series' are.
		{"tt series, anime metadata", "series", "", animeTVMetadata(nil), "series"},
		{"tt series, not anime", "series", "", drama, "series"},
		{"movie", "movie", "", nil, "movie"},
	} {
		params := &query.SearchParams{ContentType: tc.contentType, Metadata: tc.meta, Req: indexer.SearchRequest{KitsuID: tc.kitsu}}
		if got := validationContentType(params, tc.contentType); got != tc.want {
			t.Errorf("%s: validation type = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// tvdbAbsoluteStub serves TVDB's filtered single-episode lookup for one
// series ("season x number" -> absoluteNumber, 0 for an episode TVDB numbers
// without one); every other request fails.
func tvdbAbsoluteStub(t *testing.T, seriesID string, absolute map[string]int) *tvdb.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"token": "test-token"}}`))
		case "/series/" + seriesID + "/episodes/default":
			q := r.URL.Query()
			abs, ok := absolute[q.Get("season")+"x"+q.Get("episodeNumber")]
			if !ok {
				_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": []}}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"status": "success", "data": {"episodes": [{"seasonNumber": %s, "number": %s, "absoluteNumber": %d}]}}`,
				q.Get("season"), q.Get("episodeNumber"), abs)
		default:
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(ts.Close)
	// Not t.TempDir, for the reason tvdbAirDateStub gives.
	dir, err := os.MkdirTemp("", "tvdb_absolute_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	client := tvdb.NewClient("test-key", dir)
	client.BaseURL = ts.URL
	return client
}

// TVDB vetoes a derived absolute number it numbers differently and never
// supplies one. The TVDB numbers are live TVDB answers (November 2026); the
// derived ones sum TMDB's season counts as AbsoluteEpisodeFromMetadata does,
// with season and episode in TVDB's order, as Cinemeta and anime-lists give
// them.
func TestSettleEpisodeNumberingConfirmsTheAbsolute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tvdbID string
		tvdb   map[string]int
		ids    session.AvailReportMeta
		want   int
	}{
		// TVDB and TMDB both count 25, 12 and 22 episodes before season 4.
		{"Attack on Titan S02E01: agree", "267440", map[string]int{"2x1": 26}, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 26}, 26},
		{"Attack on Titan S04E01: agree", "267440", map[string]int{"4x1": 60}, session.AvailReportMeta{Season: 4, Episode: 1, AbsoluteEpisode: 60}, 60},
		// TVDB's S1 has 8 episodes, TMDB's 61: TMDB's sum names another episode.
		// The id plays no part — tt, kitsu, tvdb, or this addon's own tmdb: ids,
		// which carry TVDB numbering when TVDB is the meta source.
		{"One Piece S02E01: seasons split differently", "81797", map[string]int{"2x1": 9}, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 62}, 0},
		// Same counts, but TVDB counts two season-0 specials into its order.
		{"Mushoku Tensei S02E01: TVDB counts specials", "371310", map[string]int{"2x1": 26}, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 24}, 0},
		// Season 1 is its own numbering; TVDB's 18 (an OVA counted) is not asked.
		{"Mushoku Tensei S01E17: season 1 stands", "371310", map[string]int{"1x17": 18}, session.AvailReportMeta{Season: 1, Episode: 17, AbsoluteEpisode: 17}, 17},
		{"TVDB has no absolute number", "1", map[string]int{"2x1": 0}, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 30}, 30},
		{"TVDB does not list the episode", "1", map[string]int{}, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 30}, 30},
		{"TVDB failing", "1", nil, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 30}, 30},
		{"no TVDB id", "", map[string]int{"2x1": 9}, session.AvailReportMeta{Season: 2, Episode: 1, AbsoluteEpisode: 30}, 30},
		{"resolved for a series-spanning entry", "76703", map[string]int{}, session.AvailReportMeta{Seasonless: true, Episode: 154, AbsoluteEpisode: 154}, 154},
		{"no absolute to confirm", "1", map[string]int{"2x1": 9}, session.AvailReportMeta{Season: 2, Episode: 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := tc.ids
			ids.TvdbID = tc.tvdbID
			served := tc.tvdbID
			if tc.tvdb == nil {
				served = "unserved" // every lookup for this series fails
			}
			srv := &Server{tvdbClient: tvdbAbsoluteStub(t, served, tc.tvdb)}
			srv.settleEpisodeNumbering("anime", &ids)
			if ids.AbsoluteEpisode != tc.want {
				t.Fatalf("absolute = %d, want %d", ids.AbsoluteEpisode, tc.want)
			}
		})
	}

	// Without a TVDB client the derived number stands, and films are left alone.
	ids := session.AvailReportMeta{TvdbID: "81797", Season: 2, Episode: 1, AbsoluteEpisode: 62}
	(&Server{}).settleEpisodeNumbering("anime", &ids)
	if ids.AbsoluteEpisode != 62 {
		t.Fatalf("no TVDB client: %d, want 62", ids.AbsoluteEpisode)
	}
	(&Server{tvdbClient: tvdbAbsoluteStub(t, "81797", map[string]int{"2x1": 9})}).settleEpisodeNumbering("movie", &ids)
	if ids.AbsoluteEpisode != 62 {
		t.Fatalf("movie: %d, want 62", ids.AbsoluteEpisode)
	}
}

// A Kitsu entry anime-lists has placed is searched by the series' own titles,
// but its Kitsu titles are still the show's names: a title attempt accepts a
// romaji-named release by them, and no query is built from them.
func TestMappedKitsuTitlesValidateButDoNotQuery(t *testing.T) {
	meta := &query.ResolvedSearchMetadata{
		TVDetails: &tmdb.TVDetails{Name: "Frieren: Beyond Journey's End", OriginalLanguage: "ja", Genres: []tmdb.Genre{{Name: "Animation"}}},
		KitsuValidationTitles: &kitsu.AnimeDetails{CanonicalTitle: "Sousou no Frieren", EnglishTitle: "Frieren: Beyond Journey's End",
			RomajiTitle: "Sousou no Frieren", Synonyms: []string{"Frieren at the Funeral"}},
	}
	accepted := query.ValidationQueriesFromProfiles(query.ValidationQueryProfilesFromMetadata(meta, "anime", []string{"en-US"}, false))
	for _, want := range []string{"Sousou no Frieren", "Frieren at the Funeral", "Frieren Beyond Journeys End"} {
		if !slices.ContainsFunc(accepted, func(q string) bool { return strings.EqualFold(q, want) }) {
			t.Errorf("accepted titles %q lack %q", accepted, want)
		}
	}
	for _, q := range query.BuildSeriesQueriesFromMetadata(meta, "en-US", false, "1", "5", config.SeriesSearchScopeSeasonEpisode) {
		if strings.Contains(strings.ToLower(q), "sousou") {
			t.Errorf("query %q built from a mapped entry's Kitsu title", q)
		}
	}
}

// A tt series that only looks like anime keeps every strict series rule —
// year, title on title attempts, explicit episodes, OVAs and date-only names —
// while still being anime to plans and absolute numbering.
func TestAnimeLookingSeriesKeepsSeriesValidation(t *testing.T) {
	params := &query.SearchParams{ContentType: "series", Req: indexer.SearchRequest{IMDbID: "tt9335498", Season: "1", Episode: "2"},
		Metadata: animeTVMetadata([]tmdb.TVSeasonInfo{{SeasonNumber: 1, EpisodeCount: 25}})}
	if !query.RequestLooksLikeAnime(params) {
		t.Fatal("expected anime metadata to classify as anime")
	}
	ct := validationContentType(params, "series")
	if ct != "series" {
		t.Fatalf("validation type = %q, want series", ct)
	}
	validate := func(opts search.ValidationOptions, titles ...string) []string {
		rels := make([]*release.Release, 0, len(titles))
		for _, title := range titles {
			rels = append(rels, &release.Release{Title: title})
		}
		out, _ := search.ValidateSearchResultsWithOptions(rels, ct, []string{"Fruits Basket 2019"}, opts)
		kept := make([]string, 0, len(out))
		for _, r := range out {
			kept = append(kept, r.Title)
		}
		return kept
	}
	opts := search.ValidationOptions{Season: "1", Episode: "2", AbsoluteEpisode: "2", EnforceTitle: true, EnforceYear: true, AcceptPacks: true}
	got := validate(opts,
		"Fruits.Basket.2019.S01E02.1080p.WEB-DL-GRP",
		"Fruits.Basket.2001.S01E02.1080p.WEB-DL-GRP", // another show's year
		"Totally.Different.Show.S01E02.1080p.WEB-DL-GRP",
		"Fruits.Basket.2019.S01E03.1080p.WEB-DL-GRP",
		"[Grp] Fruits Basket OVA 2 [1080p]",
		"Fruits.Basket.2024.10.12.1080p.WEB-DL-GRP",
	)
	if !slices.Equal(got, []string{"Fruits.Basket.2019.S01E02.1080p.WEB-DL-GRP"}) {
		t.Fatalf("kept %q, want only the 2019 show's S01E02", got)
	}
}

// Only Kitsu's show type marks an entry as an OVA series, whether its details
// drive the queries or only lend validation their titles; without Kitsu
// details nothing does.
func TestOVASeriesFollowsKitsuShowType(t *testing.T) {
	s := &Server{config: &config.Config{}}
	episode := config.SearchAttempt{Address: config.SearchAddressID, Target: config.SearchTargetEpisode}
	plan := config.DefaultTVPlan("TV")
	for _, tc := range []struct {
		name string
		meta *query.ResolvedSearchMetadata
		want bool
	}{
		{"mapped Kitsu OVA", &query.ResolvedSearchMetadata{KitsuValidationTitles: &kitsu.AnimeDetails{ShowType: "OVA"}}, true},
		{"unmapped Kitsu OVA", &query.ResolvedSearchMetadata{KitsuDetails: &kitsu.AnimeDetails{ShowType: "OVA"}}, true},
		{"Kitsu TV", &query.ResolvedSearchMetadata{KitsuValidationTitles: &kitsu.AnimeDetails{ShowType: "TV"}}, false},
		{"no Kitsu details", animeTVMetadata(nil), false},
	} {
		base := &query.SearchParams{ContentType: "anime", Req: indexer.SearchRequest{KitsuID: "1", Season: "1", Episode: "2"},
			MovieTitleQueries: map[string][]string{}, SeriesTitleQueries: map[string][]string{}, Metadata: tc.meta}
		params, err := s.buildSearchParamsForAttempt(base, &plan, episode, s.searchFacts("anime", "default", base))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if params.Req.OVASeries != tc.want {
			t.Errorf("%s: OVASeries = %v, want %v", tc.name, params.Req.OVASeries, tc.want)
		}
	}
}
