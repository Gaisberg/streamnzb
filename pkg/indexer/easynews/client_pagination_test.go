package easynews

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer"
)

// pageServer is a local Easynews stand-in reached through the client's real
// transport, so pooling, cancellation and concurrency behave as in production.
// Each page carries rows titled by page and position, so result order is
// checkable.
type pageServer struct {
	numPages int
	rows     int
	// handle may take over a page; returning false falls through to the
	// normal page body.
	handle func(page int, w http.ResponseWriter, r *http.Request) bool

	mu          sync.Mutex
	requests    map[int]int
	inFlight    int
	maxInFlight int
}

func (s *pageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("pno"))
	s.mu.Lock()
	s.requests[page]++
	s.inFlight++
	s.maxInFlight = max(s.maxInFlight, s.inFlight)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()

	if s.handle != nil && s.handle(page, w, r) {
		return
	}
	fmt.Fprint(w, pageJSON(page, s.rows, s.numPages))
}

func pageJSON(page, rows, numPages int) string {
	data := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		data = append(data, fmt.Sprintf(`{"hash":"p%d-%d","fn":"Show.P%d.R%03d.1080p","extension":".mkv","rawSize":900000000,"runtime":3600}`, page, i, page, i))
	}
	return fmt.Sprintf(`{"results":%d,"numPages":%d,"data":[%s]}`, rows*numPages, numPages, strings.Join(data, ","))
}

func (s *pageServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.requests {
		n += c
	}
	return n
}

func (s *pageServer) pageRequests(page int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[page]
}

func (s *pageServer) peak() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

type rewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = r.target.Scheme
	req.URL.Host = r.target.Host
	return r.inner.RoundTrip(req)
}

func newPageServer(t *testing.T, numPages int) (*pageServer, *Client) {
	t.Helper()
	return newPageServerWithRPS(t, numPages, 0)
}

func newPageServerWithRPS(t *testing.T, numPages, rps int) (*pageServer, *Client) {
	t.Helper()
	s := &pageServer{numPages: numPages, rows: easynewsPageRows, requests: map[int]int{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, rps, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	target, _ := url.Parse(srv.URL)
	client.client.Transport = rewriteTransport{target: target, inner: client.client.Transport}
	return s, client
}

func textRequest(query string, limit int) indexer.SearchRequest {
	return indexer.SearchRequest{Query: query, SearchMode: "text", Limit: limit}
}

// pageOf reads the page number back out of a result title.
func pageOf(t *testing.T, item indexer.Item) int {
	t.Helper()
	var page, row int
	if _, err := fmt.Sscanf(item.Title, "Show.P%d.R%d", &page, &row); err != nil {
		t.Fatalf("unexpected title %q", item.Title)
	}
	return page
}

func assertPageOrder(t *testing.T, items []indexer.Item) {
	t.Helper()
	for i := 1; i < len(items); i++ {
		prev, cur := pageOf(t, items[i-1]), pageOf(t, items[i])
		// Rows are zero-padded, so within a page titles sort by position.
		if cur < prev || (cur == prev && items[i].Title <= items[i-1].Title) {
			t.Fatalf("result %d (%s) out of order after %s", i, items[i].Title, items[i-1].Title)
		}
	}
}

func TestSearchFetchesTheAvailablePages(t *testing.T) {
	for _, tc := range []struct {
		numPages, wantRequests int
	}{
		{numPages: 0, wantRequests: 1},
		{numPages: 1, wantRequests: 1},
		{numPages: 2, wantRequests: 2},
		{numPages: 5, wantRequests: 5},
		{numPages: 40, wantRequests: maxSearchPages},
	} {
		t.Run(fmt.Sprintf("numPages=%d", tc.numPages), func(t *testing.T) {
			srv, client := newPageServer(t, tc.numPages)
			resp, err := client.Search(context.Background(), textRequest("show", 0))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if got := srv.total(); got != tc.wantRequests {
				t.Fatalf("requests = %d, want %d", got, tc.wantRequests)
			}
			if got, want := len(resp.Channel.Items), tc.wantRequests*easynewsPageRows; got != want {
				t.Fatalf("results = %d, want %d", got, want)
			}
			if resp.Incomplete {
				t.Fatal("a fully fetched search was marked incomplete")
			}
			assertPageOrder(t, resp.Channel.Items)
			if hits := client.GetUsage().APIHitsUsed; hits != 1 {
				t.Fatalf("API hits = %d, want one per search however many pages (%d requests)", hits, tc.wantRequests)
			}
		})
	}
}

func TestSearchStopsAtAnEarlyEmptyPage(t *testing.T) {
	srv, client := newPageServer(t, 5)
	// Page three ends the result set even though a page after it, already in
	// flight, still answers with rows.
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		if page == 3 {
			fmt.Fprint(w, `{"results":500,"numPages":5,"data":[]}`)
			return true
		}
		return false
	}
	resp, err := client.Search(context.Background(), textRequest("show", 0))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := len(resp.Channel.Items); got != 2*easynewsPageRows {
		t.Fatalf("results = %d, want pages one and two only", got)
	}
	assertPageOrder(t, resp.Channel.Items)
	if resp.Incomplete {
		t.Fatal("an empty page ends the result set; it is not a failure")
	}

	// A limit still short after the empty page does not start another round.
	srv.mu.Lock()
	srv.requests = map[int]int{}
	srv.mu.Unlock()
	resp, err = client.Search(context.Background(), textRequest("limited", 400))
	if err != nil {
		t.Fatalf("limited Search: %v", err)
	}
	if got := len(resp.Channel.Items); got != 2*easynewsPageRows {
		t.Fatalf("limited results = %d, want pages one and two only", got)
	}
	if srv.pageRequests(5) != 0 {
		t.Fatal("a page past the end of the result set was requested")
	}
}

// A page after the end of the result set failing loses nothing, so the
// answer is complete and stays cacheable.
func TestSearchFailureAfterAnEarlyEmptyPageIsComplete(t *testing.T) {
	srv, client := newPageServer(t, 5)
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		switch page {
		case 3:
			fmt.Fprint(w, `{"results":500,"numPages":5,"data":[]}`)
		case 4, 5:
			w.WriteHeader(http.StatusNotFound)
		default:
			return false
		}
		return true
	}
	resp, err := client.Search(context.Background(), textRequest("show", 0))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := len(resp.Channel.Items); got != 2*easynewsPageRows {
		t.Fatalf("results = %d, want pages one and two", got)
	}
	if resp.Incomplete {
		t.Fatal("a failure past the end of the result set marked the answer incomplete")
	}
}

