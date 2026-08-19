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

func tvmazeShowWithAirstamp(airstamp string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lookup/shows"):
			_, _ = w.Write([]byte(`{"id": 82}`))
		case strings.HasPrefix(r.URL.Path, "/shows/82"):
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id": 82, "_embedded": {"episodes": [
				{"id": 1, "season": 2, "number": 5, "airstamp": %q}
			]}}`, airstamp)))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestEpisodeAiredState(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}

	t.Run("future airstamp gates", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)
		aired, airsAt, known := srv.episodeAiredState(context.Background(), nil, "series", ids)
		if !known || aired {
			t.Fatalf("known=%v aired=%v, want known unaired", known, aired)
		}
		if airsAt.IsZero() {
			t.Fatal("airsAt must carry the air time")
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
		futureDate := time.Now().Add(7 * 24 * time.Hour).UTC().Format("2006-01-02")
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
		aired, _, known := srv.episodeAiredState(context.Background(), nil, "series", tmdbIDs)
		if !known || aired {
			t.Fatalf("known=%v aired=%v, want gated via TMDB fallback", known, aired)
		}
	})

	t.Run("date-only air date counts as aired on the day", func(t *testing.T) {
		today := time.Now().UTC().Format("2006-01-02")
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

func TestAirstampGatesOnItsRealAirTime(t *testing.T) {
	// TVMaze knows this one airs at 21:00 Eastern. The gate must hold until
	// that instant rather than opening at midnight on either side of it.
	airsAt := time.Now().Add(6 * time.Hour).Truncate(time.Minute)
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}
	srv := airedTestServer(t, tvmazeShowWithAirstamp(airsAt.Format(time.RFC3339)), nil)

	aired, got, known := srv.episodeAiredState(context.Background(), nil, "series", ids)
	if !known || aired {
		t.Fatalf("known=%v aired=%v, want the gate to hold until the broadcast time", known, aired)
	}
	if !got.Equal(airsAt) {
		t.Fatalf("air time = %v, want the airstamp instant %v", got, airsAt)
	}
}

func TestMidnightAirstampIsReadAsADate(t *testing.T) {
	// TVMaze still emits an airstamp for shows it holds no air time for: it
	// lands on midnight in the network's own zone. That is a date in disguise,
	// so it must be read as the local air date rather than gating the search
	// until midnight somewhere else in the world.
	ids := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 2, Episode: 5}
	now := time.Now()
	stamp := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.FixedZone("EST", -5*3600))
	srv := airedTestServer(t, tvmazeShowWithAirstamp(stamp.Format(time.RFC3339)), nil)

	aired, got, known := srv.episodeAiredState(context.Background(), nil, "series", ids)
	if !known {
		t.Fatalf("a midnight airstamp should still be a known air date")
	}
	want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("air time = %v, want local midnight on the air date %v", got, want)
	}
	if !aired {
		t.Fatalf("an episode whose air date is today should count as aired")
	}
}

func TestDateOnlyAirDatesParseLocal(t *testing.T) {
	// The gate must not read a bare date as midnight UTC: east of Greenwich
	// that made an episode aired hours into its own local day, and west of it
	// hours before the day started.
	got, ok := parseAirDate("2026-08-19")
	if !ok {
		t.Fatalf("parse air date failed")
	}
	want := time.Date(2026, 8, 19, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("date-only air date parsed as %v, want %v", got, want)
	}
	if _, ok := parseAirDate(""); ok {
		t.Fatalf("an empty air date must not parse")
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
