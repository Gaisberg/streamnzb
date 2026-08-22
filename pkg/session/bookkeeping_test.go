package session

import (
	"sync"
	"testing"
)

const testKey OnceKey = "test-key"

func TestOnceClaimsExactlyOnce(t *testing.T) {
	s := &Session{ID: "sess-1"}

	if s.OnceDone(testKey) {
		t.Fatal("a fresh session should have nothing claimed")
	}
	if !s.Once(testKey) {
		t.Fatal("the first claim should succeed")
	}
	if !s.OnceDone(testKey) {
		t.Fatal("the key should read as claimed")
	}
	if s.Once(testKey) {
		t.Fatal("the second claim should fail")
	}

	s.ResetOnce(testKey)
	if s.OnceDone(testKey) {
		t.Fatal("ResetOnce should release the key")
	}
	if !s.Once(testKey) {
		t.Fatal("the key should be claimable again after a reset")
	}
}

func TestOnceIsSafeUnderConcurrency(t *testing.T) {
	// This is the actual working condition: players re-request the same URL
	// while the first attempt is in flight, and every one of those requests
	// races to record the same history row.
	s := &Session{ID: "sess-1"}

	const racers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0

	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if s.Once(testKey) {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("%d callers claimed the key, want exactly 1", winners)
	}
}

func TestOnceIsPerSession(t *testing.T) {
	// Session IDs are slot paths and slot paths are reused: a later play of the
	// same slot is a new session and must be able to record for itself.
	first := &Session{ID: "stream:movie:tt1:0"}
	second := &Session{ID: "stream:movie:tt1:0"}

	if !first.Once(testKey) {
		t.Fatal("the first session should claim the key")
	}
	if !second.Once(testKey) {
		t.Fatal("a new session at the same slot ID should start with nothing claimed")
	}
}

func TestOnceOnNilSessionClaimsNothing(t *testing.T) {
	var s *Session
	if s.Once(testKey) {
		t.Fatal("a nil session must never hand out a claim")
	}
	if s.OnceDone(testKey) {
		t.Fatal("a nil session has nothing claimed")
	}
	s.ResetOnce(testKey) // must not panic
}

func TestDeferredTokenSupersedes(t *testing.T) {
	s := &Session{ID: "sess-1"}

	first := s.BeginDeferred()
	if !s.DeferredIsCurrent(first) {
		t.Fatal("the only deferred task should be current")
	}

	second := s.BeginDeferred()
	if first == second {
		t.Fatal("two deferred tasks must get different tokens")
	}
	if s.DeferredIsCurrent(first) {
		t.Fatal("the superseded task should no longer be current")
	}
	if !s.DeferredIsCurrent(second) {
		t.Fatal("the newest task should be current")
	}

	s.CancelDeferred()
	if s.DeferredIsCurrent(second) {
		t.Fatal("a cancelled task should no longer be current")
	}

	// The zero token names nothing, so a caller that never started a task
	// cannot accidentally match one.
	if s.DeferredIsCurrent(0) {
		t.Fatal("the zero token must never be current")
	}
}
