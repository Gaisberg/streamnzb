package easynews

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"streamnzb/pkg/core/env"
)

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestBuildEasynewsSearchURLBasic(t *testing.T) {
	got := buildEasynewsSearchURL("dune", "", "", "", false, searchOptions{})

	if !strings.HasPrefix(got, easynewsBaseURL+easynewsSearchPath+"?") {
		t.Fatalf("buildEasynewsSearchURL() = %q, want prefix %q", got, easynewsBaseURL+easynewsSearchPath+"?")
	}
	for _, param := range []string{"gps=dune", "pby=250", "vv=1", "fty%5B%5D=VIDEO", "st=basic"} {
		if !strings.Contains(got, param) {
			t.Fatalf("buildEasynewsSearchURL() = %q, missing %q", got, param)
		}
	}
}

func TestBuildEasynewsSearchURLHasNoTrailingSlash(t *testing.T) {
	// A trailing slash makes Easynews serve the web app's HTML instead of JSON.
	got := buildEasynewsSearchURL("dune", "", "", "", false, searchOptions{})
	if strings.Contains(got, "/3.0/api/search/?") {
		t.Fatalf("search URL must not carry a trailing slash: %q", got)
	}
}

func TestBuildEasynewsSearchURLAdvanced(t *testing.T) {
	opts := searchOptions{advanced: true, spamFilter: true, fileExtensions: defaultFileExtensions}

	got := buildEasynewsSearchURL("dune", "", "", "", false, opts)
	for _, param := range []string{"st=adv", "gx=1", "sS=3", "spamf=1", "fex="} {
		if !strings.Contains(got, param) {
			t.Fatalf("buildEasynewsSearchURL() = %q, missing %q", got, param)
		}
	}
	if strings.Contains(got, "st=basic") {
		t.Fatalf("advanced search must not send st=basic: %q", got)
	}
}

func TestBuildEasynewsSearchURLAdvancedOmitsOptionalFilters(t *testing.T) {
	got := buildEasynewsSearchURL("dune", "", "", "", false, searchOptions{advanced: true})

	if strings.Contains(got, "spamf=") {
		t.Fatalf("spam filter off must not send spamf: %q", got)
	}
	if strings.Contains(got, "fex=") {
		t.Fatalf("empty extension list must not send fex: %q", got)
	}
}

func TestDefaultFileExtensionsMatchesAcceptedContainers(t *testing.T) {
	// The fex whitelist and the client-side filter must not drift apart.
	for _, ext := range strings.Split(defaultFileExtensions, ",") {
		if !allowedVideoExts["."+ext] {
			t.Fatalf("fex lists %q, which the result filter would drop", ext)
		}
	}
	if got, want := len(strings.Split(defaultFileExtensions, ",")), len(allowedVideoExts); got != want {
		t.Fatalf("fex has %d extensions, allowedVideoExts has %d", got, want)
	}
}

func TestAdvancedSearchDefaultsOnAndCanBeDisabled(t *testing.T) {
	t.Setenv(env.StreamNZBEasynewsAdvancedSearchEnv, "")
	t.Setenv(env.EasynewsAdvancedSearchEnv, "")

	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if !client.advancedSearch || !client.spamFilter {
		t.Fatalf("advanced=%v spam=%v, want both on by default", client.advancedSearch, client.spamFilter)
	}

	t.Setenv(env.StreamNZBEasynewsAdvancedSearchEnv, "false")
	client, err = NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.advancedSearch || client.spamFilter {
		t.Fatalf("advanced=%v spam=%v, want both off", client.advancedSearch, client.spamFilter)
	}
}

func TestSearchSendsAdvancedParams(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var gotPath, gotQuery string
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.Path
		gotQuery = req.URL.RawQuery
		return jsonResponse(http.StatusOK, `{"data":[],"results":0}`), nil
	})

	if _, _, err := client.searchInternal(context.Background(), "test", "", "", "", false, false); err != nil {
		t.Fatalf("searchInternal: %v", err)
	}
	if gotPath != easynewsSearchPath {
		t.Fatalf("path = %q, want %q", gotPath, easynewsSearchPath)
	}
	if !strings.Contains(gotQuery, "spamf=1") || !strings.Contains(gotQuery, "fex=") {
		t.Fatalf("query = %q, want the server-side filters", gotQuery)
	}
}

