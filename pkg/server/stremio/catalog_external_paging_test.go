package stremio

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/httpx"
	"streamnzb/pkg/core/logger"
)

// trustTestSource points the external fetcher at a local TLS stub. Sources
// must be HTTPS, so the stub is a TLS server and the client is the one that
// trusts its certificate.
func trustTestSource(t *testing.T, ts *httptest.Server) []*net.IPNet {
	t.Helper()
	previous := externalHTTPClient
	externalHTTPClient = func([]*net.IPNet) *http.Client { return ts.Client() }
	t.Cleanup(func() { externalHTTPClient = previous })
	allowed, err := httpx.ParseNetworks([]string{"127.0.0.0/8"}, "catalog_source_networks")
	if err != nil {
		t.Fatalf("ParseNetworks: %v", err)
	}
	return allowed
}

// wholeListAddon answers every catalog request with its entire list, the way
// many addons do, and records the skips it was asked for.
func wholeListAddon(t *testing.T, rows int) (*httptest.Server, *int64, *[]string) {
	t.Helper()
	var calls int64
	var skips []string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		skips = append(skips, r.URL.Path)
		metas := make([]map[string]any, 0, rows)
		for i := range rows {
			// releaseInfo as a number is what one real addon publishes, and
			// what used to fail the whole catalog on a field we never read.
			metas = append(metas, map[string]any{"id": fmt.Sprintf("tt%07d", i), "name": fmt.Sprintf("Title %d", i), "releaseInfo": 2024})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"metas": metas})
	}))
	t.Cleanup(ts.Close)
	return ts, &calls, &skips
}

// A pasted row that declares no paging still has every item past the
// twentieth: the addon handed the whole list over in one response. Before the
// fix the first page was truncated to 20 and every later skip answered empty,
// so a 60-title source rendered as a 20-title one.
func TestExternalManifestRowWithoutSkipPagesLocally(t *testing.T) {
	logger.Init("ERROR")
	ts, calls, _ := wholeListAddon(t, 60)
	trustTestSource(t, ts)
	def := CatalogDef{ID: "external.test", Type: "movie", Provider: "external", Kind: "manifest", SupportsSkip: false,
		ExternalManifestURL: ts.URL + "/manifest.json", ExternalRemoteType: "movie", ExternalRemoteID: "list"}
	srv := &Server{}

	for _, tc := range []struct {
		skip      int
		wantFirst string
		wantLen   int
	}{
		{0, "tt0000000", 20},
		{20, "tt0000020", 20},
		{40, "tt0000040", 20},
		{60, "", 0},
	} {
		metas, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: def.Type, ID: def.ID, Skip: tc.skip, Profile: &config.MetadataProfileConfig{}})
		if err != nil {
			t.Fatalf("skip %d: %v", tc.skip, err)
		}
		if len(metas) != tc.wantLen {
			t.Fatalf("skip %d returned %d rows, want %d", tc.skip, len(metas), tc.wantLen)
		}
		if tc.wantLen > 0 && metas[0].ID != tc.wantFirst {
			t.Fatalf("skip %d starts at %s, want %s", tc.skip, metas[0].ID, tc.wantFirst)
		}
	}
	if got := atomic.LoadInt64(calls); got == 0 {
		t.Fatal("no request reached the source")
	}
}

// A row that declares paging keeps asking the source, since only the source
// knows what comes after its own page.
func TestExternalManifestRowWithSkipAsksTheSource(t *testing.T) {
	logger.Init("ERROR")
	ts, _, paths := wholeListAddon(t, 20)
	trustTestSource(t, ts)
	def := CatalogDef{ID: "external.test", Type: "movie", Provider: "external", Kind: "manifest", SupportsSkip: true,
		ExternalManifestURL: ts.URL + "/manifest.json", ExternalRemoteType: "movie", ExternalRemoteID: "list"}
	srv := &Server{}

	if _, err := srv.buildCatalog(context.Background(), def, catalogRequest{Type: def.Type, ID: def.ID, Skip: 40, Profile: &config.MetadataProfileConfig{}}); err != nil {
		t.Fatalf("buildCatalog: %v", err)
	}
	if len(*paths) != 1 || !strings.Contains((*paths)[0], "skip=40") {
		t.Fatalf("source was asked for %v, want one request carrying skip=40", *paths)
	}
}

