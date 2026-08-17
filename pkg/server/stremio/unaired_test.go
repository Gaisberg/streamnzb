package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		aired, airsAt, known := srv.episodeAiredState(context.Background(), "series", ids)
		if !known || aired {
			t.Fatalf("known=%v aired=%v, want known unaired", known, aired)
		}
		if airsAt.IsZero() {
			t.Fatal("airsAt must carry the air time")
		}
	})

	t.Run("past airstamp searches", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(past), nil)
		aired, _, known := srv.episodeAiredState(context.Background(), "series", ids)
		if !known || !aired {
			t.Fatalf("known=%v aired=%v, want known aired", known, aired)
		}
	})

	t.Run("lookup error fails open", func(t *testing.T) {
		srv := airedTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, nil)
		if _, _, known := srv.episodeAiredState(context.Background(), "series", ids); known {
			t.Fatal("lookup failure must fail open (known=false)")
		}
	})

	t.Run("episode missing from both sources fails open", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)
		other := &session.AvailReportMeta{ImdbID: "tt0944947", Season: 9, Episode: 9}
		if _, _, known := srv.episodeAiredState(context.Background(), "series", other); known {
			t.Fatal("unknown episode must fail open")
		}
	})

	t.Run("no ids fails open", func(t *testing.T) {
		srv := airedTestServer(t, nil, nil)
		if _, _, known := srv.episodeAiredState(context.Background(), "series", &session.AvailReportMeta{Season: 1, Episode: 1}); known {
			t.Fatal("no resolvable ids must fail open")
		}
	})

	t.Run("movies never gate", func(t *testing.T) {
		srv := airedTestServer(t, tvmazeShowWithAirstamp(future), nil)
		if _, _, known := srv.episodeAiredState(context.Background(), "movie", ids); known {
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
		aired, _, known := srv.episodeAiredState(context.Background(), "series", tmdbIDs)
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
		aired, _, known := srv.episodeAiredState(context.Background(), "series", tmdbIDs)
		if !known || !aired {
			t.Fatalf("known=%v aired=%v, want aired on its air date", known, aired)
		}
	})
}

func TestRawSearchCacheUntilClampsToAirTime(t *testing.T) {
	srv := &Server{config: &config.Config{}}

	airsAt := time.Now().Add(30 * time.Minute)
	until := srv.rawSearchCacheUntil(&rawSearchResult{Unaired: true, AirsAt: airsAt})
	if !until.Equal(airsAt) {
		t.Fatalf("until = %v, want clamped to airsAt %v", until, airsAt)
	}

	// A normal result keeps the sliding TTL.
	normal := srv.rawSearchCacheUntil(&rawSearchResult{})
	if !normal.After(time.Now().Add(time.Minute)) {
		t.Fatalf("normal until = %v, want the sliding TTL", normal)
	}

	// An unaired result airing beyond the TTL is capped by the TTL, not extended.
	farOut := srv.rawSearchCacheUntil(&rawSearchResult{Unaired: true, AirsAt: time.Now().Add(90 * 24 * time.Hour)})
	if farOut.After(time.Now().Add(31 * 24 * time.Hour)) {
		t.Fatalf("far-future air date must not extend the cache TTL, got %v", farOut)
	}
}