func TestSearchWalksAllPages(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	row := `{"hash":"%s","fn":"Show.S01E01.1080p","extension":".mkv","rawSize":900000000,"runtime":3600}`
	var gotPages []string
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		page := req.URL.Query().Get("pno")
		gotPages = append(gotPages, page)
		body := fmt.Sprintf(`{"results":180,"numPages":2,"data":[`+row+`]}`, "hash"+page)
		return jsonResponse(http.StatusOK, body), nil
	})

	results, stats, err := client.searchInternal(context.Background(), "show", "1", "1", "", true, false)
	if err != nil {
		t.Fatalf("searchInternal: %v", err)
	}
	if want := []string{"1", "2"}; len(gotPages) != 2 || gotPages[0] != want[0] || gotPages[1] != want[1] {
		t.Fatalf("pages requested = %v, want %v", gotPages, want)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want both pages merged", len(results))
	}
	if stats.Pages != 2 || stats.NumPages != 2 || stats.Total != 180 {
		t.Fatalf("stats = %+v, want 2 pages fetched of 2, total 180", stats)
	}
}

func TestSearchStopsAtPageBound(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var calls int
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"results":9999,"numPages":500,"data":[{"hash":"h","fn":"Show.S01E01","extension":".mkv","rawSize":9,"runtime":3600}]}`
		return jsonResponse(http.StatusOK, body), nil
	})

	if _, _, err := client.searchInternal(context.Background(), "show", "", "", "", false, false); err != nil {
		t.Fatalf("searchInternal: %v", err)
	}
	if calls != maxSearchPages {
		t.Fatalf("requests = %d, want the %d-page bound", calls, maxSearchPages)
	}
}

func TestSearchKeepsEarlierPagesWhenALaterOneFails(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "http://addon", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("pno") != "1" {
			return jsonResponse(http.StatusInternalServerError, "boom"), nil
		}
		body := `{"results":180,"numPages":3,"data":[{"hash":"h1","fn":"Show.S01E01","extension":".mkv","rawSize":9,"runtime":3600}]}`
		return jsonResponse(http.StatusOK, body), nil
	})

	results, _, err := client.searchInternal(context.Background(), "show", "", "", "", false, false)
	if err != nil {
		t.Fatalf("searchInternal: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want page one kept despite the later failure", len(results))
	}
}

func TestSearchReportsRejectedCredentials(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, ""), nil
	})

	_, _, err = client.searchInternal(context.Background(), "test", "", "", "", false, false)
	if !errors.Is(err, errCredentialsRejected) {
		t.Fatalf("err = %v, want a rejected-credentials error", err)
	}
}

func TestSearchSendsQueryHeaderAndAuth(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "StreamNZB-Test/1.0", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var gotUA, gotAuth, gotAccept string
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotUA = req.Header.Get("User-Agent")
		gotAuth = req.Header.Get("Authorization")
		gotAccept = req.Header.Get("Accept")
		return jsonResponse(http.StatusOK, `{"data":[],"results":0}`), nil
	})

	if _, _, err := client.searchInternal(context.Background(), "test", "", "", "", false, false); err != nil {
		t.Fatalf("searchInternal: %v", err)
	}
	if gotUA != "StreamNZB-Test/1.0" {
		t.Fatalf("User-Agent = %q, want the configured query header", gotUA)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic auth", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", gotAccept)
	}
}

func TestRedirectKeepsAuthorization(t *testing.T) {
	client, err := NewClient("user", "pass", "Easynews", "", 0, 0, 0, 0, "", "", "", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Go drops Authorization on a cross-host redirect; CheckRedirect re-adds it.
	req, err := http.NewRequest("GET", "https://other.easynews.com/3.0/api/search", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.client.CheckRedirect(req, []*http.Request{{}}); err != nil {
		t.Fatalf("CheckRedirect: %v", err)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "user" || pass != "pass" {
		t.Fatalf("redirected request auth = (%q, %q, %v), want the client credentials", user, pass, ok)
	}
}
