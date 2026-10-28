package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
)

// tvdbAirDateStub serves TVDB for one series: its details, and the filtered
// single-episode lookup for the episodes given ("season x number" -> aired).
// status other than 200 fails every data request. It counts the episode
// lookups that reach it.
func tvdbAirDateStub(t *testing.T, seriesID, name string, aired map[string]string, status int) (*tvdb.Client, *atomic.Int64) {
	t.Helper()
	var lookups atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"token": "test-token"}}`))
			return
		case status != http.StatusOK:
			w.WriteHeader(status)
			return
		case r.URL.Path == "/series/"+seriesID:
			_, _ = fmt.Fprintf(w, `{"status": "success", "data": {"id": 1, "name": %q}}`, name)
			return
		case r.URL.Path == "/series/"+seriesID+"/episodes/default":
			q := r.URL.Query()
			if q.Get("season") == "" {
				// The gate's full-list walk: nothing, so it falls through.
				_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": []}}`))
				return
			}
			lookups.Add(1)
			date, ok := aired[q.Get("season")+"x"+q.Get("episodeNumber")]
			if !ok {
				_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": []}}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"status": "success", "data": {"episodes": [
				{"seasonNumber": %s, "number": %s, "aired": %q}
			]}}`, q.Get("season"), q.Get("episodeNumber"), date)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	// Not t.TempDir: the login persists its token through the process-wide
	// database, which keeps the directory open, and Windows then fails the
	// test on cleanup. Removal is best-effort, as in tvdb's newStubClient.
	dir, err := os.MkdirTemp("", "tvdb_airdate_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	client := tvdb.NewClient("test-key", dir)
	client.BaseURL = ts.URL
	return client, &lookups
}

// tmdbSeries describes one TMDB series for tmdbSeriesStub: the ids TMDB maps
// it to, and the episodes of each season it holds (episode number -> air
// date, "" for an undated episode). fail names one endpoint to answer 500 —
// "external_ids", "details" or "season".
type tmdbSeries struct {
	id      int
	name    string
	tvdbID  int
	imdbID  string
	seasons map[int]map[int]string
	fail    string
}

// tmdbSeriesStub serves the TMDB endpoints search resolution and the air-date
// cross-check read: find by TVDB id, series details with its season list,
// external ids, and season details.
func tmdbSeriesStub(series tmdbSeries) http.HandlerFunc {
	base := fmt.Sprintf("/tv/%d", series.id)
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/find/"):
			_, _ = fmt.Fprintf(w, `{"tv_results": [{"id": %d, "name": %q}]}`, series.id, series.name)
		case path == base+"/external_ids":
			if series.fail == "external_ids" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id": %d, "tvdb_id": %d, "imdb_id": %q}`, series.id, series.tvdbID, series.imdbID)
		case path == base:
			if series.fail == "details" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var seasons []string
			for n, eps := range series.seasons {
				seasons = append(seasons, fmt.Sprintf(`{"season_number": %d, "episode_count": %d}`, n, len(eps)))
			}
			_, _ = fmt.Fprintf(w, `{"id": %d, "name": %q, "original_language": "en", "seasons": [%s]}`, series.id, series.name, strings.Join(seasons, ","))
		case strings.HasPrefix(path, base+"/season/"):
			if series.fail == "season" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var n int
			_, _ = fmt.Sscanf(strings.TrimPrefix(path, base+"/season/"), "%d", &n)
			eps, ok := series.seasons[n]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var list []string
			for num, date := range eps {
				list = append(list, fmt.Sprintf(`{"episode_number": %d, "season_number": %d, "air_date": %q}`, num, n, date))
			}
			_, _ = fmt.Fprintf(w, `{"season_number": %d, "episodes": [%s]}`, n, strings.Join(list, ","))
		default:
			http.NotFound(w, r)
		}
	}
}

