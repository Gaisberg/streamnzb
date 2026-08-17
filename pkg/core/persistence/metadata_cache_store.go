package persistence

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MetadataCacheStore persists metadata API responses (TMDB, Kitsu, TVMaze, ...)
// keyed by (source, request path). The in-memory client caches are rebuilt on
// every config reload and lost on restart, so without this every reload re-burns
// provider quota for content that was already resolved. Rows carry a TTL; the
// table is a rebuildable cache, deliberately excluded from backend-switch
// imports like anime_mappings.
//
// Only successful (HTTP 200) bodies are stored, mirroring the in-memory client
// caches. Bodies are gzipped: metadata JSON compresses well and the table is
// written on every upstream fetch.
type MetadataCacheStore struct {
	db  *connRef // shared read handle
	wdb *connRef // write handle
	mu  sync.RWMutex
	// lastSweepMs gates the opportunistic expiry sweep so a burst of writes
	// does not re-scan the table on every Put.
	lastSweepMs atomic.Int64
}

const (
	// metadataCacheRowCap bounds the table. SQLite never returns free pages to
	// the OS (no VACUUM anywhere in this codebase), so an unbounded cache would
	// grow the database file forever.
	metadataCacheRowCap = 20000
	// metadataCacheSweepInterval is the minimum spacing between opportunistic
	// sweeps triggered by Put.
	metadataCacheSweepInterval = 15 * time.Minute
)

func NewMetadataCacheStore(db, wdb *connRef) *MetadataCacheStore {
	return &MetadataCacheStore{db: db, wdb: wdb}
}

// Get returns the cached body for (source, key) if present and unexpired.
func (ms *MetadataCacheStore) Get(source, key string) ([]byte, bool) {
	source, key = strings.TrimSpace(source), strings.TrimSpace(key)
	if ms == nil || ms.db == nil || source == "" || key == "" {
		return nil, false
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	var compressed []byte
	row := ms.db.QueryRow(
		`SELECT body FROM metadata_cache WHERE source = ? AND cache_key = ? AND expires_at > ?`,
		source, key, time.Now().UnixMilli(),
	)
	if err := row.Scan(&compressed); err != nil {
		return nil, false
	}
	body, err := DecompressNZB(compressed)
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return body, true
}

// Put stores (or refreshes) the body for (source, key) with the given TTL.
func (ms *MetadataCacheStore) Put(source, key string, body []byte, ttl time.Duration) {
	source, key = strings.TrimSpace(source), strings.TrimSpace(key)
	if ms == nil || ms.db == nil || source == "" || key == "" || len(body) == 0 || ttl <= 0 {
		return
	}
	compressed, err := compressNZB(body)
	if err != nil {
		return
	}
	now := time.Now()
	ms.mu.Lock()
	_, _ = ms.wdb.Exec(`
		INSERT INTO metadata_cache (source, cache_key, body, fetched_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(source, cache_key) DO UPDATE SET
			body = excluded.body,
			fetched_at = excluded.fetched_at,
			expires_at = excluded.expires_at
	`, source, key, compressed, now.UnixMilli(), now.Add(ttl).UnixMilli())
	ms.mu.Unlock()

	ms.maybeSweep(now)
}

// maybeSweep runs the expiry/row-cap sweep at most once per sweep interval.
func (ms *MetadataCacheStore) maybeSweep(now time.Time) {
	last := ms.lastSweepMs.Load()
	if now.UnixMilli()-last < metadataCacheSweepInterval.Milliseconds() {
		return
	}
	if !ms.lastSweepMs.CompareAndSwap(last, now.UnixMilli()) {
		return
	}
	ms.PurgeExpired()
	ms.EnforceRowCap(metadataCacheRowCap)
}

// PurgeExpired drops entries past their TTL; safe to call opportunistically.
func (ms *MetadataCacheStore) PurgeExpired() {
	if ms == nil || ms.db == nil {
		return
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	_, _ = ms.wdb.Exec(`DELETE FROM metadata_cache WHERE expires_at <= ?`, time.Now().UnixMilli())
}

// EnforceRowCap deletes the oldest-fetched rows beyond max. When the table is
// under the cap the cutoff subquery yields no row and nothing is deleted.
func (ms *MetadataCacheStore) EnforceRowCap(max int) {
	if ms == nil || ms.db == nil || max <= 0 {
		return
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	_, _ = ms.wdb.Exec(`DELETE FROM metadata_cache WHERE fetched_at < (
		SELECT fetched_at FROM metadata_cache ORDER BY fetched_at DESC LIMIT 1 OFFSET ?)`,
		max-1,
	)
}

// Count returns the number of rows, expired or not. Test/diagnostic helper.
func (ms *MetadataCacheStore) Count() int {
	if ms == nil || ms.db == nil {
		return 0
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	var n int
	if err := ms.db.QueryRow(`SELECT COUNT(*) FROM metadata_cache`).Scan(&n); err != nil {
		return 0
	}
	return n
}
