package indexer

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
)

type mockIndexer struct {
	name        string
	searchCalls int
	response    *SearchResponse
}

func (m *mockIndexer) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	m.searchCalls++
	return m.response, nil
}

func (m *mockIndexer) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	return nil, nil
}

func (m *mockIndexer) Ping(ctx context.Context) error {
	return nil
}

func (m *mockIndexer) Name() string {
	return m.name
}

func (m *mockIndexer) GetUsage() Usage {
	return Usage{}
}

func TestQueryCacheHitAndMiss(t *testing.T) {
	qc := NewQueryCache()
	mock := &mockIndexer{
		name: "MockIndexer",
		response: &SearchResponse{
			Releases: []*release.Release{
				{Title: "Test Release 1", Indexer: "MockIndexer"},
			},
		},
	}
	cached := NewCachedIndexer(mock, qc, 10*time.Minute)

	req1 := SearchRequest{
		SearchMode:  "id",
		IMDbID:      "tt1234567",
		Cat:         "2000",
		StreamLabel: "Stream1",
	}

	// First search -> miss, calls underlying indexer
	resp1, err := cached.Search(context.Background(), req1)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if mock.searchCalls != 1 {
		t.Fatalf("expected 1 search call, got %d", mock.searchCalls)
	}
	if len(resp1.Releases) != 1 || resp1.Releases[0].Title != "Test Release 1" {
		t.Fatalf("unexpected releases in resp1: %+v", resp1.Releases)
	}

	// Second search with identical params but different StreamLabel -> hit, does NOT call underlying indexer
	req2 := SearchRequest{
		SearchMode:  "id",
		IMDbID:      "tt1234567",
		Cat:         "2000",
		StreamLabel: "Stream2",
	}
	resp2, err := cached.Search(context.Background(), req2)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if mock.searchCalls != 1 {
		t.Fatalf("expected 1 search call after cache hit, got %d", mock.searchCalls)
	}
	if len(resp2.Releases) != 1 || resp2.Releases[0].Title != "Test Release 1" {
		t.Fatalf("unexpected releases in resp2: %+v", resp2.Releases)
	}

	// Verify mutating returned response in Stream2 does not mutate cache
	resp2.Releases[0].Title = "Mutated Title"
	resp3, _ := cached.Search(context.Background(), req1)
	if resp3.Releases[0].Title != "Test Release 1" {
		t.Fatalf("cache was mutated by caller! expected 'Test Release 1', got %q", resp3.Releases[0].Title)
	}
}

func TestQueryCacheDifferentQueryParamsMiss(t *testing.T) {
	qc := NewQueryCache()
	mock := &mockIndexer{
		name: "MockIndexer",
		response: &SearchResponse{
			Releases: []*release.Release{
				{Title: "Test Release 1"},
			},
		},
	}
	cached := NewCachedIndexer(mock, qc, 10*time.Minute)

	req1 := SearchRequest{
		SearchMode: "id",
		IMDbID:     "tt1234567",
		Cat:        "2000",
	}
	req2 := SearchRequest{
		SearchMode: "id",
		IMDbID:     "tt7654321", // Different IMDb ID
		Cat:        "2000",
	}

	_, _ = cached.Search(context.Background(), req1)
	if mock.searchCalls != 1 {
		t.Fatalf("expected 1 search call, got %d", mock.searchCalls)
	}

	_, _ = cached.Search(context.Background(), req2)
	if mock.searchCalls != 2 {
		t.Fatalf("expected 2 search calls for different IMDb ID, got %d", mock.searchCalls)
	}
}

func TestQueryCacheClear(t *testing.T) {
	qc := NewQueryCache()
	mock := &mockIndexer{
		name: "MockIndexer",
		response: &SearchResponse{
			Releases: []*release.Release{{Title: "Test"}},
		},
	}
	cached := NewCachedIndexer(mock, qc, 10*time.Minute)

	req := SearchRequest{SearchMode: "id", IMDbID: "tt123"}
	_, _ = cached.Search(context.Background(), req)
	if mock.searchCalls != 1 {
		t.Fatalf("expected 1 search call, got %d", mock.searchCalls)
	}

	qc.Clear()

	_, _ = cached.Search(context.Background(), req)
	if mock.searchCalls != 2 {
		t.Fatalf("expected 2 search calls after cache clear, got %d", mock.searchCalls)
	}
}

type slowMockIndexer struct {
	name     string
	delay    time.Duration
	onSearch func()
	response *SearchResponse
}

func (s *slowMockIndexer) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if s.onSearch != nil {
		s.onSearch()
	}
	time.Sleep(s.delay)
	return s.response, nil
}

func (s *slowMockIndexer) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	return nil, nil
}

func (s *slowMockIndexer) Ping(ctx context.Context) error {
	return nil
}

func (s *slowMockIndexer) Name() string {
	return s.name
}

func (s *slowMockIndexer) GetUsage() Usage {
	return Usage{}
}

func TestQueryCacheConcurrentInFlightDeduplication(t *testing.T) {
	qc := NewQueryCache()
	calls := int32(0)
	slowMock := &slowMockIndexer{
		name:  "SlowMock",
		delay: 50 * time.Millisecond,
		onSearch: func() {
			atomic.AddInt32(&calls, 1)
		},
		response: &SearchResponse{
			Releases: []*release.Release{{Title: "Concurrent Release"}},
		},
	}
	cached := NewCachedIndexer(slowMock, qc, 10*time.Minute)

	req1 := SearchRequest{SearchMode: "id", IMDbID: "tt9999", StreamLabel: "StreamA"}
	req2 := SearchRequest{SearchMode: "id", IMDbID: "tt9999", StreamLabel: "StreamB"}

	var wg sync.WaitGroup
	wg.Add(2)

	var res1, res2 *SearchResponse
	go func() {
		defer wg.Done()
		res1, _ = cached.Search(context.Background(), req1)
	}()
	go func() {
		defer wg.Done()
		res2, _ = cached.Search(context.Background(), req2)
	}()

	wg.Wait()

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected 1 underlying search call for concurrent in-flight requests, got %d", calls)
	}
	if res1 == nil || res2 == nil || len(res1.Releases) != 1 || len(res2.Releases) != 1 {
		t.Fatalf("both callers should receive search response, got res1=%v, res2=%v", res1, res2)
	}
}

// Regression: request-shaping fields must be part of the cache key. An
// anime-widened TV-category override and a plain request for the same content
// previously shared one entry, serving silently wrong results for the TTL.
func TestBuildQueryCacheKeyIncludesRequestShapingFields(t *testing.T) {
	base := SearchRequest{SearchMode: "id", TVDBID: "81797", Season: "10", Episode: "2"}

	animeCats := "5000,5070"
	widened := base
	widened.OptionalOverrides = &config.IndexerSearchConfig{TVCategories: &animeCats}
	if BuildQueryCacheKey("idx", base) == BuildQueryCacheKey("idx", widened) {
		t.Fatal("TV-category override must change the cache key")
	}

	withKitsu := base
	withKitsu.KitsuID = "12"
	if BuildQueryCacheKey("idx", base) == BuildQueryCacheKey("idx", withKitsu) {
		t.Fatal("KitsuID must change the cache key")
	}

	withAbs := base
	withAbs.AbsoluteEpisode = "337"
	if BuildQueryCacheKey("idx", base) == BuildQueryCacheKey("idx", withAbs) {
		t.Fatal("AbsoluteEpisode must change the cache key")
	}
}
