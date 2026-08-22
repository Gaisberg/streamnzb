package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"streamnzb/pkg/auth"
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

// The route this covers is the one that matters: /api/config is wrapped in
// authMiddleware, which authenticates *any* stream token, not just the admin's.
// So a device reading the config must not come away with the credential of the
// device next to it. Exercised through the handler rather than the redaction
// helper, because the split between the two branches is where the mistake would
// live.
func TestGetConfigWithholdsStreamTokensFromNonAdminDevices(t *testing.T) {
	cfg := &config.Config{
		AdminUsername: "admin",
		Streams: map[string]*config.StreamEntry{
			"living-room": {Username: "living-room", Token: "living-room-secret"},
			"phone":       {Username: "phone", Token: "phone-secret"},
		},
	}
	s := &Server{config: cfg}

	for _, tc := range []struct {
		name       string
		as         string
		wantTokens bool
	}{
		{"a device sees no tokens at all", "phone", false},
		{"the admin still sees them", "admin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			req = req.WithContext(auth.ContextWithStream(req.Context(), &auth.Stream{Username: tc.as}))
			rr := httptest.NewRecorder()

			s.handleGetConfig(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			body := rr.Body.String()
			leaked := strings.Contains(body, "living-room-secret") || strings.Contains(body, "phone-secret")
			if leaked != tc.wantTokens {
				t.Fatalf("tokens present = %v, want %v; body: %s", leaked, tc.wantTokens, body)
			}
		})
	}

	if cfg.Streams["phone"].Token != "phone-secret" {
		t.Fatalf("serving the config mutated it: %#v", cfg.Streams["phone"])
	}
}