// tvmazeRawS33E43 is TVMaze's WWE Raw as it really is: its S33E43 is #1743,
// aired 2026-10-26 — a year after the S33E43 Stremio asks for (RAW 1692,
// 2025-10-27), because TVMaze numbers the show's seasons differently.
func tvmazeRawS33E43(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/lookup/shows"):
		_, _ = w.Write([]byte(`{"id": 50}`))
	case strings.HasPrefix(r.URL.Path, "/shows/50"):
		_, _ = w.Write([]byte(`{"id": 50, "_embedded": {"episodes": [
			{"id": 1743, "season": 33, "number": 43, "airdate": "2026-10-26", "airtime": "", "airstamp": "2026-10-26T12:00:00+00:00"}
		]}}`))
	default:
		http.NotFound(w, r)
	}
}

// The three shows as their sources really number them (#382).
var (
	rawS33E43 = &session.AvailReportMeta{ImdbID: "tt0185103", TvdbID: "76779", TmdbID: "4656", Season: 33, Episode: 43}
	// TVDB and TMDB agree on Stremio's S33E43.
	rawTMDB = tmdbSeries{id: 4656, name: "WWE Raw", tvdbID: 76779, imdbID: "tt0185103", seasons: map[int]map[int]string{33: {42: "2025-10-20", 43: "2025-10-27"}}}

	gutfeldS4E246 = &session.AvailReportMeta{ImdbID: "tt4690816", TvdbID: "400622", TmdbID: "122653", Season: 4, Episode: 246}
	// Stremio numbers Gutfeld! as TMDB does; TVDB's S4E246 is another night.
	gutfeldTVDB = map[string]string{"4x246": "2024-12-27"}
	gutfeldTMDB = tmdbSeries{id: 122653, name: "Gutfeld!", tvdbID: 400622, imdbID: "tt4690816", seasons: map[int]map[int]string{4: {245: "2024-10-16", 246: "2024-10-17"}}}

	kimmelS24E2524 = &session.AvailReportMeta{ImdbID: "tt0320037", TvdbID: "71998", TmdbID: "1489", Season: 24, Episode: 2524}
	// TMDB restarts every season (S24E1-157), so it holds no S24E2524.
	kimmelTMDB = tmdbSeries{id: 1489, name: "Jimmy Kimmel Live", tvdbID: 71998, imdbID: "tt0320037", seasons: map[int]map[int]string{24: {1: "2025-09-02", 157: "2026-08-27"}}}
)

// Each real show resolves as it should: WWE Raw to the date TVDB and TMDB
// share (TVMaze's other-night date never enters), Gutfeld! to nothing because
// TVDB and TMDB name different nights, and Jimmy Kimmel Live to TVDB's date
// because TMDB holds no episode under those numbers.
func TestEpisodeAirDateRealShows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tmdb   tmdbSeries
		tvdbID string
		tvdb   map[string]string
		ids    *session.AvailReportMeta
		want   string
	}{
		{"WWE Raw: TVDB and TMDB agree", rawTMDB, "76779", map[string]string{"33x43": "2025-10-27"}, rawS33E43, "2025-10-27"},
		{"Gutfeld!: TVDB names another night", gutfeldTMDB, "400622", gutfeldTVDB, gutfeldS4E246, ""},
		{"Jimmy Kimmel Live: TMDB has no such episode", kimmelTMDB, "71998", map[string]string{"24x2524": "2026-10-26"}, kimmelS24E2524, "2026-10-26"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := airedTestServer(t, tvmazeRawS33E43, tmdbSeriesStub(tc.tmdb))
			srv.tvdbClient, _ = tvdbAirDateStub(t, tc.tvdbID, tc.tmdb.name, tc.tvdb, http.StatusOK)
			if got := srv.episodeAirDate("series", tc.ids); got != tc.want {
				t.Fatalf("air date = %q, want %q", got, tc.want)
			}
		})
	}
}

