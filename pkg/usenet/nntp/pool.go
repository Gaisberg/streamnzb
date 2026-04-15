package nntp

import (
	"context"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

type ClientPool struct {
	host    string
	port    int
	ssl     bool
	user    string
	pass    string
	maxConn int

	idleClients chan *Client
	slots       chan struct{}
	stopCh      chan struct{} // closed once by Shutdown(); never re-used

	bytesRead      int64
	totalBytesRead int64
	lastTotalBytes int64
	lastSpeed      float64
	lastCheck      time.Time

	providerName string
	usageManager *ProviderUsageManager

	mu     sync.Mutex
	closed bool
}

func NewClientPool(host string, port int, ssl bool, user, pass string, maxConn int) *ClientPool {
	p := &ClientPool{
		host:        host,
		port:        port,
		ssl:         ssl,
		user:        user,
		pass:        pass,
		maxConn:     maxConn,
		idleClients: make(chan *Client, maxConn),
		slots:       make(chan struct{}, maxConn),
		stopCh:      make(chan struct{}),
		lastCheck:   time.Now(),
	}

	for i := 0; i < maxConn; i++ {
		p.slots <- struct{}{}
	}

	go p.reaperLoop()
	return p
}

func (p *ClientPool) SetUsageManager(name string, mgr *ProviderUsageManager) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.providerName = name
	p.usageManager = mgr
}

func (p *ClientPool) RestoreTotalBytes(total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.totalBytesRead = total
	p.lastTotalBytes = total
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

const minSpeedWindow = 0.05

const maxSpeedDuration = 5.0

func (p *ClientPool) GetSpeed() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	duration := now.Sub(p.lastCheck).Seconds()

	p.lastCheck = now

	if duration < minSpeedWindow {
		return p.lastSpeed
	}

	if duration > maxSpeedDuration {
		duration = maxSpeedDuration
	}

	delta := p.totalBytesRead - p.lastTotalBytes
	p.lastTotalBytes = p.totalBytesRead

	if delta > 0 {

		p.lastSpeed = (float64(delta) * 8) / (1024 * 1024) / duration
	} else {
		const decay = 0.35
		p.lastSpeed *= decay
		if p.lastSpeed < 0.1 {
			p.lastSpeed = 0
		}
	}
	return p.lastSpeed
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

func (p *ClientPool) Get(ctx context.Context) (*Client, error) {
	logger.VerboseNNTP("nntp pool Get", "host", p.host)

	select {
	case <-ctx.Done():
		logger.VerboseNNTP("pool.Get ctx.Done (idle check)", "host", p.host)
		return nil, ctx.Err()
	case c := <-p.idleClients:
		logger.VerboseNNTP("nntp pool Get from idle", "host", p.host)
		return c, nil
	default:
	}

	select {
	case <-ctx.Done():
		logger.VerboseNNTP("pool.Get ctx.Done (slot check)", "host", p.host)
		return nil, ctx.Err()
	case <-p.slots:

		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, err
		}
		c.SetPool(p)
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, err
		}
		logger.VerboseNNTP("nntp pool Get new client", "host", p.host)
		return c, nil
	default:
	}

	waitStarted := time.Now()
	select {
	case <-ctx.Done():
		if wait := time.Since(waitStarted); wait >= 250*time.Millisecond {
			logger.Debug("NNTP pool wait exceeded threshold", "host", p.host, "wait", wait, "result", "context_canceled")
		}
		logger.VerboseNNTP("pool.Get ctx.Done (blocking)", "host", p.host)
		return nil, ctx.Err()
	case c := <-p.idleClients:
		if wait := time.Since(waitStarted); wait >= 250*time.Millisecond {
			logger.Debug("NNTP pool wait exceeded threshold", "host", p.host, "wait", wait, "result", "idle_client")
		}
		logger.VerboseNNTP("nntp pool Get from idle (after block)", "host", p.host)
		return c, nil
	case <-p.slots:
		wait := time.Since(waitStarted)

		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, err
		}
		c.SetPool(p)
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, err
		}
		if wait >= 250*time.Millisecond {
			logger.Debug("NNTP pool wait exceeded threshold", "host", p.host, "wait", wait, "result", "new_client")
		}
		logger.VerboseNNTP("nntp pool Get new client (after block)", "host", p.host)
		return c, nil
	}
}

func (p *ClientPool) TryGet(ctx context.Context) (*Client, bool) {

	select {
	case <-ctx.Done():
		return nil, false
	case c := <-p.idleClients:
		return c, true
	default:
	}

	select {
	case <-ctx.Done():
		return nil, false
	case <-p.slots:
		c, err := NewClient(p.host, p.port, p.ssl)
		if err != nil {
			p.slots <- struct{}{}
			return nil, false
		}
		c.SetPool(p)
		if err := c.Authenticate(p.user, p.pass); err != nil {
			c.Quit()
			p.slots <- struct{}{}
			return nil, false
		}
		return c, true
	default:
		return nil, false
	}
}

func (p *ClientPool) Put(c *Client) {
	if c == nil {
		return
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		c.Quit()
		p.slots <- struct{}{}
		return
	}
	c.LastUsed = time.Now()
	logger.VerboseNNTP("nntp pool Put", "host", p.host)

	// Use stopCh as an extra guard: if Shutdown() fires in the window between
	// reading closed==false above and reaching this select, the stopCh case
	// prevents a panic that would occur if idleClients were closed.
	select {
	case p.idleClients <- c:
		// returned to idle
	case <-p.stopCh:
		// shutdown raced with Put; close and return slot
		c.Quit()
		p.slots <- struct{}{}
	default:
		logger.VerboseNNTP("nntp pool Put idle full, closing connection", "host", p.host)
		c.Quit()
		p.slots <- struct{}{}
	}
}

func (p *ClientPool) Discard(c *Client) {
	if c == nil {
		return
	}
	logger.VerboseNNTP("nntp pool Discard connection not returned to pool", "host", p.host)
	c.Quit()
	p.slots <- struct{}{}
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
					c.Quit()
					p.slots <- struct{}{}
				} else {
					// Put connection back, but respect a concurrent Shutdown().
					select {
					case p.idleClients <- c:
					case <-p.stopCh:
						c.Quit()
						p.slots <- struct{}{}
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
	return p.host
}

func (p *ClientPool) MaxConn() int {
	return p.maxConn
}

func (p *ClientPool) TotalConnections() int {
	return p.maxConn - len(p.slots)
}

func (p *ClientPool) IdleConnections() int {
	return len(p.idleClients)
}

func (p *ClientPool) ActiveConnections() int {
	return p.TotalConnections() - p.IdleConnections()
}

func (p *ClientPool) Shutdown() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	usageMgr := p.usageManager
	providerName := p.providerName
	p.mu.Unlock()

	if usageMgr != nil && providerName != "" {
		usageMgr.FlushProvider(providerName)
	}

	// Signal reaperLoop and any racing Put() to stop.
	// idleClients is intentionally NOT closed here; closing it while Put() or
	// reaperLoop could still be writing to it causes a "send on closed channel"
	// panic.  Instead we drain it with non-blocking receives.
	close(p.stopCh)

	for {
		select {
		case c := <-p.idleClients:
			c.Quit()
		default:
			return
		}
	}
}
