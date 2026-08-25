package api

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/health"
)

// withTestHealthRegistry points the package-global registry at a fresh
// in-memory one for the duration of a test.
func withTestHealthRegistry(t *testing.T) *health.Registry {
	t.Helper()
	reg, err := health.Init(nil)
	if err != nil {
		t.Fatalf("health.Init: %v", err)
	}
	t.Cleanup(func() {
		reg.Retain(health.KindIndexer, nil)
		reg.Retain(health.KindProvider, nil)
	})
	return reg
}

func TestSyncComponentHealthRetiresVerdictOnCredentialChange(t *testing.T) {
	reg := withTestHealthRegistry(t)

	oldCfg := &config.Config{
		Providers: []config.Provider{
			{Name: "eweka", Host: "news.eweka.nl", Port: 563, Username: "me", Password: "old"},
			{Name: "backup", Host: "news.backup.nl", Port: 563, Username: "me", Password: "same"},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzbfinder", URL: "https://nzbfinder.ws", APIKey: "old-key"},
			{Name: "drunkenslug", URL: "https://drunkenslug.com", APIKey: "unchanged"},
		},
	}
	for _, name := range []string{"eweka", "backup"} {
		reg.Report(health.KindProvider, name, health.StateBlocked, health.ReasonAuthFailed, "481")
	}
	for _, name := range []string{"nzbfinder", "drunkenslug"} {
		reg.Report(health.KindIndexer, name, health.StateBlocked, health.ReasonAuthFailed, "code 100")
	}

	newCfg := &config.Config{
		Providers: []config.Provider{
			{Name: "eweka", Host: "news.eweka.nl", Port: 563, Username: "me", Password: "new"},
			{Name: "backup", Host: "news.backup.nl", Port: 563, Username: "me", Password: "same"},
		},
		Indexers: []config.IndexerConfig{
			{Name: "nzbfinder", URL: "https://nzbfinder.ws", APIKey: "new-key"},
			{Name: "drunkenslug", URL: "https://drunkenslug.com", APIKey: "unchanged"},
		},
	}
	syncComponentHealth(oldCfg, newCfg)

	if reg.Blocked(health.KindProvider, "eweka") {
		t.Error("a changed provider password must retire the stored verdict")
	}
	if !reg.Blocked(health.KindProvider, "backup") {
		t.Error("an untouched provider must keep its verdict")
	}
	if reg.Blocked(health.KindIndexer, "nzbfinder") {
		t.Error("a changed API key must retire the stored verdict")
	}
	if !reg.Blocked(health.KindIndexer, "drunkenslug") {
		t.Error("an untouched indexer must keep its verdict")
	}
}

func TestSyncComponentHealthDropsRemovedComponents(t *testing.T) {
	reg := withTestHealthRegistry(t)

	oldCfg := &config.Config{
		Providers: []config.Provider{{Name: "gone", Host: "h", Port: 563, Username: "u", Password: "p"}},
		Indexers:  []config.IndexerConfig{{Name: "gone-too", URL: "https://x", APIKey: "k"}},
	}
	reg.Report(health.KindProvider, "gone", health.StateBlocked, health.ReasonAuthFailed, "")
	reg.Report(health.KindIndexer, "gone-too", health.StateBlocked, health.ReasonAuthFailed, "")

	syncComponentHealth(oldCfg, &config.Config{})

	if _, ok := reg.Lookup(health.KindProvider, "gone"); ok {
		t.Error("a deleted provider must not leave a warning behind")
	}
	if _, ok := reg.Lookup(health.KindIndexer, "gone-too"); ok {
		t.Error("a deleted indexer must not leave a warning behind")
	}
}

func TestCredentialsChangedComparesWhatWeAuthenticateWith(t *testing.T) {
	base := config.Provider{Name: "p", Host: "h", Port: 563, Username: "u", Password: "p", Connections: 20}
	moved := base
	moved.Host = "other-host"
	if !providerCredentialsChanged(base, moved) {
		t.Error("a different host is a different login")
	}
	retuned := base
	retuned.Connections = 50
	if providerCredentialsChanged(base, retuned) {
		t.Error("connection count is not a credential")
	}

	idx := config.IndexerConfig{Name: "i", URL: "https://x", APIKey: "k"}
	rekeyed := idx
	rekeyed.APIKey = "k2"
	if !indexerCredentialsChanged(idx, rekeyed) {
		t.Error("a new API key must count as changed")
	}
	recategorized := idx
	recategorized.MovieCategories = "2000"
	if indexerCredentialsChanged(idx, recategorized) {
		t.Error("categories are not credentials")
	}
}
