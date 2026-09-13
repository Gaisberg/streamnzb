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

// An upstream that sends no Content-Length can only be capped by reading.
// A body past the cap used to be cut off behind a 200 -- a truncated JPEG a
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
