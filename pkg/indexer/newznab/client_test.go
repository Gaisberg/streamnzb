package newznab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"sync"
	"testing"
	"time"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

var (
	newznabUsageManagerOnce sync.Once
	newznabUsageManager     *indexer.UsageManager
	newznabUsageManagerErr  error
)

func testNewznabUsageManager(t *testing.T) *indexer.UsageManager {
	t.Helper()

	newznabUsageManagerOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "streamnzb-newznab-usage-")
		if err != nil {
			newznabUsageManagerErr = err
			return
		}
		stateMgr, err := persistence.GetManager(tempDir)
		if err != nil {
			newznabUsageManagerErr = err
			return
		}
		newznabUsageManager, newznabUsageManagerErr = indexer.GetUsageManager(stateMgr)
	})
	if newznabUsageManagerErr != nil {
		t.Fatalf("GetUsageManager: %v", newznabUsageManagerErr)
	}
	return newznabUsageManager
}

func TestNewznabSearch(t *testing.T) {
	logger.Init("DEBUG")
	var gotUserAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")

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
		Name:        "MockIndexer",
		URL:         server.URL,
		APIKey:      "test-api-key",
		QueryHeader: "Prowlarr/2.3.0.5236",
	}, nil)
	req := indexer.SearchRequest{
		Cat:    "2000",
		Query:  "Test Movie",
		IMDbID: "tt1234567",
	}

	resp, err := client.Search(context.Background(), req)
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
	if gotUserAgent != "Prowlarr/2.3.0.5236" {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, "Prowlarr/2.3.0.5236")
	}
}

// Users paste full API endpoints copied from other tools, e.g. NZBHydra2's
// "http://host:5076/api?indexers=abc". The query params must ride along on
// every request and the api path must not be appended twice.
func TestNewClientPreservesURLQueryParams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "NZBHydra2",
		URL:    server.URL + "/api?indexers=abc,def",
		APIKey: "test-api-key",
	}, nil)

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:        "2000",
		Query:      "Test Movie",
		SearchMode: "text",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if gotPath != "/api" {
		t.Fatalf("path = %q, want %q", gotPath, "/api")
	}
	if got := gotQuery.Get("indexers"); got != "abc,def" {
		t.Fatalf("indexers = %q, want %q", got, "abc,def")
	}
	if got := gotQuery.Get("apikey"); got != "test-api-key" {
		t.Fatalf("apikey = %q, want %q", got, "test-api-key")
	}

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if gotPath != "/api" {
		t.Fatalf("ping path = %q, want %q", gotPath, "/api")
	}
	if got := gotQuery.Get("indexers"); got != "abc,def" {
		t.Fatalf("ping indexers = %q, want %q", got, "abc,def")
	}
}

// The same pasted-endpoint tolerance for api_path: a query string there must
// become request params instead of corrupting the path.
func TestNewClientPreservesAPIPathQueryParams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:    "NZBHydra2",
		URL:     server.URL,
		APIPath: "/api?indexers=abc",
		APIKey:  "test-api-key",
	}, nil)

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:        "2000",
		Query:      "Test Movie",
		SearchMode: "text",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if gotPath != "/api" {
		t.Fatalf("path = %q, want %q", gotPath, "/api")
	}
	if got := gotQuery.Get("indexers"); got != "abc" {
		t.Fatalf("indexers = %q, want %q", got, "abc")
	}
}

// NZBHydra2 tags each result with the indexer it came from; the release must
// surface it as "<configured name> - <sub-indexer>".
func TestSearchSurfacesHydraIndexerName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<newznab:response offset="0" total="2"/>
<item>
	<title>Tagged Release</title>
	<newznab:attr name="size" value="100"/>
	<newznab:attr name="hydraIndexerName" value="NZBgeek"/>
</item>
<item>
	<title>Untagged Release</title>
	<newznab:attr name="size" value="200"/>
</item>
</channel>
</rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "Hydra",
		Type:   "aggregator",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)

	resp, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:        "2000",
		Query:      "Test Movie",
		SearchMode: "text",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Channel.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Channel.Items))
	}

	tagged := resp.Channel.Items[0].ToRelease()
	if tagged.Indexer != "Hydra - NZBgeek" {
		t.Fatalf("tagged release indexer = %q, want %q", tagged.Indexer, "Hydra - NZBgeek")
	}
	untagged := resp.Channel.Items[1].ToRelease()
	if untagged.Indexer != "Hydra" {
		t.Fatalf("untagged release indexer = %q, want %q", untagged.Indexer, "Hydra")
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
	}, nil)
	req := indexer.SearchRequest{
		Limit: 2,
	}

	resp, err := client.Search(context.Background(), req)
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

