package api

import (
	"testing"

	"streamnzb/pkg/core/config"
)

func TestConfigForAdminAPIPreservesProviderAndIndexerCredentials(t *testing.T) {
	cfg := &config.Config{
		AdminPasswordHash: "hash",
		AdminToken:        "token",
		Providers: []config.Provider{{
			Name:     "provider",
			Username: "user",
			Password: "pass",
		}},
		Indexers: []config.IndexerConfig{{
			Name:     "indexer",
			APIKey:   "key",
			Username: "easyuser",
			Password: "easypass",
		}},
	}

	out := configForAdminAPI(cfg)
	if out.AdminPasswordHash != "" || out.AdminToken != "" {
		t.Fatalf("expected admin auth secrets to be cleared, got %#v", out)
	}
	if out.Providers[0].Username != "user" || out.Providers[0].Password != "pass" {
		t.Fatalf("expected provider credentials to remain for admin config reads, got %#v", out.Providers[0])
	}
	if out.Indexers[0].APIKey != "key" || out.Indexers[0].Username != "easyuser" || out.Indexers[0].Password != "easypass" {
		t.Fatalf("expected indexer credentials to remain for admin config reads, got %#v", out.Indexers[0])
	}
}

func TestRedactedConfigForViewerRemovesProviderAndIndexerCredentials(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name:     "provider",
			Username: "user",
			Password: "pass",
		}},
		Indexers: []config.IndexerConfig{{
			Name:     "indexer",
			APIKey:   "key",
			Username: "easyuser",
			Password: "easypass",
		}},
	}

	out := redactedConfigForViewer(cfg)
	if out.Providers[0].Username != "" || out.Providers[0].Password != "" {
		t.Fatalf("expected provider credentials to be cleared for viewers, got %#v", out.Providers[0])
	}
	if out.Indexers[0].APIKey != "" || out.Indexers[0].Username != "" || out.Indexers[0].Password != "" {
		t.Fatalf("expected indexer credentials to be cleared for viewers, got %#v", out.Indexers[0])
	}
}
