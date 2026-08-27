package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
)

// A profile whose pattern cannot compile would be skipped by the ranking
// reload — its streams would silently stop filtering — so the save must be
// rejected instead.
func TestValidateConfigRejectsUncompilableFilterProfile(t *testing.T) {
	s := &Server{}

	spec := rank.Default()
	spec.Require = []string{"("}
	cfg := &config.Config{
		FilterProfiles: []config.FilterProfileConfig{{Name: "Broken", Ranking: &spec}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateFilterProfiles: true})
	if got := errs["filter_profiles.0.ranking"]; got == "" {
		t.Fatalf("expected a compile error for the broken pattern, got %#v", errs)
	}
}

// NZB limits must be non-negative, and min size must not exceed max size —
// including when the contradiction only appears after a kind entry merges
// over the default entry.
func TestValidateConfigRejectsBadProfileLimits(t *testing.T) {
	s := &Server{}

	tests := []struct {
		name    string
		limits  map[string]*config.LimitsConfig
		errPath string
	}{
		{
			"negative size",
			map[string]*config.LimitsConfig{"default": {MaxSizeGB: -1}},
			"filter_profiles.0.limits.default",
		},
		{
			"negative age",
			map[string]*config.LimitsConfig{"movie": {MaxAgeDays: -1}},
			"filter_profiles.0.limits.movie",
		},
		{
			"min above max in one entry",
			map[string]*config.LimitsConfig{"default": {MinSizeGB: 10, MaxSizeGB: 5}},
			"filter_profiles.0.limits.default",
		},
		{
			"min above max across the merge",
			map[string]*config.LimitsConfig{"default": {MinSizeGB: 10}, "series": {MaxSizeGB: 5}},
			"filter_profiles.0.limits.series",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				FilterProfiles: []config.FilterProfileConfig{{Name: "Limits", Limits: tt.limits}},
			}
			errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateFilterProfiles: true})
			if got := errs[tt.errPath]; got == "" {
				t.Fatalf("expected an error at %s, got %#v", tt.errPath, errs)
			}
		})
	}

	// A valid spread passes.
	cfg := &config.Config{
		FilterProfiles: []config.FilterProfileConfig{{Name: "Limits", Limits: map[string]*config.LimitsConfig{
			"default": {MinSizeGB: 1, MaxSizeGB: 30, MaxAgeDays: 1200, MinGrabs: 3},
			"series":  {MaxSizeGB: 5},
		}}},
	}
	if errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateFilterProfiles: true}); len(errs) > 0 {
		t.Fatalf("expected no errors for valid limits, got %#v", errs)
	}
}

