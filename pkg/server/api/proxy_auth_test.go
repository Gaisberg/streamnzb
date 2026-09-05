package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func proxyAuthTestServer(t *testing.T, header string, proxies []string) *Server {
	t.Helper()
	s := newStreamsTestServer(t)
	s.mu.Lock()
	s.config.AdminUsername = "admin"
	s.config.AdminToken = "admin-token"
	s.config.TrustedProxyAuthHeader = header
	s.config.TrustedProxies = proxies
	s.mu.Unlock()
	return s
}

func authCheck(t *testing.T, s *Server, remote, user string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
	req.RemoteAddr = remote
	if user != "" {
		req.Header.Set("Remote-User", user)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// A request the proxy vouches for is the admin: the dashboard never shows the
// login screen, and admin-only endpoints open without a cookie.
func TestTrustedProxyAuthGrantsAdmin(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})

	code, body := authCheck(t, s, "172.18.0.7:40000", "maged")
	if code != http.StatusOK || body["authenticated"] != true || body["username"] != "admin" {
		t.Fatalf("expected admin via proxy, got %d %v", code, body)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "172.18.0.7:40000"
	req.Header.Set("Remote-User", "maged")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("admin endpoint should open for a proxy-vouched request, got 401")
	}
}

// Without the proxy's address the header is just a header: the request is
// anonymous and gets the login screen, exactly as before the feature existed.
func TestTrustedProxyAuthIgnoresUntrustedAddress(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	if code, _ := authCheck(t, s, "203.0.113.9:5555", "maged"); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from an untrusted address, got %d", code)
	}
	if code, _ := authCheck(t, s, "172.18.0.7:40000", ""); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when the proxy sent no identity, got %d", code)
	}
}

// Feature off (default config): nothing changes for anyone.
func TestTrustedProxyAuthOffByDefault(t *testing.T) {
	s := proxyAuthTestServer(t, "", nil)
	if code, _ := authCheck(t, s, "172.18.0.7:40000", "maged"); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with the feature off, got %d", code)
	}
}

// A config edit that would leave the feature half-configured or unparsable is
// refused at save time.
func TestValidateConfigTrustedProxies(t *testing.T) {
	s := proxyAuthTestServer(t, "", nil)

	cfg := *s.config
	cfg.TrustedProxyAuthHeader = "Remote-User"
	cfg.TrustedProxies = []string{"not-a-network"}
	if errs := s.validateConfigWithPlan(&cfg, configValidationPlan{}); errs["trusted_proxies"] == "" {
		t.Fatalf("expected a trusted_proxies error, got %v", errs)
	}

	cfg.TrustedProxies = []string{"172.18.0.0/16"}
	cfg.TrustedProxyAuthHeader = ""
	if errs := s.validateConfigWithPlan(&cfg, configValidationPlan{}); errs["trusted_proxy_auth_header"] == "" {
		t.Fatalf("expected a trusted_proxy_auth_header error, got %v", errs)
	}

	cfg.TrustedProxyAuthHeader = "Remote-User"
	if errs := s.validateConfigWithPlan(&cfg, configValidationPlan{}); len(errs) != 0 {
		t.Fatalf("expected a clean validation, got %v", errs)
	}
}
