package newznab

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"testing"
	"time"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewznabSearch(t *testing.T) {
	logger.Init("DEBUG")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.URL.Query().Get("apikey") != "test-api-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		t := r.URL.Query().Get("t")
		if t != "movie" && t != "search" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<title>Mock Newznab Search</title>
<newznab:response offset="0" total="1"/>
<item>
	<title>Test Movie 2024</title>
	<link>http://example.com/nzb/1</link>
	<guid isPermaLink="false">123456</guid>
	<pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
	<category>Movies &gt; HD</category>
	<description>Test Movie 2024</description>
	<newznab:attr name="size" value="1073741824" />
</item>
</channel>
</rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	})
	req := indexer.SearchRequest{
		Cat:    "2000",
		Query:  "Test Movie",
		IMDbID: "tt1234567",
	}

	resp, err := client.Search(req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Channel.Items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(resp.Channel.Items))
	}

	item := resp.Channel.Items[0]
	if item.Title != "Test Movie 2024" {
		t.Errorf("Expected title 'Test Movie 2024', got '%s'", item.Title)
	}

	if item.Size != 1073741824 {
		t.Errorf("Expected size 1073741824, got %d", item.Size)
	}

	if item.SourceIndexer == nil {
		t.Error("SourceIndexer was not populated")
	}
}

func TestNewznabPagination(t *testing.T) {
	logger.Init("DEBUG")
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		limit := r.URL.Query().Get("limit")

		w.Header().Set("Content-Type", "application/xml")

		if limit == "2" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<newznab:response offset="0" total="2"/>
<item><title>Item 1</title><newznab:attr name="size" value="100"/></item>
<item><title>Item 2</title><newznab:attr name="size" value="200"/></item>
</channel>
</rss>`)
		} else {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
		}
		logger.Debug("Mock server call", "count", callCount, "limit", limit)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	})
	req := indexer.SearchRequest{
		Limit: 2,
	}

	resp, err := client.Search(req)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(resp.Channel.Items) != 2 {
		t.Fatalf("Expected 2 items, got %d", len(resp.Channel.Items))
	}

	if callCount != 1 {
		t.Errorf("Expected 1 server call (indexer handles pagination), got %d", callCount)
	}
}

func TestNewznabPing(t *testing.T) {
	logger.Init("DEBUG")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	})
	err := client.Ping()
	if err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

func TestNewClientUsesEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.IndexerConfig
		want time.Duration
	}{
		{
			name: "default internal timeout",
			cfg:  config.IndexerConfig{Name: "Internal"},
			want: 5 * time.Second,
		},
		{
			name: "default aggregator timeout",
			cfg:  config.IndexerConfig{Name: "Aggregator", Type: "aggregator"},
			want: 10 * time.Second,
		},
		{
			name: "explicit override",
			cfg:  config.IndexerConfig{Name: "Override", Type: "aggregator", TimeoutSeconds: 12},
			want: 12 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.cfg)
			if got := client.client.Timeout; got != tt.want {
				t.Fatalf("client timeout = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeDownloadURL(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config.IndexerConfig
		rawURL string
		want   string
	}{
		{
			name:   "adds api key and converts guid to id",
			cfg:    config.IndexerConfig{URL: "https://nzbfinder.ws", APIKey: "test-key"},
			rawURL: "https://api.nzbfinder.ws/api?t=get&guid=abc123",
			want:   "https://api.nzbfinder.ws/api?apikey=test-key&guid=abc123&id=abc123&t=get",
		},
		{
			name:   "preserves existing api key",
			cfg:    config.IndexerConfig{URL: "https://nzbfinder.ws", APIKey: "test-key"},
			rawURL: "https://nzbfinder.ws/api?t=get&id=abc123&apikey=existing-key",
			want:   "https://nzbfinder.ws/api?t=get&id=abc123&apikey=existing-key",
		},
		{
			name:   "does not rewrite other host",
			cfg:    config.IndexerConfig{URL: "https://nzbfinder.ws", APIKey: "test-key"},
			rawURL: "https://other.example/api?t=get&id=abc123",
			want:   "https://other.example/api?t=get&id=abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.cfg)
			if got := client.normalizeDownloadURL(tt.rawURL); got != tt.want {
				t.Fatalf("normalizeDownloadURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDownloadNZBUsesNormalizedURL(t *testing.T) {
	logger.Init("DEBUG")
	var gotAPIKey string
	var gotID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.URL.Query().Get("apikey")
		gotID = r.URL.Query().Get("id")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<nzb></nzb>")
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	})

	data, err := client.DownloadNZB(context.Background(), server.URL+"/api?t=get&guid=guid-123")
	if err != nil {
		t.Fatalf("DownloadNZB failed: %v", err)
	}
	if gotAPIKey != "test-api-key" {
		t.Fatalf("apikey = %q, want %q", gotAPIKey, "test-api-key")
	}
	if gotID != "guid-123" {
		t.Fatalf("id = %q, want %q", gotID, "guid-123")
	}
	if got := string(data); got != "<nzb></nzb>" {
		t.Fatalf("DownloadNZB data = %q, want %q", got, "<nzb></nzb>")
	}
}


