package simkl

import (
	"sort"
	"strings"
	"sync"

	"streamnzb/pkg/services/metadata/scrobble"
)

// Registry hands out one client per stream, because a Simkl account belongs to
// a person rather than to the server.
//
// Clients are kept rather than rebuilt per request: each holds its stream's
// watchlist cache, and Simkl's API terms punish clients that refetch an
// unchanged list.
type Registry struct {
	clientID string
	store    *scrobble.Store[tokenState]

	mu      sync.Mutex
	clients map[string]*Client
}

func NewRegistry(clientID, dataDir string) *Registry {
	return &Registry{
		clientID: strings.TrimSpace(clientID),
		store:    scrobble.NewStore[tokenState](dataDir, stateKey),
		clients:  make(map[string]*Client),
	}
}

// ClientID reports the id the registry was built with, so a reload can keep
// the instance — and every stream's cache — when the id did not change.
func (r *Registry) ClientID() string {
	if r == nil {
		return ""
	}
	return r.clientID
}

// Enabled reports whether a client id is available at all. Without one no
// stream can start the PIN flow.
func (r *Registry) Enabled() bool { return r != nil && r.clientID != "" }

// For returns the client for one stream, building it on first use.
func (r *Registry) For(stream string) *Client {
	stream = strings.TrimSpace(stream)
	if r == nil || stream == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if client, ok := r.clients[stream]; ok {
		return client
	}
	client := newClient(r.clientID, stream, r.store)
	r.clients[stream] = client
	return client
}

// LinkedStreams lists the streams with an account on file, sorted. It reads
// the store rather than the built clients, so a stream nobody has served yet
// still counts.
func (r *Registry) LinkedStreams() []string {
	if r == nil {
		return nil
	}
	streams := r.store.Streams()
	sort.Strings(streams)
	return streams
}

// AnyLinked reports whether at least one stream has linked an account, which
// is what decides whether the Simkl catalog rows are worth offering at all.
func (r *Registry) AnyLinked() bool { return len(r.LinkedStreams()) > 0 }

// StateKeys are every state-store key this package has written account tokens
// to, for the one-shot reset in scrobble.ResetLegacyLinks.
func StateKeys() []string { return []string{stateKey, legacyStateKey} }
