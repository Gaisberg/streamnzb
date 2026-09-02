package nntp

import (
	"context"
	"testing"
	"time"
)

// wantConns asserts one coherent ConnStats reading. Counters move inside the
// pool's own goroutines only during Shutdown teardown, so immediate reads are
// deterministic everywhere this helper is used.
func wantConns(t *testing.T, pool *ClientPool, label string, total, idle, active, pending int) {
	t.Helper()
	got := pool.ConnStats()
	if got.Total != total || got.Idle != idle || got.Active != active || got.Pending != pending {
		t.Fatalf("%s: ConnStats = total %d idle %d active %d pending %d, want total %d idle %d active %d pending %d",
			label, got.Total, got.Idle, got.Active, got.Pending, total, idle, active, pending)
	}
}

// TestConnStatsTracksConnectionLifecycle walks one connection through checkout,
// return, re-checkout and discard, asserting the counters describe established
// connections at every step.
func TestConnStatsTracksConnectionLifecycle(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, "user", "good", 2)
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wantConns(t, pool, "fresh pool", 0, 0, 0, 0)
	if pool.MaxConn() != 2 {
		t.Fatalf("MaxConn = %d, want 2", pool.MaxConn())
	}

	c, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantConns(t, pool, "after Get", 1, 0, 1, 0)

	pool.Put(c)
	wantConns(t, pool, "after Put", 1, 1, 0, 0)

	c, err = pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get from idle: %v", err)
	}
	wantConns(t, pool, "after re-Get", 1, 0, 1, 0)

	pool.Discard(c)
	wantConns(t, pool, "after Discard", 0, 0, 0, 0)
}

// TestConnStatsCountsNoConnectionOnAuthFailure: a dial that reaches the server
// but is refused at AUTHINFO must leave every counter at zero — the slot the
// attempt held is not a connection, and counting it was exactly the drift that
// showed connections the provider never saw.
func TestConnStatsCountsNoConnectionOnAuthFailure(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "reject")
	pool := NewClientPool(host, port, false, "user", "bad", 2)
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := pool.Get(ctx); err == nil {
		t.Fatal("expected the rejected AUTHINFO to fail the Get")
	}
	wantConns(t, pool, "after failed Get", 0, 0, 0, 0)
}

// TestConnStatsProbeBalances: a Probe takes an account permit like any other
// dial; its connection must be counted while open and uncounted — permit
// included — once torn down.
func TestConnStatsProbeBalances(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, "user", "good", 1)
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := pool.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	wantConns(t, pool, "after Probe", 0, 0, 0, 0)
	if inUse, _ := pool.AccountConnections(); inUse != 0 {
		t.Fatalf("AccountConnections in use = %d after Probe, want 0", inUse)
	}
}

// TestAuxConnStatsFollowsRegistration: a registered auxiliary pool contributes
// its connections under the provider's name only while it is alive — from
// TrackAux until Shutdown.
func TestAuxConnStatsFollowsRegistration(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, "user", "good", 2)
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if got := AuxConnStats("eweka"); got != (ConnSnapshot{}) {
		t.Fatalf("unregistered: AuxConnStats = %+v, want zero", got)
	}

	pool.TrackAux("eweka")
	c, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := AuxConnStats("eweka"); got.Total != 1 || got.Active != 1 {
		t.Fatalf("checked out: AuxConnStats = %+v, want total 1 active 1", got)
	}
	if got := AuxConnStats("other"); got != (ConnSnapshot{}) {
		t.Fatalf("wrong name: AuxConnStats = %+v, want zero", got)
	}

	pool.Put(c)
	if got := AuxConnStats("eweka"); got.Total != 1 || got.Idle != 1 || got.Active != 0 {
		t.Fatalf("idle: AuxConnStats = %+v, want total 1 idle 1", got)
	}

	pool.Shutdown()
	if got := AuxConnStats("eweka"); got != (ConnSnapshot{}) {
		t.Fatalf("after Shutdown: AuxConnStats = %+v, want zero", got)
	}
}

// TestConnStatsShutdownDrainsCounts: idle connections closed by Shutdown, and
// checked-out connections returned after it, must both come off the count.
func TestConnStatsShutdownDrainsCounts(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, "user", "good", 2)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	held, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get held: %v", err)
	}
	idle, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get idle: %v", err)
	}
	pool.Put(idle)
	wantConns(t, pool, "one held one idle", 2, 1, 1, 0)

	pool.Shutdown()
	wantConns(t, pool, "after Shutdown", 1, 0, 1, 0)

	// A connection still checked out at shutdown comes off the count when its
	// holder hands it back.
	pool.Put(held)
	wantConns(t, pool, "after post-shutdown Put", 0, 0, 0, 0)
}
