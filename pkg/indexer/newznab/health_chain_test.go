package newznab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/health"
	"streamnzb/pkg/indexer"
)

// TestBadAPIKeyBlocksIndexerThroughTheRealChain walks the path a real search
// takes — Aggregator over CachedIndexer over the newznab client — because every
// link in it has to preserve both the error and the indexer's name for the
// health verdict to land where the UI looks for it.
func TestBadAPIKeyBlocksIndexerThroughTheRealChain(t *testing.T) {
	reg, err := health.Init(nil)
	if err != nil {
		t.Fatalf("health.Init: %v", err)
	}
	t.Cleanup(func() { reg.Retain(health.KindIndexer, nil) })

	// What a newznab indexer answers for a key it does not recognise: an error
	// document under HTTP 200.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Incorrect user credentials"/>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "drunkenslug",
		URL:    server.URL,
		APIKey: "bogus",
	}, testNewznabUsageManager(t))
	agg := indexer.NewAggregator(indexer.NewCachedIndexer(client, indexer.NewQueryCache(), 10*time.Minute))

	if _, err := agg.Search(context.Background(), indexer.SearchRequest{
		SearchMode: "id",
		IMDbID:     "tt0111161",
		Cat:        "2000",
	}); err != nil {
		t.Fatalf("aggregated search should absorb the indexer error: %v", err)
	}

	if !reg.Blocked(health.KindIndexer, "drunkenslug") {
		t.Fatalf("a rejected API key must block the indexer, got %+v", reg.Snapshot())
	}
	rec, _ := reg.Lookup(health.KindIndexer, "drunkenslug")
	if rec.Reason != health.ReasonAuthFailed {
		t.Fatalf("reason = %q, want %q", rec.Reason, health.ReasonAuthFailed)
	}
}

// TestPingRejectsAnErrorDocumentUnderHTTP200 pins the failure mode that made a
// bogus API key look healthy: newznab answers a rejected key with an error
// document and a 200 status, so a ping that only reads the status line reports
// success for credentials the server just refused — and, worse, clears the
// stored verdict when the retry button or the background probe calls it.
func TestPingRejectsAnErrorDocumentUnderHTTP200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Incorrect user credentials"/>`)
	}))
	defer server.Close()

	client := NewClient(config.IndexerConfig{
		Name:   "drunkenslug",
		URL:    server.URL,
		APIKey: "bogus",
	}, testNewznabUsageManager(t))

	err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("ping must fail when the indexer rejects the API key")
	}
	if !errors.Is(err, indexer.ErrAuthFailed) {
		t.Fatalf("ping error = %v, want ErrAuthFailed", err)
	}
}

// TestPingExercisesTheAPIKeyNotJustCaps simulates the indexer that exposed the
// last hole: caps served publicly (200 with any key), searches gated on the
// key. A ping that asks for caps calls this indexer healthy with a bogus key;
// asking for a search is what gets the honest answer.
func TestPingExercisesTheAPIKeyNotJustCaps(t *testing.T) {
	const goodKey = "real-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			// Public caps: answers regardless of the key.
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><caps><server title="test"/><limits max="100" default="100"/></caps>`)
			return
		}
		if r.URL.Query().Get("apikey") != goodKey {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Incorrect user credentials"/>`)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><item><title>x</title></item></channel></rss>`)
	}))
	defer server.Close()

	bad := NewClient(config.IndexerConfig{Name: "althub", URL: server.URL, APIKey: "bogus"}, testNewznabUsageManager(t))
	err := bad.Ping(context.Background())
	if !errors.Is(err, indexer.ErrAuthFailed) {
		t.Fatalf("ping with a bogus key behind public caps = %v, want ErrAuthFailed", err)
	}

	// A working key must still ping clean, or every indexer would be parked.
	good := NewClient(config.IndexerConfig{Name: "althub", URL: server.URL, APIKey: goodKey}, testNewznabUsageManager(t))
	if err := good.Ping(context.Background()); err != nil {
		t.Fatalf("ping with a valid key: %v", err)
	}
}
