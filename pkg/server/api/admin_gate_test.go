package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
)

// adminServer is the minimum Server the admin gate needs: a config that names
// the admin. Everything the gated handlers reach past it is nil-safe, which is
// what lets the admin half of the table assert "not 403" without standing up a
// database.
func adminServer() *Server {
	return &Server{config: &config.Config{AdminUsername: "admin"}}
}

func deviceStreamRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	ctx := auth.ContextWithStream(req.Context(), &auth.Stream{Username: "phone", Token: "phone-token"})
	return req.WithContext(ctx)
}

// Every route here sits behind authMiddleware, which authenticates any stream
// token — the per-device credentials handed to Stremio — not just the admin's.
// Authentication and authorization being decided in different places is what
// let these drift open, so each route is asserted on its own rather than as a
// group: a gate removed from one of them must fail one named test.
func TestAdminOnlyRoutesRejectADeviceToken(t *testing.T) {
	setTestDataDir(t)

	for _, tc := range []struct {
		name    string
		method  string
		target  string
		handler func(*Server) http.HandlerFunc
	}{
		{"logs download", http.MethodGet, "/api/logs/download",
			func(s *Server) http.HandlerFunc { return s.handleDownloadLogs }},
		{"nzb attempts", http.MethodGet, "/api/nzb-attempts",
			func(s *Server) http.HandlerFunc { return s.handleNZBAttempts }},
		{"search diagnostics", http.MethodGet, "/api/search-diagnostics?stream_name=living-room",
			func(s *Server) http.HandlerFunc { return s.handleSearchDiagnostics }},
		{"library list", http.MethodGet, "/api/library",
			func(s *Server) http.HandlerFunc { return s.handleGetLibrary }},
		{"library pin", http.MethodPost, "/api/library/pin",
			func(s *Server) http.HandlerFunc { return s.handlePinLibrary }},
		{"library delete", http.MethodDelete, "/api/library/delete?id=x",
			func(s *Server) http.HandlerFunc { return s.handleDeleteLibrary }},
		{"library stats", http.MethodGet, "/api/library/stats",
			func(s *Server) http.HandlerFunc { return s.handleLibraryStats }},
		{"statistics delete", http.MethodDelete, "/api/stats/history?type=provider&name=x",
			func(s *Server) http.HandlerFunc { return s.handleStatsHistory }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := adminServer()

			rr := httptest.NewRecorder()
			tc.handler(s)(rr, deviceStreamRequest(tc.method, tc.target))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("device token: status = %d, want 403", rr.Code)
			}

			rr = httptest.NewRecorder()
			tc.handler(s)(rr, adminStreamRequest(tc.method, tc.target, nil))
			if rr.Code == http.StatusForbidden {
				t.Fatalf("admin was refused its own route")
			}
		})
	}
}

// Reading statistics stays open to any authenticated caller, matching the other
// stats routes; only the erase branch was closed. Pinned because the gate lives
// inside the method switch, where it is easy to hoist to the top by accident.
func TestStatsHistoryReadStaysOpenToADeviceToken(t *testing.T) {
	rr := httptest.NewRecorder()
	adminServer().handleStatsHistory(rr, deviceStreamRequest(http.MethodGet, "/api/stats/history"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}
