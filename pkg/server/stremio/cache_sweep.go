package stremio

import (
	"time"

	"streamnzb/pkg/core/logger"
)

// Every search-cache entry carries a deadline, but nothing ever looked at those
// deadlines except a read of that same key. An entry for a title nobody asks
// for again was therefore held until a config change cleared the whole map, so
// a long-lived instance browsing many titles only ever grew. The "next release"
// cursors had no deadline at all.
//
// This sweep is what makes the deadlines mean something on their own. It is
// deliberately not a cache size limit: entries are bounded by their TTL, and a
// count-based eviction would throw away playlists that are still valid.
const searchCacheSweepInterval = 5 * time.Minute

// searchCacheJanitor sweeps until the server is shut down.
func (s *Server) searchCacheJanitor() {
	ticker := time.NewTicker(searchCacheSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.sweepSearchCaches(time.Now())
		}
	}
}

// sweepSearchCaches drops playlist and raw-search entries whose deadline has
// passed, plus "next release" cursors nobody has touched for a full cache
// lifetime. It returns what it removed so tests can assert on it without
// reaching into the maps.
//
// A cursor is tied to a playlist: once the playlist it walks has expired, the
// position it holds means nothing, so both use the same lifetime.
func (s *Server) sweepSearchCaches(now time.Time) (playlists, raws, cursors int) {
	s.playlistCache.Range(func(key, value interface{}) bool {
		// A nil or wrong-typed entry can never be served, so it goes too.
		if ent, _ := value.(*playlistCacheEntry); ent == nil || !now.Before(ent.until) {
			s.playlistCache.Delete(key)
			playlists++
		}
		return true
	})

	s.rawSearchCache.Range(func(key, value interface{}) bool {
		if ent, _ := value.(*rawSearchCacheEntry); ent == nil || !now.Before(ent.until) {
			s.rawSearchCache.Delete(key)
			raws++
		}
		return true
	})

	cursorTTL := s.playlistCacheTTL()
	s.nextReleaseIndex.Range(func(key, value interface{}) bool {
		cursor, _ := value.(*nextReleaseCursor)
		if cursor == nil {
			s.nextReleaseIndex.Delete(key)
			cursors++
			return true
		}
		cursor.mu.Lock()
		idle := now.Sub(cursor.lastTouched)
		cursor.mu.Unlock()
		// A caller that loaded this pointer just before the delete keeps using
		// it and its update is dropped on the floor. The cost of that is one
		// "next release" press starting from the top again, and it can only
		// happen to a cursor already idle for a full cache lifetime.
		if idle > cursorTTL {
			s.nextReleaseIndex.Delete(key)
			cursors++
		}
		return true
	})

	if playlists+raws+cursors > 0 {
		logger.Debug("Search cache sweep",
			"playlists", playlists,
			"raw", raws,
			"next_cursors", cursors,
		)
	}
	return playlists, raws, cursors
}
