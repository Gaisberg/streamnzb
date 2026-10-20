package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamnzb/pkg/session"
)

func currentStatsServer() *Server {
	s := adminServer()
	at := time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)
	s.latestSnapshot = &SystemStats{
		Timestamp:         at,
		TotalSpeed:        42,
		ActiveConnections: 12,
		TotalConnections:  40,
		Providers:         []ProviderStats{{Name: "primary", ActiveConns: 12, MaxConns: 40}},
		StreamSpeeds:      map[string]float64{"phone": 30, "tv": 10},
		ActiveSessions: []session.ActiveSessionInfo{
			{ID: "a", StreamName: "phone", Title: "Movie.2024", Clients: []string{"10.0.0.2"}, StartedAt: at.Add(-90 * time.Second), BytesRead: 1000},
			{ID: "b", StreamName: "tv", Title: "Show.S01E01", Clients: []string{"10.0.0.3"}, StartedAt: at.Add(-10 * time.Second), BytesRead: 500},
			{ID: "proxy-10.0.0.9", Title: "Proxy Client (2 conns)", Clients: []string{"10.0.0.9"}},
		},
	}
	return s
}

func decodeCurrentStats(t *testing.T, rr *httptest.ResponseRecorder) currentStatsResponse {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp currentStatsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return resp
}

func TestCurrentStatsAdminSeesEverything(t *testing.T) {
	s := currentStatsServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, adminStreamRequest(http.MethodGet, "/api/stats/current", nil))
	resp := decodeCurrentStats(t, rr)

	if resp.ActiveStreams != 3 || len(resp.Sessions) != 3 {
		t.Fatalf("active_streams = %d, sessions = %d, want 3", resp.ActiveStreams, len(resp.Sessions))
	}
	if resp.TotalSpeed != 42 {
		t.Fatalf("total_speed_mbps = %v, want 42", resp.TotalSpeed)
	}
	if resp.ActiveConnections == nil || *resp.ActiveConnections != 12 || len(resp.Providers) != 1 {
		t.Fatalf("admin view is missing connections or providers")
	}
	if got := resp.Sessions[0].DurationSeconds; got != 90 {
		t.Fatalf("duration_seconds = %d, want 90", got)
	}
	if resp.Sessions[2].StartedAt != nil {
		t.Fatalf("proxy entry has a start time")
	}
	want := map[string]currentStreamStats{
		"phone": {Name: "phone", ActiveSessions: 1, SpeedMbps: 30},
		"tv":    {Name: "tv", ActiveSessions: 1, SpeedMbps: 10},
	}
	if len(resp.Streams) != len(want) {
		t.Fatalf("streams = %+v, want %d rows", resp.Streams, len(want))
	}
	for _, row := range resp.Streams {
		if row != want[row.Name] {
			t.Fatalf("stream row = %+v, want %+v", row, want[row.Name])
		}
	}
}

func TestCurrentStatsAdminCanNarrowToOneStream(t *testing.T) {
	s := currentStatsServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, adminStreamRequest(http.MethodGet, "/api/stats/current?stream=tv", nil))
	resp := decodeCurrentStats(t, rr)

	if resp.ActiveStreams != 1 || resp.Sessions[0].ID != "b" || resp.TotalSpeed != 10 {
		t.Fatalf("narrowed view = %+v", resp)
	}
}

// An idle stream still gets a row, so a sensor bound to it reads zero.
func TestCurrentStatsIdleStreamReadsZero(t *testing.T) {
	s := currentStatsServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, adminStreamRequest(http.MethodGet, "/api/stats/current?stream=kitchen", nil))
	resp := decodeCurrentStats(t, rr)

	if resp.ActiveStreams != 0 || len(resp.Sessions) != 0 || resp.TotalSpeed != 0 {
		t.Fatalf("idle stream view = %+v", resp)
	}
	if len(resp.Streams) != 1 || resp.Streams[0] != (currentStreamStats{Name: "kitchen"}) {
		t.Fatalf("streams = %+v, want one zero row", resp.Streams)
	}
}

// A stream token sees its own sessions and speed — not other streams' titles
// or clients, not the proxy, and none of the pool-wide detail.
func TestCurrentStatsStreamTokenSeesOnlyItself(t *testing.T) {
	s := currentStatsServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, deviceStreamRequest(http.MethodGet, "/api/stats/current"))
	resp := decodeCurrentStats(t, rr)

	if resp.ActiveStreams != 1 || resp.Sessions[0].Stream != "phone" {
		t.Fatalf("sessions = %+v, want only phone's", resp.Sessions)
	}
	if resp.TotalSpeed != 30 {
		t.Fatalf("total_speed_mbps = %v, want phone's 30", resp.TotalSpeed)
	}
	if resp.ActiveConnections != nil || resp.TotalConnections != nil || resp.Providers != nil {
		t.Fatalf("stream token was given pool-wide detail")
	}
	if len(resp.Streams) != 1 || resp.Streams[0].Name != "phone" {
		t.Fatalf("streams = %+v, want only phone", resp.Streams)
	}
}

func TestCurrentStatsStreamTokenRefusedOtherStream(t *testing.T) {
	s := currentStatsServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, deviceStreamRequest(http.MethodGet, "/api/stats/current?stream=tv"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.handleCurrentStats(rr, deviceStreamRequest(http.MethodGet, "/api/stats/current?stream=phone"))
	if rr.Code != http.StatusOK {
		t.Fatalf("own stream by name: status = %d, want 200", rr.Code)
	}
}

func TestCurrentStatsBeforeFirstTick(t *testing.T) {
	s := adminServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, adminStreamRequest(http.MethodGet, "/api/stats/current", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestCurrentStatsRejectsWrites(t *testing.T) {
	s := currentStatsServer()
	rr := httptest.NewRecorder()
	s.handleCurrentStats(rr, adminStreamRequest(http.MethodPost, "/api/stats/current", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}
