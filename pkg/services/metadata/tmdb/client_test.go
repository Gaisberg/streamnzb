package tmdb

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/services/metadata/metacache"
)

func TestDoRequestCachesOKResponses(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix"}`))
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.BaseURL = server.URL

	for i := 0; i < 3; i++ {
		details, err := client.GetMovieDetails(603)
		if err != nil {
			t.Fatalf("GetMovieDetails call %d: %v", i+1, err)
		}
		if details.Title != "The Matrix" {
			t.Fatalf("call %d: Title = %q", i+1, details.Title)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (responses should be cached)", got)
	}
}

// TestClientsShareInjectedCache is the config-reload scenario: a rebuilt client
// handed the same cache must serve earlier fetches without touching upstream.
func TestClientsShareInjectedCache(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix"}`))
	}))
	defer server.Close()

	shared := metacache.New(nil, "tmdb")

	first := NewClientWithCache("test-key", shared)
	first.BaseURL = server.URL
	if _, err := first.GetMovieDetails(603); err != nil {
		t.Fatalf("first client: %v", err)
	}

	rebuilt := NewClientWithCache("test-key", shared)
	rebuilt.BaseURL = server.URL
	details, err := rebuilt.GetMovieDetails(603)
	if err != nil {
		t.Fatalf("rebuilt client: %v", err)
	}
	if details.Title != "The Matrix" {
		t.Fatalf("Title = %q", details.Title)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (rebuilt client must reuse the shared cache)", got)
	}
}

func TestDoRequestDoesNotCacheErrors(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.BaseURL = server.URL

	for i := 0; i < 2; i++ {
		if _, err := client.GetMovieDetails(603); err == nil {
			t.Fatalf("call %d: expected error from 502 response", i+1)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("upstream hits = %d, want 2 (errors must not be cached)", got)
	}
}
