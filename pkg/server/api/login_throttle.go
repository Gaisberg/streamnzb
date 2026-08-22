package api

import (
	"sync"
	"time"
)

// The admin login is the one endpoint that accepts a guessable secret, and
// argon2id alone only makes each guess expensive — it does not make an
// unlimited number of them impossible. These numbers leave a mistyped password
// unpunished and turn a sustained guessing run into minutes per attempt.
const (
	// Failures allowed from one address before any delay applies.
	loginFreeAttempts = 4
	loginBaseDelay    = 2 * time.Second
	loginMaxDelay     = 15 * time.Minute
	// How long an address is remembered after its last attempt. A locked-out
	// admin gets back in by waiting rather than by restarting the process.
	loginAttemptTTL = 30 * time.Minute
	// Hard ceiling on tracked addresses, so a run from rotating source
	// addresses cannot grow the map without bound between TTL sweeps.
	loginMaxTrackedAddresses = 4096
)

// loginThrottle delays repeated failed logins per client address. The zero
// value is ready to use.
//
// The address is httpx.ClientIP: where the connection actually came from,
// never a forwarded header — see the reasoning there. Behind a reverse proxy
// that collapses every client onto the proxy's address, so the backoff becomes
// global rather than per-client. Acceptable for a single-admin app, and the
// penalty expires on its own rather than needing a restart.
type loginThrottle struct {
	mu       sync.Mutex
	attempts map[string]*loginAttemptState
}

type loginAttemptState struct {
	failures    int
	nextAllowed time.Time
	lastSeen    time.Time
}

// retryAfter reports how long addr must wait before its next attempt is
// accepted, or zero when it may proceed. The returned duration is rounded up to
// whole seconds so it can be sent as a Retry-After header without advertising a
// deadline that has not actually passed yet.
func (t *loginThrottle) retryAfter(addr string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.attempts[addr]
	if !ok || !now.Before(state.nextAllowed) {
		return 0
	}
	remaining := state.nextAllowed.Sub(now)
	if rounded := remaining.Truncate(time.Second); rounded != remaining {
		remaining = rounded + time.Second
	}
	return remaining
}

// recordFailure counts a rejected login for addr and extends its penalty.
func (t *loginThrottle) recordFailure(addr string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.attempts == nil {
		t.attempts = make(map[string]*loginAttemptState)
	}
	t.pruneLocked(now)

	state, ok := t.attempts[addr]
	if !ok {
		state = &loginAttemptState{}
		t.attempts[addr] = state
	}
	state.failures++
	state.lastSeen = now
	if delay := loginDelayFor(state.failures); delay > 0 {
		state.nextAllowed = now.Add(delay)
	}
}

// recordSuccess forgets addr entirely: a correct password proves the attempts
// before it were the admin fumbling, not an attacker making progress.
func (t *loginThrottle) recordSuccess(addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, addr)
}

// pruneLocked drops entries nobody has touched within loginAttemptTTL, then —
// if the map is still over its ceiling — every entry not currently serving a
// penalty.
//
// The first pass needs no penalty check: loginMaxDelay is shorter than
// loginAttemptTTL and lastSeen advances with every failure, so an entry idle
// long enough to expire has necessarily served its delay already. The second
// pass does check, because it ignores age entirely — without the guard, an
// attacker could clear their own backoff by flooding the map with addresses.
func (t *loginThrottle) pruneLocked(now time.Time) {
	for addr, state := range t.attempts {
		if now.Sub(state.lastSeen) > loginAttemptTTL {
			delete(t.attempts, addr)
		}
	}
	if len(t.attempts) <= loginMaxTrackedAddresses {
		return
	}
	for addr, state := range t.attempts {
		if !now.Before(state.nextAllowed) {
			delete(t.attempts, addr)
		}
	}
}

// loginDelayFor returns how long to hold off the attempt that follows the nth
// consecutive failure: nothing while the address is still inside its
// loginFreeAttempts, then doubling from loginBaseDelay up to loginMaxDelay.
func loginDelayFor(failures int) time.Duration {
	excess := failures - loginFreeAttempts
	if excess < 0 {
		return 0
	}
	// Cap the shift before it happens; by this point the doubling has long
	// since passed loginMaxDelay, and a wide shift would overflow instead.
	if excess >= 32 {
		return loginMaxDelay
	}
	delay := loginBaseDelay << excess
	if delay > loginMaxDelay || delay <= 0 {
		return loginMaxDelay
	}
	return delay
}
