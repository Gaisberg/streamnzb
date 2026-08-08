package indexer

import (
	"context"
	"testing"
	"time"
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
