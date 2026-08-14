package auth

import (
	"path/filepath"
	"testing"

	"streamnzb/pkg/core/config"
)

func TestSetConfigReloadsInMemoryStreamsFromConfig(t *testing.T) {
	cfg := &config.Config{
		Streams: map[string]*config.StreamEntry{
			"default": {
				Username:           "default",
				Token:              "token-default",
				AutoAddProviders:   ptrBool(true),
				AutoAddIndexers:    ptrBool(true),
				ProviderSelections: []string{"ProviderA"},
				IndexerSelections:  []string{"IndexerA"},
			},
		},
	}

	dm := &StreamManager{
		streams: map[string]*Stream{
			"default": {
				Username:           "default",
				Token:              "old-token",
				AutoAddProviders:   ptrBool(true),
				AutoAddIndexers:    ptrBool(true),
				ProviderSelections: []string{"ProviderA", "ProviderB"},
				IndexerSelections:  []string{"IndexerA", "IndexerB"},
			},
		},
	}

	dm.SetConfig(cfg, nil)

	stream, err := dm.GetStream("default", "admin")
	if err != nil {
		t.Fatalf("GetStream returned error: %v", err)
	}
	if stream.Token != "token-default" {
		t.Fatalf("expected token to come from config, got %q", stream.Token)
	}
	if len(stream.ProviderSelections) != 1 || stream.ProviderSelections[0] != "ProviderA" {
		t.Fatalf("expected provider selections to reload from config, got %#v", stream.ProviderSelections)
	}
	if len(stream.IndexerSelections) != 1 || stream.IndexerSelections[0] != "IndexerA" {
		t.Fatalf("expected indexer selections to reload from config, got %#v", stream.IndexerSelections)
	}
}

// UpdateStreamConfig assigns each field onto the stored stream by hand, so a
// field added to Stream but missed here is dropped without any error.
func TestUpdateStreamConfigPersistsProfileBindings(t *testing.T) {
	cfg := &config.Config{
		// Saves go to a temp file: Config.Save requires an explicit path so it
		// can never write a stray config.json into the package directory.
		LoadedPath: filepath.Join(t.TempDir(), "config.json"),
		Streams: map[string]*config.StreamEntry{
			"default": {Username: "default", Token: "token-default"},
		},
	}
	dm := &StreamManager{cfg: cfg, saveFn: func() error { return nil }}
	if err := dm.load(); err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	err := dm.UpdateStreamConfig("default", &Stream{
		FilterProfileName: "Default Profile",
		FilterProfileByType: map[string]string{
			"movie": "My Movie Profile",
			// Cleared in the UI, and a kind bound to nothing is not a binding.
			"series": "",
			"":       "Orphan",
		},
	})
	if err != nil {
		t.Fatalf("UpdateStreamConfig returned error: %v", err)
	}

	stream, err := dm.GetStream("default", "admin")
	if err != nil {
		t.Fatalf("GetStream returned error: %v", err)
	}
	if got := stream.FilterProfileByType["movie"]; got != "My Movie Profile" {
		t.Errorf("movie binding = %q, want %q", got, "My Movie Profile")
	}
	if _, ok := stream.FilterProfileByType["series"]; ok {
		t.Error("a binding cleared to empty should be dropped")
	}
	if _, ok := stream.FilterProfileByType[""]; ok {
		t.Error("a binding with no kind should be dropped")
	}

	// The binding has to survive into the config, which is what the addon reads.
	if got := cfg.Streams["default"].FilterProfileByType["movie"]; got != "My Movie Profile" {
		t.Errorf("config movie binding = %q, want %q", got, "My Movie Profile")
	}
}

func TestUpdateStreamConfigClearsProfileBindings(t *testing.T) {
	cfg := &config.Config{
		LoadedPath: filepath.Join(t.TempDir(), "config.json"),
		Streams: map[string]*config.StreamEntry{
			"default": {
				Username:            "default",
				Token:               "token-default",
				FilterProfileByType: map[string]string{"movie": "My Movie Profile"},
			},
		},
	}
	dm := &StreamManager{cfg: cfg, saveFn: func() error { return nil }}
	if err := dm.load(); err != nil {
		t.Fatalf("load returned error: %v", err)
	}

	if err := dm.UpdateStreamConfig("default", &Stream{FilterProfileName: "Default Profile"}); err != nil {
		t.Fatalf("UpdateStreamConfig returned error: %v", err)
	}

	stream, _ := dm.GetStream("default", "admin")
	if len(stream.FilterProfileByType) != 0 {
		t.Errorf("expected bindings to be cleared, got %#v", stream.FilterProfileByType)
	}
}

