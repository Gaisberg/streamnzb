package nntp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/health"
	"streamnzb/pkg/core/logger"
)

// ErrPoolClosed is returned by Get once Shutdown has run. A pool that has been
// torn down must not dial: its connections would be closed the moment they were
// returned, so every fetch on it cost a full handshake the provider counted
// against the account alongside whatever pool replaced it.
var ErrPoolClosed = errors.New("nntp: pool closed")

// dialTarget is everything a dial needs, snapshotted under the pool lock so a
// concurrent Reconfigure cannot hand a dial half of the old settings and half
// of the new.
type dialTarget struct {
	host string
	port int
	ssl  bool
	user string
	pass string
}

type ClientPool struct {
	// target, maxConn, budget and generation move together under mu when the
	// pool is reconfigured. generation is stamped on every connection dialed,
	// so a connection returned after a re-point is recognised as belonging to
	// the previous account and closed instead of pooled.
	target     dialTarget
	maxConn    int
	budget     *connBudget
	generation uint64

	idleClients chan *Client
	stopCh      chan struct{} // closed once by Shutdown(); never re-used

	// established counts connections that finished the handshake and are still
	// open — idle, checked out, or probing. It moves only in dial (on success)
	// and closeClient, so it tracks what the provider actually sees, where the
	// budget also counts dials still in progress. dialing counts those
	// in-progress dials.
	established atomic.Int64
	dialing     atomic.Int64

	bytesRead      int64
	totalBytesRead int64
	speed          SpeedMeter

	providerName string
	usageManager *ProviderUsageManager

	mu     sync.Mutex
	closed bool
}

func NewClientPool(host string, port int, ssl bool, user, pass string, maxConn int) *ClientPool {
	p := &ClientPool{
		target:      dialTarget{host: host, port: port, ssl: ssl, user: user, pass: pass},
		maxConn:     maxConn,
		idleClients: make(chan *Client, maxConn),
		stopCh:      make(chan struct{}),
	}
	p.budget = claimBudget(p, host, user, maxConn)

	go p.reaperLoop()
	return p
}

// Reconfigure re-points a live pool at new settings without replacing it, so
// the streams already fetching through it keep going. It reports whether the
// account or server changed — in which case the caller should Validate, since
// nothing has been dialed with the new credentials yet.
//
// Existing connections are handled by generation: idle ones to the old target
// are closed here, and checked-out ones are closed when they come back rather
// than pooled. Their permits return to the budget they were dialed under, so a
// pool moved to another account never counts its old connections against the
// new one, and the old account's budget keeps seeing them until they close.
func (p *ClientPool) Reconfigure(host string, port int, ssl bool, user, pass string, maxConn int) (targetChanged bool) {
	next := dialTarget{host: host, port: port, ssl: ssl, user: user, pass: pass}

	p.mu.Lock()
	targetChanged = next != p.target
	p.target = next
	p.maxConn = maxConn
	if targetChanged {
		p.generation++
	}
	// Same account resolves to the same budget, so this is a no-op claim update
	// unless the pool moved to another account.
	prev := p.budget
	p.budget = claimBudget(p, host, user, maxConn)
	if prev != p.budget {
		prev.dropClaim(p)
	}
	p.mu.Unlock()

	if targetChanged {
		p.drainIdle()
	}
	return targetChanged
}

// drainIdle closes every connection parked in the pool right now.
func (p *ClientPool) drainIdle() {
	for {
		select {
		case c := <-p.idleClients:
			p.closeClient(c)
		default:
			return
		}
	}
}

// SetProviderName names the pool for health reporting.
//
// It exists separately from SetUsageManager because the startup Validate dial
// happens before usage wiring, and that dial is the one most likely to meet a
// changed password: without a name at that point the rejection would be logged
// and the provider dropped, with nothing left for the user to see.
func (p *ClientPool) SetProviderName(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providerName = name
}

func (p *ClientPool) SetUsageManager(name string, mgr *ProviderUsageManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providerName = name
	p.usageManager = mgr
}

