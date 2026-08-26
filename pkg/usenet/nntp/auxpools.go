package nntp

import (
	"strings"
	"sync"
)

// Auxiliary pools are the short-lived ClientPools built outside the streaming
// set: speed tests, probes of providers that have no live pool, settings
// validation. Their connections are as real to the provider as the streaming
// pool's — they draw on the same account limit — so the dashboard must count
// them, and this registry is how stats finds them.
var (
	auxPoolsMu sync.Mutex
	auxPools   = map[*ClientPool]string{}
)

// TrackAux folds this pool's connections into the named provider's connection
// counts until Shutdown. Only pools outside the streaming set may register:
// stats reads the streaming pools directly, and a registered one would be
// counted twice.
func (p *ClientPool) TrackAux(name string) {
	name = strings.TrimSpace(name)
	if p == nil || name == "" {
		return
	}
	auxPoolsMu.Lock()
	auxPools[p] = name
	auxPoolsMu.Unlock()
}

// untrackAux removes the pool from the registry; Shutdown calls it so a
// finished speed test or probe stops contributing the moment it is torn down.
func (p *ClientPool) untrackAux() {
	auxPoolsMu.Lock()
	delete(auxPools, p)
	auxPoolsMu.Unlock()
}

// AuxConnStats sums the connection counts of every live auxiliary pool
// registered under name. Max is deliberately left zero: auxiliary pools borrow
// the account's headroom, they do not extend the configured ceiling.
func AuxConnStats(name string) ConnSnapshot {
	auxPoolsMu.Lock()
	pools := make([]*ClientPool, 0, 2)
	for pool, poolName := range auxPools {
		if poolName == name {
			pools = append(pools, pool)
		}
	}
	auxPoolsMu.Unlock()

	var out ConnSnapshot
	// ConnStats reads channel lengths, so it is taken outside the registry lock.
	for _, pool := range pools {
		s := pool.ConnStats()
		out.Total += s.Total
		out.Idle += s.Idle
		out.Active += s.Active
		out.Pending += s.Pending
	}
	return out
}
