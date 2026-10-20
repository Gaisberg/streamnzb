package api

import (
	"encoding/json"
	"strings"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

// The WebSocket is authenticated by the same middleware as the rest of the
// API, which admits any stream token — the credential embedded in every
// Stremio addon URL — not just the admin's. What a socket is sent is therefore
// decided here, per client: the admin gets everything, a stream token gets its
// own sessions and speed and nothing else. Logs, provider and indexer detail,
// and component health are admin-only, exactly as their REST routes are.

func clientIsAdmin(client *Client, admin string) bool {
	return client != nil && client.stream != nil && client.stream.Username == admin
}

func clientStreamName(client *Client) string {
	if client == nil || client.stream == nil {
		return ""
	}
	return strings.TrimSpace(client.stream.Username)
}

// broadcastAdmin queues msg to admin clients only, dropping it for any whose
// buffer is full.
func (s *Server) broadcastAdmin(msg WSMessage) {
	admin := s.adminUsername()
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		if clientIsAdmin(client, admin) {
			trySendWS(client, msg)
		}
	}
}

// viewerStats is snap as one stream may see it: its own sessions and its own
// share of the speed. Pool-wide totals are zeroed rather than kept, since even
// the connection count says how busy everyone else is.
func viewerStats(snap *SystemStats, stream string) SystemStats {
	out := SystemStats{
		Timestamp:      snap.Timestamp,
		Providers:      []ProviderStats{},
		ActiveSessions: []session.ActiveSessionInfo{},
		StreamSpeeds:   map[string]float64{},
	}
	if stream == "" {
		return out
	}
	for _, sess := range snap.ActiveSessions {
		if sess.StreamName == stream {
			out.ActiveSessions = append(out.ActiveSessions, sess)
		}
	}
	out.ActiveStreams = len(out.ActiveSessions)
	if speed, ok := snap.StreamSpeeds[stream]; ok {
		out.StreamSpeeds[stream] = speed
		out.TotalSpeed = speed
	}
	return out
}

// statsPayloadFor encodes the stats message one client may receive. cache
// holds the viewer payloads already encoded this tick, keyed by stream, so a
// stream with several sockets open is encoded once; nil disables it.
func statsPayloadFor(client *Client, admin string, snap *SystemStats, adminPayload json.RawMessage, cache map[string]json.RawMessage) json.RawMessage {
	if clientIsAdmin(client, admin) {
		return adminPayload
	}
	name := clientStreamName(client)
	if payload, ok := cache[name]; ok {
		return payload
	}
	payload, err := json.Marshal(viewerStats(snap, name))
	if err != nil {
		logger.Error("Failed to encode viewer stats", "err", err)
		return nil
	}
	if cache != nil {
		cache[name] = payload
	}
	return payload
}

// broadcastStats sends each client the stats it may see.
func (s *Server) broadcastStats(snap *SystemStats, adminPayload json.RawMessage) {
	admin := s.adminUsername()
	cache := map[string]json.RawMessage{}
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		if payload := statsPayloadFor(client, admin, snap, adminPayload, cache); payload != nil {
			trySendWS(client, WSMessage{Type: "stats", Payload: payload})
		}
	}
}
