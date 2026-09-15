package scrobble

import (
	"sync"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

// Store persists one linked account per stream under a single state-store key.
//
// Accounts are per stream because there is one of each per person, not per
// server: a household where everyone has their own stream needs everyone's
// viewing to land in their own history. Keeping every stream's token in one
// key rather than one key each is what lets a registry answer "which streams
// have linked this service" without enumerating the state store.
//
// T is the service's own token shape — the two services store different
// things, and neither has to know about the other's.
type Store[T any] struct {
	mu      sync.Mutex
	dataDir string
	key     string
	loaded  bool
	tokens  map[string]T
}

func NewStore[T any](dataDir, key string) *Store[T] {
	return &Store[T]{dataDir: dataDir, key: key}
}

// loadLocked reads the persisted map on first use. A missing or unreadable
// entry degrades to "nobody is linked" rather than failing the caller: the
// worst outcome is a settings card asking for a link that already exists,
// which the user can fix, unlike a startup that refuses to serve.
func (s *Store[T]) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.tokens = make(map[string]T)
	manager, err := persistence.GetManager(s.dataDir)
	if err != nil {
		return
	}
	var stored map[string]T
	if found, _ := manager.Get(s.key, &stored); found && stored != nil {
		s.tokens = stored
	}
}

func (s *Store[T]) saveLocked() {
	manager, err := persistence.GetManager(s.dataDir)
	if err != nil {
		return
	}
	if err := manager.Set(s.key, s.tokens); err != nil {
		logger.Warn("Failed to save linked accounts to state", "key", s.key, "err", err)
	}
}

// Get returns the token stored for one stream.
func (s *Store[T]) Get(stream string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	v, ok := s.tokens[stream]
	return v, ok
}

func (s *Store[T]) Set(stream string, token T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	s.tokens[stream] = token
	s.saveLocked()
}

func (s *Store[T]) Delete(stream string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	delete(s.tokens, stream)
	s.saveLocked()
}

// Streams lists the streams with a token on file, in no particular order.
func (s *Store[T]) Streams() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]string, 0, len(s.tokens))
	for stream := range s.tokens {
		out = append(out, stream)
	}
	return out
}
