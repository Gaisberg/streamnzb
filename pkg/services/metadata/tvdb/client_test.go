package tvdb

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/metacache"
)

// newStubClient builds a client against a stub API. The login endpoint is
// stubbed so ensureToken succeeds; the data dir feeds the process-wide
// persistence singleton (never closed here — see newBadReleaseTestServer for
// the same pattern).
func newStubClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	logger.Init("ERROR")
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status": "success", "data": {"token": "test-token"}}`))
	})
	mux.Handle("/", handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir, err := os.MkdirTemp("", "tvdb_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	client := NewClient("test-key", dir)
	client.BaseURL = server.URL
	return client
}

func TestGetSeriesExtended(t *testing.T) {
	client := newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/series/73739/extended" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status": "success", "data": {
			"id": 73739, "name": "Lost", "overview": "Stranded on an island.",
			"image": "https://artworks.thetvdb.com/poster.jpg",
			"firstAired": "2004-09-22", "year": "2004",
			"status": {"name": "Ended"}, "averageRuntime": 45,
			"genres": [{"name": "Drama"}],
			"artworks": [
				{"image": "https://artworks.thetvdb.com/banner.jpg", "type": 1},
				{"image": "https://artworks.thetvdb.com/fanart.jpg", "type": 3}
			]
		}}`))
	})

	ext, err := client.GetSeriesExtended("73739")
	if err != nil {
		t.Fatalf("GetSeriesExtended: %v", err)
	}
	if ext.Name != "Lost" || ext.Image != "https://artworks.thetvdb.com/poster.jpg" {
		t.Fatalf("ext = %+v", ext)
	}
	if ext.Background() != "https://artworks.thetvdb.com/fanart.jpg" {
		t.Fatalf("Background() = %q, want the type-3 artwork", ext.Background())
	}
	if ext.AverageRuntime != 45 || ext.Year != "2004" {
		t.Fatalf("runtime/year = %d/%q", ext.AverageRuntime, ext.Year)
	}
}

func TestGetSeriesEpisodesPaginates(t *testing.T) {
	var pageHits atomic.Int64
	client := newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/series/73739/episodes/default" {
			http.NotFound(w, r)
			return
		}
		pageHits.Add(1)
		switch r.URL.Query().Get("page") {
		case "0":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": [
				{"seasonNumber": 1, "number": 1, "name": "Pilot (1)", "aired": "2004-09-22", "image": "https://img/e1.jpg"},
				{"seasonNumber": 0, "number": 1, "name": "Special", "aired": "2005-01-01"}
			]}, "links": {"next": "/series/73739/episodes/default?page=1"}}`))
		case "1":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": [
				{"seasonNumber": 1, "number": 2, "name": "Pilot (2)", "aired": "2004-09-29"}
			]}, "links": {"next": null}}`))
		default:
			http.NotFound(w, r)
		}
	})

	episodes, err := client.GetSeriesEpisodes("73739")
	if err != nil {
		t.Fatalf("GetSeriesEpisodes: %v", err)
	}
	if len(episodes) != 3 {
		t.Fatalf("episodes = %d, want 3 across two pages", len(episodes))
	}
	if episodes[2].Name != "Pilot (2)" {
		t.Fatalf("last episode = %+v", episodes[2])
	}
	if got := pageHits.Load(); got != 2 {
		t.Fatalf("page hits = %d, want 2", got)
	}

	// Second call is fully served from the cache.
	if _, err := client.GetSeriesEpisodes("73739"); err != nil {
		t.Fatalf("cached GetSeriesEpisodes: %v", err)
	}
	if got := pageHits.Load(); got != 2 {
		t.Fatalf("page hits after cached call = %d, want 2", got)
	}
}

func TestExtendedSharedCacheAcrossInstances(t *testing.T) {
	var hits atomic.Int64
	var client *Client
	client = newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"status": "success", "data": {"id": 73739, "name": "Lost"}}`))
	})
	shared := metacache.New(nil, "tvdb")
	client.cache = shared

	if _, err := client.GetSeriesExtended("73739"); err != nil {
		t.Fatalf("first client: %v", err)
	}

	rebuilt := NewClientWithCache("test-key", client.dataDir, shared)
	rebuilt.BaseURL = client.BaseURL
	ext, err := rebuilt.GetSeriesExtended("73739")
	if err != nil {
		t.Fatalf("rebuilt client: %v", err)
	}
	if ext.Name != "Lost" {
		t.Fatalf("ext = %+v", ext)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (rebuilt client must reuse the shared cache)", got)
	}
}

func TestExtendedErrorsWithoutAPIKey(t *testing.T) {
	client := NewClient("", "")
	if _, err := client.GetSeriesExtended("73739"); err == nil {
		t.Fatal("expected error without API key")
	}
	if _, err := client.GetSeriesEpisodes("73739"); err == nil {
		t.Fatal("expected error without API key")
	}
	var nilClient *Client
	if _, err := nilClient.GetSeriesExtended("73739"); err == nil {
		t.Fatal("nil client must error, not panic")
	}
}
