package api

import (
	"net/http"
	"strings"
	"sync"

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
	if strings.TrimSpace(header) == "" || len(proxies) == 0 {
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
		logger.Warn("Trusted-proxy auth disabled: invalid trusted_proxies", "err", err)
		pa = nil
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
		if _, ok := auth.StreamFromContext(r); ok {
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
		token := s.adminToken()
		stream, err := s.streamManager.AuthenticateToken(token, s.adminUsername(), token)
		if err != nil {
			// No admin token yet (first start before the config was written).
			// Fall through; the request will be asked to log in like any other.
			next.ServeHTTP(w, r)
			return
		}
		logger.Debug("Auth via trusted proxy", "proxy_user", user, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(auth.ContextWithStream(r.Context(), stream)))
	})
}
