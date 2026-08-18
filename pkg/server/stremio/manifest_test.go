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

// TestForProfileUnboundIsByteIdentical pins the opt-out contract: a stream
// with no metadata profile bound (nil profile) must get exactly the manifest
// the addon served before catalogs/meta existed.
func TestForProfileUnboundIsByteIdentical(t *testing.T) {
	base := NewManifest("1.2.3")
	baseline, err := base.ToJSONForDevice(false, "Living Room", "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	got, err := base.ForProfile(nil).ToJSONForDevice(false, "Living Room", "")
	if err != nil {
		t.Fatalf("nil profile: %v", err)
	}
	if string(got) != string(baseline) {
		t.Fatal("nil profile: manifest differs from the pre-feature manifest")
	}
}

// A bound profile declares the metadata resources; a fresh profile (nil
// catalog list) carries the registry defaults.
func TestForProfileDeclaresResourcesAndCatalogs(t *testing.T) {
	base := NewManifest("1.2.3")
	profile := &config.MetadataProfileConfig{Name: "Default"}

	out := base.ForProfile(profile)
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
	profile := &config.MetadataProfileConfig{
		Catalogs: []config.CatalogToggle{
			{ID: "kitsu.trending.anime", Enabled: true},
			{ID: "tmdb.trending.movie", Enabled: true},
			{ID: "tmdb.popular.movie", Enabled: false},
			{ID: "not.a.real.catalog", Enabled: true},
			{ID: "kitsu.trending.anime", Enabled: true}, // duplicate
		},
	}
	defs := enabledCatalogDefs(profile)
	if len(defs) != 2 {
		t.Fatalf("defs = %d, want 2 (disabled, unknown, duplicate dropped)", len(defs))
	}
	// Profile order wins over registry order.
	if defs[0].ID != "kitsu.trending.anime" || defs[1].ID != "tmdb.trending.movie" {
		t.Fatalf("order = [%s, %s]", defs[0].ID, defs[1].ID)
	}

	// No profile: nothing, regardless of anything else.
	if defs := enabledCatalogDefs(nil); len(defs) != 0 {
		t.Fatalf("nil profile must disable all catalogs, got %d", len(defs))
	}
}

// TestDefaultCatalogsAreOneFlagshipPerType pins the suppressed default set: a
// fresh install gets exactly one trending row per media type; everything else
// is opt-in from the Metadata page. Search does not ride these — it rides the
// hidden search carriers, so no browse catalog declares it.
func TestDefaultCatalogsAreOneFlagshipPerType(t *testing.T) {
	defs := enabledCatalogDefs(&config.MetadataProfileConfig{})
	want := []string{"tmdb.trending.movie", "tmdb.trending.series", "kitsu.trending.anime"}
	if len(defs) != len(want) {
		t.Fatalf("defaults = %d catalogs, want %d", len(defs), len(want))
	}
	for i, id := range want {
		if defs[i].ID != id {
			t.Fatalf("defaults[%d] = %s, want %s", i, defs[i].ID, id)
		}
	}
	for _, def := range catalogRegistry {
		if def.SupportsSearch {
			t.Fatalf("browse catalog %s declares search; search must ride the hidden carriers only", def.ID)
		}
	}
}

// TestManifestCarriesHiddenSearchCatalogs pins the search decoupling: every
// profile's manifest declares one required-search carrier per content type,
// whatever browse rows it picked — required extras keep them off the board.
func TestManifestCarriesHiddenSearchCatalogs(t *testing.T) {
	for name, profile := range map[string]*config.MetadataProfileConfig{
		"registry defaults": {},
		"no browse rows":    {Catalogs: []config.CatalogToggle{}},
		"kids rows only":    {Catalogs: []config.CatalogToggle{{ID: "tmdb.family.movie", Enabled: true}}},
	} {
		catalogs := enabledCatalogs(profile)
		found := map[string]bool{}
		for _, cat := range catalogs {
			for _, extra := range cat.Extra {
				if extra.Name == "search" {
					if !extra.IsRequired {
						t.Errorf("%s: %s declares optional search; must be required to stay off the board", name, cat.ID)
					}
					found[cat.Type] = true
				}
			}
		}
		for _, contentType := range []string{"movie", "series", "anime"} {
			if !found[contentType] {
				t.Errorf("%s: no search carrier for %s", name, contentType)
			}
		}
	}
	if catalogs := enabledCatalogs(nil); len(catalogs) != 0 {
		t.Errorf("nil profile must carry no catalogs at all, got %d", len(catalogs))
	}
}

// TestEnabledCatalogDefsNilVersusEmpty pins the two distinct zero states: a
// never-configured (nil) list gets the registry defaults, while an explicitly
// saved empty list means none — a meta-only setup with every catalog removed.
func TestEnabledCatalogDefsNilVersusEmpty(t *testing.T) {
	nilList := &config.MetadataProfileConfig{}
	if defs := enabledCatalogDefs(nilList); len(defs) == 0 {
		t.Fatal("nil catalog list must yield the registry defaults")
	}
	emptyList := &config.MetadataProfileConfig{Catalogs: []config.CatalogToggle{}}
	if defs := enabledCatalogDefs(emptyList); len(defs) != 0 {
		t.Fatalf("explicit empty catalog list must yield none, got %d", len(defs))
	}
}

// TestPerStreamManifestDivergence pins the whole point of metadata profiles:
// two streams on one server get different manifests from their bindings.
func TestPerStreamManifestDivergence(t *testing.T) {
	srv := &Server{config: &config.Config{
		MetadataProfiles: []config.MetadataProfileConfig{
			{Name: "Default"},
			{Name: "Kids", Catalogs: []config.CatalogToggle{{ID: "tmdb.trending.movie", Enabled: true}}},
		},
	}}
	base := NewManifest("1.2.3")

	full := base.ForProfile(srv.metadataProfileFor(&auth.Stream{Username: "livingroom", MetadataProfileName: "Default"}))
	kids := base.ForProfile(srv.metadataProfileFor(&auth.Stream{Username: "kids", MetadataProfileName: "Kids"}))
	unbound := base.ForProfile(srv.metadataProfileFor(&auth.Stream{Username: "guest"}))

	if len(full.Catalogs) <= len(kids.Catalogs) {
		t.Fatalf("default profile catalogs = %d, kids = %d; expected the default (registry defaults) to carry more", len(full.Catalogs), len(kids.Catalogs))
	}
	// Browse rows differ per profile; the hidden search carriers ride along
	// everywhere and don't count as board rows.
	var kidsBrowse []Catalog
	for _, cat := range kids.Catalogs {
		if len(cat.Extra) == 0 || cat.Extra[0].Name != "search" {
			kidsBrowse = append(kidsBrowse, cat)
		}
	}
	if len(kidsBrowse) != 1 || kidsBrowse[0].ID != "tmdb.trending.movie" {
		t.Fatalf("kids browse catalogs = %+v", kidsBrowse)
	}
	if len(unbound.Resources) != 1 || unbound.Resources[0] != "stream" {
		t.Fatalf("unbound stream resources = %v, want stream-only", unbound.Resources)
	}
}

// The env kill-switch blanks metadata for every stream regardless of binding.
func TestMetadataProfileForKillSwitch(t *testing.T) {
	off := false
	srv := &Server{config: &config.Config{
		Metadata:         config.MetadataConfig{Enabled: &off},
		MetadataProfiles: []config.MetadataProfileConfig{{Name: "Default"}},
	}}
	if p := srv.metadataProfileFor(&auth.Stream{Username: "x", MetadataProfileName: "Default"}); p != nil {
		t.Fatal("kill-switch off must resolve every stream to nil")
	}
}

// The admin token's synthesized stream carries no binding; it falls back to
// the Default profile (else the first) so the admin install keeps catalogs.
func TestMetadataProfileForAdminFallback(t *testing.T) {
	srv := &Server{config: &config.Config{
		MetadataProfiles: []config.MetadataProfileConfig{{Name: "Something"}, {Name: "Default"}},
	}}
	adminStream := &auth.Stream{Username: "admin"}
	if p := srv.metadataProfileFor(adminStream); p == nil || p.Name != "Default" {
		t.Fatalf("admin fallback = %+v, want the Default profile", p)
	}
	srv.config.MetadataProfiles = []config.MetadataProfileConfig{{Name: "Only"}}
	if p := srv.metadataProfileFor(adminStream); p == nil || p.Name != "Only" {
		t.Fatalf("admin fallback without Default = %+v, want the first profile", p)
	}
	srv.config.MetadataProfiles = nil
	if p := srv.metadataProfileFor(adminStream); p != nil {
		t.Fatal("admin with no profiles must be metadata-off")
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
