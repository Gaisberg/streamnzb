package app

import (
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/services/availnzb"
)

func TestSetAvailNZBAPIKeyUpdatesOptsAndLiveClient(t *testing.T) {
	t.Parallel()

	client := availnzb.NewClient("https://snzb.stream", "")
	a := &App{
		components: &Components{AvailClient: client},
	}

	a.SetAvailNZBAPIKey(" updated-key ")

	if got := client.GetAPIKey(); got != "updated-key" {
		t.Fatalf("client key = %q, want %q", got, "updated-key")
	}
	if got := a.opts.AvailNZBAPIKey; got != "updated-key" {
		t.Fatalf("stored opts key = %q, want %q", got, "updated-key")
	}
}

func TestConfigChangedScopesAreIndependent(t *testing.T) {
	t.Parallel()

	base := func() *config.Config {
		enabled := true
		return &config.Config{
			Indexers:  []config.IndexerConfig{{Name: "idx", URL: "https://idx", Enabled: &enabled}},
			Providers: []config.Provider{{Name: "prov", Host: "news.example.com", Port: 563}},
			ProxyHost: "0.0.0.0",
			ProxyPort: 119,
		}
	}

	if scope := ConfigChanged(base(), base()); scope.Any() {
		t.Fatalf("identical configs should yield empty scope, got %+v", scope)
	}

	indexerChanged := base()
	indexerChanged.Indexers[0].URL = "https://other"
	if scope := ConfigChanged(base(), indexerChanged); !scope.Indexers || scope.Providers || scope.Proxy {
		t.Fatalf("indexer edit scope = %+v, want indexers only", scope)
	}

	providerChanged := base()
	providerChanged.Providers[0].Connections = 50
	if scope := ConfigChanged(base(), providerChanged); !scope.Providers || scope.Indexers || scope.Proxy {
		t.Fatalf("provider edit scope = %+v, want providers only", scope)
	}

	proxyChanged := base()
	proxyChanged.ProxyPort = 1119
	if scope := ConfigChanged(base(), proxyChanged); !scope.Proxy || scope.Indexers || scope.Providers {
		t.Fatalf("proxy edit scope = %+v, want proxy only", scope)
	}

	if scope := ConfigChanged(nil, base()); !scope.Indexers || !scope.Providers || !scope.Proxy {
		t.Fatalf("nil old config scope = %+v, want full", scope)
	}

	configOnly := base()
	configOnly.AvailNZBMode = "off"
	configOnly.LogLevel = "INFO"
	if scope := ConfigChanged(base(), configOnly); scope.Any() {
		t.Fatalf("non-structural edit scope = %+v, want empty", scope)
	}
}
