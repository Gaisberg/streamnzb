package initialization

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
)

func boolPtr(b bool) *bool { return &b }

// closedPort returns a loopback port nothing listens on, so a dial fails at
// once instead of waiting on DNS or a connect timeout.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestBuildProviderPoolsReusesUnchangedPools(t *testing.T) {
	prevCfg := &config.Config{Providers: []config.Provider{
		{Name: "keep", Host: "news.keep.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true)},
	}}
	keepPool := nntp.NewClientPool("news.keep.example", 563, true, "u", "p", 10)
	t.Cleanup(keepPool.Shutdown)
	prevPools := map[string]*nntp.ClientPool{"keep": keepPool}

	// Same connection settings; only priority changed — connections must survive.
	priority := 2
	newCfg := &config.Config{Providers: []config.Provider{
		{Name: "keep", Host: "news.keep.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true), Priority: &priority},
	}}

	set := buildProviderPools(newCfg, nil, prevCfg, &ProviderPoolSet{Pools: prevPools})
	if set.Pools["keep"] != keepPool {
		t.Fatalf("expected unchanged provider pool to be reused, got a new pool")
	}
	if len(set.Order) != 1 || set.Order[0] != "keep" {
		t.Fatalf("order = %v, want [keep]", set.Order)
	}
}

// A connection-count change re-sizes the live pool in place: same account,
// same server, so nothing needs to be dialed to trust it — and a second pool
// beside the first would have been two pools on one account.
func TestBuildProviderPoolsResizesPoolInPlace(t *testing.T) {
	prevCfg := &config.Config{Providers: []config.Provider{
		{Name: "prov", Host: "news.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true)},
	}}
	oldPool := nntp.NewClientPool("news.example", 563, true, "u", "p", 10)
	t.Cleanup(oldPool.Shutdown)
	prevPools := map[string]*nntp.ClientPool{"prov": oldPool}

	newCfg := &config.Config{Providers: []config.Provider{
		{Name: "prov", Host: "news.example", Port: 563, Username: "u", Password: "p", Connections: 20, UseSSL: true, Enabled: boolPtr(true)},
	}}

	set := buildProviderPools(newCfg, nil, prevCfg, &ProviderPoolSet{Pools: prevPools})
	if set.Pools["prov"] != oldPool {
		t.Fatalf("expected the pool to be resized in place, got a new pool")
	}
	if got := oldPool.MaxConn(); got != 20 {
		t.Fatalf("MaxConn after resize = %d, want 20", got)
	}
}

// A credential change re-points the live pool and validates it. When that
// validation fails the pool is dropped from the set and shut down, not left
// running with a claim on the account.
func TestBuildProviderPoolsShutsDownPoolThatFailsValidation(t *testing.T) {
	port := closedPort(t)
	prevCfg := &config.Config{Providers: []config.Provider{
		{Name: "prov", Host: "127.0.0.1", Port: port, Username: "u", Password: "p", Connections: 10, Enabled: boolPtr(true)},
	}}
	oldPool := nntp.NewClientPool("127.0.0.1", port, false, "u", "p", 10)
	t.Cleanup(oldPool.Shutdown)
	prevPools := map[string]*nntp.ClientPool{"prov": oldPool}

	newCfg := &config.Config{Providers: []config.Provider{
		{Name: "prov", Host: "127.0.0.1", Port: port, Username: "u2", Password: "p", Connections: 10, Enabled: boolPtr(true)},
	}}

	set := buildProviderPools(newCfg, nil, prevCfg, &ProviderPoolSet{Pools: prevPools})
	if _, ok := set.Pools["prov"]; ok {
		t.Fatalf("expected the provider that failed validation to be dropped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := oldPool.Get(ctx); !errors.Is(err, nntp.ErrPoolClosed) {
		t.Fatalf("Get on the dropped pool = %v, want ErrPoolClosed", err)
	}
}

// Toggling a provider must not build a second usenet pool: sessions already
// playing hold views of the first, so a replacement doubled the segment cache
// and left those streams on client pools the reload then shut down.
func TestBuildProviderPoolsUpdatesUsenetPoolInPlace(t *testing.T) {
	provs := func(otherEnabled bool) []config.Provider {
		return []config.Provider{
			{Name: "keep", Host: "news.keep.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true)},
			{Name: "other", Host: "news.other.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(otherEnabled)},
		}
	}
	keepPool := nntp.NewClientPool("news.keep.example", 563, true, "u", "p", 10)
	otherPool := nntp.NewClientPool("news.other.example", 563, true, "u", "p", 10)
	t.Cleanup(keepPool.Shutdown)
	t.Cleanup(otherPool.Shutdown)

	prevCfg := &config.Config{MemoryLimitMB: 1024, Providers: provs(true)}
	budget := pool.NewSegmentCacheBudget(1024 - 150)
	usenet, err := pool.NewPool(&pool.Config{
		Providers: []pool.ProviderConfig{
			{ID: "keep", Priority: 0, ClientPool: keepPool},
			{ID: "other", Priority: 1, ClientPool: otherPool},
		},
		SegmentCache: pool.NewMemorySegmentCacheWithBudget(budget),
	})
	if err != nil {
		t.Fatal(err)
	}
	view := usenet.Subset(nil)

	newCfg := &config.Config{MemoryLimitMB: 1024, Providers: provs(false)}
	set := buildProviderPools(newCfg, nil, prevCfg, &ProviderPoolSet{
		Pools:              map[string]*nntp.ClientPool{"keep": keepPool, "other": otherPool},
		UsenetPool:         usenet,
		SegmentCacheBudget: budget,
	})
	if set.UsenetPool != usenet {
		t.Fatal("expected the usenet pool to be updated in place, got a new one")
	}
	if set.SegmentCacheBudget != budget {
		t.Fatal("expected the segment cache budget to be kept")
	}
	if got := view.ProviderOrder(); len(got) != 1 || got[0] != "keep" {
		t.Fatalf("live view providers = %v, want [keep]", got)
	}
}