func TestNewznabSearchLimitUsesCapsMaxWhenRequestLimitIsZero(t *testing.T) {
	var gotLimit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Limits: indexer.CapsLimits{Max: 500}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{Limit: 0})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if gotLimit != "500" {
		t.Fatalf("limit = %q, want %q", gotLimit, "500")
	}
}

func TestNewznabSearchLimitFallsBackTo2000WithoutCaps(t *testing.T) {
	var gotLimit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)

	_, err := client.Search(context.Background(), indexer.SearchRequest{Limit: 0})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if gotLimit != "2000" {
		t.Fatalf("limit = %q, want %q", gotLimit, "2000")
	}
}

func TestNewznabSearchLimitKeepsExplicitValueEvenAboveCapsMax(t *testing.T) {
	var gotLimit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Limits: indexer.CapsLimits{Max: 500}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{Limit: 3000})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if gotLimit != "3000" {
		t.Fatalf("limit = %q, want %q", gotLimit, "3000")
	}
}

func TestNewznabPing(t *testing.T) {
	logger.Init("DEBUG")
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		// Ping must issue a real search: caps is often served without
		// authentication, so it cannot vouch for the API key.
		if r.URL.Query().Get("t") == "search" && r.URL.Query().Get("apikey") == "test-api-key" {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:        "MockIndexer",
		URL:         server.URL,
		APIKey:      "test-api-key",
		QueryHeader: "Prowlarr/2.3.0.5236",
	}, nil)
	err := client.Ping(context.Background())
	if err != nil {
		t.Errorf("Ping failed: %v", err)
	}
	if gotUserAgent != "Prowlarr/2.3.0.5236" {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, "Prowlarr/2.3.0.5236")
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
			client := NewClient(tt.cfg, nil)
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
			client := NewClient(tt.cfg, nil)
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
	var gotUserAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.URL.Query().Get("apikey")
		gotID = r.URL.Query().Get("id")
		gotUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<nzb></nzb>")
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:       "MockIndexer",
		URL:        server.URL,
		APIKey:     "test-api-key",
		GrabHeader: "SABnzbd/4.3.0",
	}, nil)

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
	if gotUserAgent != "SABnzbd/4.3.0" {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, "SABnzbd/4.3.0")
	}
}

func TestGetUsageCountsOnlyHitsInsideTrailingWindow(t *testing.T) {
	usageManager := testNewznabUsageManager(t)
	name := "newznab-rollover-usage"

	client := NewClient(config.IndexerConfig{
		Name:         name,
		APIHitsDay:   10,
		DownloadsDay: 5,
	}, usageManager)

	usageManager.RecordHits(name, 10, 5, time.Now().Add(-25*time.Hour))
	usageManager.RecordHits(name, 2, 1, time.Now())

	usage := client.GetUsage()
	if usage.APIHitsUsed != 2 || usage.DownloadsUsed != 1 {
		t.Fatalf("expected only hits inside the window, got hits=%d downloads=%d", usage.APIHitsUsed, usage.DownloadsUsed)
	}
	if usage.APIHitsRemaining != 8 || usage.DownloadsRemaining != 4 {
		t.Fatalf("expected refreshed remaining counts, got api=%d downloads=%d", usage.APIHitsRemaining, usage.DownloadsRemaining)
	}
	if usage.AllTimeAPIHitsUsed != 12 || usage.AllTimeDownloadsUsed != 6 {
		t.Fatalf("expected all-time usage to keep every hit, got hits=%d downloads=%d", usage.AllTimeAPIHitsUsed, usage.AllTimeDownloadsUsed)
	}
}