// snapshot reads the dial settings and the budget they draw on in one go.
func (p *ClientPool) snapshot() (dialTarget, *connBudget, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target, p.budget, p.generation
}

// dialsAs reports whether c was dialed with the settings the pool holds now.
func (p *ClientPool) dialsAs(c *Client) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return c.generation == p.generation
}

// dial opens one authenticated connection under a permit the caller already
// holds on budget, and reports what the handshake said about the account.
//
// On success the connection owns the permit and closeClient returns it; on
// failure the caller gives it back. That keeps the bookkeeping with the select
// arms that took the permit, which is why this returns rather than releasing.
//
// The health verdict is taken here because this is the only place the AUTHINFO
// exchange happens, and an authentication code means something specific only in
// that exchange. A successful handshake clears any stored verdict — the account
// working is the one piece of evidence that outranks a stored complaint.
func (p *ClientPool) dial(target dialTarget, budget *connBudget, generation uint64) (*Client, error) {
	p.dialing.Add(1)
	defer p.dialing.Add(-1)
	c, err := NewClient(target.host, target.port, target.ssl)
	if err != nil {
		// A connect failure is inconclusive about credentials: the box may be
		// down, DNS may be lying, the network may be out. It never blocks. The
		// one exception worth recording is a 502 greeting — providers answer it
		// when the account is already at its connection limit, which is a
		// self-healing degraded state, not an outage.
		p.reportGreeting(err)
		return nil, err
	}
	c.SetPool(p)
	c.budget = budget
	c.generation = generation
	if err := c.Authenticate(target.user, target.pass); err != nil {
		c.Quit()
		p.reportAuthResult(err)
		return nil, err
	}
	p.reportAuthResult(nil)
	p.established.Add(1)
	return c, nil
}

// dialWithPermit takes a permit on the pool's current budget without waiting
// and dials under it. ok is false when no permit was free.
func (p *ClientPool) dialWithPermit() (c *Client, ok bool, err error) {
	target, budget, generation := p.snapshot()
	if !budget.tryAcquire() {
		return nil, false, nil
	}
	c, err = p.dial(target, budget, generation)
	if err != nil {
		budget.release()
		return nil, true, err
	}
	return c, true, nil
}

// closeClient closes a connection this pool dialed and keeps the established
// count and the account budget in step with it. Every teardown of a
// successfully dialed connection must go through here, not c.Quit() directly —
// a bypassed close leaves the count claiming a connection the provider no
// longer sees, and the permit it held stranded.
func (p *ClientPool) closeClient(c *Client) {
	first, _ := c.close()
	if !first {
		return
	}
	p.established.Add(-1)
	if c.budget != nil {
		c.budget.release()
	}
}

// reportGreeting records what a refused greeting said about the account. The
// server has not seen credentials yet, so nothing here can block: a
// connection-limit refusal degrades with the fix named, and any other 502 is
// surfaced with the server's own words.
func (p *ClientPool) reportGreeting(err error) {
	p.mu.Lock()
	name := p.providerName
	p.mu.Unlock()
	if name == "" {
		return
	}
	reg := health.Global()
	switch {
	case IsConnectionLimit(err):
		reg.Report(health.KindProvider, name, health.StateDegraded, health.ReasonConnectionLimit, err.Error())
	case IsLoginRefused(err):
		reg.Report(health.KindProvider, name, health.StateDegraded, health.ReasonLoginRefused, err.Error())
	}
}

