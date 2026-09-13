package stremio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// slowRecommendingTMDBStub is slowTMDBStub's counterpart for exercising the
// enrichment path: /recommendations answers slowly with real, nonempty
// results (so perSeed is nonempty and the post-loop deadline check is what
// has to stop the enrichment step, not the pre-existing empty-perSeed check),
// while /external_ids is counted separately so a test can assert enrichment
// never ran.
func slowRecommendingTMDBStub(t *testing.T, delay time.Duration) (client *tmdb.Client, recommendCalls, externalIDCalls *int64) {
	t.Helper()
	var recCalls, extCalls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "external_ids") {
			atomic.AddInt64(&extCalls, 1)
			_, _ = w.Write([]byte(`{"id": 1, "imdb_id": "tt0000001"}`))
			return
		}
		atomic.AddInt64(&recCalls, 1)
		time.Sleep(delay)
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"results":[{"id":501,"title":"Recommended"}]}`))
	}))
	t.Cleanup(ts.Close)
	client = tmdb.NewClient("test-key")
	client.BaseURL = ts.URL
	return client, &recCalls, &extCalls
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

// TestBecauseYouWatchedCatalogSkipsEnrichmentPastDeadline exercises the
// specific gap the prior test missed: it seeds watched history with tmdb:
// ids, which resolve to a seed with no network call at all, so the run
// reaches GetRecommendations directly. The stub returns real, nonempty
// results, so perSeed is nonempty when the per-seed loop ends -- the
// pre-existing "if len(perSeed) == 0" check cannot be what stops the run.
// Only the ctx check added right after that loop, before filterTMDBResults
// and resolveIMDbIDs, can prevent the external_ids enrichment calls that
// would otherwise certainly follow.
func TestBecauseYouWatchedCatalogSkipsEnrichmentPastDeadline(t *testing.T) {
	logger.Init("ERROR")
	delay := 30 * time.Millisecond
	client, recCalls, extCalls := slowRecommendingTMDBStub(t, delay)

	dir, err := os.MkdirTemp("", "because_you_watched_enrichment_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	mgr, err := persistence.GetManager(dir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	const testStream = "catalog-deadline-enrichment-test-stream"
	t.Cleanup(func() { _, _ = mgr.DeleteAttempts(testStream) })

	// Three seeds (becauseYouWatchedSeeds) resolved for free via the tmdb:
	// shortcut in tmdbIDForPreviewID, so the per-seed GetRecommendations
	// round is what the deadline has to interrupt.
	for i, id := range []string{"tmdb:101", "tmdb:102", "tmdb:103"} {
		mgr.RecordAttempt(persistence.RecordAttemptParams{
			StreamName:  testStream,
			ContentType: "movie",
			ContentID:   id,
			Success:     true,
		})
		_ = i
	}

	srv := &Server{tmdbClient: client, attemptRecorder: mgr}
	def, ok := catalogDefByID("streamnzb.because-you-watched.movie")
	if !ok {
		t.Fatal("streamnzb.because-you-watched.movie missing from the registry")
	}

	// Expires after roughly one and a half recommendation calls: at least
	// one seed's call completes (so perSeed is nonempty), but the loop over
	// all three seeds cannot finish before the deadline is gone.
	ctx, cancel := context.WithTimeout(context.Background(), delay+delay/2)
	defer cancel()

	_, err = srv.becauseYouWatchedCatalog(ctx, def, catalogRequest{Type: "movie", ID: def.ID, StreamName: testStream, Profile: &config.MetadataProfileConfig{}})
	if err != nil {
		t.Fatalf("becauseYouWatchedCatalog: %v", err)
	}

	if got := atomic.LoadInt64(recCalls); got == 0 {
		t.Fatal("no GetRecommendations call was made; the test does not reach the code path it means to exercise")
	}
	if got := atomic.LoadInt64(recCalls); got >= 3 {
		t.Fatalf("GetRecommendations ran for all %d seeds despite an expired context", got)
	}
	if got := atomic.LoadInt64(extCalls); got != 0 {
		t.Fatalf("GetExternalIDs (enrichment) ran %d times after the deadline had already passed, want 0", got)
	}
}
