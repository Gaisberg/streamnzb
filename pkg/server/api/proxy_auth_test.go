package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"streamnzb/pkg/auth"
	"strings"
	"testing"
)

// adminWrite is the discriminator every write-path test uses: PUT /api/config
// with a body that passes the admin gate and then fails validation. Its
// answer is the auth outcome and nothing else — 400 for the admin (past the
// gate, refused by the catch-all rule this PR added), 403 for a non-admin
// stream, 401 for no identity — and nothing is ever saved, so no reload
// goroutine runs against the fixture's nil session manager. (/api/cache/clear
// is not usable: without a streaming server it answers 503 after the gate,
// which hides whether the gate was passed; a valid body is not usable either:
// the save succeeds and the async reload panics the test binary.)
func adminWrite(t *testing.T, s *Server, shape func(*http.Request)) int {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/config", strings.NewReader(`{"trusted_proxies": ["0.0.0.0/0"]}`))
	req.Host = "nzb.example.com"
	req.Header.Set("Content-Type", "application/json")
	shape(req)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code
}

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
	code, body, _ := authCheckFull(t, s, remote, user)
	return code, body
}

func authCheckFull(t *testing.T, s *Server, remote, user string) (int, map[string]any, http.Header) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/check", nil)
	req.RemoteAddr = remote
	if user != "" {
		req.Header.Set("Remote-User", user)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, rec.Header()
}

// The identity answer must never be served from a browser cache: it names a
// person and can change the moment the proxy or the cookie says so.
func TestAuthCheckIsNeverCached(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	for _, remote := range []string{"172.18.0.7:40000", "203.0.113.9:5555"} {
		if _, _, h := authCheckFull(t, s, remote, "maged"); h.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: expected Cache-Control: no-store, got %q", remote, h.Get("Cache-Control"))
		}
	}
}

// A request the proxy vouches for is the admin: the dashboard never shows the
// login screen, and admin-only endpoints open without a cookie.
func TestTrustedProxyAuthGrantsAdmin(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})

	code, body := authCheck(t, s, "172.18.0.7:40000", "maged")
	if code != http.StatusOK || body["authenticated"] != true || body["username"] != "admin" {
		t.Fatalf("expected admin via proxy, got %d %v", code, body)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "172.18.0.7:40000"
	req.Header.Set("Remote-User", "maged")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin endpoint should open for a proxy-vouched request, got %d", rec.Code)
	}
	if code := adminWrite(t, s, func(r *http.Request) {
		r.RemoteAddr = "172.18.0.7:40000"
		r.Header.Set("Remote-User", "maged")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Origin", "https://nzb.example.com")
	}); code != http.StatusBadRequest {
		t.Fatalf("admin write should reach validation (400) for a proxy-vouched same-site request, got %d", code)
	}
}

// A caller presenting its own credential is that credential. A device
// token or bearer on a request that also came through the proxy must not be
// lifted to admin.
func TestTrustedProxyAuthDoesNotOverrideExplicitCredential(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	for name, apply := range map[string]func(*http.Request){
		"query token": func(r *http.Request) {
			q := r.URL.Query()
			q.Set("token", "not-a-real-token")
			r.URL.RawQuery = q.Encode()
		},
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-a-real-token") },
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/config", nil)
		req.RemoteAddr = "172.18.0.7:40000"
		req.Header.Set("Remote-User", "maged")
		apply(req)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: an explicit bad credential must be judged on its own, got %d", name, rec.Code)
		}
	}
}

