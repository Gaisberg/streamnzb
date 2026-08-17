package stremio

import (
	"encoding/json"
	"strings"
	"testing"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
)

func manifestName(t *testing.T, isAdmin bool, streamName, addonName string) string {
	t.Helper()
	data, err := NewManifest("1.2.3").ToJSONForDevice(isAdmin, streamName, addonName)
	if err != nil {
		t.Fatalf("ToJSONForDevice failed: %v", err)
	}
	var out Manifest
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	return out.Name
}

func TestManifestNameDefaultsToServicePlusStream(t *testing.T) {
	if got := manifestName(t, false, "Living Room", ""); got != "StreamNZB · Living Room" {
		t.Fatalf("name = %q, want the service name with the stream appended", got)
	}
	if got := manifestName(t, false, "", ""); got != "StreamNZB" {
		t.Fatalf("name = %q, want the bare service name when there is no stream", got)
	}
}

func TestManifestNameOverrideReplacesTheWholeLabel(t *testing.T) {
	got := manifestName(t, false, "Living Room", "Usenet 4K")
	if got != "Usenet 4K" {
		t.Fatalf("name = %q, want the override alone — a stream setting one is not asking for a suffix", got)
	}
}

func TestManifestNameOverrideIsTrimmedAndCapped(t *testing.T) {
	if got := manifestName(t, false, "Living Room", "   "); got != "StreamNZB · Living Room" {
		t.Fatalf("name = %q, want the default back when the override is only whitespace", got)
	}

	long := strings.Repeat("a", MaxAddonNameLength+25)
	got := manifestName(t, false, "Living Room", long)
	if len(got) != MaxAddonNameLength {
		t.Fatalf("override length = %d, want it capped at %d", len(got), MaxAddonNameLength)
	}
}

func TestNormalizeAddonName(t *testing.T) {
	if got := NormalizeAddonName("  Kids  "); got != "Kids" {
		t.Fatalf("NormalizeAddonName = %q, want %q", got, "Kids")
	}
	if got := NormalizeAddonName(""); got != "" {
		t.Fatalf("NormalizeAddonName = %q, want empty", got)
	}
}

func TestServiceNameFallsBackToTheDefault(t *testing.T) {
	if got := ServiceName(nil); got != DefaultServiceName {
		t.Fatalf("ServiceName(nil) = %q, want %q", got, DefaultServiceName)
	}
	if got := ServiceName(&auth.Stream{Username: "Living Room"}); got != DefaultServiceName {
		t.Fatalf("ServiceName without an override = %q, want %q", got, DefaultServiceName)
	}
	if got := ServiceName(&auth.Stream{Username: "Living Room", AddonName: "  Usenet 4K  "}); got != "Usenet 4K" {
		t.Fatalf("ServiceName = %q, want the trimmed override", got)
	}
}

