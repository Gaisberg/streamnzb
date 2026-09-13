package stremio

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

	_, err := externalHTTPClient().Get(ts.URL)
	if err == nil {
		t.Fatal("expected the private-destination dial to be rejected")
	}
}

// TestExternalHTTPClientReusesOneTransport pins the fix for a fresh
// *http.Transport per call: InspectExternalManifest can call
// externalHTTPClient() up to externalInspectionMaxCatalogs times in one
// request, and a distinct transport per call means no connection reuse
// across those calls — exactly the per-request cost the catalog cap was
// added to bound.
func TestExternalHTTPClientReusesOneTransport(t *testing.T) {
	a := externalHTTPClient()
	b := externalHTTPClient()
	if a.Transport != b.Transport {
		t.Fatal("externalHTTPClient() must reuse one shared transport across calls")
	}
}
