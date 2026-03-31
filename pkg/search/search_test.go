package search

import (
	"context"
	"sync"
	"testing"

	"streamnzb/pkg/indexer"
)

type recordingIndexer struct {
	mu   sync.Mutex
	name string
	reqs []indexer.SearchRequest
}

func (r *recordingIndexer) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return &indexer.SearchResponse{}, nil
}

func (r *recordingIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (r *recordingIndexer) Ping() error                                         { return nil }
func (r *recordingIndexer) Name() string                                        { return r.name }
func (r *recordingIndexer) GetUsage() indexer.Usage                             { return indexer.Usage{} }

func TestRunIndexerSearchesPerIndexerTextRequestCarriesSeasonEpisodeWhenEnabled(t *testing.T) {
	idx := &recordingIndexer{name: "TestIndexer"}
	req := indexer.SearchRequest{
		Cat:                    "5000",
		Limit:                  100,
		Season:                 "1",
		Episode:                "5",
		UseSeasonEpisodeParams: true,
		PerIndexerQuery: map[string][]string{
			"TestIndexer": {"The Walking Dead"},
		},
	}

	if _, err := RunIndexerSearches(idx, nil, req, "series", nil, "", "", nil); err != nil {
		t.Fatalf("RunIndexerSearches() error = %v", err)
	}

	if len(idx.reqs) != 2 {
		t.Fatalf("expected 2 Search calls, got %d", len(idx.reqs))
	}

	var idReq *indexer.SearchRequest
	var textReq *indexer.SearchRequest
	for i := range idx.reqs {
		reqCopy := idx.reqs[i]
		if reqCopy.PerIndexerQuery != nil {
			textReq = &reqCopy
		} else {
			idReq = &reqCopy
		}
	}

	if idReq == nil {
		t.Fatal("expected an ID search request")
	}
	if textReq == nil {
		t.Fatal("expected a text search request")
	}
	if idReq.Season != "1" || idReq.Episode != "5" {
		t.Fatalf("expected ID request to keep season/episode, got season=%q episode=%q", idReq.Season, idReq.Episode)
	}
	if textReq.Season != "1" || textReq.Episode != "5" {
		t.Fatalf("expected text request to keep season/episode when enabled, got season=%q episode=%q", textReq.Season, textReq.Episode)
	}
	if got := textReq.PerIndexerQuery["TestIndexer"]; len(got) != 1 || got[0] != "The Walking Dead" {
		t.Fatalf("expected text request queries to be preserved, got %#v", got)
	}
}