// reportAuthResult translates an AUTHINFO outcome into a health verdict.
//
// The connection-limit check runs before the credential check because a 502
// can carry either meaning and only its text tells them apart; a 502 whose
// text says neither is recorded as refused rather than guessed at.
func (p *ClientPool) reportAuthResult(err error) {
	p.mu.Lock()
	name := p.providerName
	p.mu.Unlock()
	if name == "" {
		return
	}
	reg := health.Global()
	switch {
	case err == nil:
		reg.MarkOK(health.KindProvider, name)
	case IsConnectionLimit(err):
		reg.Report(health.KindProvider, name, health.StateDegraded, health.ReasonConnectionLimit, err.Error())
	case IsAuthFailure(err):
		reg.Report(health.KindProvider, name, health.StateBlocked, health.ReasonAuthFailed, err.Error())
	case IsLoginRefused(err):
		reg.Report(health.KindProvider, name, health.StateDegraded, health.ReasonLoginRefused, err.Error())
	}
}

// Probe opens a throwaway authenticated connection to find out whether a
// blocked provider has started working again, then drops it.
//
// It bypasses the idle pool — the question is whether a fresh login works, and
// a parked connection cannot answer that — but not the account budget: the
// provider counts a probe like any other connection. The answer is the health
// verdict dial already recorded.
func (p *ClientPool) Probe(ctx context.Context) error {
	if p == nil {
		return nil
	}
	target, budget, generation := p.snapshot()
	if !budget.acquire(ctx) {
		return ctx.Err()
	}

	type result struct {
		c   *Client
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := p.dial(target, budget, generation)
		if err != nil {
			budget.release()
		}
		done <- result{c: c, err: err}
	}()

	select {
	case <-ctx.Done():
		// The dial outlives the cancelled probe; close whatever it produces so
		// a slow provider cannot leak a connection per probe.
		go func() {
			if r := <-done; r.c != nil {
				p.closeClient(r.c)
			}
		}()
		return ctx.Err()
	case r := <-done:
		if r.c != nil {
			p.closeClient(r.c)
		}
		return r.err
	}
}

func (p *ClientPool) RestoreTotalBytes(total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.totalBytesRead = total
	p.speed.Rebase(total)
}

func (p *ClientPool) TrackRead(n int) {
	p.mu.Lock()
	p.bytesRead += int64(n)
	p.totalBytesRead += int64(n)
	usageMgr := p.usageManager
	providerName := p.providerName
	p.mu.Unlock()

	if usageMgr != nil && providerName != "" && n > 0 {
		usageMgr.AddBytes(providerName, int64(n))
	}
}

func (p *ClientPool) GetSpeed() float64 {
	p.mu.Lock()
	total := p.totalBytesRead
	p.mu.Unlock()
	return p.speed.Rate(total)
}

func (p *ClientPool) TotalMegabytes() float64 {
	p.mu.Lock()
	usageMgr := p.usageManager
	providerName := p.providerName
	totalBytesRead := p.totalBytesRead
	p.mu.Unlock()

	if usageMgr != nil && providerName != "" {
		if usage := usageMgr.GetUsage(providerName); usage != nil {
			return float64(usage.TotalBytes) / (1024 * 1024)
		}
	}

	return float64(totalBytesRead) / (1024 * 1024)
}

// idleStaleThreshold: idle connections older than this are liveness-checked on
// checkout instead of trusted — providers drop idle TCP silently, and a dead
// pooled connection stalls its next command for the full 60s deadline.
const idleStaleThreshold = 60 * time.Second

// checkoutIdle validates a connection pulled from the idle channel. Dead ones,
// and ones dialed for a target the pool no longer has, are closed; the caller
// retries acquisition.
func (p *ClientPool) checkoutIdle(c *Client) *Client {
	if p.dialsAs(c) && c.HealthyForCheckout(idleStaleThreshold) {
		return c
	}
	logger.Debug("NNTP pool discarding stale idle connection", "host", p.Host())
	p.closeClient(c)
	return nil
}