func TestSearchHonoursResultLimit(t *testing.T) {
	for _, tc := range []struct {
		limit, wantRequests, wantResults int
	}{
		{limit: 0, wantRequests: 5, wantResults: 500},
		{limit: 1, wantRequests: 1, wantResults: 1},
		{limit: 99, wantRequests: 1, wantResults: 99},
		{limit: 100, wantRequests: 1, wantResults: 100},
		{limit: 101, wantRequests: 2, wantResults: 101},
		{limit: 250, wantRequests: 3, wantResults: 250},
		{limit: 500, wantRequests: 5, wantResults: 500},
		{limit: 1000, wantRequests: 5, wantResults: 500},
	} {
		t.Run(fmt.Sprintf("limit=%d", tc.limit), func(t *testing.T) {
			srv, client := newPageServer(t, 9)
			resp, err := client.Search(context.Background(), textRequest("show", tc.limit))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if got := srv.total(); got != tc.wantRequests {
				t.Fatalf("requests = %d, want %d", got, tc.wantRequests)
			}
			if got := len(resp.Channel.Items); got != tc.wantResults {
				t.Fatalf("results = %d, want %d", got, tc.wantResults)
			}
			assertPageOrder(t, resp.Channel.Items)
		})
	}
}

// Rows the client filters out do not count toward the limit. Pages one and two
// come back half samples, so a limit of 150 sizes one more page after page
// one, falls short, and tops up from page three.
func TestSearchLimitTopsUpFilteredRows(t *testing.T) {
	srv, client := newPageServer(t, 5)
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		if page > 2 {
			return false
		}
		rows := make([]string, 0, easynewsPageRows)
		for i := 0; i < easynewsPageRows; i++ {
			name := fmt.Sprintf("Show.P%d.R%03d.1080p", page, i)
			if i%2 == 1 {
				name += ".sample"
			}
			rows = append(rows, fmt.Sprintf(`{"hash":"p%d-%d","fn":"%s","extension":".mkv","rawSize":9,"runtime":3600}`, page, i, name))
		}
		fmt.Fprintf(w, `{"results":500,"numPages":5,"data":[%s]}`, strings.Join(rows, ","))
		return true
	}
	resp, err := client.Search(context.Background(), textRequest("show", 150))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := len(resp.Channel.Items); got != 150 {
		t.Fatalf("results = %d, want the limit filled from page three", got)
	}
	if got := srv.total(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	assertPageOrder(t, resp.Channel.Items)
}

