package jellyfin

import "testing"

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
