package nfs

import (
	"context"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

const (
	// titleTimeout bounds one metadata lookup during title resolution.
	titleTimeout = 8 * time.Second
	// negativeTitleTTL is how long an unanswered title stays unanswered
	// before it is asked again.
	negativeTitleTTL = 15 * time.Minute
)

// TitleFunc names the title a library row belongs to, with its year; ok
// false when no metadata source could.
type TitleFunc func(ctx context.Context, item *persistence.LibraryItem) (name string, year int, ok bool)

type titleEntry struct {
	name string
	year int
	ok   bool
	at   time.Time
}

// titleCache remembers what the metadata sources said a row's title is, for
// the library folder's names. Lookups never run on a client's call: the tree
// is built from what is cached, and misses are resolved on one background
// goroutine that marks the tree dirty when it learns something, so the next
// listing is renamed.
type titleCache struct {
	mu       sync.Mutex
	entries  map[string]titleEntry
	inflight bool
	resolve  TitleFunc
	changed  func()
}

func newTitleCache(resolve TitleFunc, changed func()) *titleCache {
	return &titleCache{entries: make(map[string]titleEntry), resolve: resolve, changed: changed}
}

// titleKey groups the rows of one title: same ids, same side of the
// movie/series line (which decides what is looked up).
func titleKey(item *persistence.LibraryItem) string {
	kind := "series"
	if isMovieLike(item) {
		kind = "movie"
	}
	return kind + "|" + item.ContentType + "|" + item.ContentID + "|" + item.ImdbID + "|" + item.TmdbID + "|" + item.TvdbID + "|" + item.KitsuID
}

func (tc *titleCache) cached(item *persistence.LibraryItem) (string, int, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	e, ok := tc.entries[titleKey(item)]
	if !ok || !e.ok {
		return "", 0, false
	}
	return e.name, e.year, true
}

// resolveMissing looks up every row the cache has no (current) answer for.
// One pass runs at a time; a call during a pass is a no-op, and the next
// rebuild asks again.
func (tc *titleCache) resolveMissing(ctx context.Context, items []*persistence.LibraryItem) {
	if tc.resolve == nil {
		return
	}
	tc.mu.Lock()
	if tc.inflight {
		tc.mu.Unlock()
		return
	}
	now := time.Now()
	var todo []*persistence.LibraryItem
	seen := make(map[string]bool)
	for _, item := range items {
		if item == nil {
			continue
		}
		key := titleKey(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		if e, ok := tc.entries[key]; ok && (e.ok || now.Sub(e.at) < negativeTitleTTL) {
			continue
		}
		todo = append(todo, item)
	}
	if len(todo) == 0 {
		tc.mu.Unlock()
		return
	}
	tc.inflight = true
	tc.mu.Unlock()

	go func() {
		learned := 0
		defer func() {
			tc.mu.Lock()
			tc.inflight = false
			tc.mu.Unlock()
			if learned > 0 && tc.changed != nil {
				tc.changed()
			}
		}()
		for _, item := range todo {
			if ctx.Err() != nil {
				return
			}
			lookupCtx, cancel := context.WithTimeout(ctx, titleTimeout)
			name, year, ok := tc.resolve(lookupCtx, item)
			cancel()
			if !ok {
				logger.Debug("NFS export could not resolve a title", "content_id", item.ContentID, "imdb", item.ImdbID, "tmdb", item.TmdbID, "tvdb", item.TvdbID, "release", item.ReleaseTitle)
			}
			tc.mu.Lock()
			tc.entries[titleKey(item)] = titleEntry{name: name, year: year, ok: ok && strings.TrimSpace(name) != "", at: time.Now()}
			tc.mu.Unlock()
			if ok {
				learned++
			}
		}
		if learned > 0 {
			logger.Debug("NFS export resolved titles", "count", learned, "asked", len(todo))
		}
	}()
}