func TestSearchResultLimitSources(t *testing.T) {
	override := func(n int) *config.IndexerSearchConfig { return &config.IndexerSearchConfig{SearchResultLimit: n} }
	for _, tc := range []struct {
		name string
		req  indexer.SearchRequest
		want int
	}{
		{"none", indexer.SearchRequest{}, 0},
		{"request", indexer.SearchRequest{Limit: 50}, 50},
		{"negative", indexer.SearchRequest{Limit: -1}, 0},
		{"indexer setting wins", indexer.SearchRequest{Limit: 50, OptionalOverrides: override(200)}, 200},
		{"zero indexer setting defers", indexer.SearchRequest{Limit: 50, OptionalOverrides: override(0)}, 50},
		{"proxied query is not trimmed", indexer.SearchRequest{Limit: 100, OptionalOverrides: override(10), Passthrough: &indexer.PassthroughQuery{Function: "search"}}, 0},
	} {
		if got := searchResultLimit(tc.req); got != tc.want {
			t.Errorf("%s: limit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Page two answers only after page three has finished, yet its results still
// come first.
func TestSearchKeepsPageOrderWhenPagesFinishOutOfOrder(t *testing.T) {
	srv, client := newPageServer(t, 3)
	pageThreeDone := make(chan struct{})
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		switch page {
		case 2:
			select {
			case <-pageThreeDone:
			case <-r.Context().Done():
				return true
			}
		case 3:
			fmt.Fprint(w, pageJSON(3, easynewsPageRows, 3))
			w.(http.Flusher).Flush()
			close(pageThreeDone)
			return true
		}
		return false
	}
	resp, err := client.Search(context.Background(), textRequest("show", 0))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	items := resp.Channel.Items
	if len(items) != 3*easynewsPageRows {
		t.Fatalf("results = %d, want 300", len(items))
	}
	for i, item := range items {
		if want := i/easynewsPageRows + 1; pageOf(t, item) != want {
			t.Fatalf("result %d is from page %d, want %d", i, pageOf(t, item), want)
		}
	}
}

// Three simultaneous searches on one client share its two request slots.
func TestSimultaneousSearchesShareTheRequestCap(t *testing.T) {
	srv, client := newPageServer(t, 5)
	arrived := make(chan int, 64)
	release := make(chan struct{})
	var releaseOnce sync.Once
	// Runs before the server closes, so a failing assertion cannot leave
	// handlers blocked and hang the shutdown.
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		arrived <- page
		<-release
		return false
	}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := client.Search(context.Background(), textRequest(fmt.Sprintf("show %d", i), 0)); err != nil {
				t.Errorf("Search %d: %v", i, err)
			}
		}(i)
	}
	<-arrived
	<-arrived
	select {
	case page := <-arrived:
		t.Fatalf("a third request (page %d) went out while two were in flight", page)
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	wg.Wait()

	if got := srv.peak(); got != maxConcurrentSearchRequests {
		t.Fatalf("peak in-flight requests = %d, want %d", got, maxConcurrentSearchRequests)
	}
	if got := srv.total(); got != 3*5 {
		t.Fatalf("requests = %d, want every page of every search", got)
	}
}

func TestSearchCancelledMidwayStopsSendingAndFreesSlots(t *testing.T) {
	srv, client := newPageServer(t, 5)
	inFlight := make(chan struct{}, 8)
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		if page == 1 {
			return false
		}
		inFlight <- struct{}{}
		<-r.Context().Done()
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *indexer.SearchResponse, 1)
	go func() {
		resp, _ := client.Search(ctx, textRequest("show", 0))
		done <- resp
	}()
	<-inFlight
	<-inFlight
	cancel()
	resp := <-done

	if resp == nil || !resp.Incomplete || len(resp.Channel.Items) != easynewsPageRows {
		t.Fatalf("got %+v, want page one marked incomplete", resp)
	}
	if got := srv.total(); got != 3 {
		t.Fatalf("requests = %d, want page one plus the two in flight", got)
	}
	if hits := client.GetUsage().APIHitsUsed; hits != 1 {
		t.Fatalf("API hits = %d, want the one search counted once", hits)
	}

	// Both slots came back: a fresh search runs to completion.
	srv.handle = nil
	if _, err := client.Search(context.Background(), textRequest("again", 0)); err != nil {
		t.Fatalf("follow-up Search: %v", err)
	}
}

