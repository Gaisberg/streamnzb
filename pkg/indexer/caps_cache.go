package indexer

import (
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

// CapsCacheTTL is how long fetched capabilities are trusted without a refresh.
// Caps are near-static — supported search modes, id parameters, categories —
// so a week keeps rebuild traffic at zero without letting a real capability
// change go unnoticed for long.
const CapsCacheTTL = 7 * 24 * time.Hour

const capsCacheStateKey = "indexer_caps"

type capsCacheEntry struct {
	// Identity fingerprints what the caps were fetched with (URL, API path,
	// key). A mismatch invalidates the entry: caps from a different endpoint
	// say nothing about this one, and a changed key must produce a live fetch
	// so a rejection surfaces at save time.
	Identity  string    `json:"identity"`
	FetchedAt time.Time `json:"fetched_at"`
	Caps      *Caps     `json:"caps"`
}

// CapsStore persists parsed indexer capabilities across restarts and rebuilds,
// so the caps fetch — which spends a real API hit on most indexers — runs once
// per CapsCacheTTL instead of on every stack rebuild.
type CapsStore struct {
	state *persistence.StateManager
	mu    sync.Mutex
	data  map[string]*capsCacheEntry
}

// NewCapsStore loads the persisted cache; a load failure just starts empty,
// since the worst case is the live fetch we would have done anyway.
func NewCapsStore(sm *persistence.StateManager) *CapsStore {
	s := &CapsStore{
		state: sm,
		data:  make(map[string]*capsCacheEntry),
	}
	if sm != nil {
		if _, err := sm.Get(capsCacheStateKey, &s.data); err != nil {
			logger.Warn("Failed to load cached indexer capabilities", "err", err)
			s.data = make(map[string]*capsCacheEntry)
		}
	}
	return s
}

// Lookup returns the caps cached for name when identity matches; fresh
// reports whether the entry is still inside CapsCacheTTL. A stale entry is
// returned with fresh=false so a failed live fetch can fall back to it.
func (s *CapsStore) Lookup(name, identity string) (caps *Caps, fresh, ok bool) {
	if s == nil {
		return nil, false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.data[name]
	if !found || entry == nil || entry.Caps == nil || entry.Identity != identity {
		return nil, false, false
	}
	return entry.Caps, time.Since(entry.FetchedAt) < CapsCacheTTL, true
}

// Put stores freshly fetched caps for name and persists the cache.
func (s *CapsStore) Put(name, identity string, caps *Caps) {
	if s == nil || caps == nil {
		return
	}
	s.mu.Lock()
	s.data[name] = &capsCacheEntry{
		Identity:  identity,
		FetchedAt: time.Now(),
		Caps:      caps,
	}
	snap := s.snapshotLocked()
	s.mu.Unlock()
	s.persist(snap)
}

// Sync drops entries for indexers that no longer exist in the config, so a
// deleted indexer does not leave orphaned caps behind. Disabled indexers keep
// theirs — re-enabling inside the TTL should not cost a fetch.
func (s *CapsStore) Sync(configuredNames []string) {
	if s == nil {
		return
	}
	active := make(map[string]bool, len(configuredNames))
	for _, name := range configuredNames {
		active[name] = true
	}

	s.mu.Lock()
	changed := false
	for name := range s.data {
		if !active[name] {
			delete(s.data, name)
			changed = true
		}
	}
	var snap map[string]*capsCacheEntry
	if changed {
		snap = s.snapshotLocked()
	}
	s.mu.Unlock()

	if snap != nil {
		s.persist(snap)
	}
}

// snapshotLocked shallow-copies the map for persistence outside the lock; the
// entries themselves are replaced wholesale on Put, never mutated, so sharing
// them with the serializer is safe.
func (s *CapsStore) snapshotLocked() map[string]*capsCacheEntry {
	snap := make(map[string]*capsCacheEntry, len(s.data))
	for name, entry := range s.data {
		snap[name] = entry
	}
	return snap
}

func (s *CapsStore) persist(snap map[string]*capsCacheEntry) {
	if s.state == nil {
		return
	}
	if err := s.state.Set(capsCacheStateKey, snap); err != nil {
		logger.Error("Failed to save cached indexer capabilities", "err", err)
	}
}
