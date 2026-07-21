package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
)

func newStreamsTestServer(t *testing.T) *Server {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &config.Config{
		AdminUsername: "admin",
		AdminToken:    "admin-token",
		Streams: map[string]*config.StreamEntry{
			"stream1": {
				Username:            "stream1",
				Token:               "token-1",
				IndexerMode:         "combine",
				UseAvailNZB:         boolPtr(true),
				CombineResults:      boolPtr(true),
				EnableFailover:      boolPtr(true),
				AutoAddProviders:    boolPtr(false),
				AutoAddIndexers:     boolPtr(false),
				ProviderSelections:  []string{"ProviderA"},
				IndexerSelections:   []string{"IndexerA"},
				IndexerOverrides:    map[string]config.IndexerSearchConfig{},
				MovieSearchQueries:  []string{"MovieQueryA"},
				SeriesSearchQueries: []string{"SeriesQueryA"},
			},
		},
		LoadedPath: cfgPath,
	}

	streamManager, err := auth.NewStreamManagerFromConfig(cfg, func() error { return nil })
	if err != nil {
		t.Fatalf("NewStreamManagerFromConfig failed: %v", err)
	}

	return &Server{
		config:        cfg,
		streamManager: streamManager,
	}
}

func adminStreamRequest(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx := auth.ContextWithStream(req.Context(), &auth.Stream{Username: "admin", Token: "admin-token"})
	return req.WithContext(ctx)
}

// TestHandleStreamConfigsFilterProfileNameRoundTrip verifies that filter_profile_name
// sent in a PUT /api/streams/configs payload is persisted and then surfaced in both
// the stream list (GET /api/streams) and single-stream (GET /api/streams/{username})
// responses so the frontend can restore the selected profile on reload.
func TestHandleStreamConfigsFilterProfileNameRoundTrip(t *testing.T) {
	srv := newStreamsTestServer(t)

	// 1. PUT a stream config that includes filter_profile_name.
	payload := map[string]map[string]interface{}{
		"stream1": {
			"filter_sorting_mode":   "none",
			"indexer_mode":          "combine",
			"use_availnzb":          true,
			"combine_results":       true,
			"enable_failover":       true,
			"results_mode":          "combined_stream",
			"auto_add_providers":    false,
			"auto_add_indexers":     false,
			"provider_selections":   []string{"ProviderA"},
			"indexer_selections":    []string{"IndexerA"},
			"indexer_overrides":     map[string]config.IndexerSearchConfig{},
			"movie_search_queries":  []string{"MovieQueryA"},
			"series_search_queries": []string{"SeriesQueryA"},
			"filter_profile_name":   "4K HDR Profile",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	req := adminStreamRequest(http.MethodPut, "/api/streams/configs", body)
	rr := httptest.NewRecorder()
	srv.handlePutStreamConfigs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify the value reached the in-memory stream manager.
	stream, err := srv.streamManager.GetStream("stream1", "admin")
	if err != nil {
		t.Fatalf("GetStream failed: %v", err)
	}
	if stream.FilterProfileName != "4K HDR Profile" {
		t.Fatalf("expected persisted FilterProfileName %q, got %q", "4K HDR Profile", stream.FilterProfileName)
	}

	// 2. GET /api/streams should include filter_profile_name so the frontend can reload it.
	listReq := adminStreamRequest(http.MethodGet, "/api/streams", nil)
	listRR := httptest.NewRecorder()
	srv.handleStreamsList(listRR, listReq)

	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", listRR.Code, http.StatusOK)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
		t.Fatalf("list json.Unmarshal failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 stream in list, got %d", len(list))
	}
	if got := list[0]["filter_profile_name"]; got != "4K HDR Profile" {
		t.Fatalf("expected filter_profile_name in list response, got %v", got)
	}

	// 3. GET /api/streams/{username} should also include filter_profile_name.
	singleReq := adminStreamRequest(http.MethodGet, "/api/streams/stream1", nil)
	singleRR := httptest.NewRecorder()
	srv.handleStreamByUsername(singleRR, singleReq)

	if singleRR.Code != http.StatusOK {
		t.Fatalf("single status = %d, want %d", singleRR.Code, http.StatusOK)
	}
	var single map[string]interface{}
	if err := json.Unmarshal(singleRR.Body.Bytes(), &single); err != nil {
		t.Fatalf("single json.Unmarshal failed: %v", err)
	}
	if got := single["filter_profile_name"]; got != "4K HDR Profile" {
		t.Fatalf("expected filter_profile_name in single stream response, got %v", got)
	}
}