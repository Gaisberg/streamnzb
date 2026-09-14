package httpx

import (
	"net"
	"testing"
)

// TestIsPublicIPBlocksNonPublicRanges covers the ranges an operator-supplied
// URL must not be able to reach. The CGNAT case is the one Go's own
// net.IP.IsPrivate misses: 100.64.0.0/10 is what Tailscale hands out, so on a
// self-hosted box it is very much an internal address.
func TestIsPublicIPBlocksNonPublicRanges(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		// An IPv4-mapped IPv6 loopback must not sneak past the v4 predicates.
		{"::ffff:127.0.0.1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fd00::1", false},
		// The cloud metadata endpoint, the highest-value SSRF target there is.
		{"169.254.169.254", false},
		{"100.64.1.5", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"198.18.0.1", false},
		{"224.0.0.1", false},
		// IPv4-in-IPv6 forms To4() does not unwrap. Behind DNS64/NAT64 the
		// first of these reaches the 169.254.169.254 metadata endpoint and the
		// second reaches 127.0.0.1.
		{"64:ff9b::a9fe:a9fe", false},
		{"64:ff9b::7f00:1", false},
		{"2002:7f00:1::1", false},
		{"fec0::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			if got := IsPublicIP(net.ParseIP(tc.ip)); got != tc.want {
				t.Fatalf("IsPublicIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
	if IsPublicIP(nil) {
		t.Fatal("an unparseable address must not count as public")
	}
}

func TestParseNetworks(t *testing.T) {
	nets, err := ParseNetworks([]string{"192.168.1.0/24", "10.0.0.5", "fd00::/8"}, "catalog_source_networks")
	if err != nil {
		t.Fatalf("ParseNetworks: %v", err)
	}
	if len(nets) != 3 {
		t.Fatalf("nets = %d, want 3", len(nets))
	}
	// A bare address stands for that single host, not its whole class.
	if !nets[1].Contains(net.ParseIP("10.0.0.5")) || nets[1].Contains(net.ParseIP("10.0.0.6")) {
		t.Fatalf("bare address widened to %s", nets[1])
	}

	for _, tc := range []struct {
		name    string
		entries []string
	}{
		{"blank entry", []string{"192.168.1.0/24", "  "}},
		// A catch-all would disable the guard while looking like a setting.
		{"catch-all v4", []string{"0.0.0.0/0"}},
		{"catch-all v6", []string{"::/0"}},
		{"not an address", []string{"my-nas.local"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseNetworks(tc.entries, "catalog_source_networks"); err == nil {
				t.Fatalf("%v was accepted", tc.entries)
			}
		})
	}
}

// TestGuardedClientPoolsTransportsPerAllowlist keeps the dial guard from
// costing a fresh TCP+TLS handshake per request: one manifest inspection can
// issue a hundred of them.
func TestGuardedClientPoolsTransportsPerAllowlist(t *testing.T) {
	lan, err := ParseNetworks([]string{"192.168.0.0/16"}, "catalog_source_networks")
	if err != nil {
		t.Fatalf("ParseNetworks: %v", err)
	}
	if GuardedClient(0, nil).Transport != GuardedClient(0, nil).Transport {
		t.Fatal("the same allowlist must share one transport")
	}
	if GuardedClient(0, nil).Transport == GuardedClient(0, lan).Transport {
		t.Fatal("a different allowlist must get its own guarded dialer")
	}
}

// TestGuardedClientDropsProxy pins a deliberate choice: with a proxy in play
// every dial would go to the proxy's address, so the guard would be checking
// the proxy and the real destination would go unchecked.
func TestGuardedClientDropsProxy(t *testing.T) {
	transport := guardedTransport(0, nil)
	if transport.Proxy != nil {
		t.Fatal("a proxy would bypass the dial-time destination check")
	}
}
