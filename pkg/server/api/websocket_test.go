package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
)

func TestValidateConfigRejectsUnresolvedProwlarrIndexerPlaceholder(t *testing.T) {
	enabled := true
	s := &Server{}

	errs := s.validateConfig(&config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{{
			Enabled: &enabled,
			Name:    "Prowlarr",
			URL:     "http://[::1",
			APIPath: "{indexer_id}/api",
			Type:    "aggregator",
		}},
	})

	if got := errs["indexers.0.api_path"]; got == "" {
		t.Fatalf("expected api_path validation error, got %#v", errs)
	}
	if got := errs["indexers.0.url"]; got != "" {
		t.Fatalf("expected placeholder validation to stop ping before url validation, got url error %q", got)
	}
}

func TestValidateConfigWithPlanIgnoresUnchangedInvalidProviderDuringEdit(t *testing.T) {
	enabled := true
	disabled := false
	s := &Server{}

	current := &config.Config{
		Providers: []config.Provider{
			{Enabled: &enabled},
			{Enabled: &disabled, Host: "provider.example"},
		},
	}
	next := &config.Config{
		Providers: []config.Provider{
			{Enabled: &enabled},
			{Enabled: &disabled, Host: "provider.example", Name: "Updated"},
		},
	}
	body, err := json.Marshal(map[string]interface{}{"providers": next.Providers})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	errs := s.validateConfigWithPlan(next, validationPlanFromPatch(body, current, next))
	if got := errs["providers.0.host"]; got != "" {
		t.Fatalf("expected unchanged invalid provider to be ignored during unrelated edit, got %q", got)
	}
}

func TestValidateConfigWithPlanAllowsProviderDeleteDespiteOtherInvalidProvider(t *testing.T) {
	enabled := true
	disabled := false
	s := &Server{}

	current := &config.Config{
		Providers: []config.Provider{
			{Enabled: &enabled},
			{Enabled: &disabled, Host: "provider.example"},
		},
	}
	next := &config.Config{
		Providers: []config.Provider{
			{Enabled: &enabled},
		},
	}
	body, err := json.Marshal(map[string]interface{}{"providers": next.Providers})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	errs := s.validateConfigWithPlan(next, validationPlanFromPatch(body, current, next))
	if got := errs["providers.0.host"]; got != "" {
		t.Fatalf("expected provider delete to skip unrelated provider validation, got %q", got)
	}
}

func TestValidateConfigWithPlanIgnoresUnchangedInvalidIndexerDuringEdit(t *testing.T) {
	enabled := true
	disabled := false
	s := &Server{}

	current := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
			{Enabled: &disabled, Name: "Valid", URL: "https://indexer.example"},
		},
	}
	next := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
			{Enabled: &disabled, Name: "Updated", URL: "https://indexer.example"},
		},
	}
	body, err := json.Marshal(map[string]interface{}{"indexers": next.Indexers})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	errs := s.validateConfigWithPlan(next, validationPlanFromPatch(body, current, next))
	if got := errs["indexers.0.url"]; got != "" {
		t.Fatalf("expected unchanged invalid indexer to be ignored during unrelated edit, got %q", got)
	}
}

func TestValidateConfigWithPlanAllowsIndexerDeleteDespiteOtherInvalidIndexer(t *testing.T) {
	enabled := true
	disabled := false
	s := &Server{}

	current := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
			{Enabled: &disabled, Name: "Valid", URL: "https://indexer.example"},
		},
	}
	next := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
		},
	}
	body, err := json.Marshal(map[string]interface{}{"indexers": next.Indexers})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	errs := s.validateConfigWithPlan(next, validationPlanFromPatch(body, current, next))
	if got := errs["indexers.0.url"]; got != "" {
		t.Fatalf("expected indexer delete to skip unrelated indexer validation, got %q", got)
	}
}

func TestValidateConfigRejectsOutOfRangePlaybackStartupTimeout(t *testing.T) {
	s := &Server{}

	errs := s.validateConfig(&config.Config{
		KeepLogFiles:                  9,
		NZBHistoryRetentionDays:       90,
		PlaybackStartupTimeoutSeconds: 0,
	})

	if got := errs["playback_startup_timeout_seconds"]; got == "" {
		t.Fatalf("expected playback startup timeout validation error, got %#v", errs)
	}
}