// A stream that the Stremio path-token router placed in the context is not
// a proxy voucher: the API's credential checks still apply to it.
func TestPathTokenStreamIsNotVouched(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req = req.WithContext(auth.ContextWithStream(req.Context(), &auth.Stream{Username: "tv", Token: "device-token"}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a bare context stream must not pass the credential middleware, got %d", rec.Code)
	}
}

// The third credential form: a Stremio client behind the proxy reaches the
// API as /<stream-token>/api/..., which the path-token router turns into a
// device stream in the context with nothing left in the URL or headers. The
// proxy layer must leave that stream alone. Discriminated on the admin gate:
// if the device were overwritten with the admin, this write would answer 200
// and the device token would be writing global config; left as the device,
// the unvouched stream goes through the credential checks and, with no
// cookie, gets 401. This fails at eeac0ea and passes with the fix.
func TestProxyDoesNotOverwritePathTokenStream(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	code := adminWrite(t, s, func(r *http.Request) {
		r.RemoteAddr = "172.18.0.7:40000"
		r.Header.Set("Remote-User", "maged")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Origin", "https://nzb.example.com")
		// What the Stremio front handler hands the API mux: the device
		// stream, vouched by nobody, and the token stripped from the path.
		*r = *r.WithContext(auth.ContextWithStream(r.Context(), &auth.Stream{Username: "tv", Token: "device-token"}))
	})
	if code == http.StatusBadRequest {
		t.Fatalf("a device stream behind the proxy was elevated to admin: the device token reached config validation")
	}
	if code != http.StatusUnauthorized {
		t.Fatalf("expected the device stream to be judged on its own (401 without a cookie), got %d", code)
	}
}

// The non-admin config view must not reveal which header and which
// network the proxy identity trusts.
func TestRedactForAPIHidesTrustedProxySettings(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	out := s.config.RedactForAPI()
	if out.TrustedProxyAuthHeader != "" || len(out.TrustedProxies) != 0 {
		t.Fatalf("trusted-proxy settings leaked through RedactForAPI: %q %v", out.TrustedProxyAuthHeader, out.TrustedProxies)
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

// Validation runs only when the pair is being edited: a half-configured pair
// that came from the environment must not block an unrelated save.
func TestValidateConfigTrustedProxies(t *testing.T) {
	s := proxyAuthTestServer(t, "", nil)
	editing := configValidationPlan{validateTrustedProxyAuth: true}

	cfg := *s.config
	cfg.TrustedProxyAuthHeader = "Remote-User"
	cfg.TrustedProxies = []string{"not-a-network"}
	if errs := s.validateConfigWithPlan(&cfg, editing); errs["trusted_proxies"] == "" {
		t.Fatalf("expected a trusted_proxies error, got %v", errs)
	}
	if errs := s.validateConfigWithPlan(&cfg, configValidationPlan{}); len(errs) != 0 {
		t.Fatalf("an untouched pair must not block an unrelated save, got %v", errs)
	}

	for name, proxies := range map[string][]string{
		"blank entry": {"172.18.0.0/16", " "},
		"catch-all":   {"0.0.0.0/0"},
	} {
		cfg.TrustedProxies = proxies
		if errs := s.validateConfigWithPlan(&cfg, editing); errs["trusted_proxies"] == "" {
			t.Fatalf("%s: expected a trusted_proxies error, got %v", name, errs)
		}
	}

	cfg.TrustedProxies = []string{"172.18.0.0/16"}
	cfg.TrustedProxyAuthHeader = ""
	if errs := s.validateConfigWithPlan(&cfg, editing); errs["trusted_proxy_auth_header"] == "" {
		t.Fatalf("expected a trusted_proxy_auth_header error, got %v", errs)
	}

	cfg.TrustedProxyAuthHeader = "Remote-User"
	if errs := s.validateConfigWithPlan(&cfg, editing); len(errs) != 0 {
		t.Fatalf("expected a clean validation, got %v", errs)
	}
}

// A half-configured pair set outside the dashboard leaves the feature off.
func TestTrustedProxyAuthHalfConfiguredIsOff(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", nil)
	if code, _ := authCheck(t, s, "172.18.0.7:40000", "maged"); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with only the header set, got %d", code)
	}
}

// The proxy vouches for a person, not for possession of the admin token. The
// check endpoint must not hand that token out, or a proxy logout would no
// longer revoke anything.
func TestTrustedProxyAuthDoesNotLeakAdminToken(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	code, body := authCheck(t, s, "172.18.0.7:40000", "maged")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if _, leaked := body["token"]; leaked {
		t.Fatalf("admin token must not be returned to a proxy-vouched request: %v", body)
	}

	// The bearer path still echoes the token it was given.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/check", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out["token"] != "admin-token" {
		t.Fatalf("bearer caller should still get its token back: %d %v", rec.Code, out)
	}
}

// Proxy identity must not become a CSRF hole: a state-changing request the
// browser marks as cross-site gets no identity and is refused.
func TestTrustedProxyAuthRefusesCrossSiteWrites(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	post := func(site, origin string) int {
		return adminWrite(t, s, func(r *http.Request) {
			r.RemoteAddr = "172.18.0.7:40000"
			r.Header.Set("Remote-User", "maged")
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
			}
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		})
	}
	// Refused: no identity is granted, and with no cookie that is 401.
	for name, tc := range map[string][2]string{
		"cross-site":            {"cross-site", "https://evil.example"},
		"mismatched origin":     {"same-origin", "https://evil.example"},
		"origin null, no site":  {"", "null"},
		"same-site (subdomain)": {"same-site", "https://other.nzb.example.com"},
	} {
		if code := post(tc[0], tc[1]); code != http.StatusUnauthorized {
			t.Fatalf("%s: must be refused with 401, got %d", name, code)
		}
	}
	// Granted: the proxy identity is the admin, the write passes the gate and
	// is refused by validation (400) — past the gate is the whole claim.
	if code := post("same-origin", "https://nzb.example.com"); code != http.StatusBadRequest {
		t.Fatalf("same-origin POST must reach validation as admin (400), got %d", code)
	}
	if code := post("", ""); code != http.StatusBadRequest {
		t.Fatalf("a non-browser write without fetch metadata must keep working as admin (400), got %d", code)
	}
	// Safe methods are never subject to the check.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/auth/check", nil)
	req.RemoteAddr = "172.18.0.7:40000"
	req.Header.Set("Remote-User", "maged")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET is safe and must still be vouched for, got %d", rec.Code)
	}
}

