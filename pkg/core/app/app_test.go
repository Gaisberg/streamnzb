package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

	globalProxyChanged := base()
	globalProxyChanged.IndexerProxyURL = "http://proxy:8888"
	if scope := ConfigChanged(base(), globalProxyChanged); !scope.Indexers || scope.Providers || scope.Proxy {
		t.Fatalf("global indexer proxy edit scope = %+v, want indexers only", scope)
	}

	globalProxyCleared := base()
	globalProxyCleared.IndexerProxyURL = "http://proxy:8888"
	if scope := ConfigChanged(globalProxyCleared, base()); !scope.Indexers {
		t.Fatalf("clearing the global indexer proxy scope = %+v, want indexers rebuilt", scope)
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

func TestPrefetchAvailNZBBackbonesRespectsOptIn(t *testing.T) {
	t.Parallel()

	newProbe := func(t *testing.T) (string, chan struct{}) {
		t.Helper()
		hit := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			select {
			case hit <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(srv.Close)
		return srv.URL, hit
	}

	t.Run("off makes no request", func(t *testing.T) {
		url, hit := newProbe(t)
		prefetchAvailNZBBackbones(&config.Config{AvailNZBMode: "off"}, availnzb.NewClient(url, "key"))
		select {
		case <-hit:
			t.Fatal("AvailNZB was contacted while the integration is off")
		case <-time.After(250 * time.Millisecond):
		}
	})

	t.Run("unset mode makes no request", func(t *testing.T) {
		url, hit := newProbe(t)
		prefetchAvailNZBBackbones(&config.Config{}, availnzb.NewClient(url, "key"))
		select {
		case <-hit:
			t.Fatal("AvailNZB was contacted by an install that never enabled it")
		case <-time.After(250 * time.Millisecond):
		}
	})

	t.Run("on refreshes backbones", func(t *testing.T) {
		url, hit := newProbe(t)
		prefetchAvailNZBBackbones(&config.Config{AvailNZBMode: "on"}, availnzb.NewClient(url, "key"))
		select {
		case <-hit:
		case <-time.After(5 * time.Second):
			t.Fatal("AvailNZB was not contacted after opting in")
		}
	})
}
