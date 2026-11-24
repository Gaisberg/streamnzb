package nntp

import (
	"context"
	"testing"
)

// Generation moves with the dial target and nothing else, so equal generations
// mean the same server and account were asked.
func TestClientPoolGenerationTracksDialTarget(t *testing.T) {
	p := NewClientPool("127.0.0.1", 1, false, "u", "p", 2)
	defer p.Shutdown()

	g0 := p.Generation()
	if p.Reconfigure("127.0.0.1", 1, false, "u", "p", 4) || p.Generation() != g0 {
		t.Fatal("a connection-count change must not move the generation")
	}
	if !p.Reconfigure("127.0.0.1", 2, false, "u", "p", 4) || p.Generation() == g0 {
		t.Fatal("a new dial target must move the generation")
	}
	var nilPool *ClientPool
	if nilPool.Generation() != 0 {
		t.Fatal("nil pool reports generation 0")
	}
}

// A connection keeps the configuration it was dialed for after the pool is
// re-pointed, which is what lets a caller say which server answered a command
// that was already running when the settings changed.
func TestClientOriginSurvivesReconfigure(t *testing.T) {
	first, second := startBenchServer(t, 0), startBenchServer(t, 0)
	h1, p1 := first.addr()
	h2, p2 := second.addr()
	p := NewClientPool(h1, p1, false, "u", "p", 2)
	defer p.Shutdown()

	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Discard(c)
	pool, gen := c.Origin()
	if pool != p || gen != p.Generation() {
		t.Fatalf("origin = (%p, %d), want (%p, %d)", pool, gen, p, p.Generation())
	}

	p.Reconfigure(h2, p2, false, "u", "p", 2)
	if pool2, gen2 := c.Origin(); pool2 != pool || gen2 != gen || p.Generation() == gen {
		t.Fatalf("origin must stay (%p, %d) while the pool moves to %d; got (%p, %d)", pool, gen, p.Generation(), pool2, gen2)
	}
}
