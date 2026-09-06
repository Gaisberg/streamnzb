package api

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
)

// proxyAuthCache holds the parsed trusted-proxy settings so the CIDR list is
// not re-parsed on every request. It is keyed on the raw config values: a
// reload that changes them rebuilds it on the next request, one that leaves
// them alone reuses it.
type proxyAuthCache struct {
	mu   sync.Mutex
	key  string
	auth *auth.ProxyAuth
}

func proxyAuthKey(header string, proxies []string) string {
	return header + "\x00" + strings.Join(proxies, "\x00")
}

// proxyAuth returns the current trusted-proxy authenticator, or nil when the
// feature is off or misconfigured. A bad entry is logged once per distinct
// config and disables the feature rather than trusting part of the list —
// validation rejects such a save, so this only guards config edited by hand.
func (s *Server) proxyAuth() *auth.ProxyAuth {
	s.mu.RLock()
	header, proxies := s.config.TrustedProxyAuthHeader, s.config.TrustedProxies
	s.mu.RUnlock()
	if strings.TrimSpace(header) == "" && len(proxies) == 0 {
		return nil
	}
	key := proxyAuthKey(header, proxies)

	c := &s.proxyAuthState
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == key {
		return c.auth
	}
	pa, err := auth.NewProxyAuth(header, proxies)
	if err != nil {
		// Logged once per distinct config, then the feature stays off until
		// the values change. Validation refuses a save that would land here,
		// so this is reached only by values set outside the dashboard.
		logger.Warn("Trusted-proxy auth is off: settings are incomplete or invalid", "err", err)
		pa = nil
	}
	if pa != nil {
		// Said once per distinct config so the operator can see exactly which
		// addresses are trusted. A Docker bridge gateway or 127.0.0.1 in this
		// list means "everyone who can reach the published port", which is the
		// one misconfiguration the code cannot detect from inside.
		logger.Info("Trusted-proxy auth enabled", "header", header, "trusted_proxies", strings.Join(proxies, ", "))
	}
	c.key, c.auth = key, pa
	return pa
}

// proxyAuthMiddleware settles the identity of a request the reverse proxy
// vouches for before the cookie and bearer checks run. It never rejects: a
// request the proxy does not vouch for is passed on untouched, so the usual
// login still applies to it.
//
// The identity granted is the admin's. The proxy's login is the one gate the
// operator chose to put in front of the dashboard; whoever it let through is
// whoever it was configured to let through. The name the proxy sent is kept
// for the log line so the audit trail still says who.
func (s *Server) proxyAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.VouchedFromContext(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		// A caller that presents its own credential is asking to be that
		// credential. A device token or bearer must not be upgraded to the
		// admin just because the request also passed through the proxy.
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("token") != "" {
			next.ServeHTTP(w, r)
			return
		}
		pa := s.proxyAuth()
		if pa == nil {
			next.ServeHTTP(w, r)
			return
		}
		user, ok := pa.Identify(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if !proxyRequestIsSameSite(r) {
			// A state-changing request the browser itself marks as coming from
			// another site. Proxy identity rides on the proxy's cookie, whose
			// SameSite policy is not ours to set, so this layer does its own
			// check; the request falls through to the cookie path and is
			// refused there.
			// Warn, not Debug: when a proxy rewrites Host and does not forward
			// the original, every save fails this way, and the dashboard's
			// only symptom is a login screen on save. The log should say so.
			// A hostile page can drive cross-site posts through a logged-in
			// browser at will, each carrying an origin of its choosing, so
			// the Warn is rate-limited and the rest go to Debug.
			logCrossSiteRefusal(r)
			next.ServeHTTP(w, r)
			return
		}
		token := s.adminToken()
		stream, err := s.streamManager.AuthenticateToken(token, s.adminUsername(), token)
		if err != nil {
			// No admin token yet (first start before the config was written).
			// Fall through; the request will be asked to log in like any other.
			next.ServeHTTP(w, r)
			return
		}
		logger.Debug("Auth via trusted proxy", "proxy_user", user, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(auth.ContextWithVouchedStream(r.Context(), stream)))
	})
}

// proxyRequestIsSameSite is the CSRF check for proxy-vouched requests. The
// dashboard's own session rides a SameSite=Strict cookie, which the browser
// refuses to attach to cross-site requests; a header identity from the proxy
// has no such property, so the same guarantee is rebuilt from what the browser
// says about the request. Safe methods always pass. For the rest, Sec-Fetch-Site
// must say same-origin or none (a direct navigation), and an Origin, when
// present, must match the host. A request that carries neither header is not
// a browser's cross-site request — browsers always send Sec-Fetch-Site — and
// is allowed, so scripts behind the proxy keep working.
func proxyRequestIsSameSite(r *http.Request) bool {
	// A WebSocket handshake is a GET, but the socket it opens is readable by
	// the page that opened it — WebSockets are not subject to CORS — and this
	// one streams stats and log history the moment it connects. It gets the
	// full check, not the safe-method exemption.
	if !isWebSocketHandshake(r) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return true
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		// "null" is an opaque origin — a sandboxed frame or a redirect chain
		// through another site — and older engines send it without any
		// Sec-Fetch-Site to disambiguate. It is never our own page.
		if origin == "null" {
			return false
		}
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, requestHost(r)) {
			return false
		}
	}
	return true
}

// isWebSocketHandshake reports whether the request asks to upgrade to a
// WebSocket, which is the one GET whose response a cross-site page can read.
func isWebSocketHandshake(r *http.Request) bool {
	return headerHasToken(r.Header.Get("Upgrade"), "websocket") && headerHasToken(r.Header.Get("Connection"), "upgrade")
}

// headerHasToken reports whether a comma-separated header value lists the
// token, case-insensitively. Both Upgrade and Connection are token lists
// ("websocket, h2c"; "keep-alive, Upgrade"), so neither side is compared as
// a whole string.
func headerHasToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

// requestHost is the host the browser addressed, as far as it can be known.
// This runs only for requests the trusted proxy vouched for. X-Forwarded-Host
// is preferred because it is the browser's view even when the proxy rewrote
// Host on the way in — but only a proxy that SETS it (overwriting whatever
// the client sent) makes it trustworthy; Traefik and Caddy do, nginx only
// with an explicit proxy_set_header, and the docs say so. A proxy that sets
// neither leaves r.Host, which then must equal what the browser sees.
func requestHost(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); h != "" {
		// Comma-joined when several proxies appended; the first is the
		// client-facing one.
		if i := strings.IndexByte(h, ','); i >= 0 {
			h = strings.TrimSpace(h[:i])
		}
		return h
	}
	return r.Host
}

// crossSiteWarnEvery bounds how often a refusal is logged at Warn; refusals in
// between are logged at Debug so a flood from one hostile page cannot fill
// the log or the dashboard's log-history buffer.
const crossSiteWarnEvery = 30 * time.Second

var lastCrossSiteWarn atomic.Int64

func logCrossSiteRefusal(r *http.Request) {
	fields := []any{
		"method", r.Method, "path", r.URL.Path,
		"origin", r.Header.Get("Origin"), "host", requestHost(r), "sec_fetch_site", r.Header.Get("Sec-Fetch-Site"),
	}
	now := time.Now().UnixNano()
	last := lastCrossSiteWarn.Load()
	if now-last >= int64(crossSiteWarnEvery) && lastCrossSiteWarn.CompareAndSwap(last, now) {
		logger.Warn("Trusted-proxy identity withheld: request did not look same-site (further refusals at Debug for 30s)", fields...)
		return
	}
	logger.Debug("Trusted-proxy identity withheld: request did not look same-site", fields...)
}
