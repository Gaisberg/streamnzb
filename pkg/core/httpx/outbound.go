package httpx

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ParseNetworks turns CIDR ("172.18.0.0/16", "fd00::/8") or bare-address
// entries into networks, a bare address standing for that single host.
// subject names the setting in the error messages so a settings page can
// repeat them verbatim.
//
// Every entry must parse: a blank one, a typo, or a catch-all such as
// 0.0.0.0/0 is an error, because each of those would leave a config that
// looks valid and enforces something other than what it says.
func ParseNetworks(entries []string, subject string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, fmt.Errorf("%s has a blank entry", subject)
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			if ones, _ := n.Mask.Size(); ones == 0 {
				return nil, fmt.Errorf("%s %q covers every address; list the specific network only", subject, entry)
			}
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("%s %q is neither a CIDR nor an IP address", subject, entry)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets, nil
}

// extraBlockedNets are non-public ranges Go's own IP predicates do not report.
//
//   - 100.64.0.0/10 is carrier-grade NAT, which is also the range Tailscale
//     hands out — on a self-hosted box that is very much an internal address.
//   - 0.0.0.0/8 and 198.18.0.0/15 are "this network" and the benchmarking
//     range; neither is somewhere a public catalog source can live.
//   - 64:ff9b::/96 (NAT64) and 2002::/16 (6to4) embed an IPv4 address in an
//     IPv6 one. Go's To4() unwraps ::ffff:a.b.c.d but not these, so on a host
//     behind DNS64/NAT64 a name resolving to 64:ff9b::a9fe:a9fe would reach
//     169.254.169.254 while every predicate above reports it as public.
//   - fec0::/10 is the deprecated site-local range, which IsPrivate (RFC 4193
//     only) does not cover.
var extraBlockedNets = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range []string{
		"100.64.0.0/10", "0.0.0.0/8", "198.18.0.0/15",
		"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "fec0::/10",
	} {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// IsPublicIP reports whether ip is an address a fetch of an operator-supplied
// URL may legitimately reach — that is, not loopback, private, link-local
// (including the 169.254.169.254 cloud metadata endpoint), multicast,
// unspecified, or one of extraBlockedNets.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, n := range extraBlockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// privateDialGuard builds a net.Dialer.Control hook that rejects a connection
// to a non-public address unless it falls inside one of allowPrivate.
//
// Control runs after DNS resolution but before the connection is used, on the
// actual resolved address — so unlike a pre-resolve check on the hostname, a
// DNS answer that changes between validation and dial, or a redirect pointing
// straight at an internal host, cannot slip through.
func privateDialGuard(allowPrivate []*net.IPNet) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("could not parse resolved address %q", host)
		}
		if IsPublicIP(ip) {
			return nil
		}
		for _, n := range allowPrivate {
			if n.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("refusing to connect to non-public address %s; add its network to the allowed list to reach a self-hosted source", ip)
	}
}

// guardedTransports caches one transport per distinct allowlist. A fresh
// *http.Transport per call would mean a fresh, un-pooled TCP+TLS handshake for
// every request instead of reusing keep-alive connections — one manifest
// inspection alone can issue a hundred of them.
//
// Entries are never evicted, which is deliberate rather than an oversight: a
// key is (timeout, allowlist), both of which only change when an operator
// edits the configuration, so the map holds one entry per allowlist that
// install has ever run with. Evicting would risk dropping a transport with
// requests still in flight to save a handful of idle connections that the
// cloned transport's own IdleConnTimeout already reaps.
var guardedTransports = struct {
	sync.Mutex
	byKey map[string]*http.Transport
}{byKey: make(map[string]*http.Transport)}

func guardedTransport(timeout time.Duration, allowPrivate []*net.IPNet) *http.Transport {
	keys := make([]string, 0, len(allowPrivate))
	for _, n := range allowPrivate {
		keys = append(keys, n.String())
	}
	sort.Strings(keys)
	key := fmt.Sprintf("%s|%s", timeout, strings.Join(keys, ","))

	guardedTransports.Lock()
	defer guardedTransports.Unlock()
	if t, ok := guardedTransports.byKey[key]; ok {
		return t
	}
	// Cloning http.DefaultTransport keeps its other defaults (HTTP/2, idle
	// connection and TLS handshake timeouts) and only swaps in the guarded
	// dialer.
	t := http.DefaultTransport.(*http.Transport).Clone()
	// Proxy support is deliberately dropped. With one configured, every dial
	// would go to the proxy's address instead of the target's, so the guard
	// below would be validating the proxy and the real destination would go
	// unchecked — a silent hole in exactly the check this transport exists for.
	t.Proxy = nil
	t.DialContext = (&net.Dialer{Timeout: timeout, Control: privateDialGuard(allowPrivate)}).DialContext
	guardedTransports.byKey[key] = t
	return t
}

// GuardedClient returns a client for fetching an operator-supplied URL — a
// pasted catalog manifest, a public list page — with two protections a plain
// http.Client does not give:
//
//   - every dial, including ones a redirect points at, must resolve to a
//     public address, or to one of the allowPrivate networks the operator
//     listed to reach a self-hosted source on their own LAN (SSRF);
//   - redirects may only stay on HTTPS, so an http:// hop cannot downgrade
//     the fetch.
//
// Transports are pooled per allowlist, so calling this per request is cheap.
func GuardedClient(timeout time.Duration, allowPrivate []*net.IPNet) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: guardedTransport(timeout, allowPrivate),
		CheckRedirect: func(next *http.Request, _ []*http.Request) error {
			if next.URL.Scheme != "https" || next.URL.Host == "" {
				return fmt.Errorf("redirect to %q is not HTTPS", next.URL.Redacted())
			}
			return nil
		},
	}
}
