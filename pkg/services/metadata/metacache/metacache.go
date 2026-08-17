// Package metacache layers an in-memory L1 over the persistent metadata_cache
// table (L2). Metadata clients are rebuilt on every config reload, which used
// to throw their in-memory caches away; with the L2 shared through the
// persistence singleton, a rebuild only costs the L1.
//
// Keys are request paths relative to a client's base URL (never absolute URLs)
// so test-stub base URLs cannot poison persisted entries, and auth — which
// lives in headers — never reaches the key.
package metacache

import (
	"sync"
	"time"

	"streamnzb/pkg/core/persistence"
)

type Cache struct {
	store  *persistence.MetadataCacheStore
	source string
	l1     sync.Map // key -> l1Entry
}

type l1Entry struct {
	body    []byte
	expires time.Time
}

// New builds a cache for one provider source ("tmdb", "kitsu", ...). A nil
// store is fine: the cache then works as a plain in-memory cache, which keeps
// constructors and tests that have no database working unchanged.
func New(store *persistence.MetadataCacheStore, source string) *Cache {
	return &Cache{store: store, source: source}
}

// Get returns the cached body for key, consulting L1 then the persistent L2.
// An L2 hit is promoted into L1 so repeat lookups skip the database.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	if v, ok := c.l1.Load(key); ok {
		ent := v.(l1Entry)
		if time.Now().Before(ent.expires) {
			return ent.body, true
		}
		c.l1.Delete(key)
	}
	body, ok := c.store.Get(c.source, key)
	if !ok {
		return nil, false
	}
	// The row's exact remaining TTL is not read back; promote with a short L1
	// lease so the entry re-checks L2 (and its real expiry) periodically.
	c.l1.Store(key, l1Entry{body: body, expires: time.Now().Add(l1PromoteTTL)})
	return body, true
}

// l1PromoteTTL bounds how long an L2 hit is served from memory without
// re-checking the persistent row's expiry.
const l1PromoteTTL = 15 * time.Minute

// Put stores the body in both layers with the given TTL.
func (c *Cache) Put(key string, body []byte, ttl time.Duration) {
	if c == nil || key == "" || len(body) == 0 || ttl <= 0 {
		return
	}
	c.l1.Store(key, l1Entry{body: body, expires: time.Now().Add(ttl)})
	c.store.Put(c.source, key, body, ttl)
}
