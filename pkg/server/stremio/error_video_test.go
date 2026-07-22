package stremio

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"streamnzb/pkg/server/web"
)

func TestErrorVideoServing(t *testing.T) {
	handler := web.Handler()
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, path := range []string{"/error/failure.mp4", "/error/failure_muted.mp4"} {
		res, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("failed to fetch %s: %v", path, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status for %s = %d, want 200", path, res.StatusCode)
		}
		ct := res.Header.Get("Content-Type")
		t.Logf("Path: %s, Content-Type: %s, Content-Length: %d", path, ct, res.ContentLength)
		res.Body.Close()
	}
}
