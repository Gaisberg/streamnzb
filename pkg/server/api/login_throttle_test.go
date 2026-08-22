package api

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginThrottleAllowsTheFirstFailures(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < loginFreeAttempts; i++ {
		if wait := throttle.retryAfter("10.0.0.1", now); wait != 0 {
			t.Fatalf("failure %d was delayed by %v; the first %d must be free", i+1, wait, loginFreeAttempts)
		}
		throttle.recordFailure("10.0.0.1", now)
	}

	if wait := throttle.retryAfter("10.0.0.1", now); wait != loginBaseDelay {
		t.Fatalf("after %d failures the wait was %v, want %v", loginFreeAttempts, wait, loginBaseDelay)
	}
}

func TestLoginThrottleBacksOffAndCaps(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < loginFreeAttempts; i++ {
		throttle.recordFailure("10.0.0.1", now)
	}

	previous := time.Duration(0)
	for i := 0; i < 30; i++ {
		throttle.recordFailure("10.0.0.1", now)
		wait := throttle.retryAfter("10.0.0.1", now)
		if wait > loginMaxDelay {
			t.Fatalf("wait %v exceeded the cap of %v", wait, loginMaxDelay)
		}
		if wait < previous {
			t.Fatalf("wait went backwards: %v after %v", wait, previous)
		}
		previous = wait
	}
	if previous != loginMaxDelay {
		t.Fatalf("sustained failures settled at %v, want the cap %v", previous, loginMaxDelay)
	}
}

func TestLoginThrottleIsPerAddress(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < loginFreeAttempts+3; i++ {
		throttle.recordFailure("10.0.0.1", now)
	}

	if wait := throttle.retryAfter("10.0.0.1", now); wait == 0 {
		t.Fatal("the failing address was not throttled")
	}
	if wait := throttle.retryAfter("10.0.0.2", now); wait != 0 {
		t.Fatalf("an unrelated address was delayed by %v", wait)
	}
}

func TestLoginThrottleExpiresAndClears(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < loginFreeAttempts+1; i++ {
		throttle.recordFailure("10.0.0.1", now)
	}
	if wait := throttle.retryAfter("10.0.0.1", now); wait == 0 {
		t.Fatal("expected a penalty after repeated failures")
	}

	// Waiting out the penalty is enough; no restart required.
	later := now.Add(loginMaxDelay + time.Second)
	if wait := throttle.retryAfter("10.0.0.1", later); wait != 0 {
		t.Fatalf("the penalty had not expired after %v: %v remaining", loginMaxDelay, wait)
	}

	// A correct password clears the history rather than leaving the admin
	// halfway to a longer backoff.
	for i := 0; i < loginFreeAttempts+1; i++ {
		throttle.recordFailure("10.0.0.2", now)
	}
	throttle.recordSuccess("10.0.0.2")
	if wait := throttle.retryAfter("10.0.0.2", now); wait != 0 {
		t.Fatalf("a successful login left %v of penalty behind", wait)
	}
	throttle.recordFailure("10.0.0.2", now)
	if wait := throttle.retryAfter("10.0.0.2", now); wait != 0 {
		t.Fatal("the failure count did not reset after a successful login")
	}
}

func TestLoginThrottlePrunesIdleAddresses(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	throttle.recordFailure("10.0.0.1", now)
	throttle.recordFailure("10.0.0.2", now)

	// Any later failure sweeps entries nobody has touched within the TTL.
	throttle.recordFailure("10.0.0.3", now.Add(loginAttemptTTL+time.Minute))

	throttle.mu.Lock()
	tracked := len(throttle.attempts)
	throttle.mu.Unlock()
	if tracked != 1 {
		t.Fatalf("expected the two idle addresses to be pruned, %d entries remain", tracked)
	}
}

func TestLoginThrottleKeepsPenalisedAddressesWhenOverCapacity(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	// One address deep enough into the backoff to be serving a real penalty.
	for i := 0; i < loginFreeAttempts+20; i++ {
		throttle.recordFailure("10.0.0.1", now)
	}
	// Then a flood of fresh addresses, none of them penalised yet. This is the
	// shape of an attacker trying to push their own lockout out of the map.
	for i := 0; i < loginMaxTrackedAddresses+10; i++ {
		throttle.recordFailure(fmt.Sprintf("198.51.100.%d", i), now)
	}

	if wait := throttle.retryAfter("10.0.0.1", now); wait == 0 {
		t.Fatal("the penalised address was evicted by a flood of unpenalised ones")
	}

	throttle.mu.Lock()
	tracked := len(throttle.attempts)
	throttle.mu.Unlock()
	if tracked > loginMaxTrackedAddresses {
		t.Fatalf("the map grew to %d entries, past its ceiling of %d", tracked, loginMaxTrackedAddresses)
	}
}

func TestLoginThrottleRoundsRetryAfterUp(t *testing.T) {
	var throttle loginThrottle
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < loginFreeAttempts+1; i++ {
		throttle.recordFailure("10.0.0.1", now)
	}

	// Retry-After is sent in whole seconds; truncating would advertise a
	// deadline that is still a fraction of a second too early.
	wait := throttle.retryAfter("10.0.0.1", now.Add(500*time.Millisecond))
	if wait%time.Second != 0 {
		t.Fatalf("wait %v is not a whole number of seconds", wait)
	}
	if wait < loginBaseDelay-500*time.Millisecond {
		t.Fatalf("wait %v rounded down past the real deadline", wait)
	}
}
