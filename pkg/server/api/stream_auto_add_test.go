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

// A non-empty overrides map is the selected-indexer set at search time, so an
// auto-added indexer without an entry there is silently never queried.
func TestApplyStreamAutoSelectionsSeedsOverridesForAutoAddedIndexers(t *testing.T) {
	nextCfg := &config.Config{
		Indexers: []config.IndexerConfig{
			{Name: "Existing", Enabled: boolPtr(true)},
			{Name: "NewlyAdded", Enabled: boolPtr(true)},
		},
		Streams: map[string]*config.StreamEntry{
			"default": {
				AutoAddIndexers:   boolPtr(true),
				IndexerSelections: []string{"Existing"},
				IndexerOverrides: map[string]config.IndexerSearchConfig{
					"Existing": {SearchResultLimit: 10},
				},
			},
			"unrestricted": {
				AutoAddIndexers:   boolPtr(true),
				IndexerSelections: []string{"Existing"},
			},
		},
	}

	applyStreamAutoSelections(nextCfg)

	overrides := nextCfg.Streams["default"].IndexerOverrides
	if _, ok := overrides["NewlyAdded"]; !ok {
		t.Fatalf("expected auto-added indexer to get an override entry, got %#v", overrides)
	}
	if got := overrides["Existing"].SearchResultLimit; got != 10 {
		t.Fatalf("expected existing override to be preserved, got limit %d", got)
	}
	// Empty/nil maps mean "no restriction" and must stay empty.
	if got := nextCfg.Streams["unrestricted"].IndexerOverrides; len(got) != 0 {
		t.Fatalf("expected empty overrides to stay empty, got %#v", got)
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

// Deleting a profile must not be blocked by the streams still pointing at it.
func TestDropDeletedFilterProfilesClearsDanglingReferences(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			FilterProfileName: "Deleted",
			FilterProfileByType: map[string]string{
				"movie":      "Deleted",
				"anime_show": "Anime",
			},
		},
		"living-room": {
			FilterProfileName:   "Kept",
			FilterProfileByType: map[string]string{"series": "Deleted"},
		},
	}
	remaining := []config.FilterProfileConfig{{Name: "Kept"}, {Name: "Anime"}}

	dropDeletedFilterProfiles(streams, remaining)

	if got := streams["default"].FilterProfileName; got != "" {
		t.Errorf("stream-wide reference to a deleted profile = %q, want cleared", got)
	}
	if _, ok := streams["default"].FilterProfileByType["movie"]; ok {
		t.Error("binding to a deleted profile should be removed")
	}
	if got := streams["default"].FilterProfileByType["anime_show"]; got != "Anime" {
		t.Errorf("surviving binding = %q, want %q", got, "Anime")
	}
	if got := streams["living-room"].FilterProfileName; got != "Kept" {
		t.Errorf("surviving profile = %q, want %q", got, "Kept")
	}
	// A stream left with no bindings should not keep an empty map around.
	if streams["living-room"].FilterProfileByType != nil {
		t.Errorf("expected bindings to be nil, got %#v", streams["living-room"].FilterProfileByType)
	}
}

// A renamed profile must not be mistaken for a deleted one.
func TestRenameThenDropKeepsRenamedProfiles(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			FilterProfileName:   "Old Name",
			FilterProfileByType: map[string]string{"movie": "Old Name"},
		},
	}

	applyFilterProfileRenames(streams, map[string]string{"old name": "New Name"})
	dropDeletedFilterProfiles(streams, []config.FilterProfileConfig{{Name: "New Name"}})

	if got := streams["default"].FilterProfileName; got != "New Name" {
		t.Errorf("renamed profile = %q, want %q", got, "New Name")
	}
	if got := streams["default"].FilterProfileByType["movie"]; got != "New Name" {
		t.Errorf("renamed binding = %q, want %q", got, "New Name")
	}
}

// A metadata-profile rename follows into stream bindings, and deletion clears
// them — a stream left unbound serves the stream-only manifest.
func TestMetadataProfileRenameAndDropPropagation(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default":     {MetadataProfileName: "Old Name"},
		"living-room": {MetadataProfileName: "old name"}, // case-insensitive match
		"bedroom":     {MetadataProfileName: "Untouched"},
	}

	applyMetadataProfileRenames(streams, map[string]string{"old name": "New Name"})
	if got := streams["default"].MetadataProfileName; got != "New Name" {
		t.Errorf("default binding = %q, want %q", got, "New Name")
	}
	if got := streams["living-room"].MetadataProfileName; got != "New Name" {
		t.Errorf("case-insensitive binding = %q, want %q", got, "New Name")
	}
	if got := streams["bedroom"].MetadataProfileName; got != "Untouched" {
		t.Errorf("unrelated binding changed to %q", got)
	}

	// Rename resolved first, so the renamed profile is never seen as deleted.
	dropDeletedMetadataProfiles(streams, []config.MetadataProfileConfig{{Name: "New Name"}})
	if got := streams["default"].MetadataProfileName; got != "New Name" {
		t.Errorf("renamed binding dropped: %q", got)
	}
	if got := streams["bedroom"].MetadataProfileName; got != "" {
		t.Errorf("dangling binding = %q, want cleared", got)
	}
}