func (p *ClientPool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Get hands out a connection: an idle one if there is one, a fresh dial if the
// account budget allows, otherwise whichever of the two frees up first.
func (p *ClientPool) Get(ctx context.Context) (*Client, error) {
	host := p.Host()
	logger.VerboseNNTP("nntp pool Get", "host", host)

	for {
		if p.isClosed() {
			return nil, ErrPoolClosed
		}

		select {
		case <-ctx.Done():
			logger.VerboseNNTP("pool.Get ctx.Done (idle check)", "host", host)
			return nil, ctx.Err()
		case c := <-p.idleClients:
			logger.VerboseNNTP("nntp pool Get from idle", "host", host)
			if c = p.checkoutIdle(c); c == nil {
				continue
			}
			return c, nil
		default:
		}

		if ctx.Err() != nil {
			logger.VerboseNNTP("pool.Get ctx.Done (permit check)", "host", host)
			return nil, ctx.Err()
		}
		c, ok, err := p.dialWithPermit()
		if ok {
			if err != nil {
				return nil, err
			}
			logger.VerboseNNTP("nntp pool Get new client", "host", host)
			return c, nil
		}

		waitStarted := time.Now()
		_, budget, _ := p.snapshot()
		w := budget.wait()
		select {
		case <-ctx.Done():
			budget.cancel(w)
			if wait := time.Since(waitStarted); wait >= 250*time.Millisecond {
				logger.Debug("NNTP pool wait exceeded threshold", "host", host, "wait", wait, "result", "context_canceled")
			}
			logger.VerboseNNTP("pool.Get ctx.Done (blocking)", "host", host)
			// Say what was being waited for: a connection test that times out
			// because playback holds every permit should read as that, not as
			// a provider that did not answer.
			return nil, fmt.Errorf("nntp: waiting for a free connection to %s (%d/%d in use on the account): %w",
				host, budget.InUse(), budget.Limit(), ctx.Err())
		case c := <-p.idleClients:
			budget.cancel(w)
			if wait := time.Since(waitStarted); wait >= 250*time.Millisecond {
				logger.Debug("NNTP pool wait exceeded threshold", "host", host, "wait", wait, "result", "idle_client")
			}
			logger.VerboseNNTP("nntp pool Get from idle (after block)", "host", host)
			if c = p.checkoutIdle(c); c == nil {
				continue
			}
			return c, nil
		case <-w.granted:
			wait := time.Since(waitStarted)
			// The permit was taken on the budget snapshotted above; a
			// Reconfigure that swapped budgets in the meantime is the rare
			// case, and dialing the old target under the old permit is still
			// consistent — the connection returns its permit to that budget.
			target, _, generation := p.snapshot()
			c, err := p.dial(target, budget, generation)
			if err != nil {
				budget.release()
				return nil, err
			}
			if wait >= 250*time.Millisecond {
				logger.Debug("NNTP pool wait exceeded threshold", "host", host, "wait", wait, "result", "new_client")
			}
			logger.VerboseNNTP("nntp pool Get new client (after block)", "host", host)
			return c, nil
		}
	}
}

// TryGet is Get without the wait: it returns false when nothing is idle and the
// account is at its limit.
func (p *ClientPool) TryGet(ctx context.Context) (*Client, bool) {
	for {
		if p.isClosed() {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return nil, false
		case c := <-p.idleClients:
			if c = p.checkoutIdle(c); c == nil {
				continue
			}
			return c, true
		default:
		}

		if ctx.Err() != nil {
			return nil, false
		}
		c, ok, err := p.dialWithPermit()
		if !ok || err != nil {
			return nil, false
		}
		return c, true
	}
}

func (p *ClientPool) Put(c *Client) {
	if c == nil {
		return
	}
	p.mu.Lock()
	closed := p.closed
	current := c.generation == p.generation
	p.mu.Unlock()
	if closed || !current {
		p.closeClient(c)
		return
	}
	c.LastUsed = time.Now()
	logger.VerboseNNTP("nntp pool Put", "host", p.Host())

	// Use stopCh as an extra guard: if Shutdown() fires in the window between
	// reading closed==false above and reaching this select, the stopCh case
	// prevents a panic that would occur if idleClients were closed.
	select {
	case p.idleClients <- c:
		// returned to idle
	case <-p.stopCh:
		// shutdown raced with Put; close and return the permit
		p.closeClient(c)
	default:
		logger.VerboseNNTP("nntp pool Put idle full, closing connection", "host", p.Host())
		p.closeClient(c)
	}
}

func (p *ClientPool) Discard(c *Client) {
	if c == nil {
		return
	}
	logger.VerboseNNTP("nntp pool Discard connection not returned to pool", "host", p.Host())
	p.closeClient(c)
}

func (p *ClientPool) reaperLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop() // always release the timer

	const timeout = 30 * time.Second

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
		}

		count := len(p.idleClients)
		for i := 0; i < count; i++ {
			select {
			case c := <-p.idleClients:
				if time.Since(c.LastUsed) > timeout {
					p.closeClient(c)
				} else {
					// Put connection back, but respect a concurrent Shutdown().
					select {
					case p.idleClients <- c:
					case <-p.stopCh:
						p.closeClient(c)
						return
					}
				}
			default:
			}
		}
	}
}

