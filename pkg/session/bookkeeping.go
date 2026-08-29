package session

// Callers above this package need to do certain things at most once for the
// life of a session — record a preload row, record a failure, save a pending
// library entry, log a diagnostic. What those things *are* is not this
// package's business, so the state is generic: a set of claimed keys, and a
// token for work that is deferred and can be superseded.
//
// This lives on the session rather than in a map beside it because a session ID
// is a slot path, and slot paths are reused. Keyed off to the side, every flag
// needed a goroutine parked on Done() to delete it before the same slot came
// back — five such goroutines per live session, doing nothing but waiting to
// remove a map entry. Held here, the flags are gone when the session is, which
// is what "once per session" meant in the first place.

// OnceKey names a piece of per-session bookkeeping.
type OnceKey string

// Once reports whether key had not been claimed yet, claiming it if so. It
// returns true to exactly one caller per session, which makes it safe as a gate
// for concurrent requests against the same session — and those are routine,
// since players re-request a URL while the first attempt is still running.
func (s *Session) Once(key OnceKey) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, claimed := s.once[key]; claimed {
		return false
	}
	if s.once == nil {
		s.once = make(map[OnceKey]struct{}, 4)
	}
	s.once[key] = struct{}{}
	return true
}

// OnceDone reports whether key has been claimed, without claiming it.
func (s *Session) OnceDone(key OnceKey) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, claimed := s.once[key]
	return claimed
}

// ResetOnce releases key so it can be claimed again.
func (s *Session) ResetOnce(key OnceKey) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.once, key)
}

// CountEvent increments the per-session tally named by key and returns the new
// count. Counters follow the same lifecycle argument as the once-flags above:
// held on the session, they end when it does, so a reused slot path starts
// from zero under a fresh session.
func (s *Session) CountEvent(key OnceKey) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = make(map[OnceKey]int, 2)
	}
	s.counts[key]++
	return s.counts[key]
}

// BeginDeferred starts a deferred task on this session, superseding any earlier
// one, and returns the token naming it. The token is a per-session counter
// rather than a timestamp, so two tasks started in the same instant still get
// different tokens.
func (s *Session) BeginDeferred() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deferredToken++
	return s.deferredToken
}

// DeferredIsCurrent reports whether token still names the newest deferred task.
// A task whose token is stale has been superseded or cancelled and should stop
// without acting.
func (s *Session) DeferredIsCurrent(token int64) bool {
	if s == nil || token == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deferredToken == token
}

// CancelDeferred supersedes any in-flight deferred task.
func (s *Session) CancelDeferred() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deferredToken++
}