func TestValidateConfigRejectsInvalidGlobalIndexerProxyURL(t *testing.T) {
	s := &Server{}

	errs := s.validateConfig(&config.Config{
		KeepLogFiles:                  9,
		NZBHistoryRetentionDays:       90,
		PlaybackStartupTimeoutSeconds: 5,
		IndexerProxyURL:               "socks5://127.0.0.1:1080",
	})

	if got := errs["indexer_proxy_url"]; got == "" {
		t.Fatalf("expected global indexer proxy validation error, got %#v", errs)
	}
}

func TestValidateConfigRejectsUnreachableGlobalIndexerProxyURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := &Server{}
	enabled := true
	errs := s.validateConfig(&config.Config{
		KeepLogFiles:                  9,
		NZBHistoryRetentionDays:       90,
		PlaybackStartupTimeoutSeconds: 5,
		IndexerProxyURL:               "http://" + addr,
		Indexers: []config.IndexerConfig{{
			Enabled: &enabled,
			Name:    "BrokenIndexer",
			URL:     "http://example.invalid",
			APIPath: "/api",
			APIKey:  "abc",
			Type:    "newznab",
		}},
	})

	if got := errs["indexer_proxy_url"]; got == "" {
		t.Fatalf("expected global indexer proxy reachability error, got %#v", errs)
	}
}

func TestValidateConfigWithPlanAllowsLegacyOriginalIDTitleLanguage(t *testing.T) {
	s := &Server{}

	cfg := &config.Config{
		MovieSearchQueries: []config.SearchQueryConfig{{
			Name:                "MovieQuery01",
			SearchMode:          "id",
			SearchTitleLanguage: "original",
		}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateMovieSearchQueries: true})
	if got := errs["movie_search_queries.0.search_title_languages"]; got != "" {
		t.Fatalf("expected legacy original title language to be accepted, got %q", got)
	}
}

func TestValidateConfigWithPlanGlobalProxyPassesWhenAnyIndexerReachable(t *testing.T) {
	enabled := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "ok.indexer" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	s := &Server{}
	cfg := &config.Config{
		IndexerProxyURL: proxy.URL,
		Indexers: []config.IndexerConfig{
			{
				Enabled: &enabled,
				Name:    "Failing",
				URL:     "http://fail.indexer",
				APIPath: "/api",
				APIKey:  "abc",
				Type:    "newznab",
			},
			{
				Enabled: &enabled,
				Name:    "Healthy",
				URL:     "http://ok.indexer",
				APIPath: "/api",
				APIKey:  "abc",
				Type:    "newznab",
			},
		},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateIndexerProxyURL: true})
	if got := errs["indexer_proxy_url"]; got != "" {
		t.Fatalf("expected global proxy verification to pass when one indexer is reachable, got %q", got)
	}
}

func TestValidateConfigWithPlanGlobalProxyFailsWhenNoIndexerReachable(t *testing.T) {
	enabled := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	s := &Server{}
	cfg := &config.Config{
		IndexerProxyURL: proxy.URL,
		Indexers: []config.IndexerConfig{{
			Enabled: &enabled,
			Name:    "Failing",
			URL:     "http://fail.indexer",
			APIPath: "/api",
			APIKey:  "abc",
			Type:    "newznab",
		}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateIndexerProxyURL: true})
	got := errs["indexer_proxy_url"]
	if got == "" {
		t.Fatalf("expected global proxy verification error, got %#v", errs)
	}
	if !strings.Contains(got, "could not reach any enabled indexer") {
		t.Fatalf("expected aggregate global proxy error, got %q", got)
	}
}

func TestValidateConfigWithPlanIndexerProxyChecksIndexerConnection(t *testing.T) {
	enabled := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	s := &Server{}
	cfg := &config.Config{
		Indexers: []config.IndexerConfig{{
			Enabled:  &enabled,
			Name:     "Failing",
			URL:      "http://blocked.indexer",
			APIPath:  "/api",
			APIKey:   "abc",
			Type:     "newznab",
			ProxyURL: proxy.URL,
		}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateIndexers: true})
	if got := errs["indexers.0.url"]; got == "" {
		t.Fatalf("expected indexer connectivity error, got %#v", errs)
	}
	if got := errs["indexers.0.proxy_url"]; got != "" {
		t.Fatalf("expected no standalone proxy reachability error, got %q", got)
	}
}