func TestSearchTimeoutOnLaterPageIsIncompleteAndNotCached(t *testing.T) {
	srv, client := newPageServer(t, 5)
	client.searchTimeout = 300 * time.Millisecond
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		if page != 3 {
			return false
		}
		<-r.Context().Done()
		return true
	}
	cached := indexer.NewCachedIndexer(client, indexer.NewQueryCache(), 10*time.Minute)

	resp, err := cached.Search(context.Background(), textRequest("show", 0))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !resp.Incomplete {
		t.Fatal("a search that lost a page was not marked incomplete")
	}
	if got := len(resp.Channel.Items); got != 4*easynewsPageRows {
		t.Fatalf("results = %d, want the four pages that answered", got)
	}
	assertPageOrder(t, resp.Channel.Items)

	before := srv.total()
	if _, err := cached.Search(context.Background(), textRequest("show", 0)); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if srv.total() == before {
		t.Fatal("the incomplete result was served from cache")
	}
}

func TestSearchRefusalOnLaterPageCancelsTheRest(t *testing.T) {
	for _, tc := range []struct {
		status   int
		sentinel error
	}{
		{http.StatusTooManyRequests, indexer.ErrRateLimited},
		{http.StatusUnauthorized, indexer.ErrAuthFailed},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			srv, client := newPageServer(t, 5)
			srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
				switch page {
				case 2:
					w.WriteHeader(tc.status)
					return true
				case 3:
					// In flight when page two is refused; must be abandoned.
					<-r.Context().Done()
					return true
				}
				return false
			}
			resp, err := client.Search(context.Background(), textRequest("show", 0))
			if err != nil {
				t.Fatalf("Search: %v, want page one kept", err)
			}
			if !resp.Incomplete || len(resp.Channel.Items) != easynewsPageRows {
				t.Fatalf("got incomplete=%v results=%d, want page one marked incomplete", resp.Incomplete, len(resp.Channel.Items))
			}
			if srv.pageRequests(4) != 0 || srv.pageRequests(5) != 0 {
				t.Fatal("pages after the refusal were still sent")
			}
			if hits := client.GetUsage().APIHitsUsed; hits != 1 {
				t.Fatalf("API hits = %d, want the one search counted once", hits)
			}
		})
	}
}

func TestSearchThrottledLaterPageHoldsOffTheNextSearch(t *testing.T) {
	srv, client := newPageServer(t, 2)
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		if page == 2 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	}
	if _, err := client.Search(context.Background(), textRequest("show", 0)); err != nil {
		t.Fatalf("Search: %v", err)
	}
	before := srv.total()
	_, err := client.Search(context.Background(), textRequest("other", 0))
	if !errors.Is(err, indexer.ErrRateLimited) {
		t.Fatalf("err = %v, want the cooldown to refuse the next search", err)
	}
	if srv.total() != before {
		t.Fatal("a search went out during the cooldown")
	}
}

func TestSearchMalformedPages(t *testing.T) {
	t.Run("first page fails the search", func(t *testing.T) {
		srv, client := newPageServer(t, 3)
		srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
			fmt.Fprint(w, `{"data":[`)
			return true
		}
		if _, err := client.Search(context.Background(), textRequest("show", 0)); err == nil {
			t.Fatal("want an error for a malformed first page")
		}
		if got := srv.total(); got != 1 {
			t.Fatalf("requests = %d, want 1", got)
		}
	})
	t.Run("later page keeps the others", func(t *testing.T) {
		srv, client := newPageServer(t, 3)
		srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
			if page == 2 {
				fmt.Fprint(w, `not json`)
				return true
			}
			return false
		}
		resp, err := client.Search(context.Background(), textRequest("show", 0))
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !resp.Incomplete || len(resp.Channel.Items) != 2*easynewsPageRows {
			t.Fatalf("got incomplete=%v results=%d, want pages one and three, incomplete", resp.Incomplete, len(resp.Channel.Items))
		}
		assertPageOrder(t, resp.Channel.Items)
	})
}

