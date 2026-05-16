package api

import (
	"reflect"
	"testing"

	"streamnzb/pkg/core/config"
)

func boolPtr(v bool) *bool { return &v }

func TestApplyStreamAutoSelectionsSyncsEnabledAndKeepsOrderPreference(t *testing.T) {
	nextCfg := &config.Config{
		Providers: []config.Provider{
			{Name: "ProviderA", Enabled: boolPtr(true)},
			{Name: "ProviderB", Enabled: boolPtr(false)},
			{Name: "ProviderC", Enabled: boolPtr(true)},
		},
		Indexers: []config.IndexerConfig{
			{Name: "IndexerA", Enabled: boolPtr(true)},
			{Name: "IndexerB", Enabled: boolPtr(false)},
			{Name: "IndexerC", Enabled: boolPtr(true)},
		},
		Streams: map[string]*config.StreamEntry{
			"default": {
				AutoAddProviders:   boolPtr(true),
				AutoAddIndexers:    boolPtr(true),
				ProviderSelections: []string{"ProviderC", "ProviderB", "ProviderA", "Missing"},
				IndexerSelections:  []string{"IndexerC", "IndexerB", "IndexerA", "Missing"},
				IndexerOverrides: map[string]config.IndexerSearchConfig{
					"IndexerC": {SearchResultLimit: 10},
					"IndexerB": {SearchResultLimit: 20},
				},
			},
			"custom": {
				AutoAddProviders:   boolPtr(false),
				AutoAddIndexers:    boolPtr(false),
				ProviderSelections: []string{"ProviderA", "ProviderB"},
				IndexerSelections:  []string{"IndexerA", "IndexerB"},
			},
		},
	}

	applyStreamAutoSelections(nextCfg)

	if got := nextCfg.Streams["default"].ProviderSelections; !reflect.DeepEqual(got, []string{"ProviderC", "ProviderA"}) {
		t.Fatalf("default provider selections = %#v", got)
	}
	if got := nextCfg.Streams["default"].IndexerSelections; !reflect.DeepEqual(got, []string{"IndexerC", "IndexerA"}) {
		t.Fatalf("default indexer selections = %#v", got)
	}
	if _, ok := nextCfg.Streams["default"].IndexerOverrides["IndexerB"]; ok {
		t.Fatalf("expected disabled indexer override to be removed")
	}
	if _, ok := nextCfg.Streams["default"].IndexerOverrides["IndexerC"]; !ok {
		t.Fatalf("expected enabled indexer override to remain")
	}
	if got := nextCfg.Streams["custom"].ProviderSelections; !reflect.DeepEqual(got, []string{"ProviderA", "ProviderB"}) {
		t.Fatalf("custom provider selections = %#v", got)
	}
	if got := nextCfg.Streams["custom"].IndexerSelections; !reflect.DeepEqual(got, []string{"IndexerA", "IndexerB"}) {
		t.Fatalf("custom indexer selections = %#v", got)
	}
}

func TestApplyStreamAutoSelectionsSkipsWhenFlagMissing(t *testing.T) {
	nextCfg := &config.Config{
		Providers: []config.Provider{
			{Name: "ProviderA", Enabled: boolPtr(true)},
			{Name: "ProviderB", Enabled: boolPtr(false)},
		},
		Streams: map[string]*config.StreamEntry{
			"stream": {
				ProviderSelections: []string{"ProviderA"},
			},
		},
	}

	applyStreamAutoSelections(nextCfg)

	if got := nextCfg.Streams["stream"].ProviderSelections; !reflect.DeepEqual(got, []string{"ProviderA"}) {
		t.Fatalf("provider selections = %#v", got)
	}
}
