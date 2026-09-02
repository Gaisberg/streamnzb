package nntp

import (
	"context"
	"strings"
	"sync"
)

// connBudget is the connection ceiling for one provider account, shared by
// every ClientPool that dials that account.
//
// A provider enforces its limit per account, not per pool. Before this budget
// existed each ClientPool policed only itself, so anything that built a second
// pool for the same account — a speed test, a settings-page probe, a reload
// that rebuilt the streaming pool while the old one still held live playback
// connections — dialed on top of the first pool's connections and tripped the
// provider's limit. Every dial now takes a permit here first, whichever pool
// issues it, so the account as a whole can never hold more than the configured
// count.
//
// The limit is the largest claim among the live pools on the account. Claims
// are per pool rather than summed because two pools on one account are two
// views of the same allowance, not two allowances.
type connBudget struct {
	key string

	mu      sync.Mutex
	limit   int
	inUse   int
	claims  map[*ClientPool]int
	waiters []*budgetWaiter
}

// budgetWaiter is one blocked permit request. granted closes exactly once,
// when the permit is handed over; the waiter then owns it and must release it
// (or hand it to a connection that will).
type budgetWaiter struct {
	granted chan struct{}
	served  bool
}

// The registry lock is always taken before a budget's own lock, never after.
var (
	budgetsMu sync.Mutex
	budgets   = map[string]*connBudget{}
)

// budgetKey names the account a pool dials. Port is deliberately left out: the
// same account on 119 and 563 is still one account to the provider.
func budgetKey(host, user string) string {
	return strings.ToLower(strings.TrimSpace(host)) + "|" + strings.TrimSpace(user)
}

// claimBudget registers p's claim on the account's budget and returns it. A
// raised limit hands permits to anyone already waiting.
func claimBudget(p *ClientPool, host, user string, maxConn int) *connBudget {
	key := budgetKey(host, user)
	budgetsMu.Lock()
	defer budgetsMu.Unlock()
	b := budgets[key]
	if b == nil {
		b = &connBudget{key: key, claims: map[*ClientPool]int{}}
		budgets[key] = b
	}
	b.mu.Lock()
	b.claims[p] = maxConn
	b.recomputeLimitLocked()
	b.mu.Unlock()
	return b
}

// dropClaim removes p from the budget. Permits its connections still hold are
// released when those connections close, so a shrinking limit never cuts a
// live connection — it only stops new ones until the count is back under it.
func (b *connBudget) dropClaim(p *ClientPool) {
	b.mu.Lock()
	delete(b.claims, p)
	b.recomputeLimitLocked()
	b.mu.Unlock()
	b.retire()
}

// retire drops the budget from the registry once nothing references it: no
// pool claims it and no connection holds one of its permits. Keeping it
// registered while permits are out matters — a pool re-pointed at this account
// must see the connections its predecessor still has open.
func (b *connBudget) retire() {
	budgetsMu.Lock()
	defer budgetsMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.claims) == 0 && b.inUse == 0 && budgets[b.key] == b {
		delete(budgets, b.key)
	}
}

func (b *connBudget) recomputeLimitLocked() {
	limit := 0
	for _, n := range b.claims {
		if n > limit {
			limit = n
		}
	}
	b.limit = limit
	b.grantLocked()
}

// grantLocked hands permits to waiters, oldest first, while there is room.
func (b *connBudget) grantLocked() {
	for len(b.waiters) > 0 && b.inUse < b.limit {
		w := b.waiters[0]
		b.waiters = b.waiters[1:]
		b.inUse++
		w.served = true
		close(w.granted)
	}
}

// tryAcquire takes a permit if one is free right now.
func (b *connBudget) tryAcquire() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inUse >= b.limit {
		return false
	}
	b.inUse++
	return true
}

// wait queues for a permit. The returned waiter's granted channel closes when
// the permit is the caller's; a caller that stops waiting for any reason must
// call cancel so a permit granted in the meantime is not stranded.
func (b *connBudget) wait() *budgetWaiter {
	w := &budgetWaiter{granted: make(chan struct{})}
	b.mu.Lock()
	b.waiters = append(b.waiters, w)
	b.grantLocked()
	b.mu.Unlock()
	return w
}

// cancel withdraws a waiter. If the permit was already granted it is handed
// straight back.
func (b *connBudget) cancel(w *budgetWaiter) {
	b.mu.Lock()
	if w.served {
		b.releaseLocked()
		b.mu.Unlock()
		return
	}
	for i, queued := range b.waiters {
		if queued == w {
			b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
			break
		}
	}
	b.mu.Unlock()
}

// acquire blocks for a permit until ctx ends.
func (b *connBudget) acquire(ctx context.Context) bool {
	if b.tryAcquire() {
		return true
	}
	w := b.wait()
	select {
	case <-w.granted:
		return true
	case <-ctx.Done():
		b.cancel(w)
		return false
	}
}

// release returns one permit, passing it on to the oldest waiter if any.
func (b *connBudget) release() {
	b.mu.Lock()
	b.releaseLocked()
	orphaned := len(b.claims) == 0 && b.inUse == 0
	b.mu.Unlock()
	if orphaned {
		b.retire()
	}
}

func (b *connBudget) releaseLocked() {
	if b.inUse > 0 {
		b.inUse--
	}
	b.grantLocked()
}

// InUse reports how many permits are out across every pool on the account.
func (b *connBudget) InUse() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inUse
}

// Limit reports the account ceiling currently in force.
func (b *connBudget) Limit() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit
}
