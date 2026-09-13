package stremio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/tmdb"
)

// slowTMDBStub answers every request after sleeping delay, counting calls.
// It serves a shape valid for both listing and recommendation endpoints.
func slowTMDBStub(t *testing.T, delay time.Duration) (*tmdb.Client, *int64) {
	t.Helper()
	var calls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"results":[],"total_pages":1,"total_results":0}`))
	}))
	t.Cleanup(ts.Close)
	client := tmdb.NewClient("test-key")
	client.BaseURL = ts.URL
	return client, &calls
}

// TestHigherRankedCatalogIDsStopsOnExpiredContext proves that de-duplicating
// against higher-ranked catalogs does not keep paying for more of them once
// the request's own deadline is gone. Before the fix, every higher catalog
// was fetched regardless of ctx, so viewing a catalog ranked behind several
// slow ones could take their combined latency instead of roughly one of it.
func TestHigherRankedCatalogIDsStopsOnExpiredContext(t *testing.T) {
	logger.Init("ERROR")
	delay := 40 * time.Millisecond
	client, calls := slowTMDBStub(t, delay)
	srv := &Server{tmdbClient: client}

	// Four movie catalogs, ranked in this order; the one under test is last,
	// so three higher-ranked catalogs would normally be fetched first.
	rankedIDs := []string{"tmdb.trending.movie", "tmdb.popular.movie", "tmdb.top_rated.movie", "tmdb.now_playing.movie"}
	profile := &config.MetadataProfileConfig{Catalogs: make([]config.CatalogToggle, 0, len(rankedIDs))}
	for _, id := range rankedIDs {
		profile.Catalogs = append(profile.Catalogs, config.CatalogToggle{ID: id, Enabled: true})
	}
	current, ok := catalogDefByID("tmdb.now_playing.movie")
	if !ok {
		t.Fatal("tmdb.now_playing.movie missing from the registry")
	}

	// A deadline that only ever allows about one of the three higher fetches
	// to complete.
	ctx, cancel := context.WithTimeout(context.Background(), delay+15*time.Millisecond)
	defer cancel()

	start := time.Now()
	_ = srv.higherRankedCatalogIDs(ctx, profile, current)
	elapsed := time.Since(start)

	got := atomic.LoadInt64(calls)
	if got >= 3 {
		t.Fatalf("higherRankedCatalogIDs made %d calls despite an expired context, want it to stop well short of all 3 higher-ranked catalogs", got)
	}
	if elapsed > 3*delay {
		t.Fatalf("higherRankedCatalogIDs took %v, want it bounded near the context deadline (%v) instead of the sum of every higher catalog's latency", elapsed, delay)
	}
}

// TestBecauseYouWatchedCatalogBoundedBySlowProvider proves the recommendation
// row does not run to the sum of up to ~19 sequential provider calls (10 seed
// lookups plus up to 9 recommendation pages) when the provider is slow.
// Before the fix, neither loop checked ctx between iterations, so a slow or
// degraded TMDB could make one Jellyfin catalog page take tens of seconds —
// long past what a client like Infuse waits for a browse request.
func TestBecauseYouWatchedCatalogBoundedBySlowProvider(t *testing.T) {
	logger.Init("ERROR")
	delay := 30 * time.Millisecond
	client, calls := slowTMDBStub(t, delay)

	dir, err := os.MkdirTemp("", "because_you_watched_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	mgr, err := persistence.GetManager(dir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	// GetManager is a process-wide singleton shared by the whole test binary
	// (see newBadReleaseTestServer), so a stream name unique to this test plus
	// an explicit cleanup keeps these rows from leaking into any other test's
	// watch-history query.
	const testStream = "catalog-deadline-test-stream"
	t.Cleanup(func() { _, _ = mgr.DeleteAttempts(testStream) })

	// Ten distinct watched movies, imdb-id keyed so resolving each one to a
	// TMDB id is a real (stubbed, slow) network call, not the free tmdb:N
	// shortcut. Ten is becauseYouWatchedWindow, so an unbounded scoring loop
	// would make ten sequential calls before even reaching recommendations.
	for i := 0; i < 10; i++ {
		mgr.RecordAttempt(persistence.RecordAttemptParams{
			StreamName:  testStream,
			ContentType: "movie",
			ContentID:   "tt000000" + string(rune('0'+i)),
			Success:     true,
		})
	}

	srv := &Server{tmdbClient: client, attemptRecorder: mgr}
	def, ok := catalogDefByID("streamnzb.because-you-watched.movie")
	if !ok {
		t.Fatal("streamnzb.because-you-watched.movie missing from the registry")
	}

	ctx, cancel := context.WithTimeout(context.Background(), delay+15*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = srv.becauseYouWatchedCatalog(ctx, def, catalogRequest{Type: "movie", ID: def.ID, StreamName: testStream, Profile: &config.MetadataProfileConfig{}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("becauseYouWatchedCatalog: %v", err)
	}

	got := atomic.LoadInt64(calls)
	if got >= 10 {
		t.Fatalf("becauseYouWatchedCatalog made %d provider calls despite an expired context, want it to stop well short of the full 10-seed window", got)
	}
	if elapsed > 5*delay {
		t.Fatalf("becauseYouWatchedCatalog took %v, want it bounded near the context deadline (%v) instead of the sum of up to 19 sequential provider calls", elapsed, delay)
	}
}
