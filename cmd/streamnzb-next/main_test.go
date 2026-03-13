package main

import (
	"net/url"
	"streamnzb/pkg/core/config"
	"testing"
)

func TestBuildPlaybackDownloadHostAPIKeys(t *testing.T) {
	cfg := &config.Config{
		Indexers: []config.IndexerConfig{
			{URL: "https://example.com/api", APIKey: "key1"},
			{URL: "https://other.com/api", APIKey: "key2"},
			{URL: "", APIKey: ""},
		},
	}
	keys := buildPlaybackDownloadHostAPIKeys(cfg)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	for _, k := range keys {
		u, _ := url.Parse("https://" + k.Host)
		if u.Hostname() == "" {
			t.Fatalf("empty host in key %+v", k)
		}
	}
}
