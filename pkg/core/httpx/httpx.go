// Package httpx holds the small request-inspection helpers that both server
// packages need. They live down here rather than in pkg/server because
// pkg/server/api and pkg/server/stremio are siblings and cannot import each
// other, and pkg/auth — which builds the session cookie — sits below both.
package httpx

import (
	"net"
	"net/http"
	"strings"
)

// HostFromAddr strips the port from a "host:port" address, returning addr
// unchanged when it carries no port. IPv6 literals lose their brackets.
func HostFromAddr(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		return host
	}
	return addr
}

// ClientIP is the address the connection actually came from.
//
// Forwarded headers are deliberately ignored. Anything that keys off client
// identity — rate limiting above all — must not accept a value the client sets
// itself, or the limit is only ever applied to attackers who chose not to
// evade it. Behind a reverse proxy this collapses every client onto the proxy's
// address; that is the honest answer, and callers that cannot live with it need
// a trusted-proxy configuration rather than a spoofable header.
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	return HostFromAddr(r.RemoteAddr)
}

// IsSecure reports whether the client reached this request over HTTPS,
// including via a TLS-terminating proxy.
//
// Unlike ClientIP, X-Forwarded-Proto is trusted here, and the asymmetry is
// deliberate. A forged header only ever describes the attacker's own request,
// and the only thing it can change is whether their own cookie is marked
// Secure. Claiming "https" makes their browser refuse to send that cookie back
// over plain HTTP — a self-inflicted lockout, not an escalation. A forged
// "http" cannot reach a victim's request at all, because only the proxy in
// front of them writes that header.
func IsSecure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	// Proxies chain the header, oldest first; the first entry is the scheme the
	// original client used.
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		return false
	}
	if comma := strings.IndexByte(proto, ','); comma >= 0 {
		proto = proto[:comma]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