func TestLimitChecksRefreshUsageFromPersistedHits(t *testing.T) {
	usageManager := testNewznabUsageManager(t)
	name := "newznab-rollover-limits"

	client := NewClient(config.IndexerConfig{
		Name:         name,
		APIHitsDay:   10,
		DownloadsDay: 5,
	}, usageManager)

	// Hits older than the trailing window never count against the budget.
	usageManager.RecordHits(name, 10, 5, time.Now().Add(-25*time.Hour))
	if err := client.checkAPILimit(); err != nil {
		t.Fatalf("checkAPILimit() error = %v, want nil for hits outside the window", err)
	}
	if err := client.checkDownloadLimit(); err != nil {
		t.Fatalf("checkDownloadLimit() error = %v, want nil for hits outside the window", err)
	}

	// Fresh hits recorded behind the client's back are picked up on refresh.
	usageManager.RecordHits(name, 0, 5, time.Now())
	if err := client.checkDownloadLimit(); err == nil {
		t.Fatal("checkDownloadLimit() = nil, want an error once persisted downloads spend the budget")
	}
}

func TestSearchTVTextModeDoesNotUseTVSearchParams(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Searching: indexer.CapsSearching{TVSearch: true}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		Query:             "The Last of Us S01E02",
		Season:            "1",
		Episode:           "2",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "text",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if got := gotQuery.Get("t"); got != "search" {
		t.Fatalf("t = %q, want %q", got, "search")
	}
	if got := gotQuery.Get("q"); got != "The Last of Us S01E02" {
		t.Fatalf("q = %q, want %q", got, "The Last of Us S01E02")
	}
	if got := gotQuery.Get("season"); got != "" {
		t.Fatalf("season = %q, want empty", got)
	}
	if got := gotQuery.Get("ep"); got != "" {
		t.Fatalf("ep = %q, want empty", got)
	}
}

func TestSearchTVIDModeKeepsTVSearchParams(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Searching: indexer.CapsSearching{TVSearch: true}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		TVDBID:            "121361",
		Season:            "1",
		Episode:           "2",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if got := gotQuery.Get("t"); got != "tvsearch" {
		t.Fatalf("t = %q, want %q", got, "tvsearch")
	}
	if got := gotQuery.Get("tvdbid"); got != "121361" {
		t.Fatalf("tvdbid = %q, want %q", got, "121361")
	}
	if got := gotQuery.Get("season"); got != "1" {
		t.Fatalf("season = %q, want %q", got, "1")
	}
	if got := gotQuery.Get("ep"); got != "2" {
		t.Fatalf("ep = %q, want %q", got, "2")
	}
}

func TestSearchTVIDModeUsesIMDbIDWhenCapsSupportIt(t *testing.T) {
	var gotQuery url.Values
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{
		Searching: indexer.CapsSearching{
			TVSearch:                true,
			TVSearchSupportedParams: map[string]bool{"imdbid": true, "season": true, "ep": true},
		},
	}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		IMDbID:            "tt1190634",
		Season:            "1",
		Episode:           "2",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := gotQuery.Get("t"); got != "tvsearch" {
		t.Fatalf("t = %q, want %q", got, "tvsearch")
	}
	if got := gotQuery.Get("imdbid"); got != "1190634" {
		t.Fatalf("imdbid = %q, want %q", got, "1190634")
	}
	if got := gotQuery.Get("tvdbid"); got != "" {
		t.Fatalf("tvdbid = %q, want empty", got)
	}
}

func TestSearchTVIDModeUsesTMDBIDWhenCapsSupportIt(t *testing.T) {
	var gotQuery url.Values
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{
		Searching: indexer.CapsSearching{
			TVSearch:                true,
			TVSearchSupportedParams: map[string]bool{"tmdbid": true, "season": true},
		},
	}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		TMDBID:            "250307",
		Season:            "1",
		SeriesSearchScope: config.SeriesSearchScopeSeason,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := gotQuery.Get("tmdbid"); got != "250307" {
		t.Fatalf("tmdbid = %q, want %q", got, "250307")
	}
	if got := gotQuery.Get("season"); got != "1" {
		t.Fatalf("season = %q, want %q", got, "1")
	}
}

func TestSearchTVIDModeSkipsWhenCapsDoNotSupportAvailableIDs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{
		Searching: indexer.CapsSearching{
			TVSearch:                true,
			TVSearchSupportedParams: map[string]bool{"tvdbid": true, "season": true, "ep": true},
		},
	}

	resp, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		IMDbID:            "tt1190634",
		Season:            "1",
		Episode:           "2",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if resp == nil || len(resp.Channel.Items) != 0 {
		t.Fatalf("expected empty response when caps do not support available ids, got %#v", resp)
	}
}