// TestForConfigDisabledIsByteIdentical pins the opt-out contract: with the
// metadata master switch explicitly off (or no config at all), the manifest
// must be exactly what the addon served before catalogs/meta existed.
func TestForConfigDisabledIsByteIdentical(t *testing.T) {
	base := NewManifest("1.2.3")
	baseline, err := base.ToJSONForDevice(false, "Living Room", "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for name, cfg := range map[string]*config.Config{
		"nil config": nil,
		"master off": {Metadata: config.MetadataConfig{Enabled: boolPtr(false)}},
	} {
		got, err := base.ForConfig(cfg).ToJSONForDevice(false, "Living Room", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != string(baseline) {
			t.Fatalf("%s: manifest differs from the pre-feature manifest", name)
		}
	}
}

// The master switch defaults to on: an untouched config already declares the
// metadata resources.
func TestForConfigEnabledDeclaresResourcesAndCatalogs(t *testing.T) {
	base := NewManifest("1.2.3")
	cfg := &config.Config{}

	out := base.ForConfig(cfg)
	if len(out.Resources) != 3 || out.Resources[0] != "stream" || out.Resources[1] != "catalog" || out.Resources[2] != "meta" {
		t.Fatalf("Resources = %v", out.Resources)
	}
	if len(out.Catalogs) == 0 {
		t.Fatal("expected default catalogs with an empty toggle list")
	}
	// Search-capable catalogs must declare the extra or clients never search.
	foundSearch := false
	for _, cat := range out.Catalogs {
		for _, extra := range cat.Extra {
			if extra.Name == "search" {
				foundSearch = true
			}
		}
	}
	if !foundSearch {
		t.Fatal("no catalog declares the search extra")
	}

	// The copy is shallow: the base manifest must be untouched.
	if len(base.Resources) != 1 || base.Resources[0] != "stream" {
		t.Fatalf("base manifest Resources mutated: %v", base.Resources)
	}
	if len(base.Catalogs) != 0 {
		t.Fatalf("base manifest Catalogs mutated: %v", base.Catalogs)
	}
}

func TestEnabledCatalogDefsRespectstogglesAndOrder(t *testing.T) {
	cfg := &config.Config{Metadata: config.MetadataConfig{
		Catalogs: []config.CatalogToggle{
			{ID: "kitsu.trending.anime", Enabled: true},
			{ID: "tmdb.trending.movie", Enabled: true},
			{ID: "tmdb.popular.movie", Enabled: false},
			{ID: "not.a.real.catalog", Enabled: true},
			{ID: "kitsu.trending.anime", Enabled: true}, // duplicate
		},
	}}
	defs := enabledCatalogDefs(cfg)
	if len(defs) != 2 {
		t.Fatalf("defs = %d, want 2 (disabled, unknown, duplicate dropped)", len(defs))
	}
	// Config order wins over registry order.
	if defs[0].ID != "kitsu.trending.anime" || defs[1].ID != "tmdb.trending.movie" {
		t.Fatalf("order = [%s, %s]", defs[0].ID, defs[1].ID)
	}

	// Master off: nothing, regardless of toggles.
	cfg.Metadata.Enabled = boolPtr(false)
	if defs := enabledCatalogDefs(cfg); len(defs) != 0 {
		t.Fatalf("master off must disable all catalogs, got %d", len(defs))
	}
}

// TestDefaultCatalogsAreOneFlagshipPerType pins the suppressed default set: a
// fresh install gets exactly one search-carrying trending row per media type;
// everything else is opt-in from the Metadata page.
func TestDefaultCatalogsAreOneFlagshipPerType(t *testing.T) {
	defs := enabledCatalogDefs(&config.Config{})
	want := []string{"tmdb.trending.movie", "tmdb.trending.series", "kitsu.trending.anime"}
	if len(defs) != len(want) {
		t.Fatalf("defaults = %d catalogs, want %d", len(defs), len(want))
	}
	for i, id := range want {
		if defs[i].ID != id {
			t.Fatalf("defaults[%d] = %s, want %s", i, defs[i].ID, id)
		}
		if !defs[i].SupportsSearch {
			t.Fatalf("default %s must carry the search extra", id)
		}
	}
}

// TestEnabledCatalogDefsNilVersusEmpty pins the two distinct zero states: a
// never-configured (nil) list gets the registry defaults, while an explicitly
// saved empty list means none — a meta-only setup with every catalog removed.
func TestEnabledCatalogDefsNilVersusEmpty(t *testing.T) {
	nilCfg := &config.Config{}
	if defs := enabledCatalogDefs(nilCfg); len(defs) == 0 {
		t.Fatal("nil catalog list must yield the registry defaults")
	}
	emptyCfg := &config.Config{Metadata: config.MetadataConfig{Catalogs: []config.CatalogToggle{}}}
	if defs := enabledCatalogDefs(emptyCfg); len(defs) != 0 {
		t.Fatalf("explicit empty catalog list must yield none, got %d", len(defs))
	}
}

func TestStreamDisplayNameUsesOverrideWithoutTheStreamSuffix(t *testing.T) {
	// Without an override the label is two lines so several installed configs
	// are tellable apart.
	if got := streamDisplayName(&auth.Stream{Username: "Living Room"}); got != "StreamNZB\nLiving Room" {
		t.Fatalf("display name = %q, want the default two-line label", got)
	}
	// With one, the override IS the name — appending the stream name under it
	// would undo the point of setting it.
	if got := streamDisplayName(&auth.Stream{Username: "Living Room", AddonName: "Dandelion is 1337"}); got != "Dandelion is 1337" {
		t.Fatalf("display name = %q, want the override alone", got)
	}
	if got := streamDisplayName(nil); got != DefaultServiceName {
		t.Fatalf("display name for no stream = %q, want %q", got, DefaultServiceName)
	}
}
