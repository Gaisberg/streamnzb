package kitsu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The trending feed is a fixed top 20 upstream; asking past it must answer
// empty without a request, not the same twenty again.
func TestTrendingListingHasOnePage(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/trending/anime" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.Write([]byte(`{"data":[{"id":"1","type":"anime","attributes":{"canonicalTitle":"One"}}]}`))
	}))
	defer ts.Close()
	client := NewClient(ts.Client())
	client.BaseURL = ts.URL

	first, err := client.GetAnimeListing(context.Background(), "trending", 0)
	if err != nil || len(first) != 1 {
		t.Fatalf("first page: %v, %d rows", err, len(first))
	}
	second, err := client.GetAnimeListing(context.Background(), "trending", 20)
	if err != nil || len(second) != 0 {
		t.Fatalf("second page must be empty: %v, %d rows", err, len(second))
	}
	if hits.Load() != 1 {
		t.Fatalf("a skipped trending page must not hit Kitsu: %d requests", hits.Load())
	}
}