// An addon that publishes only the older extraSupported/extraRequired
// spelling declares the same contract. Reading just "extra" made every one of
// its rows look unpaged, which is what capped a saved row at 20 items.
func TestInspectExternalManifestReadsLegacyExtraFields(t *testing.T) {
	logger.Init("ERROR")
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "manifest.json") {
			fmt.Fprint(w, `{"name":"Legacy Addon","catalogs":[
				{"id":"paged","type":"movie","name":"Paged","extraSupported":["genre","skip"]},
				{"id":"searchonly","type":"movie","name":"Search Only","extraSupported":["search"],"extraRequired":["search"]}]}`)
			return
		}
		fmt.Fprint(w, `{"metas":[{"id":"tt0000001","name":"A Title"}]}`)
	}))
	t.Cleanup(ts.Close)

	inspection, err := InspectExternalManifest(context.Background(), ts.URL+"/manifest.json", trustTestSource(t, ts))
	if err != nil {
		t.Fatalf("InspectExternalManifest: %v", err)
	}
	if len(inspection.Catalogs) != 1 {
		t.Fatalf("inspection offered %d rows, want only the browseable one: %+v", len(inspection.Catalogs), inspection.Catalogs)
	}
	if row := inspection.Catalogs[0]; row.Name != "Paged" || !row.SupportsSkip {
		t.Fatalf("row = %+v, want the paged row with SupportsSkip true", row)
	}
}

// Testing a pasted source is a live question: the operator is asking what it
// serves now, and often asking because they just changed it. Answering the
// probe from a page cached before that change reports the old failure back to
// them as if the fix had not worked.
func TestInspectExternalManifestBypassesThePageCache(t *testing.T) {
	logger.Init("ERROR")
	var calls int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "manifest.json") {
			fmt.Fprint(w, `{"name":"Self-hosted","catalogs":[{"id":"top","type":"movie","name":"Top","extra":[{"name":"skip"}]}]}`)
			return
		}
		atomic.AddInt64(&calls, 1)
		// The source was broken when the row was last served and works now.
		fmt.Fprint(w, `{"metas":[{"id":"tt0000001","name":"Now Working"}]}`)
	}))
	t.Cleanup(ts.Close)
	allowed := trustTestSource(t, ts)

	def := CatalogDef{Type: "movie", Provider: "external", ExternalManifestURL: ts.URL + "/manifest.json", ExternalRemoteType: "movie", ExternalRemoteID: "top"}
	key, err := externalCatalogPageURL(def, 0)
	if err != nil {
		t.Fatalf("externalCatalogPageURL: %v", err)
	}
	resetExternalManifestPageCache(t)
	storeExternalManifestPage(key, []MetaPreview{{ID: "tt0000009", Type: "movie", Name: "Stale Row"}})

	inspection, err := InspectExternalManifest(context.Background(), ts.URL+"/manifest.json", allowed)
	if err != nil {
		t.Fatalf("InspectExternalManifest: %v", err)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("the source was asked %d times, want exactly one live request rather than a cached answer", atomic.LoadInt64(&calls))
	}
	if len(inspection.Catalogs) != 1 || inspection.Catalogs[0].RowCount != 1 {
		t.Fatalf("inspection = %+v, want the row the source serves now", inspection.Catalogs)
	}

	// Serving the same row still goes through the cache.
	resetExternalManifestPageCache(t)
	storeExternalManifestPage(key, []MetaPreview{{ID: "tt0000009", Type: "movie", Name: "Cached Row"}})
	metas, err := externalManifestCatalogPage(context.Background(), def, 0, allowed)
	if err != nil {
		t.Fatalf("externalManifestCatalogPage: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != "tt0000009" {
		t.Fatalf("serving path returned %+v, want the cached row", metas)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("the serving path made another request; the cache should have answered it")
	}
}
