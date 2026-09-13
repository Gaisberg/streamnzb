package logger

import (
	"sync"
	"time"
)

var (
	throttleMu   sync.Mutex
	throttleSeen = map[string]time.Time{} // key → time of the last permitted log
)

// throttleMaxKeys bounds throttleSeen. Keys are never evicted individually —
// every distinct id a catalog rejects mints one — so without a bound a
// long-running process serving many different bad ids would grow the map
// forever. Past the bound the whole table is cleared: the next call for any
// key just passes again, which is the same behavior as a key seen for the
// first time.
const throttleMaxKeys = 10000

// Throttle reports whether a log line keyed by key may be written now: the
// first call for a key always passes, later calls pass once every interval.
// It keeps a dead provider or a stale profile from repeating the same warning
// on every request while still surfacing it.
func Throttle(key string, every time.Duration) bool {
	now := time.Now()
	throttleMu.Lock()
	defer throttleMu.Unlock()
	if last, ok := throttleSeen[key]; ok && now.Sub(last) < every {
		return false
	}
	if len(throttleSeen) >= throttleMaxKeys {
		clear(throttleSeen)
	}
	throttleSeen[key] = now
	return true
}
