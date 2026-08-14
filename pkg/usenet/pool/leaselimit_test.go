package pool

import (
	"context"
	"testing"
	"time"
)

func TestLeaseLimiterCapsConcurrentPermits(t *testing.T) {
	limiter := newLeaseLimiter("stream-a", map[string]int{"eweka": 2})

	first, ok := limiter.tryAcquire("eweka")
	if !ok {
		t.Fatal("first permit should be granted")
	}
	second, ok := limiter.tryAcquire("eweka")
	if !ok {
		t.Fatal("second permit should be granted")
	}
	if _, ok := limiter.tryAcquire("eweka"); ok {
		t.Fatal("third permit exceeds the cap and must be refused")
	}

	first()
	third, ok := limiter.tryAcquire("eweka")
	if !ok {
		t.Fatal("releasing a permit should free a slot")
	}

	// Releasing twice must not hand back a slot that was never taken.
	first()
	if _, ok := limiter.tryAcquire("eweka"); ok {
		t.Fatal("a double release must not inflate the cap")
	}
	second()
	third()
}

func TestLeaseLimiterLeavesUnlistedProvidersUncapped(t *testing.T) {
	limiter := newLeaseLimiter("stream-a", map[string]int{"eweka": 1})
	for i := 0; i < 50; i++ {
		if _, ok := limiter.tryAcquire("newshosting"); !ok {
			t.Fatalf("uncapped provider refused a permit on attempt %d", i+1)
		}
	}
}

func TestLeaseLimiterAcquireWaitsThenGivesUpWithContext(t *testing.T) {
	limiter := newLeaseLimiter("stream-a", map[string]int{"eweka": 1})
	held, ok := limiter.tryAcquire("eweka")
	if !ok {
		t.Fatal("expected the first permit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, ok := limiter.acquire(ctx, "eweka"); ok {
		t.Fatal("acquire should have given up when the cap stayed full")
	}

	held()
	if _, ok := limiter.acquire(context.Background(), "eweka"); !ok {
		t.Fatal("acquire should succeed once a permit is free")
	}
}

func TestLeaseRegistrySharesOneBudgetPerLease(t *testing.T) {
	registry := newLeaseRegistry()
	limits := map[string]int{"eweka": 1}

	// Two subsets of the same stream must not each get their own allowance.
	first := registry.limiter("alice", limits)
	second := registry.limiter("alice", limits)
	if first != second {
		t.Fatal("the same lease key must resolve to one limiter")
	}
	if _, ok := first.tryAcquire("eweka"); !ok {
		t.Fatal("expected the first permit")
	}
	if _, ok := second.tryAcquire("eweka"); ok {
		t.Fatal("a second subset must draw on the same budget, not a fresh one")
	}

	// A different stream has its own budget.
	other := registry.limiter("bob", limits)
	if _, ok := other.tryAcquire("eweka"); !ok {
		t.Fatal("a different lease should have its own allowance")
	}
}

func TestLeaseRegistryIgnoresEmptyLeaseOrLimits(t *testing.T) {
	registry := newLeaseRegistry()
	if limiter := registry.limiter("", map[string]int{"eweka": 1}); limiter != nil {
		t.Fatal("an empty lease key must not create a limiter")
	}
	if limiter := registry.limiter("alice", nil); limiter != nil {
		t.Fatal("no limits means no limiter")
	}
}

func TestLeaseLimiterAppliesRaisedLimit(t *testing.T) {
	registry := newLeaseRegistry()
	limiter := registry.limiter("alice", map[string]int{"eweka": 1})
	if _, ok := limiter.tryAcquire("eweka"); !ok {
		t.Fatal("expected the first permit")
	}
	if _, ok := limiter.tryAcquire("eweka"); ok {
		t.Fatal("cap of 1 should refuse a second permit")
	}

	// Saving a higher cap must take effect for connections taken from then on.
	registry.limiter("alice", map[string]int{"eweka": 3})
	for i := 0; i < 3; i++ {
		if _, ok := limiter.tryAcquire("eweka"); !ok {
			t.Fatalf("raised cap should grant permit %d", i+1)
		}
	}
}