func TestDropDeletedProvidersClearsDanglingReferences(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			ProviderSelections: []string{"Newshosting", "DeletedProvider", "Eweka"},
		},
	}
	remaining := []config.Provider{{Name: "Newshosting"}, {Name: "Eweka"}}

	dropDeletedProviders(streams, remaining)

	got := streams["default"].ProviderSelections
	if len(got) != 2 || got[0] != "Newshosting" || got[1] != "Eweka" {
		t.Errorf("expected [Newshosting, Eweka], got %#v", got)
	}
}

func TestDropDeletedIndexersClearsDanglingReferences(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			IndexerSelections: []string{"altHUB", "DeletedIndexer"},
			IndexerOverrides: map[string]config.IndexerSearchConfig{
				"altHUB":         {SearchResultLimit: 10},
				"DeletedIndexer": {SearchResultLimit: 10},
			},
		},
	}
	remaining := []config.IndexerConfig{{Name: "altHUB"}}

	dropDeletedIndexers(streams, remaining)

	gotSelections := streams["default"].IndexerSelections
	if len(gotSelections) != 1 || gotSelections[0] != "altHUB" {
		t.Errorf("expected [altHUB], got %#v", gotSelections)
	}
	if _, ok := streams["default"].IndexerOverrides["DeletedIndexer"]; ok {
		t.Error("override for deleted indexer should be removed")
	}
	if _, ok := streams["default"].IndexerOverrides["altHUB"]; !ok {
		t.Error("override for surviving indexer should remain")
	}
}

func TestSanitizeConnectionLimitsKeepsOnlyMeaningfulCaps(t *testing.T) {
	providers := []config.Provider{
		{Name: "Eweka", Connections: 50},
		{Name: "Newshosting", Connections: 20},
	}
	selected := []string{"Eweka", "Newshosting"}

	limits := sanitizeConnectionLimits(map[string]int{
		"Eweka":       8,  // kept
		"Newshosting": 20, // equals the pool size, so it caps nothing
		"Unselected":  4,  // not selected by this stream
	}, selected, providers)

	if len(limits) != 1 {
		t.Fatalf("expected 1 kept limit, got %#v", limits)
	}
	if limits["Eweka"] != 8 {
		t.Errorf("expected Eweka capped at 8, got %#v", limits)
	}
}

func TestSanitizeConnectionLimitsDropsNonPositive(t *testing.T) {
	providers := []config.Provider{{Name: "Eweka", Connections: 50}}
	if limits := sanitizeConnectionLimits(map[string]int{"Eweka": 0}, []string{"Eweka"}, providers); limits != nil {
		t.Errorf("zero means uncapped and should be dropped, got %#v", limits)
	}
	if limits := sanitizeConnectionLimits(map[string]int{"Eweka": -3}, []string{"Eweka"}, providers); limits != nil {
		t.Errorf("negative limits should be dropped, got %#v", limits)
	}
}

func TestRenameAndDeleteFollowConnectionLimits(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			ProviderSelections:       []string{"OldName", "Doomed"},
			ProviderConnectionLimits: map[string]int{"OldName": 8, "Doomed": 4},
		},
	}

	applyStreamNameRenames(streams, map[string]string{"oldname": "NewName"}, nil)
	if streams["default"].ProviderConnectionLimits["NewName"] != 8 {
		t.Errorf("cap should follow the rename, got %#v", streams["default"].ProviderConnectionLimits)
	}

	dropDeletedProviders(streams, []config.Provider{{Name: "NewName"}})
	if _, ok := streams["default"].ProviderConnectionLimits["Doomed"]; ok {
		t.Errorf("cap for a deleted provider should be removed, got %#v", streams["default"].ProviderConnectionLimits)
	}
	if streams["default"].ProviderConnectionLimits["NewName"] != 8 {
		t.Errorf("surviving cap should remain, got %#v", streams["default"].ProviderConnectionLimits)
	}
}

func TestSanitizeDisabledProvidersKeepsOnlySelected(t *testing.T) {
	kept := sanitizeDisabledProviders([]string{"Eweka", "Removed"}, []string{"Eweka", "Newshosting"})
	if len(kept) != 1 || kept[0] != "Eweka" {
		t.Fatalf("expected [Eweka], got %#v", kept)
	}
	if got := sanitizeDisabledProviders([]string{"Removed"}, []string{"Eweka"}); got != nil {
		t.Fatalf("expected nil when nothing survives, got %#v", got)
	}
}

func TestDisabledProvidersSurviveSyncAndFollowRenames(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"default": {
			AutoAddProviders:   boolPtr(true),
			ProviderSelections: []string{"OldName", "Newshosting"},
			DisabledProviders:  []string{"OldName"},
		},
	}

	applyStreamNameRenames(streams, map[string]string{"oldname": "NewName"}, nil)
	disabled := streams["default"].DisabledProviders
	if len(disabled) != 1 || disabled[0] != "NewName" {
		t.Fatalf("disabled entry should follow the rename, got %#v", disabled)
	}

	dropDeletedProviders(streams, []config.Provider{{Name: "Newshosting"}})
	if len(streams["default"].DisabledProviders) != 0 {
		t.Fatalf("disabled entry for a deleted provider should be removed, got %#v", streams["default"].DisabledProviders)
	}
}
