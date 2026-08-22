package stremio

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
)

// /debug/play used to be routed ahead of the SPA and deliberately exempted from
// the token check every other Stremio route gets, so an unauthenticated caller
// could hand it a local path or a URL and have the server read or fetch it. The
// endpoint is gone. This pins that it is gone rather than merely gated: the
// request has to end at the catch-all handler with the file untouched.
func TestDebugPlayIsNoLongerRouted(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.nzb")
	if err := os.WriteFile(secret, []byte("MUST-NOT-BE-SERVED"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var reachedCatchAll bool
	srv := &Server{config: &config.Config{}}
	srv.SetWebHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedCatchAll = true
		w.WriteHeader(http.StatusNotFound)
	}))

	mux := http.NewServeMux()
	srv.SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/debug/play?nzb="+secret, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !reachedCatchAll {
		t.Fatalf("expected /debug/play to fall through to the catch-all, got status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "MUST-NOT-BE-SERVED") {
		t.Fatalf("the request read the local file: %q", rec.Body.String())
	}
}
