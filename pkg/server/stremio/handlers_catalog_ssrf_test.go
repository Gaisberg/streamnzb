package stremio

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"streamnzb/pkg/core/httpx"
)

// TestExternalHTTPClientRejectsPrivateDestination confirms the SSRF guard on
// externalHTTPClient() actually blocks a connection to a loopback address —
// exactly what a user-pasted manifest URL could resolve to (directly, or via
// a redirect). httptest.NewServer always binds a loopback address, so this
// server standing up successfully and the client still failing to reach it
// proves the dial-time check runs, not just that the server is unreachable.
func TestExternalHTTPClientRejectsPrivateDestination(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request must never reach the server — the SSRF guard should have blocked the dial")
	}))
	defer ts.Close()

	_, err := externalHTTPClient(nil).Get(ts.URL)
	if err == nil {
		t.Fatal("expected the private-destination dial to be rejected")
	}
}

// TestExternalHTTPClientReachesAllowedPrivateNetwork is the other half of the
// guard: a self-hosted Stremio addon on the operator's own LAN is exactly the
// case CatalogSourceNetworks exists for, so an address inside a listed network
// must still be reachable. Without this the guard would be indistinguishable
// from "no self-hosted sources at all".
func TestExternalHTTPClientReachesAllowedPrivateNetwork(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	defer ts.Close()

	allowed, err := httpx.ParseNetworks([]string{"127.0.0.0/8"}, "catalog_source_networks")
	if err != nil {
		t.Fatalf("ParseNetworks: %v", err)
	}
	resp, err := externalHTTPClient(allowed).Get(ts.URL)
	if err != nil {
		t.Fatalf("an allowed private destination must be reachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestExternalHTTPClientReusesOneTransport pins the fix for a fresh
// *http.Transport per call: InspectExternalManifest can call
// externalHTTPClient() up to externalInspectionMaxCatalogs times in one
// request, and a distinct transport per call means no connection reuse
// across those calls — exactly the per-request cost the catalog cap was
// added to bound. Distinct allowlists legitimately get distinct transports,
// since the dial guard is baked into the dialer.
func TestExternalHTTPClientReusesOneTransport(t *testing.T) {
	if externalHTTPClient(nil).Transport != externalHTTPClient(nil).Transport {
		t.Fatal("externalHTTPClient() must reuse one shared transport across calls")
	}
	allowed := []*net.IPNet{{IP: net.IPv4(10, 0, 0, 0), Mask: net.CIDRMask(8, 32)}}
	if externalHTTPClient(allowed).Transport != externalHTTPClient(allowed).Transport {
		t.Fatal("the same allowlist must reuse one transport")
	}
	if externalHTTPClient(nil).Transport == externalHTTPClient(allowed).Transport {
		t.Fatal("a different allowlist must not silently reuse another allowlist's guarded dialer")
	}
}
