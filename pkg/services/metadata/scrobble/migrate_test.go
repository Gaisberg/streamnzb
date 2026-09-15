package scrobble

import (
	"testing"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

type link struct {
	Token string `json:"token"`
}

// The upgrade to per-stream accounts clears every stored link exactly once:
// carrying a server-wide link onto some stream would file one person's viewing
// into another's history, and re-clearing on every start would make linking
// impossible.
func TestResetLegacyLinksRunsOnce(t *testing.T) {
	logger.Init("ERROR")
	dir := t.TempDir()
	manager, err := persistence.GetManager(dir)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	if err := manager.Set("simkl_token", link{Token: "server-wide"}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := manager.Set("simkl_tokens", map[string]link{"alice": {Token: "per-stream"}}); err != nil {
		t.Fatalf("seed per-stream: %v", err)
	}

	if !ResetLegacyLinks(dir, "simkl_token", "simkl_tokens") {
		t.Fatal("the reset reported no work on a store holding links")
	}
	store := NewStore[link](dir, "simkl_tokens")
	if streams := store.Streams(); len(streams) != 0 {
		t.Fatalf("links survived the reset: %v", streams)
	}
	var legacy link
	if found, _ := manager.Get("simkl_token", &legacy); found && legacy.Token != "" {
		t.Fatalf("the legacy link survived: %+v", legacy)
	}

	// A link made after the reset must survive every later start, or nobody
	// could stay connected.
	store.Set("alice", link{Token: "linked-after"})
	if ResetLegacyLinks(dir, "simkl_token", "simkl_tokens") {
		t.Fatal("the reset ran a second time")
	}
	if got, ok := NewStore[link](dir, "simkl_tokens").Get("alice"); !ok || got.Token != "linked-after" {
		t.Fatalf("a link made after the reset was wiped: %+v ok=%v", got, ok)
	}
}
