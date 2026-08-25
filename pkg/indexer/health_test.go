package indexer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"streamnzb/pkg/core/health"
)

func testRegistry(t *testing.T) *health.Registry {
	t.Helper()
	reg, err := health.Init(nil)
	if err != nil {
		t.Fatalf("health.Init: %v", err)
	}
	t.Cleanup(func() { reg.Retain(health.KindIndexer, nil) })
	return reg
}

func TestReportHealthClassifiesOutcomes(t *testing.T) {
	reg := testRegistry(t)

	// A rejected credential is the only search outcome allowed to park an
	// indexer.
	ReportHealth("nzbfinder", fmt.Errorf("wrapped: %w", ErrAuthFailed))
	if !reg.Blocked(health.KindIndexer, "nzbfinder") {
		t.Fatal("an auth failure must block the indexer")
	}

	// A rate limit passes with time, so it degrades rather than blocks.
	ReportHealth("throttled", fmt.Errorf("slow down: %w", ErrRateLimited))
	if reg.Blocked(health.KindIndexer, "throttled") {
		t.Fatal("a rate limit must not block")
	}
	if rec, ok := reg.Lookup(health.KindIndexer, "throttled"); !ok || rec.State != health.StateDegraded {
		t.Fatalf("rate limit should degrade, got %+v", rec)
	}

	// Everything else is inconclusive and must leave no verdict at all.
	ReportHealth("flaky", errors.New("context deadline exceeded"))
	if _, ok := reg.Lookup(health.KindIndexer, "flaky"); ok {
		t.Fatal("a timeout must not record a verdict")
	}

	// Success outranks whatever we stored earlier.
	ReportHealth("nzbfinder", nil)
	if reg.Blocked(health.KindIndexer, "nzbfinder") {
		t.Fatal("a successful search must clear the block")
	}
}

func TestBlockedIndexerIsSkippedBySearch(t *testing.T) {
	reg := testRegistry(t)

	var calls int
	idx := &testIndexer{
		name: "blocked-one",
		searchFn: func(SearchRequest) (*SearchResponse, error) {
			calls++
			return &SearchResponse{Channel: Channel{Items: []Item{{Title: "x", Size: 1}}}}, nil
		},
	}
	reg.Report(health.KindIndexer, "blocked-one", health.StateBlocked, health.ReasonAuthFailed, "code 100")

	items, err := searchItemsForIndexer(context.Background(), idx, SearchRequest{})
	if err != nil {
		t.Fatalf("searching a blocked indexer must not error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no items from a blocked indexer, got %d", len(items))
	}
	if calls != 0 {
		t.Fatalf("a blocked indexer must not be queried, got %d calls", calls)
	}

	// Cleared verdict, and the indexer is back in rotation.
	reg.Forget(health.KindIndexer, "blocked-one")
	if _, err := searchItemsForIndexer(context.Background(), idx, SearchRequest{}); err != nil {
		t.Fatalf("search after clearing: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected the indexer to be queried once cleared, got %d calls", calls)
	}
}
