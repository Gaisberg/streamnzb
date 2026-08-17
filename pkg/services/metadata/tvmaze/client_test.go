package tvmaze

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const showJSON = `{
	"id": 82,
	"name": "Game of Thrones",
	"premiered": "2011-04-17",
	"status": "Ended",
	"runtime": 60,
	"rating": {"average": 8.9},
	"image": {"medium": "https://img/m.jpg", "original": "https://img/o.jpg"},
	"_embedded": {"episodes": [
		{"id": 1, "season": 1, "number": 1, "name": "Winter Is Coming",
		 "airdate": "2011-04-17", "airstamp": "2011-04-18T01:00:00+00:00"}
	]}
}`

func TestLookupByIMDBFollowsRedirect(t *testing.T) {
	var lookupHits, showHits atomic.Int64
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/lookup/shows", func(w http.ResponseWriter, r *http.Request) {
		lookupHits.Add(1)
		if got := r.URL.Query().Get("imdb"); got != "tt0944947" {
			t.Errorf("imdb query = %q", got)
		}
		http.Redirect(w, r, server.URL+"/shows/82", http.StatusFound)
	})
	mux.HandleFunc("/shows/82", func(w http.ResponseWriter, r *http.Request) {
		showHits.Add(1)
		_, _ = w.Write([]byte(showJSON))
	})

	client := NewClient(nil, nil)
	client.BaseURL = server.URL

	for i := 0; i < 2; i++ {
		show, err := client.LookupByIMDB(context.Background(), "tt0944947")
		if err != nil {
			t.Fatalf("lookup %d: %v", i+1, err)
		}
		if show.ID != 82 || show.Name != "Game of Thrones" {
			t.Fatalf("show = %+v", show)
		}
	}
	// The redirect-following lookup result is cached under the lookup key.
	if lookupHits.Load() != 1 || showHits.Load() != 1 {
		t.Fatalf("hits lookup=%d show=%d, want 1/1 (cached)", lookupHits.Load(), showHits.Load())
	}
}

func TestGetShowWithEpisodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shows/82" || r.URL.Query().Get("embed") != "episodes" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(showJSON))
	}))
	defer server.Close()

	client := NewClient(nil, nil)
	client.BaseURL = server.URL

	show, err := client.GetShowWithEpisodes(context.Background(), 82)
	if err != nil {
		t.Fatalf("GetShowWithEpisodes: %v", err)
	}
	eps := show.Embedded.Episodes
	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want 1", len(eps))
	}
	if eps[0].Season != 1 || eps[0].Number != 1 || eps[0].Airstamp != "2011-04-18T01:00:00+00:00" {
		t.Fatalf("episode = %+v", eps[0])
	}
}

func TestRateLimitRetriesOnce(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(showJSON))
	}))
	defer server.Close()

	client := NewClient(nil, nil)
	client.BaseURL = server.URL

	show, err := client.GetShowWithEpisodes(context.Background(), 82)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if show.ID != 82 {
		t.Fatalf("show id = %d", show.ID)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2 (one retry)", got)
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(nil, nil)
	client.BaseURL = server.URL

	for i := 0; i < 2; i++ {
		if _, err := client.LookupByIMDB(context.Background(), "tt404"); err == nil {
			t.Fatalf("call %d: expected error", i+1)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("hits = %d, want 2 (errors must not be cached)", got)
	}
}
