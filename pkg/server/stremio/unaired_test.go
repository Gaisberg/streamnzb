package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvmaze"
	"streamnzb/pkg/session"
)

func airedTestServer(t *testing.T, tvmazeHandler, tmdbHandler http.HandlerFunc) *Server {
	t.Helper()
	srv := &Server{config: &config.Config{}}
	if tvmazeHandler != nil {
		ts := httptest.NewServer(tvmazeHandler)
		t.Cleanup(ts.Close)
		srv.tvmazeClient = tvmaze.NewClient(ts.Client(), nil)
		srv.tvmazeClient.BaseURL = ts.URL
	}
	if tmdbHandler != nil {
		ts := httptest.NewServer(tmdbHandler)
		t.Cleanup(ts.Close)
		srv.tmdbClient = tmdb.NewClient("test-key")
		srv.tmdbClient.BaseURL = ts.URL
	}
	return srv
}

// tvmazeShowWithEpisode serves one episode exactly as TVMaze shapes it: an
// airdate, an airtime that is empty whenever TVMaze holds no broadcast time,
// and an airstamp that is present either way.
func tvmazeShowWithEpisode(airdate, airtime, airstamp string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lookup/shows"):
			_, _ = w.Write([]byte(`{"id": 82}`))
		case strings.HasPrefix(r.URL.Path, "/shows/82"):
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id": 82, "_embedded": {"episodes": [
				{"id": 1, "season": 2, "number": 5, "airdate": %q, "airtime": %q, "airstamp": %q}
			]}}`, airdate, airtime, airstamp)))
		default:
			http.NotFound(w, r)
		}
	}
}

// tvmazeShowWithAirstamp serves a show TVMaze knows a real air time for.
func tvmazeShowWithAirstamp(airstamp string) http.HandlerFunc {
	date := airstamp
	if t, err := time.Parse(time.RFC3339, airstamp); err == nil {
		date = t.Format(airDateLayout)
	}
	return tvmazeShowWithEpisode(date, "21:00", airstamp)
}

func TestEpisodeAiredState(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}

	t.Run("future airstamp gates", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)
		aired, window, known := srv.episodeAiredState(context.Background(), nil, "series", ids)
		if !known || aired {
			t.Fatalf("known=%v aired=%v, want known unaired", known, aired)
		}
		if window.opensAt.IsZero() {
			t.Fatal("the window must carry the instant the gate opens")
		}
		if at, timeKnown := window.reportAt(); !timeKnown || at.IsZero() {
			t.Fatalf("reportAt = %v (known=%v), want the scheduled broadcast", at, timeKnown)
		}
	})

	t.Run("past airstamp searches", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(past), nil)
		aired, _, known := srv.episodeAiredState(context.Background(), nil, "series", ids)
		if !known || !aired {
			t.Fatalf("known=%v aired=%v, want known aired", known, aired)
		}
	})

	t.Run("lookup error fails open", func(t *testing.T) {
		srv := airedTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, nil)
		if _, _, known := srv.episodeAiredState(context.Background(), nil, "series", ids); known {
			t.Fatal("lookup failure must fail open (known=false)")
		}
	})

	t.Run("episode missing from both sources fails open", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)
		other := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 9, Episode: 9}
		if _, _, known := srv.episodeAiredState(context.Background(), nil, "series", other); known {
			t.Fatal("unknown episode must fail open")
		}
	})

	t.Run("an episode with no air date at all fails open", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithEpisode("", "", ""), nil)
		if _, _, known := srv.episodeAiredState(context.Background(), nil, "series", ids); known {
			t.Fatal("an episode TVMaze has no date for must fail open")
		}
	})

	t.Run("no ids fails open", func(t *testing.T) {
		srv := airedTestServer(t, nil, nil)
		if _, _, known := srv.episodeAiredState(context.Background(), nil, "series", &session.AvailReportMeta{Season: 1, Episode: 1}); known {
			t.Fatal("no resolvable ids must fail open")
		}
	})

	t.Run("movies never gate", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)
		if _, _, known := srv.episodeAiredState(context.Background(), nil, "movie", ids); known {
			t.Fatal("movies must not be gated")
		}
	})

	t.Run("tmdb fallback with future date gates", func(t *testing.T) {
		futureDate := time.Now().Add(7 * 24 * time.Hour).UTC().Format(airDateLayout)
		srv := airedTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/tv/1399/season/2") {
				_, _ = w.Write([]byte(fmt.Sprintf(`{"season_number": 2, "episodes": [
					{"episode_number": 5, "air_date": %q}
				]}`, futureDate)))
				return
			}
			http.NotFound(w, r)
		})
		tmdbIDs := &session.AvailReportMeta{TmdbID: "1399", Season: 2, Episode: 5}
		aired, window, known := srv.episodeAiredState(context.Background(), nil, "series", tmdbIDs)
		if !known || aired {
			t.Fatalf("known=%v aired=%v, want gated via TMDB fallback", known, aired)
		}
		if _, timeKnown := window.reportAt(); timeKnown {
			t.Fatal("TMDB serves bare dates; nothing may claim to know an air time")
		}
	})

	t.Run("date-only air date counts as aired on the day", func(t *testing.T) {
		today := time.Now().UTC().Format(airDateLayout)
		srv := airedTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(fmt.Sprintf(`{"season_number": 2, "episodes": [
				{"episode_number": 5, "air_date": %q}
			]}`, today)))
		})
		tmdbIDs := &session.AvailReportMeta{TmdbID: "1399", Season: 2, Episode: 5}
		aired, _, known := srv.episodeAiredState(context.Background(), nil, "series", tmdbIDs)
		if !known || !aired {
			t.Fatalf("known=%v aired=%v, want aired on its air date", known, aired)
		}
	})
}

func TestGateOpensWhereTheAirDateBegins(t *testing.T) {
	// A release cannot precede its own broadcast, and the earliest a broadcast
	// on 2026-08-21 can happen anywhere on Earth is midnight at UTC+14. Gating
	// on anything later would hide releases from the zones ahead of the server.
	w, ok := dateWindow("2026-08-21")
	if !ok {
		t.Fatal("parsing a bare air date failed")
	}
	want := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	if !w.opensAt.Equal(want) {
		t.Fatalf("gate opens at %v, want %v (2026-08-21T00:00+14:00)", w.opensAt, want)
	}
	if !w.date.Equal(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("air date = %v, want 2026-08-21 UTC", w.date)
	}
	if at, timeKnown := w.reportAt(); timeKnown || !at.Equal(w.date) {
		t.Fatalf("reportAt = %v (known=%v), want the date with no time claimed", at, timeKnown)
	}
	if _, ok := dateWindow(""); ok {
		t.Fatal("an empty air date must not parse")
	}
}

func TestNoonUTCAirstampIsAPlaceholderNotABroadcast(t *testing.T) {
	// TVMaze emits an airstamp for every episode. When it holds no air time it
	// leaves airtime empty and stamps noon UTC — Apple TV+, Netflix and Disney+
	// titles all land here. Reading that stamp as a broadcast instant held the
	// gate shut until midday UTC on episodes already dropped hours earlier.
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}
	srv := airedTestServer(t, tvmazeShowWithEpisode("2026-08-21", "", "2026-08-21T12:00:00+00:00"), nil)

	w, ok := srv.tvmazeAirWindow(context.Background(), ids)
	if !ok {
		t.Fatal("a placeholder airstamp must still yield a known air date")
	}
	if !w.scheduled.IsZero() {
		t.Fatalf("scheduled = %v, want zero: TVMaze stated no air time", w.scheduled)
	}
	want := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	if !w.opensAt.Equal(want) {
		t.Fatalf("gate opens at %v, want %v — the noon stamp must not gate", w.opensAt, want)
	}
}

func TestKnownAirstampReportsTheScheduleButGatesOnTheDate(t *testing.T) {
	// TVMaze normalises airstamps to UTC, so a US primetime broadcast lands on
	// the UTC day after its own air date. The schedule is what the user is
	// told; the gate still opens when the air date begins, or the hours
	// between would hide a release that is already out.
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}
	srv := airedTestServer(t, tvmazeShowWithEpisode("2019-05-19", "21:00", "2019-05-20T01:00:00+00:00"), nil)

	w, ok := srv.tvmazeAirWindow(context.Background(), ids)
	if !ok {
		t.Fatal("an episode with a real air time must be known")
	}
	wantSchedule := time.Date(2019, 5, 20, 1, 0, 0, 0, time.UTC)
	if !w.scheduled.Equal(wantSchedule) {
		t.Fatalf("scheduled = %v, want the airstamp %v", w.scheduled, wantSchedule)
	}
	if at, timeKnown := w.reportAt(); !timeKnown || !at.Equal(wantSchedule) {
		t.Fatalf("reportAt = %v (known=%v), want the broadcast instant", at, timeKnown)
	}
	wantOpen := time.Date(2019, 5, 18, 10, 0, 0, 0, time.UTC)
	if !w.opensAt.Equal(wantOpen) {
		t.Fatalf("gate opens at %v, want %v — the schedule must not gate past its own date", w.opensAt, wantOpen)
	}
}

func TestAnEarlyAirstampPullsTheGateForward(t *testing.T) {
	// If a source ever states a broadcast earlier than the air date begins,
	// the earlier instant wins: the gate tracks the earliest evidence, never
	// the later one.
	early := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	w := withSchedule(early, "2026-08-21")
	if !w.opensAt.Equal(early) {
		t.Fatalf("gate opens at %v, want the earlier stated broadcast %v", w.opensAt, early)
	}
}

func TestAiredByTimeHonoursTheMargin(t *testing.T) {
	if !airedByTime(time.Now().Add(unairedMargin / 2)) {
		t.Fatalf("an episode airing within the margin should count as aired")
	}
	if airedByTime(time.Now().Add(2 * unairedMargin)) {
		t.Fatalf("an episode airing past the margin should still gate")
	}
	if !airedByTime(time.Now().Add(-time.Hour)) {
		t.Fatalf("an episode that already aired should count as aired")
	}
}

func TestUnairedSearchGateIsPerStream(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}
	srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)

	off := false
	opted := &auth.Stream{Username: "opted-out", UnairedSearchGate: &off}
	if _, _, known := srv.episodeAiredState(context.Background(), opted, "series", ids); known {
		t.Fatalf("with the gate off the search must run, so known=false")
	}

	on := true
	opted.UnairedSearchGate = &on
	if aired, _, known := srv.episodeAiredState(context.Background(), opted, "series", ids); !known || aired {
		t.Fatalf("known=%v aired=%v, want the gate to hold a future episode", known, aired)
	}

	// Unset means on: a stream nobody configured still skips the fan-out.
	fresh := &auth.Stream{Username: "fresh"}
	if aired, _, known := srv.episodeAiredState(context.Background(), fresh, "series", ids); !known || aired {
		t.Fatalf("known=%v aired=%v, want an unconfigured stream to keep the gate", known, aired)
	}

	// One stream opting out must not open the gate for another.
	if _, _, known := srv.episodeAiredState(context.Background(), &auth.Stream{UnairedSearchGate: &off}, "series", ids); known {
		t.Fatalf("the opted-out stream should still search")
	}
	if _, _, known := srv.episodeAiredState(context.Background(), fresh, "series", ids); !known {
		t.Fatalf("the other stream must keep its own gate")
	}

	// The gate is not a metadata setting: it holds with the metadata provider
	// switched off entirely.
	srv.config.Metadata.Enabled = &off
	if aired, _, known := srv.episodeAiredState(context.Background(), fresh, "series", ids); !known || aired {
		t.Fatalf("known=%v aired=%v, want the gate to hold regardless of metadata", known, aired)
	}
}
