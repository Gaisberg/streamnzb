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
