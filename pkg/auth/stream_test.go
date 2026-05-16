package auth

import (
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
