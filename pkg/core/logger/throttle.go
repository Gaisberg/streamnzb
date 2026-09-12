package logger

import (
	"sync"
	"time"
)

var throttleSeen sync.Map // key → time.Time of the last permitted log

// Throttle reports whether a log line keyed by key may be written now: the
// first call for a key always passes, later calls pass once every interval.
// It keeps a dead provider or a stale profile from repeating the same warning
// on every request while still surfacing it.
func Throttle(key string, every time.Duration) bool {
	now := time.Now()
	last, loaded := throttleSeen.LoadOrStore(key, now)
	if !loaded {
		return true
	}
	if now.Sub(last.(time.Time)) < every {
		return false
	}
	throttleSeen.Store(key, now)
	return true
}
