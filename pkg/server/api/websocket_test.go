package api

import (
	"encoding/json"
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
