package nntp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// budgetTestUser keeps these tests off the "user" account the other pool tests
// dial, so a connection one of them leaves open cannot skew a ceiling here.
const budgetTestUser = "budget"

// TestBudgetSharedAcrossPoolsOnOneAccount: two pools on the same account — even
// on different ports — draw on one ceiling. The provider counts the account,
// so a second pool must not be able to dial on top of the first pool's
// connections.
func TestBudgetSharedAcrossPoolsOnOneAccount(t *testing.T) {
	testHealthRegistry(t)
	host, portA := fakeNNTPListener(t, "accept")
	_, portB := fakeNNTPListener(t, "accept")
	a := NewClientPool(host, portA, false, budgetTestUser, "good", 2)
	t.Cleanup(a.Shutdown)
	b := NewClientPool(host, portB, false, budgetTestUser, "good", 2)
	t.Cleanup(b.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	held := make([]*Client, 0, 2)
	for i := 0; i < 2; i++ {
		c, err := a.Get(ctx)
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		held = append(held, c)
	}
	if inUse, limit := b.AccountConnections(); inUse != 2 || limit != 2 {
		t.Fatalf("AccountConnections = %d/%d, want 2/2", inUse, limit)
	}
	if c, ok := b.TryGet(ctx); ok {
		b.Discard(c)
		t.Fatal("second pool dialed past the account ceiling")
	}

	// A permit freed by one pool is a permit the other can use.
	a.Discard(held[0])
	c, err := b.Get(ctx)
	if err != nil {
		t.Fatalf("Get after Discard on the other pool: %v", err)
	}
	b.Discard(c)
	a.Discard(held[1])
	if inUse, _ := a.AccountConnections(); inUse != 0 {
		t.Fatalf("AccountConnections in use = %d after every connection closed, want 0", inUse)
	}
}

// TestBudgetLimitFollowsLargestLiveClaim: the ceiling is the largest claim, not
// the sum, and a pool that shuts down takes its claim with it.
func TestBudgetLimitFollowsLargestLiveClaim(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	small := NewClientPool(host, port, false, budgetTestUser, "good", 2)
	t.Cleanup(small.Shutdown)
	big := NewClientPool(host, port, false, budgetTestUser, "good", 5)

	if _, limit := small.AccountConnections(); limit != 5 {
		t.Fatalf("limit with both pools = %d, want 5", limit)
	}
	big.Shutdown()
	if _, limit := small.AccountConnections(); limit != 2 {
		t.Fatalf("limit after the larger pool shut down = %d, want 2", limit)
	}
}

// TestBudgetCancelReturnsGrantedPermit: a waiter that stops waiting after its
// permit was granted — a Get whose context ended in the same instant — must
// hand the permit back, or the account would leak a slot per such race.
func TestBudgetCancelReturnsGrantedPermit(t *testing.T) {
	b := &connBudget{key: "test", claims: map[*ClientPool]int{}, limit: 1}
	if !b.tryAcquire() {
		t.Fatal("first acquire should succeed")
	}
	w := b.wait()
	select {
	case <-w.granted:
		t.Fatal("waiter granted while the budget was full")
	default:
	}
	b.release()
	select {
	case <-w.granted:
	default:
		t.Fatal("release did not hand the permit to the waiter")
	}
	b.cancel(w)
	if b.InUse() != 0 {
		t.Fatalf("InUse = %d after cancelling a granted waiter, want 0", b.InUse())
	}
	if !b.tryAcquire() {
		t.Fatal("permit not reusable after the granted waiter cancelled")
	}
}

// TestGetAfterShutdownRefuses: a pool that has been torn down must not dial.
// Before this, Get kept working after Shutdown and every fetch on an old pool
// cost the account a handshake alongside the pool that replaced it.
func TestGetAfterShutdownRefuses(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, budgetTestUser, "good", 2)
	pool.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Get(ctx); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Get after Shutdown = %v, want ErrPoolClosed", err)
	}
	if _, ok := pool.TryGet(ctx); ok {
		t.Fatal("TryGet after Shutdown handed out a connection")
	}
	wantConns(t, pool, "after refused Get", 0, 0, 0, 0)
}

// TestReconnectAfterCloseRefuses: a connection a watchdog discarded must stay
// closed. Reconnect used to redial it into an orphan the pool never counted.
func TestReconnectAfterCloseRefuses(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, budgetTestUser, "good", 1)
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	pool.Discard(c)
	if err := c.Reconnect(); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Reconnect on a discarded connection = %v, want ErrClientClosed", err)
	}
	wantConns(t, pool, "after refused Reconnect", 0, 0, 0, 0)
	if inUse, _ := pool.AccountConnections(); inUse != 0 {
		t.Fatalf("AccountConnections in use = %d, want 0", inUse)
	}
}

// TestReconfigureRetiresOldTargetConnections: after a re-point, connections
// dialed against the previous settings are closed on return instead of pooled,
// idle ones are closed at once, and a same-target Reconfigure keeps them.
func TestReconfigureRetiresOldTargetConnections(t *testing.T) {
	testHealthRegistry(t)
	host, port := fakeNNTPListener(t, "accept")
	_, otherPort := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, budgetTestUser, "good", 3)
	t.Cleanup(pool.Shutdown)

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
	wantConns(t, pool, "before Reconfigure", 2, 1, 1, 0)

	if changed := pool.Reconfigure(host, port, false, budgetTestUser, "good", 2); changed {
		t.Fatal("a connection-count change alone reported the target as changed")
	}
	wantConns(t, pool, "after same-target Reconfigure", 2, 1, 1, 0)
	if pool.MaxConn() != 2 {
		t.Fatalf("MaxConn = %d, want 2", pool.MaxConn())
	}

	if changed := pool.Reconfigure(host, otherPort, false, budgetTestUser, "good", 2); !changed {
		t.Fatal("a port change did not report the target as changed")
	}
	wantConns(t, pool, "after re-point", 1, 0, 1, 0)
	pool.Put(held)
	wantConns(t, pool, "old connection returned", 0, 0, 0, 0)

	c, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Get on the new target: %v", err)
	}
	pool.Put(c)
	wantConns(t, pool, "new connection pooled", 1, 1, 0, 0)
}