func (p *ClientPool) Validate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := p.Get(ctx)
	if err != nil {
		return err
	}
	p.Put(c)
	return nil
}

func (p *ClientPool) Host() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target.host
}

func (p *ClientPool) MaxConn() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxConn
}

// ConnSnapshot is one coherent reading of the pool's connection counts.
type ConnSnapshot struct {
	// Max is the configured connection ceiling.
	Max int
	// Total is how many connections are actually established with the provider
	// right now — idle, in use, or held by a probe. Dials still in progress are
	// not connections yet and are excluded.
	Total int
	// Idle is how many established connections are parked in the pool.
	Idle int
	// Active is Total minus Idle: connections a command is (or is about to be)
	// running on.
	Active int
	// Pending is how many dials are in flight — a permit is claimed but the
	// handshake has not finished.
	Pending int
}

// ConnStats reads all connection counters in one call so a caller gets a set
// of numbers that agree with each other. The individual reads still are not one
// atomic transaction, so the derived values are clamped: a connection finishing
// its dial or its teardown between two loads must never produce idle > total or
// a negative active count.
func (p *ClientPool) ConnStats() ConnSnapshot {
	total := int(p.established.Load())
	idle := len(p.idleClients)
	if idle > total {
		idle = total
	}
	return ConnSnapshot{
		Max:     p.MaxConn(),
		Total:   total,
		Idle:    idle,
		Active:  total - idle,
		Pending: int(p.dialing.Load()),
	}
}

// TotalConnections reports connections actually established with the provider.
// This is deliberately not "permits claimed": a permit held by a dial that has
// not finished — or that is failing against a dead server — is not a connection
// the provider sees, and counting it had the dashboard reporting connections
// that did not exist.
func (p *ClientPool) TotalConnections() int {
	return int(p.established.Load())
}

func (p *ClientPool) IdleConnections() int {
	return p.ConnStats().Idle
}

func (p *ClientPool) ActiveConnections() int {
	return p.ConnStats().Active
}

// AccountConnections reports how many connections every pool on this pool's
// account holds or is dialing right now, and the ceiling they share. It is the
// number the provider is comparing against its limit.
func (p *ClientPool) AccountConnections() (inUse, limit int) {
	_, budget, _ := p.snapshot()
	return budget.InUse(), budget.Limit()
}

func (p *ClientPool) Shutdown() {
	p.untrackAux()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	usageMgr := p.usageManager
	providerName := p.providerName
	budget := p.budget
	p.mu.Unlock()

	if usageMgr != nil && providerName != "" {
		usageMgr.FlushProvider(providerName)
	}

	// Signal reaperLoop and any racing Put() to stop.
	// idleClients is intentionally NOT closed here; closing it while Put() or
	// reaperLoop could still be writing to it causes a "send on closed channel"
	// panic.  Instead we drain it with non-blocking receives.
	close(p.stopCh)
	p.drainIdle()

	// Checked-out connections keep their permits until they come back; the
	// claim goes now so the account's ceiling stops counting a pool that will
	// never dial again.
	budget.dropClaim(p)
}
