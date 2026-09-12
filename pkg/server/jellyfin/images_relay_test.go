package jellyfin

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// An image relay serves the upstream's bytes from this server's own origin, so
// the upstream must not get to pick a content type the browser will execute.
func TestRelayableImageType(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{"", "image/jpeg", true}, // CDNs that omit it are assumed to be JPEG
		{"image/jpeg", "image/jpeg", true},
		{"IMAGE/PNG", "image/png", true},
		{"image/webp; charset=binary", "image/webp", true},
		{"image/svg+xml", "", false}, // carries script
		{"text/html", "", false},
		{"text/html; charset=utf-8", "", false},
		{"application/javascript", "", false},
		{"image/svg+xml; charset=utf-8", "", false},
		{"not a media type", "", false},
	} {
		got, ok := relayableImageType(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Errorf("relayableImageType(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

// The poster fallback is a real CDN in production; tests must never reach it.
// It is pointed at the test CDN's always-404 path so a relay that exhausts its
// candidates ends the way it would with no fallback, and a test that wants a
// serving fallback swaps in a served path for its own duration.
func init() {
	posterFallbackURL = testCDNServer().URL + "/fallback-missing/%s"
}

func withPosterFallback(t *testing.T, pattern string) {
	t.Helper()
	prev := posterFallbackURL
	posterFallbackURL = pattern
	t.Cleanup(func() { posterFallbackURL = prev })
}

// A profile's poster overlay (poster_url_pattern) replaces the provider poster
// before this layer sees it, so when the overlay has no image for a title the
// relay would answer 502 and the title lost its artwork entirely. The relay
// now falls back to a poster reachable from the IMDb id alone.
func TestImageRelayOverlayMissingFallsBackToIMDbPoster(t *testing.T) {
	f := newFixture()
	cdn := testCDNServer().URL
	withPosterFallback(t, cdn+"/fallback/%s")
	f.catalog.metas["movie/tt0111161"].Poster = cdn + "/overlay-missing/tt0111161.jpg"
	movie, _ := itemIDFor("movie", "tt0111161")
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "/fallback/tt0111161" {
		t.Fatalf("overlay missing: %d %q, want 200 /fallback/tt0111161", rec.Code, rec.Body.String())
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len("/fallback/tt0111161")) {
		t.Fatalf("fallback content-length: %q", cl)
	}
}

// Only a Primary poster of an IMDb-keyed movie or series has a fallback. An
// episode's Primary is a still and a backdrop has no id-keyed source, so those
// stay a plain miss.
func TestImageRelayFallbackOnlyForIMDbPosters(t *testing.T) {
	f := newFixture()
	cdn := testCDNServer().URL
	withPosterFallback(t, cdn+"/fallback/%s")
	f.catalog.metas["movie/tt0111161"].Background = cdn + "/bg-missing.jpg"
	movie, _ := itemIDFor("movie", "tt0111161")
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Backdrop", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("backdrop missing with a poster fallback configured: %d", rec.Code)
	}
	series, _ := itemIDFor("series", "tt0903747")
	meta := f.catalog.metas["series/tt0903747"]
	for i := range meta.Videos {
		if meta.Videos[i].Season == 1 && meta.Videos[i].Episode == 1 {
			meta.Videos[i].Thumbnail = cdn + "/still-missing.jpg"
		}
	}
	ep := series.episode(1, 1)
	if rec := f.do(http.MethodGet, "/jellyfin/Items/"+ep.encode()+"/Images/Primary", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("episode still missing: %d", rec.Code)
	}
}

// When neither the advertised poster nor the fallback has an image, the
// answer is a plain 404 — not a 502, which is for an upstream that failed,
// and never a redirect, which the clients that need relaying ignore.
func TestImageRelayAllMissingIsNotFound(t *testing.T) {
	f := newFixture()
	f.catalog.metas["movie/tt0111161"].Poster = testCDNServer().URL + "/overlay-missing/tt0111161.jpg"
	movie, _ := itemIDFor("movie", "tt0111161")
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
		t.Fatalf("all missing: %d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}

// A poster URL this layer cannot fetch used to be answered with a redirect to
// it — the one response the relay exists to avoid. It is a miss like any other.
func TestImageRelayUnfetchableURLIsNotFoundNotRedirect(t *testing.T) {
	f := newFixture()
	for _, raw := range []string{"ftp://cdn.example/poster.jpg", "poster.jpg", "://bad"} {
		f.catalog.metas["movie/tt0111161"].Poster = raw
		movie, _ := itemIDFor("movie", "tt0111161")
		rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", "")
		if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
			t.Fatalf("%q: %d location=%q, want 404 and no redirect", raw, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// An upstream that sends no Content-Length can only be capped by reading.
// A body past the cap used to be cut off behind a 200 — a truncated JPEG a
// client cannot tell from a whole one. It is refused before any header goes
// out; one under the cap is served whole, with the length it turned out to be.
func TestImageRelayUnknownLengthBody(t *testing.T) {
	small := strings.Repeat("x", 4096)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Transfer-Encoding", "chunked") // no Content-Length
		if strings.Contains(r.URL.Path, "huge") {
			chunk := []byte(strings.Repeat("y", 1<<20))
			for sent := 0; sent <= maxImageBody; sent += len(chunk) {
				w.Write(chunk)
			}
			return
		}
		w.Write([]byte(small))
	}))
	defer upstream.Close()
	f := newFixture()
	movie, _ := itemIDFor("movie", "tt0111161")

	f.catalog.metas["movie/tt0111161"].Background = upstream.URL + "/huge.jpg"
	rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Backdrop", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("oversized unknown-length body: %d, want 502", rec.Code)
	}
	if rec.Header().Get("Content-Type") == "image/jpeg" || strings.Contains(rec.Body.String(), "yyyy") {
		t.Fatalf("oversized unknown-length body leaked: %+v %d bytes", rec.Header(), rec.Body.Len())
	}

	f.catalog.metas["movie/tt0111161"].Background = upstream.URL + "/small.jpg"
	rec = f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Backdrop", "")
	if rec.Code != http.StatusOK || rec.Body.String() != small {
		t.Fatalf("unknown-length body under the cap: %d %d bytes", rec.Code, rec.Body.Len())
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(small)) {
		t.Fatalf("unknown-length body served without its length: %q", cl)
	}
}
