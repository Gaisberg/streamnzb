package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"streamnzb/pkg/auth"
)

// currentStatsResponse is the live snapshot reshaped for polling consumers —
// Home Assistant REST sensors, scripts, status pages — that cannot hold the
// dashboard's WebSocket open. Field names are kept flat and stable so a
// template can read them without walking the dashboard's internal shape.
//
// Connections and providers are pool-wide detail only the admin sees; a stream
// token gets its own sessions and its own share of the speed, nothing more.
type currentStatsResponse struct {
	Timestamp         time.Time            `json:"timestamp"`
	ActiveStreams     int                  `json:"active_streams"`
	TotalSpeed        float64              `json:"total_speed_mbps"`
	ActiveConnections *int                 `json:"active_connections,omitempty"`
	TotalConnections  *int                 `json:"total_connections,omitempty"`
	Streams           []currentStreamStats `json:"streams"`
	Sessions          []currentSession     `json:"sessions"`
	Providers         []ProviderStats      `json:"providers,omitempty"`
}

// currentStreamStats is one stream's row. Every configured stream is listed,
// idle ones at zero, so a sensor bound to a stream reads 0 rather than going
// unavailable whenever nobody is watching.
type currentStreamStats struct {
	Name           string  `json:"name"`
	ActiveSessions int     `json:"active_sessions"`
	SpeedMbps      float64 `json:"speed_mbps"`
}

type currentSession struct {
	ID              string     `json:"id"`
	Stream          string     `json:"stream"`
	Title           string     `json:"title"`
	Clients         []string   `json:"clients"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	DurationSeconds int64      `json:"duration_seconds"`
	BytesRead       int64      `json:"bytes_read"`
}

// handleCurrentStats serves GET /api/stats/current from the collector's last
// snapshot. It never collects one itself: the speed meters are stateful, and
// a poll taking a sample would steal part of the window from the dashboard.
//
// ?stream=<name> narrows the response to one stream, which keeps a per-stream
// sensor's template trivial. A stream token is always narrowed to itself and
// is refused any other name.
func (s *Server) handleCurrentStats(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	caller, ok := auth.StreamFromContext(r)
	if !ok || caller == nil {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	admin := s.isAdmin(r)
	want := strings.TrimSpace(r.URL.Query().Get("stream"))
	if !admin {
		own := strings.TrimSpace(caller.Username)
		if want != "" && want != own {
			writeJSONError(w, http.StatusForbidden, "A stream token can only read its own stats")
			return
		}
		want = own
	}

	snap := s.snapshotSystemStats()
	if snap == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Stats are not collected yet")
		return
	}
	writeJSON(w, http.StatusOK, s.buildCurrentStats(snap, want, admin))
}

// buildCurrentStats shapes snap for one viewer. An empty stream means the
// admin's unfiltered view.
func (s *Server) buildCurrentStats(snap *SystemStats, stream string, admin bool) currentStatsResponse {
	resp := currentStatsResponse{
		Timestamp: snap.Timestamp,
		Streams:   []currentStreamStats{},
		Sessions:  []currentSession{},
	}

	rows := map[string]*currentStreamStats{}
	var order []string
	addRow := func(name string) *currentStreamStats {
		if row, ok := rows[name]; ok {
			return row
		}
		row := &currentStreamStats{Name: name, SpeedMbps: snap.StreamSpeeds[name]}
		rows[name] = row
		order = append(order, name)
		return row
	}
	visible := func(name string) bool { return stream == "" || name == stream }

	// Configured streams first, in the dashboard's order; anything else that is
	// playing or moving bytes (the admin's own playback) follows by name.
	if stream != "" {
		addRow(stream)
	} else if s.streamManager != nil {
		for _, st := range s.streamManager.GetAllStreams() {
			addRow(st.Username)
		}
	}
	var extra []string
	for name := range snap.StreamSpeeds {
		if _, ok := rows[name]; !ok && name != "" && visible(name) {
			extra = append(extra, name)
		}
	}

	now := snap.Timestamp
	for _, sess := range snap.ActiveSessions {
		// Proxy entries belong to no stream; only the unfiltered view has them.
		if sess.StreamName == "" && stream != "" {
			continue
		}
		if sess.StreamName != "" && !visible(sess.StreamName) {
			continue
		}
		out := currentSession{
			ID:        sess.ID,
			Stream:    sess.StreamName,
			Title:     sess.Title,
			Clients:   sess.Clients,
			BytesRead: sess.BytesRead,
		}
		if out.Clients == nil {
			out.Clients = []string{}
		}
		if !sess.StartedAt.IsZero() {
			started := sess.StartedAt
			out.StartedAt = &started
			if d := now.Sub(started); d > 0 {
				out.DurationSeconds = int64(d / time.Second)
			}
		}
		resp.Sessions = append(resp.Sessions, out)
		if sess.StreamName == "" {
			continue
		}
		if _, ok := rows[sess.StreamName]; !ok {
			extra = append(extra, sess.StreamName)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		addRow(name)
	}
	for _, sess := range resp.Sessions {
		if sess.Stream != "" {
			rows[sess.Stream].ActiveSessions++
		}
	}
	for _, name := range order {
		resp.Streams = append(resp.Streams, *rows[name])
	}

	resp.ActiveStreams = len(resp.Sessions)
	if stream != "" {
		resp.TotalSpeed = rows[stream].SpeedMbps
	} else {
		resp.TotalSpeed = snap.TotalSpeed
	}
	if admin && stream == "" {
		active, total := snap.ActiveConnections, snap.TotalConnections
		resp.ActiveConnections = &active
		resp.TotalConnections = &total
		resp.Providers = snap.Providers
	}
	return resp
}
