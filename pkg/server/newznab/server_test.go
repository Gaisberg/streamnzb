package newznab

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	nzbclient "streamnzb/pkg/indexer/newznab"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	testToken       = "newznab-api-key"
	testUpstreamKey = "upstream-key"
	testNZB         = `<?xml version="1.0"?><nzb><file subject="test"/></nzb>`
)

// upstream is a stand-in Newznab indexer that records the query it was asked.
type upstream struct {
	server    *httptest.Server
	lastQuery url.Values
	lastPath  string
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	up := &upstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.lastQuery = r.URL.Query()
		up.lastPath = r.URL.Path
		if r.URL.Query().Get("apikey") != testUpstreamKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getnzb") {
			w.Header().Set("Content-Type", "application/x-nzb")
			fmt.Fprint(w, testNZB)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>
<newznab:response offset="0" total="1"/>
<item>
	<title>Some.Show.S01E02.1080p.WEB-DL</title>
	<guid isPermaLink="false">abc123</guid>
	<link>%s/getnzb?apikey=%s&amp;id=abc123</link>
	<comments>%s/details/abc123</comments>
	<pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate>
	<category>TV &gt; HD</category>
	<enclosure url="%s/getnzb?apikey=%s&amp;id=abc123" length="1073741824" type="application/x-nzb"/>
	<newznab:attr name="size" value="1073741824"/>
	<newznab:attr name="grabs" value="42"/>
</item>
</channel>
</rss>`, up.server.URL, testUpstreamKey, up.server.URL, up.server.URL, testUpstreamKey)
	}))
	t.Cleanup(up.server.Close)
	return up
}

func testEndpoint(t *testing.T, up *upstream, caps map[string]*indexer.Caps) *Server {
	t.Helper()
	cfg := &config.Config{
		AdminUsername:  "admin",
		AdminToken:     "admin-token",
		AddonBaseURL:   "http://streamnzb.test",
		NewznabEnabled: true,
		NewznabAPIKey:  testToken,
		Indexers: []config.IndexerConfig{{
			Name:   "Mock",
			URL:    up.server.URL,
			APIKey: testUpstreamKey,
		}},
	}
	client := nzbclient.NewClient(cfg.Indexers[0], nil)
	agg := indexer.NewAggregator(client)

	return New(Options{
		Enabled:    func() bool { return cfg.NewznabEnabled },
		Indexer:    func() indexer.Indexer { return agg },
		Caps:       func() map[string]*indexer.Caps { return caps },
		Config:     func() *config.Config { return cfg },
		APIKey:     func() string { return cfg.NewznabAPIKey },
		GrabSecret: func() string { return "grab-secret" },
		Version:    "test",
	})
}

func do(t *testing.T, srv *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestCapsReportsMergedIndexerCapabilities(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, map[string]*indexer.Caps{
		"Mock": {
			Categories: []indexer.CapsCategory{{ID: "5000", Name: "TV", Subcats: []indexer.CapsCategory{{ID: "5040", Name: "HD"}}}},
			Searching: indexer.CapsSearching{
				Search:                  true,
				TVSearch:                true,
				TVSearchSupportedParams: map[string]bool{"q": true, "season": true, "ep": true, "tvdbid": true},
			},
			Limits:        indexer.CapsLimits{Max: 200, Default: 100},
			RetentionDays: 3000,
		},
	})

	rec := do(t, srv, APIPath+"?t=caps&apikey="+testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var doc capsDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("caps is not valid XML: %v\n%s", err, rec.Body.String())
	}
	if doc.Limits.Max != 200 || doc.Limits.Default != 100 {
		t.Errorf("limits = %+v, want max 200 default 100", doc.Limits)
	}
	if doc.Retention == nil || doc.Retention.Days != 3000 {
		t.Errorf("retention = %+v, want 3000 days", doc.Retention)
	}
	if doc.Searching.TVSearch.Available != "yes" {
		t.Error("tv-search should be available")
	}
	if !strings.Contains(doc.Searching.TVSearch.SupportedParams, "tvdbid") {
		t.Errorf("tv-search params = %q, want tvdbid", doc.Searching.TVSearch.SupportedParams)
	}
	if !strings.Contains(doc.Searching.TVSearch.SupportedParams, "season") {
		t.Errorf("tv-search params = %q, want season", doc.Searching.TVSearch.SupportedParams)
	}
	if len(doc.Categories.Categories) != 1 || doc.Categories.Categories[0].ID != "5000" {
		t.Fatalf("categories = %+v, want just the indexer's 5000", doc.Categories.Categories)
	}
	if len(doc.Categories.Categories[0].Subcats) != 1 || doc.Categories.Categories[0].Subcats[0].ID != "5040" {
		t.Errorf("subcats = %+v, want 5040", doc.Categories.Categories[0].Subcats)
	}
}

func TestCapsWithoutIndexerCapsAdvertisesStandardTree(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=caps&apikey="+testToken)
	var doc capsDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("caps is not valid XML: %v", err)
	}
	if len(doc.Categories.Categories) != len(indexer.StandardCategories()) {
		t.Fatalf("categories = %d, want the full standard tree", len(doc.Categories.Categories))
	}
	if doc.Searching.Search.Available != "yes" || doc.Searching.MovieSearch.Available != "yes" {
		t.Error("search functions should be claimed when no caps were published")
	}
	if doc.Retention != nil {
		t.Errorf("retention = %+v, want it omitted when unknown", doc.Retention)
	}
}

func TestRequestsWithoutAValidAPIKeyAreRejected(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	for _, target := range []string{
		APIPath + "?t=caps",
		APIPath + "?t=caps&apikey=wrong",
		APIPath + "?t=search&q=x&apikey=wrong",
	} {
		rec := do(t, srv, target)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", target, rec.Code)
		}
		var doc errorDocument
		if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatalf("%s: error is not valid XML: %v", target, err)
		}
		if doc.Code != errIncorrectCredentials {
			t.Errorf("%s: error code = %d, want %d", target, doc.Code, errIncorrectCredentials)
		}
	}
}

// A switched-off endpoint is not here at all, even for a caller holding the
// right key.
func TestDisabledEndpointIsNotServed(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)
	srv.currentConfig().NewznabEnabled = false

	for _, target := range []string{
		APIPath + "?t=caps&apikey=" + testToken,
		APIPath + "?t=search&q=x&apikey=" + testToken,
		Mount,
	} {
		if rec := do(t, srv, target); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
		}
	}
}

// No key configured means no way in — never a way in for everyone.
func TestBlankAPIKeyRefusesEveryRequest(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)
	srv.currentConfig().NewznabAPIKey = ""

	for _, target := range []string{
		APIPath + "?t=caps",
		APIPath + "?t=caps&apikey=",
		APIPath + "?t=search&q=x&apikey=" + testToken,
	} {
		if rec := do(t, srv, target); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", target, rec.Code)
		}
	}
}

func TestTVSearchForwardsTheQueryVerbatim(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=tvsearch&apikey="+testToken+"&q=Some+Show&season=1&ep=2&cat=5030%2C5040&limit=50")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if got := up.lastQuery.Get("t"); got != "tvsearch" {
		t.Errorf("upstream t = %q, want tvsearch — a text tvsearch must stay a tvsearch", got)
	}
	if got := up.lastQuery.Get("season"); got != "1" {
		t.Errorf("upstream season = %q, want 1", got)
	}
	if got := up.lastQuery.Get("ep"); got != "2" {
		t.Errorf("upstream ep = %q, want 2", got)
	}
	if got := up.lastQuery.Get("q"); got != "Some Show" {
		t.Errorf("upstream q = %q, want the caller's query", got)
	}
	if got := up.lastQuery.Get("cat"); got != "5030,5040" {
		t.Errorf("upstream cat = %q, want the caller's categories", got)
	}
	if got := up.lastQuery.Get("limit"); got != "50" {
		t.Errorf("upstream limit = %q, want 50", got)
	}
	if got := up.lastQuery.Get("apikey"); got != testUpstreamKey {
		t.Errorf("upstream apikey = %q, want the indexer's own key", got)
	}
}

func TestSearchRewritesDownloadLinks(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=search&apikey="+testToken+"&q=show")
	body := rec.Body.String()
	if strings.Contains(body, testUpstreamKey) {
		t.Fatalf("response leaks the indexer API key:\n%s", body)
	}
	if strings.Contains(body, up.server.URL+"/getnzb") {
		t.Fatalf("response leaks the upstream download URL:\n%s", body)
	}

	parsed := parseFeed(t, rec.Body.Bytes())
	if len(parsed.Channel.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(parsed.Channel.Items))
	}
	item := parsed.Channel.Items[0]
	if item.Title != "Some.Show.S01E02.1080p.WEB-DL" {
		t.Errorf("title = %q", item.Title)
	}
	if !strings.HasPrefix(item.Link, "http://streamnzb.test"+APIPath) {
		t.Errorf("link = %q, want it pointed back at the endpoint", item.Link)
	}
	if item.Enclosure.URL != item.Link {
		t.Errorf("enclosure url = %q, want it to match the link", item.Enclosure.URL)
	}
	if item.Enclosure.Length != 1073741824 {
		t.Errorf("enclosure length = %d, want the release size", item.Enclosure.Length)
	}
	if item.Comments != up.server.URL+"/details/abc123" {
		t.Errorf("comments = %q, want the indexer's details page", item.Comments)
	}
	if item.GetAttribute("grabs") != "42" {
		t.Errorf("attrs = %+v, want the source attributes carried through", item.Attributes)
	}
	if parsed.Channel.Response.Total != 1 {
		t.Errorf("total = %d, want 1", parsed.Channel.Response.Total)
	}
	if !strings.Contains(body, "newznab:attr") || !strings.Contains(body, `xmlns:newznab=`) {
		t.Errorf("feed is missing the newznab namespace:\n%s", body)
	}
}

func TestGrabServesTheNZBThroughTheSourceIndexer(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=search&apikey="+testToken+"&q=show")
	parsed := parseFeed(t, rec.Body.Bytes())
	link, err := url.Parse(parsed.Channel.Items[0].Link)
	if err != nil {
		t.Fatalf("download link is not a URL: %v", err)
	}

	grab := do(t, srv, APIPath+"?"+link.RawQuery)
	if grab.Code != http.StatusOK {
		t.Fatalf("grab status = %d, want 200: %s", grab.Code, grab.Body.String())
	}
	if grab.Body.String() != testNZB {
		t.Errorf("grab body = %q, want the upstream NZB", grab.Body.String())
	}
	if got := grab.Header().Get("Content-Type"); got != "application/x-nzb" {
		t.Errorf("content type = %q", got)
	}
	if got := grab.Header().Get("Content-Disposition"); !strings.Contains(got, ".nzb") {
		t.Errorf("content disposition = %q, want an .nzb attachment", got)
	}
	if up.lastPath != "/getnzb" {
		t.Errorf("upstream path = %q, want the grab to reach the indexer", up.lastPath)
	}
}

func TestGrabRejectsUnknownIDs(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=get&apikey="+testToken+"&id=not-a-real-reference")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var doc errorDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("error is not valid XML: %v", err)
	}
	if doc.Code != errNoSuchItem {
		t.Errorf("error code = %d, want %d", doc.Code, errNoSuchItem)
	}
}

func TestUnknownFunctionReportsNoSuchFunction(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=details&apikey="+testToken)
	var doc errorDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("error is not valid XML: %v", err)
	}
	if doc.Code != errNoSuchFunction {
		t.Errorf("error code = %d, want %d", doc.Code, errNoSuchFunction)
	}

	missing := do(t, srv, APIPath+"?apikey="+testToken)
	if err := xml.Unmarshal(missing.Body.Bytes(), &doc); err != nil {
		t.Fatalf("error is not valid XML: %v", err)
	}
	if doc.Code != errMissingParameter {
		t.Errorf("error code = %d, want %d", doc.Code, errMissingParameter)
	}
}

func TestJSONOutput(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	rec := do(t, srv, APIPath+"?t=search&apikey="+testToken+"&q=show&o=json")
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want JSON", got)
	}
	var parsed jsonFeed
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	if len(parsed.Channel.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(parsed.Channel.Items))
	}
	if parsed.Channel.Items[0].Enclosure.Attributes["length"] != "1073741824" {
		t.Errorf("enclosure = %+v, want the release size", parsed.Channel.Items[0].Enclosure)
	}
}

func TestOnlyTheAPIPathIsServed(t *testing.T) {
	up := newUpstream(t)
	srv := testEndpoint(t, up, nil)

	for path, want := range map[string]int{
		Mount:                    http.StatusBadRequest, // reached, but t is missing
		Mount + "api":            http.StatusBadRequest,
		Mount + "api/":           http.StatusBadRequest,
		Mount + "somethingelse":  http.StatusNotFound,
		Mount + "api/extra/path": http.StatusNotFound,
	} {
		rec := do(t, srv, path+"?apikey="+testToken)
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestGrabReferencesAreSealed(t *testing.T) {
	ref := grabRef{Indexer: "Mock", URL: "http://indexer.test/getnzb?apikey=secret-key&id=1", Title: "Some Release"}
	sealed, err := sealGrabRef("secret", ref)
	if err != nil {
		t.Fatalf("sealGrabRef: %v", err)
	}
	if strings.Contains(sealed, "secret-key") || strings.Contains(sealed, "indexer.test") {
		t.Fatalf("sealed reference is readable: %q", sealed)
	}

	opened, err := openGrabRef("secret", sealed)
	if err != nil {
		t.Fatalf("openGrabRef: %v", err)
	}
	if opened != ref {
		t.Errorf("opened = %+v, want %+v", opened, ref)
	}
	if _, err := openGrabRef("other-secret", sealed); err == nil {
		t.Error("a reference sealed under another secret should not open")
	}
	if _, err := openGrabRef("secret", sealed[:len(sealed)-4]+"AAAA"); err == nil {
		t.Error("a tampered reference should not open")
	}
}

func TestNZBFilename(t *testing.T) {
	cases := map[string]string{
		"":                       "release.nzb",
		"Some.Release-GROUP":     "Some.Release-GROUP.nzb",
		"Already.nzb":            "Already.nzb",
		"bad/name:with*chars":    "bad_name_with_chars.nzb",
		strings.Repeat("a", 300): strings.Repeat("a", 180) + ".nzb",
	}
	for input, want := range cases {
		if got := nzbFilename(input); got != want {
			t.Errorf("nzbFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

// parseFeed reads a response back with the app's own Newznab parser: whatever
// that accepts, a real client can read.
func parseFeed(t *testing.T, body []byte) *indexer.SearchResponse {
	t.Helper()
	var parsed indexer.SearchResponse
	if err := xml.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("feed is not valid Newznab XML: %v\n%s", err, body)
	}
	indexer.NormalizeSearchResponse(&parsed)
	return &parsed
}
