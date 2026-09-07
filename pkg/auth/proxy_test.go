package auth

import (
	"net/http/httptest"
	"testing"
)

func TestNewProxyAuthOffOnlyWhenBothEmpty(t *testing.T) {
	p, err := NewProxyAuth("", nil)
	if err != nil || p != nil {
		t.Fatalf("both empty must mean off: %v %v", p, err)
	}
	if _, ok := p.Identify(httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)); ok {
		t.Fatal("a nil ProxyAuth must never identify anyone")
	}
}

// Anything that looks configured but could not enforce what it promises is an
// error, not a quietly disabled feature.
func TestNewProxyAuthRejectsHalfConfiguredAndUnenforceable(t *testing.T) {
	for name, tc := range map[string]struct {
		header  string
		proxies []string
	}{
		"no header":         {"", []string{"172.18.0.0/16"}},
		"no proxies":        {"Remote-User", nil},
		"blank entry":       {"Remote-User", []string{"172.18.0.0/16", " "}},
		"only blank":        {"Remote-User", []string{"  "}},
		"garbage entry":     {"Remote-User", []string{"172.18.0.0/16", "not-an-address"}},
		"catch-all v4":      {"Remote-User", []string{"0.0.0.0/0"}},
		"catch-all v6":      {"Remote-User", []string{"::/0"}},
		"header with space": {"Remote User", []string{"172.18.0.0/16"}},
		"header with colon": {"Remote-User:", []string{"172.18.0.0/16"}},
	} {
		if _, err := NewProxyAuth(tc.header, tc.proxies); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestProxyAuthIdentify(t *testing.T) {
	p, err := NewProxyAuth("Remote-User", []string{"172.18.0.0/16", "10.0.0.5", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		remote string
		header string
		want   string
		ok     bool
	}{
		{"trusted cidr with header", "172.18.0.7:41000", "maged", "maged", true},
		{"trusted single host", "10.0.0.5:9", "alice", "alice", true},
		{"trusted v6", "[fd00::1]:443", "bob", "bob", true},
		{"trusted but no header", "172.18.0.7:41000", "", "", false},
		{"trusted but blank header", "172.18.0.7:41000", "   ", "", false},
		{"untrusted address with header", "203.0.113.9:5555", "maged", "", false},
		{"neighbouring host of the single entry", "10.0.0.6:9", "maged", "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/auth/check", nil)
		r.RemoteAddr = tc.remote
		if tc.header != "" {
			r.Header.Set("Remote-User", tc.header)
		}
		got, ok := p.Identify(r)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// A forwarded header must not stand in for the address check: ClientIP reads
// the socket peer, never X-Forwarded-For, so an outsider claiming to be the
// proxy is still an outsider.
func TestProxyAuthIgnoresForwardedFor(t *testing.T) {
	p, _ := NewProxyAuth("Remote-User", []string{"172.18.0.0/16"})
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "172.18.0.7")
	r.Header.Set("Remote-User", "maged")
	if _, ok := p.Identify(r); ok {
		t.Fatal("X-Forwarded-For must not make an outside address trusted")
	}
}
