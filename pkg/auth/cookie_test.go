package auth

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestSessionCookieMarksSecureOnlyOverHTTPS(t *testing.T) {
	cases := []struct {
		name       string
		request    *http.Request
		wantSecure bool
	}{
		{
			name:       "plain http on a LAN",
			request:    &http.Request{Header: http.Header{}},
			wantSecure: false,
		},
		{
			name:       "direct TLS",
			request:    &http.Request{Header: http.Header{}, TLS: &tls.ConnectionState{}},
			wantSecure: true,
		},
		{
			name:       "behind a TLS-terminating proxy",
			request:    &http.Request{Header: http.Header{"X-Forwarded-Proto": []string{"https"}}},
			wantSecure: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cookie := SessionCookie(tc.request, "token-value", SessionCookieMaxAge)
			if cookie.Secure != tc.wantSecure {
				t.Fatalf("Secure = %v, want %v", cookie.Secure, tc.wantSecure)
			}
			if !cookie.HttpOnly {
				t.Fatal("the session cookie must stay HttpOnly")
			}
			if cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("SameSite = %v, want Strict", cookie.SameSite)
			}
			if cookie.Name != SessionCookieName || cookie.Value != "token-value" {
				t.Fatalf("unexpected cookie %+v", cookie)
			}
		})
	}
}

func TestClearSessionCookieExpiresAndKeepsAttributes(t *testing.T) {
	r := &http.Request{Header: http.Header{"X-Forwarded-Proto": []string{"https"}}}
	cookie := ClearSessionCookie(r)

	if cookie.MaxAge != -1 || cookie.Value != "" {
		t.Fatalf("expected an expiring empty cookie, got %+v", cookie)
	}
	// The clearing cookie carries the same attributes as the one it replaces,
	// so a browser cannot be left holding a stale credential that only differed
	// by a flag.
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("clearing cookie lost its attributes: %+v", cookie)
	}
}