// TVDB proposes the date and TMDB may only confirm it or stand aside. Every
// answer TMDB cannot settle — a failed request, an undated episode, a series
// it cannot be shown to share with TVDB — leaves no date; none of them is
// taken as agreement.
func TestEpisodeAirDateTMDBCrossCheck(t *testing.T) {
	rawTVDB := map[string]string{"33x43": "2025-10-27"}
	with := func(edit func(*tmdbSeries)) tmdbSeries { s := rawTMDB; edit(&s); return s }
	withIDs := func(edit func(*session.AvailReportMeta)) *session.AvailReportMeta {
		ids := *rawS33E43
		edit(&ids)
		return &ids
	}

	for _, tc := range []struct {
		name   string
		tmdb   *tmdbSeries // nil: no TMDB client
		tvdb   map[string]string
		status int
		ids    *session.AvailReportMeta
		want   string
	}{
		{"both agree", &rawTMDB, rawTVDB, http.StatusOK, rawS33E43, "2025-10-27"},
		{"TMDB has no such season", ptr(with(func(s *tmdbSeries) { s.seasons = map[int]map[int]string{32: {1: "2024-01-01"}} })), rawTVDB, http.StatusOK, rawS33E43, "2025-10-27"},
		{"TMDB has the season, not the episode", ptr(with(func(s *tmdbSeries) { s.seasons = map[int]map[int]string{33: {1: "2025-01-06"}} })), rawTVDB, http.StatusOK, rawS33E43, "2025-10-27"},
		{"TMDB disagrees by a night", ptr(with(func(s *tmdbSeries) { s.seasons = map[int]map[int]string{33: {43: "2025-10-28"}} })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB episode undated", ptr(with(func(s *tmdbSeries) { s.seasons = map[int]map[int]string{33: {43: ""}} })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB season request fails", ptr(with(func(s *tmdbSeries) { s.fail = "season" })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB details request fails", ptr(with(func(s *tmdbSeries) { s.fail = "details" })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB external ids request fails", ptr(with(func(s *tmdbSeries) { s.fail = "external_ids" })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB maps another TVDB series", ptr(with(func(s *tmdbSeries) { s.tvdbID = 99999 })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB maps no TVDB id, same IMDb id", ptr(with(func(s *tmdbSeries) { s.tvdbID = 0 })), rawTVDB, http.StatusOK, rawS33E43, "2025-10-27"},
		{"TMDB maps no TVDB id, another IMDb id", ptr(with(func(s *tmdbSeries) { s.tvdbID, s.imdbID = 0, "tt0000001" })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TMDB maps no ids at all", ptr(with(func(s *tmdbSeries) { s.tvdbID, s.imdbID = 0, "" })), rawTVDB, http.StatusOK, rawS33E43, ""},
		{"no TMDB series id", &rawTMDB, rawTVDB, http.StatusOK, withIDs(func(ids *session.AvailReportMeta) { ids.TmdbID = "" }), ""},
		{"no TMDB client", nil, rawTVDB, http.StatusOK, rawS33E43, ""},
		{"TVDB holds no such episode", &rawTMDB, nil, http.StatusOK, rawS33E43, ""},
		{"TVDB failing", &rawTMDB, rawTVDB, http.StatusInternalServerError, rawS33E43, ""},
		{"TVDB with no date for it", &rawTMDB, map[string]string{"33x43": ""}, http.StatusOK, rawS33E43, ""},
		{"no TVDB id", &rawTMDB, rawTVDB, http.StatusOK, withIDs(func(ids *session.AvailReportMeta) { ids.TvdbID = "" }), ""},
		{"specials", &rawTMDB, rawTVDB, http.StatusOK, withIDs(func(ids *session.AvailReportMeta) { ids.Season = 0 }), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tmdbHandler http.HandlerFunc
			if tc.tmdb != nil {
				tmdbHandler = tmdbSeriesStub(*tc.tmdb)
			}
			srv := airedTestServer(t, tvmazeRawS33E43, tmdbHandler)
			srv.tvdbClient, _ = tvdbAirDateStub(t, "76779", "WWE Raw", tc.tvdb, tc.status)
			if got := srv.episodeAirDate("series", tc.ids); got != tc.want {
				t.Fatalf("air date = %q, want %q", got, tc.want)
			}
		})
	}

	// Movies never carry one.
	srv := airedTestServer(t, tvmazeRawS33E43, tmdbSeriesStub(rawTMDB))
	srv.tvdbClient, _ = tvdbAirDateStub(t, "76779", "WWE Raw", rawTVDB, http.StatusOK)
	if got := srv.episodeAirDate("movie", rawS33E43); got != "" {
		t.Fatalf("movie air date = %q, want none", got)
	}
}

func ptr[T any](v T) *T { return &v }

// The unaired gate keeps its own sources and order: it still reads TVMaze
// first, whatever date matching settles on.
func TestEpisodeAirDateLeavesTheGateAlone(t *testing.T) {
	srv := airedTestServer(t, tvmazeRawS33E43, tmdbSeriesStub(rawTMDB))
	srv.tvdbClient, _ = tvdbAirDateStub(t, "76779", "WWE Raw", map[string]string{"33x43": "2025-10-27"}, http.StatusOK)
	if got := srv.episodeAirDate("series", rawS33E43); got != "2025-10-27" {
		t.Fatalf("air date = %q, want 2025-10-27", got)
	}
	if _, window, known := srv.episodeAiredState(context.Background(), nil, "series", rawS33E43); !known || !window.date.Equal(mustDay(t, "2026-10-26")) {
		t.Fatalf("gate window date = %v (known=%v), want TVMaze's 2026-10-26 as before", window.date, known)
	}
}

// dailyIndexer answers every request with the releases given.
type dailyIndexer []string

func (d dailyIndexer) Search(context.Context, indexer.SearchRequest) (*indexer.SearchResponse, error) {
	items := make([]indexer.Item, 0, len(d))
	for _, title := range d {
		items = append(items, indexer.Item{Title: title, GUID: "https://example.invalid/" + title, Comments: "https://example.invalid/" + title, ActualIndexer: "Prowlarr"})
	}
	return &indexer.SearchResponse{Channel: indexer.Channel{Items: items}}, nil
}
func (dailyIndexer) Name() string                                        { return "Prowlarr" }
func (dailyIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }
func (dailyIndexer) Ping(context.Context) error                          { return nil }
func (dailyIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }

// dailySearchServer is a server searching the default TV plan against the
// releases given, with TVMaze, TVDB and TMDB stubbed as given.
func dailySearchServer(t *testing.T, tvmaze http.HandlerFunc, tmdbSeries tmdbSeries, tvdbID string, tvdbAired map[string]string, releases ...string) (*Server, *atomic.Int64) {
	t.Helper()
	logger.Init("ERROR")
	srv := airedTestServer(t, tvmaze, tmdbSeriesStub(tmdbSeries))
	srv.config.SeriesSearchQueries = []config.SearchQueryConfig{config.DefaultTVPlan("TV")}
	srv.indexer = dailyIndexer(releases)
	srv.uniqueIndexerHits = make(map[string]int64)
	var lookups *atomic.Int64
	srv.tvdbClient, lookups = tvdbAirDateStub(t, tvdbID, tmdbSeries.name, tvdbAired, http.StatusOK)
	return srv, lookups
}

func rawTitles(t *testing.T, raw *rawSearchResult) []string {
	t.Helper()
	var got []string
	for _, rel := range raw.IndexerReleases {
		got = append(got, rel.Title)
	}
	return got
}

// End to end through the search handler: the date TVDB and TMDB agree on for
// Stremio's WWE Raw S33E43 reaches validation and decides the results — that
// night's release is the episode; TVMaze's 2026-10-26 for the same numbers
// must not let the other night's in.
func TestBuildRawSearchResultMatchesDailyShowByVerifiedAirDate(t *testing.T) {
	srv, lookups := dailySearchServer(t, tvmazeRawS33E43, rawTMDB, "76779", map[string]string{"33x43": "2025-10-27"},
		"WWE.Raw.2025.10.27.1080p.WEB.h264-GROUP",
		"WWE.Raw.2026.10.26.1080p.WEB.h264-GROUP",
	)
	stream := &auth.Stream{Username: "daily", SeriesSearchQueries: []string{"TV"}}

	raw, err := srv.buildRawSearchResult(t.Context(), "series", "tvdb:76779:33:43", stream)
	if err != nil {
		t.Fatalf("buildRawSearchResult() error = %v", err)
	}
	if raw.Unaired {
		t.Fatal("the episode has aired; the gate must not hold it")
	}
	if got := rawTitles(t, raw); len(got) != 1 || got[0] != "WWE.Raw.2025.10.27.1080p.WEB.h264-GROUP" {
		t.Fatalf("releases = %q, want only the 2025-10-27 night", got)
	}
	if raw.Params.ContentIDs.AirDate != "2025-10-27" {
		t.Fatalf("ContentIDs.AirDate = %q, want 2025-10-27 for playback selection", raw.Params.ContentIDs.AirDate)
	}
	// Resolved once per search, however many plan attempts ran.
	if n := lookups.Load(); n != 1 {
		t.Fatalf("TVDB episode lookups = %d, want 1", n)
	}
}

// Gutfeld! end to end: TVDB's S4E246 is another night than the S4E246 Stremio
// asks for. That night's date-named release must not be accepted — and with
// the date unconfirmed, no date-named release is — while a release numbered
// for the episode is accepted as always.
func TestBuildRawSearchResultRejectsDailyShowWhenTVDBNamesAnotherNight(t *testing.T) {
	srv, _ := dailySearchServer(t, nil, gutfeldTMDB, "400622", gutfeldTVDB,
		"Gutfeld.2024.12.27.1080p.WEB.h264-GROUP",
		"Gutfeld.2024.10.17.1080p.WEB.h264-GROUP",
		"Gutfeld.S04E246.1080p.WEB.h264-GROUP",
	)
	stream := &auth.Stream{Username: "daily", SeriesSearchQueries: []string{"TV"}}

	raw, err := srv.buildRawSearchResult(t.Context(), "series", "tvdb:400622:4:246", stream)
	if err != nil {
		t.Fatalf("buildRawSearchResult() error = %v", err)
	}
	if got := rawTitles(t, raw); len(got) != 1 || got[0] != "Gutfeld.S04E246.1080p.WEB.h264-GROUP" {
		t.Fatalf("releases = %q, want only the numbered release", got)
	}
	if raw.Params.ContentIDs.AirDate != "" {
		t.Fatalf("ContentIDs.AirDate = %q, want none: TVDB and TMDB disagree", raw.Params.ContentIDs.AirDate)
	}
}

// The air date is resolved only for a search that will run: a request the
// certification cap turns away answers without asking TVDB for it.
func TestBuildRawSearchResultSkipsAirDateWhenCertificationBlocks(t *testing.T) {
	srv, lookups := dailySearchServer(t, tvmazeRawS33E43, rawTMDB, "76779", map[string]string{"33x43": "2025-10-27"},
		"WWE.Raw.2025.10.27.1080p.WEB.h264-GROUP",
	)
	srv.config.MetadataProfiles = []config.MetadataProfileConfig{{Name: "Kids", MaxCertification: "13"}}
	stream := &auth.Stream{Username: "kids", SeriesSearchQueries: []string{"TV"}, MetadataProfileName: "Kids"}

	raw, err := srv.buildRawSearchResult(t.Context(), "series", "tvdb:76779:33:43", stream)
	if err != nil {
		t.Fatalf("buildRawSearchResult() error = %v", err)
	}
	if len(raw.IndexerReleases) != 0 {
		t.Fatalf("releases = %d, want none: an unrated title is over a capped profile", len(raw.IndexerReleases))
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("TVDB episode lookups = %d, want 0 for a search that never runs", n)
	}
	if raw.Params.ContentIDs.AirDate != "" {
		t.Fatalf("ContentIDs.AirDate = %q, want none", raw.Params.ContentIDs.AirDate)
	}
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	w, ok := dateWindow(s)
	if !ok {
		t.Fatalf("bad date %q", s)
	}
	return w.date
}