func TestActiveProviderSelectionsDropsDisabled(t *testing.T) {
	stream := &Stream{
		ProviderSelections: []string{"Eweka", "Newshosting", "Frugal"},
		DisabledProviders:  []string{"newshosting"},
	}
	active := stream.ActiveProviderSelections()
	if len(active) != 2 || active[0] != "Eweka" || active[1] != "Frugal" {
		t.Fatalf("active = %#v, want [Eweka Frugal] in priority order", active)
	}
}

func TestActiveProviderSelectionsPassesThroughWhenNoneDisabled(t *testing.T) {
	stream := &Stream{ProviderSelections: []string{"Eweka"}}
	if active := stream.ActiveProviderSelections(); len(active) != 1 || active[0] != "Eweka" {
		t.Fatalf("active = %#v, want [Eweka]", active)
	}
	if (*Stream)(nil).ActiveProviderSelections() != nil {
		t.Fatal("a nil stream should have no active providers")
	}
}

func newTestStreamManager(t *testing.T, names ...string) *StreamManager {
	t.Helper()
	// Saves go to a temp file: Config.Save requires an explicit path so it can
	// never write a stray config.json into the package directory.
	cfg := &config.Config{
		LoadedPath: filepath.Join(t.TempDir(), "config.json"),
		Streams:    make(map[string]*config.StreamEntry),
	}
	dm := &StreamManager{streams: make(map[string]*Stream), cfg: cfg, saveFn: func() error { return nil }}
	for _, name := range names {
		dm.streams[name] = &Stream{Username: name, Token: "token-" + name}
	}
	return dm
}

func TestRenameStreamKeepsTokenAndSettings(t *testing.T) {
	dm := newTestStreamManager(t, "Living Room")
	dm.streams["Living Room"].ProviderSelections = []string{"Eweka"}

	if err := dm.RenameStream("Living Room", "Lounge", "admin"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if _, exists := dm.streams["Living Room"]; exists {
		t.Fatal("the old name should be gone")
	}
	renamed, exists := dm.streams["Lounge"]
	if !exists {
		t.Fatal("the stream should be reachable under its new name")
	}
	if renamed.Username != "Lounge" {
		t.Fatalf("Username = %q, want Lounge", renamed.Username)
	}
	if renamed.Token != "token-Living Room" {
		t.Fatalf("token changed on rename (%q) — installed addon URLs would break", renamed.Token)
	}
	if len(renamed.ProviderSelections) != 1 || renamed.ProviderSelections[0] != "Eweka" {
		t.Fatalf("settings did not survive the rename: %#v", renamed.ProviderSelections)
	}
}

func TestRenameStreamRejectsCollisionsAndAdmin(t *testing.T) {
	dm := newTestStreamManager(t, "Alpha", "Beta")

	if err := dm.RenameStream("Alpha", "beta", "admin"); err == nil {
		t.Fatal("a case-insensitive collision should be refused")
	}
	if _, exists := dm.streams["Alpha"]; !exists {
		t.Fatal("a refused rename must leave the stream where it was")
	}
	if err := dm.RenameStream("Alpha", "Admin", "admin"); err == nil {
		t.Fatal("renaming onto the admin name should be refused")
	}
	if err := dm.RenameStream("Alpha", "  ", "admin"); err == nil {
		t.Fatal("a blank name should be refused")
	}
	if err := dm.RenameStream("Missing", "Gamma", "admin"); err == nil {
		t.Fatal("renaming an unknown stream should fail")
	}
}

func TestRenameStreamToSameNameIsANoop(t *testing.T) {
	dm := newTestStreamManager(t, "Alpha")
	if err := dm.RenameStream("Alpha", "Alpha", "admin"); err != nil {
		t.Fatalf("renaming to the same name should succeed: %v", err)
	}
	if _, exists := dm.streams["Alpha"]; !exists {
		t.Fatal("the stream should still be there")
	}
}
