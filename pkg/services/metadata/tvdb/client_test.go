package tvdb

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
				{"image": "https://artworks.thetvdb.com/banner.jpg", "type": 1, "score": 900},
				{"image": "https://artworks.thetvdb.com/fanart-bad.jpg", "type": 3, "score": 5},
				{"image": "https://artworks.thetvdb.com/fanart.jpg", "type": 3, "score": 100},
				{"image": "https://artworks.thetvdb.com/fanart-unscored.jpg", "type": 3}
			],
			"characters": [
				{"name": "Jack Shephard", "personName": "Matthew Fox", "peopleType": "Actor", "personImgURL": "https://artworks.thetvdb.com/fox.jpg"},
				{"name": "Kate Austen", "personName": "Evangeline Lilly", "peopleType": "Actor"},
				{"name": "", "personName": "J.J. Abrams", "peopleType": "Creator"}
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
		t.Fatalf("Background() = %q, want the highest-scored type-3 artwork", ext.Background())
	}
	members := ext.CastMembers(10)
	if len(members) != 2 || members[0].Name != "Matthew Fox" || members[0].Character != "Jack Shephard" ||
		members[0].Photo != "https://artworks.thetvdb.com/fox.jpg" || members[1].Photo != "" {
		t.Fatalf("CastMembers() = %+v", members)
	}
	if ext.AverageRuntime != 45 || ext.Year != "2004" {
		t.Fatalf("runtime/year = %d/%q", ext.AverageRuntime, ext.Year)
	}
}

// TestDisplayLanguageTranslations covers the whole localization path: the
// TMDB-style tag converts to TVDB's ISO 639-3 code, series name/overview and
// episode text overlay from the translation endpoints, and untranslated
// fields keep the default-language record.
func TestDisplayLanguageTranslations(t *testing.T) {
	client := newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/series/73739/extended":
			_, _ = w.Write([]byte(`{"status": "success", "data": {
				"id": 73739, "name": "Lost", "overview": "Stranded on an island."
			}}`))
		case "/series/73739/translations/deu":
			_, _ = w.Write([]byte(`{"status": "success", "data": {
				"name": "Lost (DE)", "overview": ""
			}}`))
		case "/series/73739/episodes/default":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": [
				{"seasonNumber": 1, "number": 1, "name": "Pilot (1)", "overview": "The crash."},
				{"seasonNumber": 1, "number": 2, "name": "Pilot (2)"}
			]}, "links": {"next": null}}`))
		case "/series/73739/episodes/default/deu":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"episodes": [
				{"seasonNumber": 1, "number": 1, "name": "Pilot (1, DE)", "overview": "Der Absturz."},
				{"seasonNumber": 1, "number": 2, "name": ""}
			]}, "links": {"next": null}}`))
		default:
			http.NotFound(w, r)
		}
	})
	client.SetLanguage("de-DE")

	ext, err := client.GetSeriesExtended("73739")
	if err != nil {
		t.Fatalf("GetSeriesExtended: %v", err)
	}
	// Translated name wins; the empty translated overview keeps the default.
	if ext.Name != "Lost (DE)" || ext.Overview != "Stranded on an island." {
		t.Fatalf("translated ext = %q / %q", ext.Name, ext.Overview)
	}
	// The raw translation is exposed so callers can tell a real translation
	// from the overlaid fallback (the anime description path needs this).
	if name, overview := client.SeriesTranslation("73739"); name != "Lost (DE)" || overview != "" {
		t.Fatalf("SeriesTranslation = %q / %q", name, overview)
	}

	episodes, err := client.GetSeriesEpisodes("73739")
	if err != nil {
		t.Fatalf("GetSeriesEpisodes: %v", err)
	}
	if episodes[0].Name != "Pilot (1, DE)" || episodes[0].Overview != "Der Absturz." {
		t.Fatalf("episode 1 = %+v, want the German overlay", episodes[0])
	}
	if episodes[1].Name != "Pilot (2)" {
		t.Fatalf("episode 2 = %+v, want the default name kept", episodes[1])
	}
}

// TestDisplayLanguageMissingTranslationFallsBack pins fail-open behavior: a
// 404 translation keeps the default record, and English disables the lookups.
func TestDisplayLanguageMissingTranslationFallsBack(t *testing.T) {
	var translationHits atomic.Int64
	client := newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/series/73739/extended":
			_, _ = w.Write([]byte(`{"status": "success", "data": {"id": 73739, "name": "Lost"}}`))
		case strings.HasPrefix(r.URL.Path, "/series/73739/translations/"):
			translationHits.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	client.SetLanguage("fi-FI")
	ext, err := client.GetSeriesExtended("73739")
	if err != nil || ext.Name != "Lost" {
		t.Fatalf("ext = %+v, err = %v, want the default record", ext, err)
	}
	if translationHits.Load() != 1 {
		t.Fatalf("translation hits = %d, want 1", translationHits.Load())
	}

	// The 404 is a cached answer: repeat requests must not re-ask TVDB.
	if _, err := client.GetSeriesExtended("73739"); err != nil {
		t.Fatalf("second GetSeriesExtended: %v", err)
	}
	if name, overview := client.SeriesTranslation("73739"); name != "" || overview != "" {
		t.Fatalf("SeriesTranslation = %q / %q, want empty", name, overview)
	}
	if translationHits.Load() != 1 {
		t.Fatalf("translation hits after repeat = %d, want still 1 (404 cached)", translationHits.Load())
	}

	// English (any region) turns translation lookups off entirely.
	client.SetLanguage("en-GB")
	if _, err := client.GetSeriesExtended("73739"); err != nil {
		t.Fatalf("english GetSeriesExtended: %v", err)
	}
	if translationHits.Load() != 1 {
		t.Fatalf("translation hits after en-GB = %d, want still 1", translationHits.Load())
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

// A remote id that TVDB matches only as a movie must not resolve. Movie ids
// share numbers with unrelated series (movie 276 is Spirited Away, series 276
// is Soccer Aid), and every caller reads the result as a series id.
func TestResolveTVDBIDIgnoresMovieOnlyMatches(t *testing.T) {
	client := newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/search/remoteid/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status": "success", "data": [
			{"movie": {"id": 276, "name": "Spirited Away"}}
		]}`))
	})

	if id, err := client.ResolveTVDBID("tt0245429"); err == nil {
		t.Fatalf("ResolveTVDBID = %q, want an error for a movie-only match", id)
	}
}

func TestResolveTVDBIDPicksSeriesOverMovie(t *testing.T) {
	client := newStubClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/search/remoteid/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status": "success", "data": [
			{"movie": {"id": 900}},
			{"series": {"id": 355480}}
		]}`))
	})

	id, err := client.ResolveTVDBID("tt9307686")
	if err != nil {
		t.Fatalf("ResolveTVDBID: %v", err)
	}
	if id != "355480" {
		t.Fatalf("ResolveTVDBID = %q, want the series id 355480", id)
	}
}