func TestValidateConfigTMDBAndTVDBAPIKeys(t *testing.T) {
	// 1. Setup mock TMDB server
	tmdbMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/configuration" {
			authHeader := r.Header.Get("Authorization")
			if strings.Contains(authHeader, "correct_tmdb_key") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"images":{}}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer tmdbMock.Close()

	// 2. Setup mock TVDB server
	tvdbMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				if body["apikey"] == "correct_tvdb_key" {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"status":"success","data":{"token":"test_token"}}`))
					return
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"failure","data":{}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer tvdbMock.Close()

	// Set env overrides
	t.Setenv("STREAMNZB_TMDB_BASE_URL", tmdbMock.URL)
	t.Setenv("STREAMNZB_TVDB_BASE_URL", tvdbMock.URL)

	s := &Server{}

	// Test 1: Valid keys pass validation
	cfgValid := &config.Config{
		TMDBAPIKey: "correct_tmdb_key",
		TVDBAPIKey: "correct_tvdb_key",
	}
	errs := s.validateConfigWithPlan(cfgValid, configValidationPlan{
		validateTMDBAPIKey: true,
		validateTVDBAPIKey: true,
	})
	if len(errs) > 0 {
		t.Fatalf("expected valid TMDB/TVDB keys to pass, got errors: %#v", errs)
	}

	// Test 2: Invalid keys fail validation
	cfgInvalid := &config.Config{
		TMDBAPIKey: "wrong_tmdb_key",
		TVDBAPIKey: "wrong_tvdb_key",
	}
	errs = s.validateConfigWithPlan(cfgInvalid, configValidationPlan{
		validateTMDBAPIKey: true,
		validateTVDBAPIKey: true,
	})
	if errs["tmdb_api_key"] == "" {
		t.Error("expected tmdb_api_key error, got none")
	}
	if errs["tvdb_api_key"] == "" {
		t.Error("expected tvdb_api_key error, got none")
	}

	// Test 3: Empty keys are skipped/pass validation
	cfgEmpty := &config.Config{
		TMDBAPIKey: "",
		TVDBAPIKey: "",
	}
	errs = s.validateConfigWithPlan(cfgEmpty, configValidationPlan{
		validateTMDBAPIKey: true,
		validateTVDBAPIKey: true,
	})
	if len(errs) > 0 {
		t.Fatalf("expected empty keys to pass, got errors: %#v", errs)
	}
}

// Resetting a trait back to its default drops its key from the profile's
// attribute map. The save path unmarshals the patch over the current config, and
// encoding/json keeps map entries the patch does not mention, so without
// clearPatchedFilterProfiles the removed override would survive the round trip.
func TestClearPatchedFilterProfilesDropsResetAttribute(t *testing.T) {
	current := &config.Config{FilterProfiles: []config.FilterProfileConfig{config.DefaultFilterProfile()}}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal current: %v", err)
	}

	// The UI sends the profile back with "cam" reset to its default.
	profile := config.DefaultFilterProfile()
	attrs := map[string]any{}
	rawAttrs, err := json.Marshal(profile.Ranking.Attributes)
	if err != nil {
		t.Fatalf("marshal attributes: %v", err)
	}
	if err := json.Unmarshal(rawAttrs, &attrs); err != nil {
		t.Fatalf("unmarshal attributes: %v", err)
	}
	delete(attrs, "cam")

	body, err := json.Marshal(map[string]any{
		"filter_profiles": []map[string]any{{
			"name":    profile.Name,
			"ranking": map[string]any{"name": profile.Name, "attributes": attrs},
		}},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	var newCfg config.Config
	if err := json.Unmarshal(currentJSON, &newCfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	clearPatchedFilterProfiles(body, &newCfg)
	if err := json.Unmarshal(body, &newCfg); err != nil {
		t.Fatalf("apply patch: %v", err)
	}

	if len(newCfg.FilterProfiles) != 1 {
		t.Fatalf("expected 1 filter profile, got %d", len(newCfg.FilterProfiles))
	}
	if _, still := newCfg.FilterProfiles[0].Ranking.Attributes["cam"]; still {
		t.Fatal("reset attribute survived the patch")
	}
}

// A save that does not mention filter profiles must leave them untouched.
func TestClearPatchedFilterProfilesLeavesUnrelatedPatchAlone(t *testing.T) {
	cfg := &config.Config{FilterProfiles: []config.FilterProfileConfig{config.DefaultFilterProfile()}}
	clearPatchedFilterProfiles([]byte(`{"keep_log_files":9}`), cfg)
	if len(cfg.FilterProfiles) != 1 {
		t.Fatalf("expected filter profiles preserved, got %d", len(cfg.FilterProfiles))
	}
}