// A define library is data for profiles to reference: define-only, one
// namespace across every library, self-contained, and compiled as strictly as
// a profile's own rules.
func TestValidateConfigDefineLibraries(t *testing.T) {
	s := &Server{}
	define := func(name, when string) config.RuleConfig {
		return config.RuleConfig{Name: name, When: when, Action: config.RuleActionDefine}
	}
	plan := configValidationPlan{validateDefineLibraries: true, validateFilterProfiles: true}

	t.Run("a profile referencing a library define is valid", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{{Name: "Tiers", Rules: []config.RuleConfig{define("T1", `group == "GRP"`)}}},
			FilterProfiles:  []config.FilterProfileConfig{{Name: "Mine", Rules: []config.RuleConfig{{Name: "Bonus", When: `matched("T1")`, Points: 500}}}},
		}
		if errs := s.validateConfigWithPlan(cfg, plan); len(errs) > 0 {
			t.Fatalf("expected no errors, got %#v", errs)
		}
	})

	t.Run("only define rules are allowed", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{{Name: "Tiers", Rules: []config.RuleConfig{
				{Name: "Sneaky", When: "true", Action: config.RuleActionScore, Points: 500},
			}}},
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if got := errs["define_libraries.0.rules"]; !strings.Contains(got, "only contain define rules") {
			t.Fatalf("expected the define-only refusal, got %#v", errs)
		}
	})

	t.Run("define names are one namespace across libraries", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{
				{Name: "A", Rules: []config.RuleConfig{define("T1", "true")}},
				{Name: "B", Rules: []config.RuleConfig{define("t1", "true")}},
			},
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if got := errs["define_libraries.1.rules"]; !strings.Contains(got, `already in library "A"`) {
			t.Fatalf("expected the cross-library collision, got %#v", errs)
		}
	})

	t.Run("a broken define fails the library, not the profiles", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{{Name: "Tiers", Rules: []config.RuleConfig{define("Broken", "group ==")}}},
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if errs["define_libraries.0.rules"] == "" {
			t.Fatalf("expected a compile error on the library, got %#v", errs)
		}
	})

	t.Run("a library must be self-contained", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{{Name: "Tiers", Rules: []config.RuleConfig{define("Leans", `matched("Profile rule")`)}}},
			FilterProfiles:  []config.FilterProfileConfig{{Name: "Mine", Rules: []config.RuleConfig{{Name: "Profile rule", When: "true", Points: 1}}}},
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if got := errs["define_libraries.0.rules"]; !strings.Contains(got, `no rule is named "Profile rule"`) {
			t.Fatalf("expected the self-containment refusal, got %#v", errs)
		}
	})

	t.Run("removing a referenced define breaks the profile save", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{{Name: "Tiers"}},
			FilterProfiles:  []config.FilterProfileConfig{{Name: "Mine", Rules: []config.RuleConfig{{Name: "Bonus", When: `matched("T1")`, Points: 500}}}},
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if got := errs["filter_profiles.0.ranking"]; !strings.Contains(got, `no rule is named "T1"`) {
			t.Fatalf("expected the profile to stop compiling, got %#v", errs)
		}
	})

	t.Run("the source snapshot must be a define code", func(t *testing.T) {
		cfg := &config.Config{
			DefineLibraries: []config.DefineLibraryConfig{{
				Name:   "Tiers",
				Source: &config.ProfileSourceConfig{URL: "https://example.com/lib.txt", Code: "SNZBP1:abc"},
			}},
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if got := errs["define_libraries.0.source"]; !strings.Contains(got, "SNZBD1:") {
			t.Fatalf("expected the prefix refusal, got %#v", errs)
		}
	})
}

// A define_libraries patch revalidates the filter profiles too: a library
// edit can rename or remove a define a profile references.
func TestDefineLibraryPatchRevalidatesProfiles(t *testing.T) {
	body := []byte(`{"define_libraries": []}`)
	plan := validationPlanFromPatch(body, &config.Config{}, &config.Config{})
	if !plan.validateDefineLibraries || !plan.validateFilterProfiles {
		t.Fatalf("plan = %+v, want define libraries and filter profiles validated", plan)
	}
}

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

// Deleting a rule has to survive the save. The save path unmarshals the patch
// over the current config, and encoding/json keeps what the patch does not
// mention, so without clearPatchedFilterProfiles the deleted rule would come
// straight back.
func TestClearPatchedFilterProfilesDropsDeletedRule(t *testing.T) {
	profile := config.DefaultFilterProfile()
	profile.Rules = []config.RuleConfig{
		{Name: "Keep", When: "dolbyVision", Points: 100},
		{Name: "Delete me", When: "seasonPack", Points: 200},
	}
	current := &config.Config{FilterProfiles: []config.FilterProfileConfig{profile}}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("marshal current: %v", err)
	}

	// The UI sends the profile back with the second rule gone.
	body, err := json.Marshal(map[string]any{
		"filter_profiles": []map[string]any{{
			"name":   profile.Name,
			"preset": profile.Preset,
			"rules": []map[string]any{
				{"name": "Keep", "when": "dolbyVision", "points": 100},
			},
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
	rules := newCfg.FilterProfiles[0].Rules
	if len(rules) != 1 || rules[0].Name != "Keep" {
		t.Fatalf("rules = %+v, want only the kept one", rules)
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
