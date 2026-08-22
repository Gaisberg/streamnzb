package newznab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
)

// passthroughUpstream answers caps advertising a tv-search that takes tvdbid
// and nothing else, and records every search it is asked.
func passthroughUpstream(t *testing.T) (*Client, *[]url.Values) {
	t.Helper()
	var searches []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <limits max="100" default="100"/>
  <searching>
    <search available="yes" supportedParams="q"/>
    <tv-search available="yes" supportedParams="q,season,ep,tvdbid"/>
    <movie-search available="yes" supportedParams="q,imdbid"/>
  </searching>
</caps>`)
			return
		}
		searches = append(searches, r.URL.Query())
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	t.Cleanup(server.Close)

	client := NewClient(config.IndexerConfig{Name: "Mock", URL: server.URL, APIKey: "k"}, nil)
	if _, err := client.GetCaps(); err != nil {
		t.Fatalf("GetCaps: %v", err)
	}
	return client, &searches
}

func passthroughRequest(function string, params url.Values) indexer.SearchRequest {
	return indexer.SearchRequest{
		SearchMode:  "id",
		Passthrough: &indexer.PassthroughQuery{Function: function, Params: params},
	}
}

// An indexer sent an id it never claimed does not fail — it ignores the
// parameter and answers with its latest listing, which would reach the caller
// as results it never asked for. So the query is not sent at all.
func TestPassthroughSkipsIDsTheIndexerDoesNotSupport(t *testing.T) {
	client, searches := passthroughUpstream(t)

	resp, err := client.Search(context.Background(), passthroughRequest("tvsearch", url.Values{"rid": {"12345"}}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Channel.Items) != 0 {
		t.Errorf("items = %d, want none", len(resp.Channel.Items))
	}
	if len(*searches) != 0 {
		t.Fatalf("indexer was queried with %v, want the request skipped", *searches)
	}
}

func TestPassthroughForwardsSupportedIDs(t *testing.T) {
	client, searches := passthroughUpstream(t)

	if _, err := client.Search(context.Background(), passthroughRequest("tvsearch", url.Values{"tvdbid": {"121361"}})); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(*searches) != 1 {
		t.Fatalf("searches = %d, want the request forwarded", len(*searches))
	}
	if got := (*searches)[0].Get("tvdbid"); got != "121361" {
		t.Errorf("tvdbid = %q, want it forwarded", got)
	}
}

// A text query still means what it says even when the indexer ignores the id
// riding along with it, so it is never declined.
func TestPassthroughKeepsTextQueriesWithUnsupportedIDs(t *testing.T) {
	client, searches := passthroughUpstream(t)

	params := url.Values{"rid": {"12345"}, "q": {"Some Show"}}
	if _, err := client.Search(context.Background(), passthroughRequest("tvsearch", params)); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(*searches) != 1 {
		t.Fatalf("searches = %d, want the text query forwarded", len(*searches))
	}
	if got := (*searches)[0].Get("q"); got != "Some Show" {
		t.Errorf("q = %q, want the caller's query", got)
	}
}

// A listing with no ids at all is not an id search and must reach everyone.
func TestPassthroughKeepsListingQueries(t *testing.T) {
	client, searches := passthroughUpstream(t)

	if _, err := client.Search(context.Background(), passthroughRequest("search", url.Values{"cat": {"5040"}})); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(*searches) != 1 {
		t.Fatalf("searches = %d, want the listing forwarded", len(*searches))
	}
}

func TestPassthroughSkipsFunctionsTheIndexerDoesNotOffer(t *testing.T) {
	client, searches := passthroughUpstream(t)
	client.mu.Lock()
	client.caps.Searching.TVSearch = false
	client.mu.Unlock()

	if _, err := client.Search(context.Background(), passthroughRequest("tvsearch", url.Values{"tvdbid": {"121361"}})); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(*searches) != 0 {
		t.Fatalf("indexer was queried with %v, want the request skipped", *searches)
	}
}
