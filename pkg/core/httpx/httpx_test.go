package httpx

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestHostFromAddr(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"192.0.2.10:54321", "192.0.2.10"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		// Not every caller supplies host:port. Returning the value unchanged
		// keeps it usable as a key, rather than collapsing everything onto "".
		{"192.0.2.10", "192.0.2.10"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := HostFromAddr(tc.addr); got != tc.want {
			t.Fatalf("HostFromAddr(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestClientIPIgnoresForwardedHeaders(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "192.0.2.10:54321",
		Header: http.Header{
			"X-Forwarded-For": []string{"198.51.100.7"},
			"X-Real-Ip":       []string{"198.51.100.8"},
		},
	}
	// Honouring these would let a client pick its own rate-limit bucket.
	if got := ClientIP(r); got != "192.0.2.10" {
		t.Fatalf("ClientIP = %q, want the real peer address 192.0.2.10", got)
	}
	if got := ClientIP(nil); got != "" {
		t.Fatalf("ClientIP(nil) = %q, want empty", got)
	}
}

func TestIsSecure(t *testing.T) {
	cases := []struct {
		name  string
		tls   bool
		proto string
		want  bool
	}{
		{"direct tls", true, "", true},
		{"plain http", false, "", false},
		{"terminating proxy", false, "https", true},
		{"proxy on plain http", false, "http", false},
		{"case insensitive", false, "HTTPS", true},
		{"padded", false, "  https  ", true},
		// Chained proxies append; the first entry is the scheme the original
		// client used, which is the one the cookie has to match.
		{"chained, client on https", false, "https, http", true},
		{"chained, client on http", false, "http, https", false},
		{"nonsense", false, "gopher", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{Header: http.Header{}}
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			if got := IsSecure(r); got != tc.want {
				t.Fatalf("IsSecure = %v, want %v", got, tc.want)
			}
		})
	}

	if IsSecure(nil) {
		t.Fatal("IsSecure(nil) should be false")
	}
}
