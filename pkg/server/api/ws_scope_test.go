package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/session"
)

func wsTestClient(username string) *Client {
	return &Client{send: make(chan WSMessage, 16), stream: &auth.Stream{Username: username}}
}

func drainWS(c *Client) []WSMessage {
	var out []WSMessage
	for {
		select {
		case msg := <-c.send:
			out = append(out, msg)
		default:
			return out
		}
	}
}

func wsServerWithClients(clients ...*Client) *Server {
	s := adminServer()
	s.latestSnapshot = &SystemStats{
		Timestamp:         time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC),
		TotalSpeed:        42,
		ActiveConnections: 12,
		TotalConnections:  40,
		Providers:         []ProviderStats{{Name: "primary", ActiveConns: 12, MaxConns: 40}},
		Indexers:          []IndexerStats{{Name: "indexer"}},
		StreamSpeeds:      map[string]float64{"phone": 30, "tv": 10},
		ActiveSessions: []session.ActiveSessionInfo{
			{ID: "a", StreamName: "phone", Title: "Movie.2024", Clients: []string{"10.0.0.2"}},
			{ID: "b", StreamName: "tv", Title: "Show.S01E01", Clients: []string{"10.0.0.3"}},
			{ID: "proxy-10.0.0.9", Title: "Proxy Client (2 conns)", Clients: []string{"10.0.0.9"}},
		},
	}
	s.clients = map[*Client]bool{}
	for _, c := range clients {
		s.clients[c] = true
	}
	return s
}

func TestBroadcastStatsScopesEachClient(t *testing.T) {
	admin, phone := wsTestClient("admin"), wsTestClient("phone")
	s := wsServerWithClients(admin, phone)
	full, _ := json.Marshal(s.latestSnapshot)

	s.broadcastStats(s.latestSnapshot, full)

	adminMsgs := drainWS(admin)
	if len(adminMsgs) != 1 || string(adminMsgs[0].Payload) != string(full) {
		t.Fatalf("admin did not get the full snapshot: %+v", adminMsgs)
	}

	phoneMsgs := drainWS(phone)
	if len(phoneMsgs) != 1 || phoneMsgs[0].Type != "stats" {
		t.Fatalf("phone messages = %+v, want one stats message", phoneMsgs)
	}
	var got SystemStats
	if err := json.Unmarshal(phoneMsgs[0].Payload, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(got.ActiveSessions) != 1 || got.ActiveSessions[0].StreamName != "phone" {
		t.Fatalf("phone saw sessions %+v, want only its own", got.ActiveSessions)
	}
	if got.TotalSpeed != 30 || len(got.StreamSpeeds) != 1 {
		t.Fatalf("phone saw speed %v / %v, want only its own 30", got.TotalSpeed, got.StreamSpeeds)
	}
	if len(got.Providers) != 0 || len(got.Indexers) != 0 || got.ActiveConnections != 0 || got.TotalConnections != 0 {
		t.Fatalf("phone saw pool-wide detail: %+v", got)
	}
}

func TestBroadcastAdminSkipsStreamTokens(t *testing.T) {
	admin, phone := wsTestClient("admin"), wsTestClient("phone")
	s := wsServerWithClients(admin, phone)

	s.broadcastAdmin(WSMessage{Type: "log_entry", Payload: json.RawMessage(`"secret"`)})

	if n := len(drainWS(admin)); n != 1 {
		t.Fatalf("admin got %d messages, want 1", n)
	}
	if msgs := drainWS(phone); len(msgs) != 0 {
		t.Fatalf("stream token got admin-only messages: %+v", msgs)
	}
}

// A stream token opening the socket — every Stremio addon URL carries one —
// must not be handed the log history or other streams' sessions on connect.
func TestWebSocketConnectScopesStreamToken(t *testing.T) {
	s := wsServerWithClients()
	full, _ := json.Marshal(s.latestSnapshot)
	s.latestStats = full

	as := func(username string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.ContextWithStream(r.Context(), &auth.Stream{Username: username})
			s.handleWebSocket(w, r.WithContext(ctx))
		})
	}
	read := func(t *testing.T, username string) map[string]json.RawMessage {
		t.Helper()
		srv := httptest.NewServer(as(username))
		defer srv.Close()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		got := map[string]json.RawMessage{}
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var msg WSMessage
			if err := conn.ReadJSON(&msg); err != nil {
				t.Fatalf("read: %v (got %v so far)", err, got)
			}
			got[msg.Type] = msg.Payload
			if msg.Type == "auth_info" {
				return got
			}
		}
	}

	phone := read(t, "phone")
	if _, ok := phone["log_history"]; ok {
		t.Fatalf("stream token was sent the log history")
	}
	var stats SystemStats
	if err := json.Unmarshal(phone["stats"], &stats); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats.ActiveSessions) != 1 || stats.ActiveSessions[0].StreamName != "phone" || len(stats.Providers) != 0 {
		t.Fatalf("stream token was sent %+v", stats)
	}

	admin := read(t, "admin")
	if _, ok := admin["log_history"]; !ok {
		t.Fatalf("admin was not sent the log history")
	}
	if string(admin["stats"]) != string(full) {
		t.Fatalf("admin was not sent the full snapshot")
	}
}