func TestSearchMovieIDModeUsesTMDBIDWhenCapsSupportIt(t *testing.T) {
	var gotQuery url.Values
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{
		Searching: indexer.CapsSearching{
			MovieSearch:                true,
			MovieSearchSupportedParams: map[string]bool{"tmdbid": true},
		},
	}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:        "2000",
		TMDBID:     "83533",
		SearchMode: "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := gotQuery.Get("t"); got != "movie" {
		t.Fatalf("t = %q, want %q", got, "movie")
	}
	if got := gotQuery.Get("tmdbid"); got != "83533" {
		t.Fatalf("tmdbid = %q, want %q", got, "83533")
	}
}

func TestSearchMovieIDModeUsesTMDBIDWithoutCaps(t *testing.T) {
	var gotQuery url.Values
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:        "2000",
		TMDBID:     "83533",
		SearchMode: "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := gotQuery.Get("t"); got != "movie" {
		t.Fatalf("t = %q, want %q", got, "movie")
	}
	if got := gotQuery.Get("tmdbid"); got != "83533" {
		t.Fatalf("tmdbid = %q, want %q", got, "83533")
	}
}

func TestSearchTVIDModeUsesTMDBIDWithoutCaps(t *testing.T) {
	var gotQuery url.Values
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		TMDBID:            "250307",
		Season:            "1",
		SeriesSearchScope: config.SeriesSearchScopeSeason,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := gotQuery.Get("t"); got != "tvsearch" {
		t.Fatalf("t = %q, want %q", got, "tvsearch")
	}
	if got := gotQuery.Get("tmdbid"); got != "250307" {
		t.Fatalf("tmdbid = %q, want %q", got, "250307")
	}
}

func TestSearchTVIDModeOmitsQueryWhenUsingTVSearchParams(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Searching: indexer.CapsSearching{TVSearch: true}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		Query:             "Star Wars Maul Shadow Lord S01E01",
		TVDBID:            "462715",
		Season:            "1",
		Episode:           "1",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if got := gotQuery.Get("t"); got != "tvsearch" {
		t.Fatalf("t = %q, want %q", got, "tvsearch")
	}
	if got := gotQuery.Get("season"); got != "1" {
		t.Fatalf("season = %q, want %q", got, "1")
	}
	if got := gotQuery.Get("ep"); got != "1" {
		t.Fatalf("ep = %q, want %q", got, "1")
	}
	if got := gotQuery.Get("q"); got != "" {
		t.Fatalf("q = %q, want empty", got)
	}
}

func TestSearchTextModeOrdersQueryParams(t *testing.T) {
	var gotRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Searching: indexer.CapsSearching{TVSearch: true}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		Query:             "The Last of Us S01E02",
		Season:            "1",
		Episode:           "2",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "text",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	want := "apikey=test-api-key&t=search&cat=5000&q=The+Last+of+Us+S01E02&offset=0&limit=2000&o=xml"
	if gotRawQuery != want {
		t.Fatalf("raw query = %q, want %q", gotRawQuery, want)
	}
}

func TestSearchTVIDModeOrdersQueryParams(t *testing.T) {
	var gotRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "MockIndexer",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)
	client.caps = &indexer.Caps{Searching: indexer.CapsSearching{TVSearch: true}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:               "5000",
		TVDBID:            "462715",
		Season:            "1",
		Episode:           "1",
		SeriesSearchScope: config.SeriesSearchScopeSeasonEpisode,
		SearchMode:        "id",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	want := "apikey=test-api-key&t=tvsearch&cat=5000&tvdbid=462715&season=1&ep=1&offset=0&limit=2000&o=xml"
	if gotRawQuery != want {
		t.Fatalf("raw query = %q, want %q", gotRawQuery, want)
	}
}

