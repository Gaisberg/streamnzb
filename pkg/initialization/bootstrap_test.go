package initialization

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/usenet/nntp"
)

func boolPtr(b bool) *bool { return &b }

func TestBuildProviderPoolsReusesUnchangedPools(t *testing.T) {
	prevCfg := &config.Config{Providers: []config.Provider{
		{Name: "keep", Host: "news.keep.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true)},
	}}
	keepPool := nntp.NewClientPool("news.keep.example", 563, true, "u", "p", 10)
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

func TestBuildProviderPoolsDropsChangedPoolFromReuse(t *testing.T) {
	prevCfg := &config.Config{Providers: []config.Provider{
		{Name: "prov", Host: "news.example", Port: 563, Username: "u", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true)},
	}}
	oldPool := nntp.NewClientPool("news.example", 563, true, "u", "p", 10)
	prevPools := map[string]*nntp.ClientPool{"prov": oldPool}

	// Credentials changed — the old pool must not be carried over. The rebuild
	// attempt will fail Validate against the fake host and be skipped, which
	// is fine: what matters is the stale pool is not reused.
	newCfg := &config.Config{Providers: []config.Provider{
		{Name: "prov", Host: "news.example", Port: 563, Username: "u2", Password: "p", Connections: 10, UseSSL: true, Enabled: boolPtr(true)},
	}}

	set := buildProviderPools(newCfg, nil, prevCfg, prevPools)
	if set.Pools["prov"] == oldPool {
		t.Fatalf("expected changed provider pool to be rebuilt, got the old pool")
	}
}