func TestSearchFirstPageAuthFailureSendsNothingElse(t *testing.T) {
	srv, client := newPageServer(t, 5)
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	_, err := client.Search(context.Background(), textRequest("show", 0))
	if !errors.Is(err, indexer.ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	if got := srv.total(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// The configured rate limit spaces every page request, not just the first
// one of a search.
func TestRateLimitAppliesToEveryPage(t *testing.T) {
	const rps = 20
	srv, client := newPageServerWithRPS(t, 5, rps)
	start := time.Now()
	if _, err := client.Search(context.Background(), textRequest("show", 0)); err != nil {
		t.Fatalf("Search: %v", err)
	}
	elapsed := time.Since(start)
	if srv.total() != 5 {
		t.Fatalf("requests = %d, want 5", srv.total())
	}
	if minimum := 4 * time.Second / rps; elapsed < minimum-10*time.Millisecond {
		t.Fatalf("five pages took %v, want at least %v at %d requests/s", elapsed, minimum, rps)
	}
}

// Even when page one holds nothing the client would accept, the probe stops
// there: it is a credential check, not a search.
func TestPingSendsOnePage(t *testing.T) {
	srv, client := newPageServer(t, 5)
	srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
		fmt.Fprint(w, `{"results":500,"numPages":5,"data":[{"hash":"h","fn":"Dune.sample","extension":".mkv","rawSize":9,"runtime":3600}]}`)
		return true
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := srv.total(); got != 1 {
		t.Fatalf("requests = %d, want the probe to cost one request", got)
	}
	if hits := client.GetUsage().APIHitsUsed; hits != 1 {
		t.Fatalf("API hits = %d, want 1", hits)
	}
}

// The daily API budget counts searches, not result pages: a budget of two
// admits two five-page searches and turns the third away before any request
// goes out, the same number of searches it admitted before pagination.
func TestAPIBudgetCountsSearchesNotPages(t *testing.T) {
	srv, client := newPageServer(t, 5)
	client.core = indexer.NewClientCore("Easynews", 2, 0, 0, nil)
	for i := 0; i < 2; i++ {
		if _, err := client.Search(context.Background(), textRequest(fmt.Sprintf("show %d", i), 0)); err != nil {
			t.Fatalf("Search %d: %v", i, err)
		}
	}
	if got := srv.total(); got != 10 {
		t.Fatalf("requests = %d, want every page of both searches", got)
	}
	if hits := client.GetUsage().APIHitsUsed; hits != 2 {
		t.Fatalf("API hits = %d, want 2", hits)
	}
	// The spent budget lets one probe search through and then holds; the
	// probe itself is the documented valve, so check the hold after it.
	_, _ = client.Search(context.Background(), textRequest("probe", 0))
	before := srv.total()
	if _, err := client.Search(context.Background(), textRequest("held", 0)); !errors.Is(err, indexer.ErrRateLimited) {
		t.Fatalf("err = %v, want the spent budget to refuse the search", err)
	}
	if srv.total() != before {
		t.Fatal("a search went out with the budget spent")
	}
}

// A search that Easynews answered, even with a refusal, spent its hit; one
// that never reached it did not.
func TestAPIHitNeedsAnAnswer(t *testing.T) {
	t.Run("answered refusal counts", func(t *testing.T) {
		srv, client := newPageServer(t, 5)
		srv.handle = func(page int, w http.ResponseWriter, r *http.Request) bool {
			w.WriteHeader(http.StatusUnauthorized)
			return true
		}
		_, _ = client.Search(context.Background(), textRequest("show", 0))
		if hits := client.GetUsage().APIHitsUsed; hits != 1 {
			t.Fatalf("API hits = %d, want 1", hits)
		}
	})
	t.Run("unreachable search does not", func(t *testing.T) {
		_, client := newPageServer(t, 5)
		dead := httptest.NewServer(http.NotFoundHandler())
		target, _ := url.Parse(dead.URL)
		dead.Close()
		// Past the slot and the rate limiter, refused at the socket.
		client.client.Transport = rewriteTransport{target: target, inner: client.client.Transport.(rewriteTransport).inner}
		if _, err := client.Search(context.Background(), textRequest("show", 0)); err == nil {
			t.Fatal("want the unreachable search to fail")
		}
		if hits := client.GetUsage().APIHitsUsed; hits != 0 {
			t.Fatalf("API hits = %d, want 0", hits)
		}
	})
	t.Run("unsent search does not", func(t *testing.T) {
		_, client := newPageServer(t, 5)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := client.Search(ctx, textRequest("show", 0)); err == nil {
			t.Fatal("want the cancelled search to fail")
		}
		if hits := client.GetUsage().APIHitsUsed; hits != 0 {
			t.Fatalf("API hits = %d, want 0", hits)
		}
	})
}
