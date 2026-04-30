package api

import (
	"testing"

	"streamnzb/pkg/core/config"
)

func TestConfigForAdminAPIPreservesProviderAndIndexerCredentials(t *testing.T) {
	cfg := &config.Config{
		AdminPasswordHash: "hash",
		AdminToken:        "token",
		IndexerProxyURL:   "http://u:p@proxy-global:8080",
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
			ProxyURL: "http://u:p@proxy:8888",
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
	if out.Indexers[0].ProxyURL != "http://u:p@proxy:8888" {
		t.Fatalf("expected full indexer proxy URL for admin config reads, got %q", out.Indexers[0].ProxyURL)
	}
	if out.IndexerProxyURL != "http://u:p@proxy-global:8080" {
		t.Fatalf("expected full global indexer proxy URL for admin config reads, got %q", out.IndexerProxyURL)
	}
}

func TestRedactedConfigForViewerRemovesProviderAndIndexerCredentials(t *testing.T) {
	cfg := &config.Config{
		IndexerProxyURL: "http://u:p@proxy-global:8080",
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
			ProxyURL: "http://u:p@proxy:8888",
		}},
	}

	out := redactedConfigForViewer(cfg)
	if out.Providers[0].Username != "" || out.Providers[0].Password != "" {
		t.Fatalf("expected provider credentials to be cleared for viewers, got %#v", out.Providers[0])
	}
	if out.Indexers[0].APIKey != "" || out.Indexers[0].Username != "" || out.Indexers[0].Password != "" {
		t.Fatalf("expected indexer credentials to be cleared for viewers, got %#v", out.Indexers[0])
	}
	if out.Indexers[0].ProxyURL != "http://proxy:8888" {
		t.Fatalf("expected proxy userinfo redacted for viewers, got %q", out.Indexers[0].ProxyURL)
	}
	if out.IndexerProxyURL != "http://proxy-global:8080" {
		t.Fatalf("expected global proxy userinfo redacted for viewers, got %q", out.IndexerProxyURL)
	}
}
