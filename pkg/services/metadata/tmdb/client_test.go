package tmdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/services/metadata/metacache"
)

func TestLetterboxdTitleParsesPublicFilmAndListPages(t *testing.T) {
	for _, tc := range []struct {
		name string
		html string
		want string
	}{
		{"film", `<title>&lrm;The Departed (2006) directed by Martin Scorsese • Reviews, film + cast &bull; Letterboxd</title>`, "The Departed"},
		{"list", `<meta property="og:title" content="Movies everyone should watch at least once during their lifetime"><title>&lrm;Movies everyone should watch at least once during their lifetime, a list of films by fcbarcelona &bull; Letterboxd</title>`, "Movies everyone should watch at least once during their lifetime"},
		{"list og directional mark", `<meta property="og:title" content="&lrm;Movies everyone should watch">`, "Movies everyone should watch"},
		// A real list page carries film years further down. The film pattern
		// used to scan past </title> to the first of them and return ~80KB of
		// page source as the list's name, which the catalog editor then
		// rendered in full.
		{"list with years later in the page", `<meta property="og:title" content="Films by fcbarcelona"><title>&lrm;Films by fcbarcelona &bull; Letterboxd</title><script>var conf = {}</script><span>Inception (2010)</span>`, "Films by fcbarcelona"},
		{"list with no og title and years later in the page", `<title>&lrm;Films by fcbarcelona &bull; Letterboxd</title><span>Inception (2010)</span>`, "Films by fcbarcelona"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := letterboxdTitle([]byte(tc.html)); got != tc.want {
				t.Fatalf("letterboxdTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

// unguardedPublicListClient points the public-list fetchers at a loopback test
// server. publicListHTTPClient refuses to dial one by design (see
// httpx.GuardedClient), which is exactly the protection under test in
// pkg/server/stremio — here the destination is our own stub, so the guard is
// swapped out for the duration of the test.
func unguardedPublicListClient(t *testing.T) {
	t.Helper()
	guarded := publicListHTTPClient
	publicListHTTPClient = &http.Client{Timeout: publicListRequestTimeout}
	t.Cleanup(func() { publicListHTTPClient = guarded })
}

func TestFetchPublicHTMLRetriesTransientRateLimit(t *testing.T) {
	unguardedPublicListClient(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("<html>ok</html>"))
	}))
	defer server.Close()

	body, err := fetchPublicHTML(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchPublicHTML() error = %v", err)
	}
	if string(body) != "<html>ok</html>" || calls.Load() != 2 {
		t.Fatalf("body/calls = %q/%d, want success after two requests", body, calls.Load())
	}
}

// TestResolveLetterboxdFilmsSkipsRemovedFilm pins the fix for a film page that
// 404s. fetchPublicHTML reports a 404 as ErrPublicListPageNotFound, which the
// catalog layer reads as "the list ended here". Before the fix a single
// removed film wrapped that sentinel into the page's error, so
// publicListCatalog stopped paging and silently dropped every later page of
// the list — the films after it simply vanished from the catalog.
func TestResolveLetterboxdFilmsSkipsRemovedFilm(t *testing.T) {
	unguardedPublicListClient(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/film/removed/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<title>&lrm;Kept Film (2006) directed by Someone &bull; Letterboxd</title>` +
			`<a href="https://www.imdb.com/title/tt0111161/">IMDb</a>`))
	}))
	defer server.Close()

	items, err := resolveLetterboxdFilms(context.Background(), []string{
		server.URL + "/film/kept/",
		server.URL + "/film/removed/",
	})
	if err != nil {
		t.Fatalf("a removed film must not fail the page: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want one slot per film URL", len(items))
	}
	if items[0].IMDbID != "tt0111161" || items[0].Name != "Kept Film" {
		t.Fatalf("kept film = %+v", items[0])
	}
	if items[1].IMDbID != "" {
		t.Fatalf("removed film should resolve to no canonical id, got %+v", items[1])
	}
}

// TestResolveLetterboxdFilmsFailsOnNonTerminalError is the other half: a real
// upstream failure must still fail the whole page rather than quietly shorten
// it, since a partial page causes duplicate and missing rows once the client
// pages past it.
func TestResolveLetterboxdFilmsFailsOnNonTerminalError(t *testing.T) {
	unguardedPublicListClient(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	if _, err := resolveLetterboxdFilms(context.Background(), []string{server.URL + "/film/x/"}); err == nil {
		t.Fatal("a 500 on a film page must fail the list page")
	}
}

// TestFetchPublicListPageCachesPages pins the fix for re-walking a public list
// on every request: one Stremio page at a deep skip reads every upstream page
// before it, and a Jellyfin client asks for ten such windows to fill one view,
// so an uncached fetcher turned a single view into dozens of outbound requests.
func TestFetchPublicListPageCachesPages(t *testing.T) {
	unguardedPublicListClient(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`<a title="The Matrix" href="/movie/603-the-matrix">x</a>`))
	}))
	defer server.Close()

	// FetchPublicListPage only accepts a themoviedb.org URL, so drive the
	// cache through the same helper the fetchers use, keyed by request URL.
	key := server.URL + "/list/1?page=1"
	if _, ok := loadPublicListPage(key); ok {
		t.Fatal("cache must start empty for this key")
	}
	storePublicListPage(key, PublicListPage{Items: []PublicListItem{{ID: 603, Type: "movie", Name: "The Matrix"}}})
	cached, ok := loadPublicListPage(key)
	if !ok || len(cached.Items) != 1 || cached.Items[0].ID != 603 {
		t.Fatalf("cached page = %+v (ok=%v)", cached, ok)
	}
	// A cached page must be a copy: a caller appending to the returned slice
	// must not mutate what the next reader sees.
	cached.Items = append(cached.Items, PublicListItem{ID: 604})
	again, _ := loadPublicListPage(key)
	if len(again.Items) != 1 {
		t.Fatalf("cache handed out shared storage: %+v", again.Items)
	}
}

// TestStorePublicListPageSkipsEmptyPages pins the negative-caching guard. A
// 200 that parsed to nothing — an anti-bot interstitial, a loading shell, or
// markup that has moved on — must not be cached: publicListCatalog reads an
// empty page as the end of the list, so caching one bad scrape would truncate
// or empty the catalog for the full TTL and the source tester would keep
// reporting that same cached emptiness.
func TestStorePublicListPageSkipsEmptyPages(t *testing.T) {
	key := "https://example.invalid/list/empty?page=1"
	storePublicListPage(key, PublicListPage{Name: "Interstitial"})
	if _, ok := loadPublicListPage(key); ok {
		t.Fatal("an unparseable page must not be cached — the next request has to retry")
	}
	storePublicListPage(key, PublicListPage{Items: []PublicListItem{{ID: 1, Type: "movie", Name: "Real"}}})
	if _, ok := loadPublicListPage(key); !ok {
		t.Fatal("a page that actually parsed must still be cached")
	}
}

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

	if _, err := client.GetMovieDetailsFull(603, "fi-FI"); err != nil {
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
	if _, err := client2.GetMovieDetailsFull(603, ""); err != nil {
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
