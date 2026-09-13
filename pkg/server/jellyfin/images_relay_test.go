package jellyfin

import (
	"net/http"
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

// A poster URL this layer cannot fetch used to be answered with a redirect to
// it -- the one response the relay exists to avoid. It is a miss like any
// other, not a gateway failure.
func TestImageRelayUnfetchableURLIsNotFoundNotRedirect(t *testing.T) {
	for _, raw := range []string{"ftp://cdn.example/poster.jpg", "poster.jpg", "://bad"} {
		f := newFixture()
		f.catalog.metas["movie/tt0111161"].Poster = raw
		movie, _ := itemIDFor("movie", "tt0111161")
		rec := f.do(http.MethodGet, "/jellyfin/Items/"+movie.encode()+"/Images/Primary", "")
		if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
			t.Fatalf("%q: %d location=%q, want 404 and no redirect", raw, rec.Code, rec.Header().Get("Location"))
		}
	}
}
