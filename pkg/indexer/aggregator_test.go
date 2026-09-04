package indexer

import (
	"context"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
)

type testIndexer struct {
	name     string
	searchFn func(req SearchRequest) (*SearchResponse, error)
}

func (t *testIndexer) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	return t.searchFn(req)
}
func (t *testIndexer) DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error) {
	return nil, nil
}
func (t *testIndexer) Ping(context.Context) error { return nil }
func (t *testIndexer) Name() string               { return t.name }
func (t *testIndexer) GetUsage() Usage            { return Usage{} }

func TestSkipIndexerReasonContentScope(t *testing.T) {
	scope := func(s string) *config.IndexerSearchConfig {
		return &config.IndexerSearchConfig{ContentScope: &s}
	}
	cases := []struct {
		name      string
		req       SearchRequest
		overrides *config.IndexerSearchConfig
		wantSkip  bool
	}{
		{name: "anime-only indexer skips non-anime", req: SearchRequest{}, overrides: scope("anime"), wantSkip: true},
		{name: "anime-only indexer runs anime", req: SearchRequest{ContentIsAnime: true}, overrides: scope("anime")},
		{name: "non-anime indexer skips anime", req: SearchRequest{ContentIsAnime: true}, overrides: scope("non_anime"), wantSkip: true},
		{name: "non-anime indexer runs non-anime", req: SearchRequest{}, overrides: scope("non_anime")},
		{name: "unset scope runs everything", req: SearchRequest{ContentIsAnime: true}, overrides: &config.IndexerSearchConfig{}},
		{name: "unknown scope runs everything", req: SearchRequest{}, overrides: scope("garbage")},
		{name: "nil overrides run everything", req: SearchRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := skipIndexerReason(tc.req, tc.overrides)
			if gotSkip := reason != ""; gotSkip != tc.wantSkip {
				t.Fatalf("skipIndexerReason() = %q, want skip=%v", reason, tc.wantSkip)
			}
		})
	}
}

func TestAggregatorFailoverStartsInParallelButKeepsPriority(t *testing.T) {
	started := make(chan string, 2)
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})

	first := &testIndexer{
		name: "first",
		searchFn: func(req SearchRequest) (*SearchResponse, error) {
			started <- "first"
			<-firstRelease
			return &SearchResponse{Channel: Channel{Items: []Item{}}}, nil
		},
	}
	second := &testIndexer{
		name: "second",
		searchFn: func(req SearchRequest) (*SearchResponse, error) {
			started <- "second"
			<-secondRelease
			return &SearchResponse{Channel: Channel{Items: []Item{{Title: "from-second", Size: 1}}}}, nil
		},
	}

	agg := NewAggregator(first, second)
	done := make(chan *SearchResponse, 1)
	errCh := make(chan error, 1)

	go func() {
		resp, err := agg.Search(context.Background(), SearchRequest{IndexerMode: "failover"})
		if err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case err := <-errCh:
			t.Fatalf("Search() returned error: %v", err)
		case <-time.After(500 * time.Millisecond):
			close(firstRelease)
			close(secondRelease)
			t.Fatalf("expected both failover indexers to start in parallel; only saw %d", i)
		}
	}

	close(secondRelease)

	select {
	case <-done:
		t.Fatal("failover should not return the second indexer before the first finishes")
	case err := <-errCh:
		t.Fatalf("Search() returned error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(firstRelease)

	select {
	case err := <-errCh:
		t.Fatalf("Search() returned error: %v", err)
	case resp := <-done:
		if resp == nil || len(resp.Channel.Items) != 1 || resp.Channel.Items[0].Title != "from-second" {
			t.Fatalf("expected failover to return second indexer items after first completed empty, got %#v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failover search result")
	}
}

// usageIndexer reports a fixed Usage, so the aggregator's folding can be
// checked without a client behind it.
type usageIndexer struct {
	testIndexer
	usage Usage
}

func (u *usageIndexer) GetUsage() Usage { return u.usage }

func TestAggregatorWeightsGrabAverageByGrabCount(t *testing.T) {
	agg := NewAggregator(
		&usageIndexer{usage: Usage{SearchesCount: 1, AvgResponseMS: 100, GrabsCount: 1, AvgGrabMS: 1000}},
		&usageIndexer{usage: Usage{SearchesCount: 9, AvgResponseMS: 200, GrabsCount: 3, AvgGrabMS: 200}},
	)

	u := agg.GetUsage()
	if u.GrabsCount != 4 {
		t.Fatalf("GrabsCount = %d, want 4", u.GrabsCount)
	}
	// (1*1000 + 3*200) / 4 — a busy indexer must not be outvoted by a quiet
	// one that grabbed once.
	if u.AvgGrabMS != 400 {
		t.Fatalf("AvgGrabMS = %v, want 400", u.AvgGrabMS)
	}
	if u.AvgResponseMS != 190 {
		t.Fatalf("AvgResponseMS = %v, want 190", u.AvgResponseMS)
	}
}