// A WebSocket handshake is a GET, but the socket it opens is readable
// cross-site and streams stats and log history on connect. A cross-site
// handshake must get no proxy identity, so /api/ws refuses it; a same-site
// one is vouched for as before.
func TestTrustedProxyAuthRefusesCrossSiteWebSocket(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	handshake := func(site, origin string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/ws", nil)
		req.Host = "nzb.example.com"
		req.RemoteAddr = "172.18.0.7:40000"
		req.Header.Set("Remote-User", "maged")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := handshake("cross-site", "https://evil.example"); code != http.StatusUnauthorized {
		t.Fatalf("cross-site WebSocket handshake must be refused, got %d", code)
	}
	if code := handshake("", "https://evil.example"); code != http.StatusUnauthorized {
		t.Fatalf("WebSocket handshake with a foreign Origin must be refused, got %d", code)
	}
	// Same-site: identity granted, so the request reaches the upgrader. The
	// recorder cannot be hijacked, so the upgrade itself fails with 400 —
	// which is past the auth gate; 401 or 403 would mean it was not.
	if code := handshake("same-origin", "https://nzb.example.com"); code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("same-site WebSocket handshake must be vouched for, got %d", code)
	}
}

func TestIsWebSocketHandshake(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/ws", nil)
	if isWebSocketHandshake(r) {
		t.Fatal("plain GET is not a handshake")
	}
	r.Header.Set("Connection", "keep-alive, Upgrade")
	r.Header.Set("Upgrade", "WebSocket")
	if !isWebSocketHandshake(r) {
		t.Fatal("Connection: keep-alive, Upgrade + Upgrade: WebSocket is a handshake")
	}
	// Upgrade is a token list too.
	r.Header.Set("Upgrade", "h2c, websocket")
	if !isWebSocketHandshake(r) {
		t.Fatal("Upgrade: h2c, websocket is a handshake")
	}
	r.Header.Set("Upgrade", "h2c")
	if isWebSocketHandshake(r) {
		t.Fatal("Upgrade: h2c alone is not a WebSocket handshake")
	}
}

// A proxy that rewrites Host but forwards the original in X-Forwarded-Host
// must not turn every save into a 401: the Origin is compared against what
// the browser addressed, which for a proxy-vouched request the proxy is
// trusted to report.
func TestTrustedProxyAuthHonoursForwardedHost(t *testing.T) {
	s := proxyAuthTestServer(t, "Remote-User", []string{"172.18.0.0/16"})
	post := func(forwardedHost string) int {
		return adminWrite(t, s, func(r *http.Request) {
			r.Host = "streamnzb:7000" // what the upstream sees after the proxy rewrote Host
			r.RemoteAddr = "172.18.0.7:40000"
			r.Header.Set("Remote-User", "maged")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", "https://nzb.example.com")
			if forwardedHost != "" {
				r.Header.Set("X-Forwarded-Host", forwardedHost)
			}
		})
	}
	if code := post("nzb.example.com"); code != http.StatusBadRequest {
		t.Fatalf("forwarded host matching the Origin must reach validation as admin (400), got %d", code)
	}
	if code := post("nzb.example.com, inner.proxy"); code != http.StatusBadRequest {
		t.Fatalf("first entry of a chained X-Forwarded-Host must be used (400), got %d", code)
	}
	if code := post("nzb.example.com:443"); code != http.StatusUnauthorized {
		t.Fatalf("forwarded host with a port the Origin does not carry must not match, got %d", code)
	}
	if code := post("other.example.com"); code != http.StatusUnauthorized {
		t.Fatalf("forwarded host that does not match the Origin must be refused, got %d", code)
	}
	if code := post(""); code != http.StatusUnauthorized {
		t.Fatalf("rewritten Host with nothing forwarded cannot match the Origin, got %d", code)
	}
}
