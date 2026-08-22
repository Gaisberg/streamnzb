package newznab

import (
	"net/http"
	"net/url"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
)

func TestMovieSearchForwardsIdentifiers(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=movie&apikey="+testToken+"&imdbid=tt0133093")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := up.lastQuery.Get("t"); got != "movie" {
		t.Errorf("upstream t = %q, want movie", got)
	}
	if got := up.lastQuery.Get("imdbid"); got != "tt0133093" {
		t.Errorf("upstream imdbid = %q, want it forwarded as sent", got)
	}
	if got := up.lastQuery.Get("q"); got != "" {
		t.Errorf("upstream q = %q, want an id search to stay an id search", got)
	}
}

// A query with neither text nor identifiers is an RSS listing. It must reach
// the indexers as one rather than being classified as an id or text search,
// which would drop indexers that have either switched off.
func TestListingQueryReachesEveryIndexer(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)
	cfg := srv.currentConfig()
	disabled := true
	cfg.Indexers[0].DisableIdSearch = &disabled

	rec := do(t, srv, APIPath+"?t=search&apikey="+testToken+"&cat=5030%2C5040")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := up.lastQuery.Get("cat"); got != "5030,5040" {
		t.Errorf("upstream cat = %q, want the listing to reach the indexer", got)
	}
	parsed := parseFeed(t, rec.Body.Bytes())
	if len(parsed.Channel.Items) != 1 {
		t.Fatalf("items = %d, want the indexer's listing", len(parsed.Channel.Items))
	}
}

// An indexer that only does text search must not be asked an id search.
func TestIDSearchSkipsIndexersThatCannotServeIt(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)
	cfg := srv.currentConfig()
	disabled := true
	cfg.Indexers[0].DisableIdSearch = &disabled

	rec := do(t, srv, APIPath+"?t=movie&apikey="+testToken+"&imdbid=tt0133093")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if up.lastQuery != nil {
		t.Errorf("upstream was queried with %v, want it skipped", up.lastQuery)
	}
	parsed := parseFeed(t, rec.Body.Bytes())
	if len(parsed.Channel.Items) != 0 {
		t.Errorf("items = %d, want none", len(parsed.Channel.Items))
	}
}

func TestClampLimit(t *testing.T) {
	limits := indexer.CapsLimits{Max: 200, Default: 75}
	cases := map[string]int{
		"":     75,
		"0":    75,
		"50":   50,
		"500":  200,
		"junk": 75,
	}
	for raw, want := range cases {
		if got := clampLimit(raw, limits); got != want {
			t.Errorf("clampLimit(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestForwardedParamsDropsCredentialsAndPaging(t *testing.T) {
	forwarded := forwardedParams(url.Values{
		"q":      {"some show"},
		"cat":    {"5040"},
		"season": {"2"},
		"ep":     {""},
		"apikey": {"stream-token"},
		"o":      {"json"},
		"limit":  {"100"},
		"t":      {"tvsearch"},
	})
	if forwarded.Get("q") != "some show" || forwarded.Get("cat") != "5040" || forwarded.Get("season") != "2" {
		t.Errorf("forwarded = %v, want the search parameters kept", forwarded)
	}
	for _, dropped := range []string{"apikey", "o", "limit", "t", "ep"} {
		if forwarded.Has(dropped) {
			t.Errorf("forwarded %q, want it left behind", dropped)
		}
	}
}

func TestSeriesScopeFor(t *testing.T) {
	cases := []struct {
		function, season, episode, want string
	}{
		{"tvsearch", "1", "2", config.SeriesSearchScopeSeasonEpisode},
		{"tvsearch", "1", "", config.SeriesSearchScopeSeason},
		{"tvsearch", "", "", config.SeriesSearchScopeNone},
		{"search", "1", "2", config.SeriesSearchScopeNone},
	}
	for _, tc := range cases {
		if got := seriesScopeFor(tc.function, tc.season, tc.episode); got != tc.want {
			t.Errorf("seriesScopeFor(%q, %q, %q) = %q, want %q", tc.function, tc.season, tc.episode, got, tc.want)
		}
	}
}
