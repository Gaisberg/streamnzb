package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubShareFetch points the relay's client at a local TLS server, which the
// SSRF guard would otherwise refuse as loopback; the guard has its own tests.
func stubShareFetch(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	t.Cleanup(ts.Close)
	previous := shareFetchClient
	shareFetchClient = func([]*net.IPNet) *http.Client { return ts.Client() }
	t.Cleanup(func() { shareFetchClient = previous })
	return ts.URL
}

func postShareFetch(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	s.handleShareFetch(rr, req)
	return rr
}

func shareFetchBody(url string) []byte {
	b, _ := json.Marshal(map[string]string{"url": url})
	return b
}

func TestShareFetchRelaysTheBody(t *testing.T) {
	base := stubShareFetch(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("the relay forwarded credentials")
		}
		_, _ = w.Write([]byte("SNZBP1:abc"))
	})
	rr := postShareFetch(adminServer(), adminStreamRequest(http.MethodPost, "/api/share/fetch", shareFetchBody(base+"/profile.txt")))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var got struct{ Text string }
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.Text != "SNZBP1:abc" {
		t.Fatalf("text = %q (%v), want the upstream body", got.Text, err)
	}
}

func TestShareFetchReportsTheUpstreamStatus(t *testing.T) {
	base := stubShareFetch(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret page body", http.StatusNotFound)
	})
	rr := postShareFetch(adminServer(), adminStreamRequest(http.MethodPost, "/api/share/fetch", shareFetchBody(base+"/missing")))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "HTTP 404") || strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("body = %q, want the status and not the upstream body", rr.Body.String())
	}
}

func TestShareFetchCapsTheBody(t *testing.T) {
	base := stubShareFetch(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", shareFetchMaxBytes+1)))
	})
	rr := postShareFetch(adminServer(), adminStreamRequest(http.MethodPost, "/api/share/fetch", shareFetchBody(base+"/huge")))
	if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), "far more data") {
		t.Fatalf("status = %d, body %q, want the size refusal", rr.Code, rr.Body.String())
	}
}

func TestShareFetchRefusesPlainHTTP(t *testing.T) {
	rr := postShareFetch(adminServer(), adminStreamRequest(http.MethodPost, "/api/share/fetch", shareFetchBody("http://example.com/profile.txt")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestShareFetchRefusesADeviceToken(t *testing.T) {
	req := deviceStreamRequest(http.MethodPost, "/api/share/fetch")
	rr := postShareFetch(adminServer(), req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// The relay must not become a way to reach the box's own network: with the
// real guarded client, a loopback URL is refused at dial time.
func TestShareFetchRefusesLoopback(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the relay reached a loopback address")
	}))
	defer ts.Close()
	rr := postShareFetch(adminServer(), adminStreamRequest(http.MethodPost, "/api/share/fetch", shareFetchBody(ts.URL+"/profile.txt")))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}
