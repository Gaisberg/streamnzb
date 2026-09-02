package initialization

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/usenet/nntp"
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

	set := buildProviderPools(newCfg, nil, prevCfg, prevPools)
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

	set := buildProviderPools(newCfg, nil, prevCfg, prevPools)
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

	set := buildProviderPools(newCfg, nil, prevCfg, prevPools)
	if _, ok := set.Pools["prov"]; ok {
		t.Fatalf("expected the provider that failed validation to be dropped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := oldPool.Get(ctx); !errors.Is(err, nntp.ErrPoolClosed) {
		t.Fatalf("Get on the dropped pool = %v, want ErrPoolClosed", err)
	}
}