func TestSearchAggregatorIncludesCacheTimeParam(t *testing.T) {
	var gotRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:                   "NZBHydra2",
		Type:                   "aggregator",
		URL:                    server.URL,
		APIKey:                 "test-api-key",
		SearchResultsCacheTime: 60,
	}, nil)
	client.caps = &indexer.Caps{Searching: indexer.CapsSearching{TVSearch: true}}

	_, err := client.Search(context.Background(), indexer.SearchRequest{
		Cat:        "5000",
		Query:      "Interstellar",
		SearchMode: "text",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	want := "apikey=test-api-key&t=search&cat=5000&q=Interstellar&cachetime=60&offset=0&limit=2000&o=xml"
	if gotRawQuery != want {
		t.Fatalf("raw query = %q, want %q", gotRawQuery, want)
	}
}

// The field scenario: Treasuremaps answered 429 to every grab during playback
// failover. The first refusal must open a cooldown so the remaining candidates
// from that indexer fail instantly and locally, instead of each one spending a
// full round trip to be told "too many requests" again.
func TestDownloadNZBOn429OpensCooldownAndStopsFurtherRequests(t *testing.T) {
	logger.Init("ERROR")
	var requests int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "Treasuremaps",
		URL:    server.URL,
		APIKey: "test-api-key",
	}, nil)

	_, err := client.DownloadNZB(context.Background(), server.URL+"/api?t=get&guid=a")
	if !errors.Is(err, indexer.ErrRateLimited) {
		t.Fatalf("a 429 grab must classify as rate limited, got %v", err)
	}

	for i := 0; i < 8; i++ {
		if _, err := client.DownloadNZB(context.Background(), server.URL+"/api?t=get&guid=b"); !errors.Is(err, indexer.ErrRateLimited) {
			t.Fatalf("grab %d during cooldown = %v, want ErrRateLimited", i, err)
		}
	}

	if requests != 1 {
		t.Fatalf("indexer received %d requests; the cooldown should have stopped every grab after the first", requests)
	}
}

// A refused grab returns no NZB, so it must not spend the daily download budget.
func TestDownloadNZBOn429DoesNotSpendDownloadBudget(t *testing.T) {
	logger.Init("ERROR")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:         "Treasuremaps",
		URL:          server.URL,
		APIKey:       "test-api-key",
		DownloadsDay: 100,
	}, nil)

	before := client.GetUsage().DownloadsUsed
	if _, err := client.DownloadNZB(context.Background(), server.URL+"/api?t=get&guid=a"); !errors.Is(err, indexer.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	if after := client.GetUsage().DownloadsUsed; after != before {
		t.Fatalf("DownloadsUsed went %d -> %d; a refused grab must not consume quota", before, after)
	}
}

// 404 stays definitive: that one really is about the NZB, not the indexer.
func TestDownloadNZBOn404DoesNotOpenCooldown(t *testing.T) {
	logger.Init("ERROR")
	var requests int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{Name: "Treasuremaps", URL: server.URL, APIKey: "k"}, nil)

	for i := 0; i < 2; i++ {
		if _, err := client.DownloadNZB(context.Background(), server.URL+"/api?t=get&guid=a"); err == nil {
			t.Fatal("expected an error for 404")
		} else if errors.Is(err, indexer.ErrRateLimited) {
			t.Fatalf("404 must not classify as rate limited, got %v", err)
		}
	}
	if requests != 2 {
		t.Fatalf("indexer received %d requests, want 2; 404 must not open a cooldown", requests)
	}
}

func TestCheckNewznabErrorClassifiesCredentialCodes(t *testing.T) {
	c := &Client{cfg: config.IndexerConfig{Name: "test-indexer"}}

	// The 1xx block is newznab's credential range.
	err := c.checkNewznabError([]byte(`<error code="100" description="Incorrect user credentials"/>`))
	if err == nil || !errors.Is(err, indexer.ErrAuthFailed) {
		t.Fatalf("code 100 = %v, want ErrAuthFailed", err)
	}
	if errors.Is(err, indexer.ErrRateLimited) {
		t.Fatal("a credential rejection must not read as a rate limit")
	}

	// 201 is a quota, which passes with time and must stay separate.
	err = c.checkNewznabError([]byte(`<error code="201" description="Request limit reached"/>`))
	if err == nil || !errors.Is(err, indexer.ErrRateLimited) {
		t.Fatalf("code 201 = %v, want ErrRateLimited", err)
	}
	if errors.Is(err, indexer.ErrAuthFailed) {
		t.Fatal("a quota error must not park the indexer")
	}

	// A server-side fault is inconclusive: neither sentinel applies.
	err = c.checkNewznabError([]byte(`<error code="300" description="No such item"/>`))
	if err == nil || errors.Is(err, indexer.ErrAuthFailed) || errors.Is(err, indexer.ErrRateLimited) {
		t.Fatalf("code 300 = %v, want an unclassified error", err)
	}
}
