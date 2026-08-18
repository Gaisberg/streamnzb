package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadFromJSON(t *testing.T, content string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithPath(path)
	if err != nil {
		t.Fatalf("LoadWithPath error = %v", err)
	}
	return cfg
}

func TestMetadataProfileMigrationSeedsAndBinds(t *testing.T) {
	cfg := loadFromJSON(t, `{
		"config_version": 2,
		"streams": {
			"living-room": {"username": "living-room", "token": "tok1"},
			"bedroom": {"username": "bedroom", "token": "tok2"}
		},
		"metadata": {
			"catalogs": [{"id": "tmdb.trending.movie", "enabled": true}],
			"series_source": "tmdb",
			"language": "de-DE",
			"tvmaze_air_dates": false
		}
	}`)

	if len(cfg.MetadataProfiles) != 1 {
		t.Fatalf("MetadataProfiles len = %d, want 1", len(cfg.MetadataProfiles))
	}
	p := cfg.MetadataProfiles[0]
	if p.Name != DefaultMetadataProfileName {
		t.Errorf("seeded profile name = %q", p.Name)
	}
	if len(p.Catalogs) != 1 || p.Catalogs[0].ID != "tmdb.trending.movie" {
		t.Errorf("catalogs not carried over: %+v", p.Catalogs)
	}
	if p.SeriesSource != "tmdb" || p.Language != "de-DE" {
		t.Errorf("sources/language not carried over: %+v", p)
	}
	if p.EffectiveTVMazeAirDates() {
		t.Error("tvmaze_air_dates=false not carried over")
	}
	for name, entry := range cfg.Streams {
		if entry.MetadataProfileName != DefaultMetadataProfileName {
			t.Errorf("stream %q binding = %q, want Default", name, entry.MetadataProfileName)
		}
	}
	if cfg.Metadata.Enabled != nil {
		t.Error("legacy master switch should be cleared after migration")
	}

	// Idempotency: a second load of the saved file must not duplicate the
	// profile or rebind anything.
	cfg2, err := LoadWithPath(cfg.LoadedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.MetadataProfiles) != 1 {
		t.Errorf("second load MetadataProfiles len = %d, want 1", len(cfg2.MetadataProfiles))
	}
}

func TestMetadataProfileMigrationDisabledMetadataStaysUnbound(t *testing.T) {
	cfg := loadFromJSON(t, `{
		"config_version": 2,
		"streams": {"tv": {"username": "tv", "token": "tok"}},
		"metadata": {"enabled": false, "catalogs": [], "language": "fi-FI"}
	}`)

	if len(cfg.MetadataProfiles) != 1 {
		t.Fatalf("MetadataProfiles len = %d, want 1", len(cfg.MetadataProfiles))
	}
	if cfg.MetadataProfiles[0].Language != "fi-FI" {
		t.Error("settings should stay recoverable in the seeded profile")
	}
	if got := cfg.Streams["tv"].MetadataProfileName; got != "" {
		t.Errorf("disabled metadata must not bind streams, got %q", got)
	}
	if cfg.Metadata.Enabled != nil {
		t.Error("legacy master switch should be cleared after migration")
	}
}

func TestMetadataProfileEmptyListNotReseeded(t *testing.T) {
	cfg := loadFromJSON(t, `{
		"config_version": 2,
		"metadata_profiles": [],
		"streams": {"tv": {"username": "tv", "token": "tok"}}
	}`)

	if cfg.MetadataProfiles == nil || len(cfg.MetadataProfiles) != 0 {
		t.Errorf("deleted-all-profiles state must stay empty, got %+v", cfg.MetadataProfiles)
	}
	if got := cfg.Streams["tv"].MetadataProfileName; got != "" {
		t.Errorf("no binding expected, got %q", got)
	}
}

func TestMetadataProfileFreshInstallBindsDefaultStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := LoadWithPath(path)
	if err != nil {
		t.Fatalf("LoadWithPath error = %v", err)
	}
	if len(cfg.MetadataProfiles) != 1 {
		t.Fatalf("MetadataProfiles len = %d, want 1", len(cfg.MetadataProfiles))
	}
	if len(cfg.Streams) == 0 {
		t.Fatal("fresh install should seed a default stream")
	}
	for name, entry := range cfg.Streams {
		if entry.MetadataProfileName != DefaultMetadataProfileName {
			t.Errorf("fresh stream %q binding = %q, want Default", name, entry.MetadataProfileName)
		}
	}
}

func TestMetadataProfilesRoundTripAsEmptyNotNull(t *testing.T) {
	// Once migrated, the field must serialize (no omitempty) so nil-vs-empty
	// stays distinguishable across saves.
	cfg := &Config{MetadataProfiles: []MetadataProfileConfig{}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	v, ok := raw["metadata_profiles"]
	if !ok {
		t.Fatal("metadata_profiles must serialize even when empty")
	}
	if string(v) != "[]" {
		t.Errorf("empty profile list serialized as %s, want []", v)
	}
}

func TestMetadataProfileByName(t *testing.T) {
	cfg := &Config{MetadataProfiles: []MetadataProfileConfig{{Name: "Kids"}, {Name: "Default"}}}
	if p := cfg.MetadataProfileByName("kids"); p == nil || p.Name != "Kids" {
		t.Error("lookup should be case-insensitive")
	}
	if p := cfg.MetadataProfileByName(""); p != nil {
		t.Error("empty name must resolve to nil")
	}
	if p := cfg.MetadataProfileByName("nope"); p != nil {
		t.Error("unknown name must resolve to nil")
	}
}
