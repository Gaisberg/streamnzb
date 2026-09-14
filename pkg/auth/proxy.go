package auth

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	"streamnzb/pkg/core/httpx"
)

// ProxyConfigError says which of the two settings a rejected configuration
// fails on, so a settings page can point at the right field without parsing
// the message.
type ProxyConfigError struct {
	Field string // "trusted_proxy_auth_header" or "trusted_proxies"
	Msg   string
}

func (e *ProxyConfigError) Error() string { return e.Msg }

func headerErr(format string, a ...any) error {
	return &ProxyConfigError{Field: "trusted_proxy_auth_header", Msg: fmt.Sprintf(format, a...)}
}

func proxiesErr(format string, a ...any) error {
	return &ProxyConfigError{Field: "trusted_proxies", Msg: fmt.Sprintf(format, a...)}
}

// headerTokenRE is an RFC 9110 field-name token. A header name outside it can
// never be sent by a proxy, so accepting one would configure a gate that no
// request can pass — the feature looks on and enforces nothing.
var headerTokenRE = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// ProxyAuth accepts an identity asserted by a reverse proxy that has already
// authenticated the person — Authelia, Authentik, oauth2-proxy and the like
// forward the login name in a header such as Remote-User once their own login
// has passed. A request is trusted only when both halves hold: it arrived
// from an address inside one of the trusted networks, and it carries the
// header with a non-empty value. Anything else falls through to the usual
// cookie and bearer checks, so a proxy that is down or bypassed leaves the
// dashboard exactly as locked as it was.
//
// The address check is what makes the header safe. A client that reaches the
// listener directly can write any header it likes, so the header alone proves
// nothing; the proxy's address is the part an outsider cannot forge, and the
// proxy overwrites the header on every request it forwards.
type ProxyAuth struct {
	header string
	nets   []*net.IPNet
}

// NewProxyAuth parses the trusted networks. Each entry is a CIDR ("172.18.0.0/16",
// "fd00::/8") or a bare address, which stands for that single host. With both
// the header and the list empty the feature is off and nil, nil is returned.
// Anything else that cannot enforce what it appears to promise — one half of
// the pair missing, a blank entry, a header name no proxy could send, a
// catch-all network — is an error, never a silently narrower or wider gate.
func NewProxyAuth(header string, proxies []string) (*ProxyAuth, error) {
	header = strings.TrimSpace(header)
	if header == "" && len(proxies) == 0 {
		return nil, nil
	}
	if header == "" {
		return nil, headerErr("trusted_proxy_auth_header is empty: set the header the proxy sends (for example Remote-User), or clear trusted_proxies")
	}
	if !headerTokenRE.MatchString(header) {
		return nil, headerErr("trusted_proxy_auth_header %q is not a valid header name", header)
	}
	if len(proxies) == 0 {
		return nil, proxiesErr("trusted_proxies is empty: list the proxy's address or network, or clear trusted_proxy_auth_header")
	}
	nets, err := parseTrustedProxies(proxies)
	if err != nil {
		return nil, err
	}
	return &ProxyAuth{header: header, nets: nets}, nil
}

// parseTrustedProxies turns the configured entries into networks, reporting a
// rejected one as a ProxyConfigError so the settings page can point at the
// trusted_proxies field. The parsing rules — and why a blank entry or a
// catch-all is an error rather than a silently wider gate — live with the
// shared parser.
func parseTrustedProxies(proxies []string) ([]*net.IPNet, error) {
	nets, err := httpx.ParseNetworks(proxies, "trusted_proxies")
	if err != nil {
		return nil, proxiesErr("%s", err)
	}
	return nets, nil
}

// Header is the request header the proxy is expected to fill in.
func (p *ProxyAuth) Header() string {
	if p == nil {
		return ""
	}
	return p.header
}

// Identify reports the user the proxy vouches for. It returns ok=false when
// the request did not come from a trusted address or carries no identity; the
// caller then treats the request as anonymous, never as an error.
func (p *ProxyAuth) Identify(r *http.Request) (user string, ok bool) {
	if p == nil || r == nil {
		return "", false
	}
	ip := net.ParseIP(httpx.ClientIP(r))
	if ip == nil || !p.trusted(ip) {
		return "", false
	}
	user = strings.TrimSpace(r.Header.Get(p.header))
	if user == "" {
		return "", false
	}
	return user, true
}

func (p *ProxyAuth) trusted(ip net.IP) bool {
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
