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

func TestApplyStreamNameRenamesRenamesSelectionsAndOverrides(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"stream": {
			ProviderSelections: []string{"ProviderOne", "ProviderTwo"},
			IndexerSelections:  []string{"IndexerOne", "IndexerTwo"},
			IndexerOverrides: map[string]config.IndexerSearchConfig{
				"IndexerOne": {SearchResultLimit: 10},
				"IndexerTwo": {SearchResultLimit: 20},
			},
		},
	}

	applyStreamNameRenames(
		streams,
		map[string]string{"providerone": "ProviderRenamed"},
		map[string]string{"indexerone": "IndexerRenamed"},
	)

	if got := streams["stream"].ProviderSelections; !reflect.DeepEqual(got, []string{"ProviderRenamed", "ProviderTwo"}) {
		t.Fatalf("provider selections = %#v", got)
	}
	if got := streams["stream"].IndexerSelections; !reflect.DeepEqual(got, []string{"IndexerRenamed", "IndexerTwo"}) {
		t.Fatalf("indexer selections = %#v", got)
	}
	if _, ok := streams["stream"].IndexerOverrides["IndexerRenamed"]; !ok {
		t.Fatalf("expected renamed override to exist")
	}
	if _, ok := streams["stream"].IndexerOverrides["IndexerOne"]; ok {
		t.Fatalf("expected old override key to be removed")
	}
}

func TestRenamedNamesByIndexDetectsNameChanges(t *testing.T) {
	currentProviders := []config.Provider{
		{Name: "ProviderOne"},
		{Name: "ProviderTwo"},
	}
	nextProviders := []config.Provider{
		{Name: "ProviderRenamed"},
		{Name: "ProviderTwo"},
	}
	providerRenames := renamedNamesByIndex(currentProviders, nextProviders, func(provider config.Provider) string {
		return provider.Name
	})
	if got := providerRenames["providerone"]; got != "ProviderRenamed" {
		t.Fatalf("provider rename map = %#v", providerRenames)
	}
	if _, exists := providerRenames["providertwo"]; exists {
		t.Fatalf("unexpected rename for unchanged provider: %#v", providerRenames)
	}
}

// Renaming a profile has to follow every reference to it, or a stream keeps
// pointing at a name that no longer exists and silently stops filtering.
func TestApplyFilterProfileRenamesFollowsEveryReference(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			FilterProfileName: "Old Name",
			FilterProfileByType: map[string]string{
				"movie":      "Old Name",
				"anime_show": "Anime",
			},
		},
		"living-room": {
			FilterProfileName:   "Untouched",
			FilterProfileByType: map[string]string{"series": "old name"},
		},
	}

	applyFilterProfileRenames(streams, map[string]string{"old name": "New Name"})

	if got := streams["default"].FilterProfileName; got != "New Name" {
		t.Errorf("default profile = %q, want %q", got, "New Name")
	}
	if got := streams["default"].FilterProfileByType["movie"]; got != "New Name" {
		t.Errorf("movie binding = %q, want %q", got, "New Name")
	}
	if got := streams["default"].FilterProfileByType["anime_show"]; got != "Anime" {
		t.Errorf("unrelated binding changed to %q", got)
	}
	// Matching is case-insensitive, as it is for the stream-wide profile.
	if got := streams["living-room"].FilterProfileByType["series"]; got != "New Name" {
		t.Errorf("series binding = %q, want %q", got, "New Name")
	}
	if got := streams["living-room"].FilterProfileName; got != "Untouched" {
		t.Errorf("unrelated profile changed to %q", got)
	}
}
