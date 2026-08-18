package tmdb

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/services/metadata/metacache"
)

// TestDisplayLanguageParams pins the localization request shape: language on
// the details call, image/video language lists carrying the configured
// language ahead of the English and textless fallbacks.
func TestDisplayLanguageParams(t *testing.T) {
	var gotQuery atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery.Store(r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 603, "title": "Matrix"}`))
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.BaseURL = server.URL
	client.SetLanguage("fi-FI")

	if _, err := client.GetMovieDetailsFull(603); err != nil {
		t.Fatalf("GetMovieDetailsFull: %v", err)
	}
	q := gotQuery.Load().(url.Values)
	if q.Get("language") != "fi-FI" {
		t.Fatalf("language = %q", q.Get("language"))
	}
	if q.Get("include_image_language") != "fi,en,null" || q.Get("include_video_language") != "fi,en,null" {
		t.Fatalf("image/video languages = %q / %q", q.Get("include_image_language"), q.Get("include_video_language"))
	}

	// The English default stays parameter-free apart from the image filter.
	client2 := NewClient("test-key")
	client2.BaseURL = server.URL
	if _, err := client2.GetMovieDetailsFull(603); err != nil {
		t.Fatalf("default GetMovieDetailsFull: %v", err)
	}
	q = gotQuery.Load().(url.Values)
	if q.Get("language") != "" || q.Get("include_image_language") != "en,null" {
		t.Fatalf("default params = %v", q)
	}
}

func TestBestLogoPrefersConfiguredLanguage(t *testing.T) {
	var images Images
	for _, logo := range []struct {
		path  string
		lang  string
		votes float64
	}{
		{"/en.png", "en", 9},
		{"/fi-low.png", "fi", 1},
		{"/fi.png", "fi", 4},
	} {
		images.Logos = append(images.Logos, struct {
			FilePath    string  `json:"file_path"`
			ISO639_1    string  `json:"iso_639_1"`
			VoteAverage float64 `json:"vote_average"`
		}{logo.path, logo.lang, logo.votes})
	}
	if got := images.BestLogo("fi"); got != "/fi.png" {
		t.Fatalf("BestLogo(fi) = %q", got)
	}
	if got := images.BestLogo(""); got != "/en.png" {
		t.Fatalf("BestLogo() = %q", got)
	}
	if got := images.BestLogo("de"); got != "/en.png" {
		t.Fatalf("BestLogo(de) = %q, want the English fallback", got)
	}
	var nilImages *Images
	if got := nilImages.BestLogo("fi"); got != "" {
		t.Fatalf("nil BestLogo = %q", got)
	}
}

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
