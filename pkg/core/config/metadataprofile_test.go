package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// Each media type resolves its configured order to exactly the sources that
// will be tried, in order. Unknown entries and repeats are dropped rather than
// shortening the chain in a surprising way, and a list left with nothing
// usable falls back to the media type's default instead of serving no
// metadata at all.
func TestEffectiveMetaSourceOrders(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*MetadataProfileConfig)
		want      func(*MetadataProfileConfig) []string
		expect    []string
	}{
		{"movie default is TMDB alone — Cinemeta is opt-in",
			func(p *MetadataProfileConfig) {},
			(*MetadataProfileConfig).EffectiveMovieMetaSources, []string{"tmdb"}},
		{"movie order is honored",
			func(p *MetadataProfileConfig) { p.MovieSources = []string{"cinemeta", "tmdb"} },
			(*MetadataProfileConfig).EffectiveMovieMetaSources, []string{"cinemeta", "tmdb"}},
		{"movie repeats collapse",
			func(p *MetadataProfileConfig) { p.MovieSources = []string{"tmdb", "tmdb", "cinemeta"} },
			(*MetadataProfileConfig).EffectiveMovieMetaSources, []string{"tmdb", "cinemeta"}},
		{"movie unknown entries are dropped",
			func(p *MetadataProfileConfig) { p.MovieSources = []string{"netflix", "cinemeta"} },
			(*MetadataProfileConfig).EffectiveMovieMetaSources, []string{"cinemeta"}},
		{"movie all-unknown falls back to the default",
			func(p *MetadataProfileConfig) { p.MovieSources = []string{"netflix"} },
			(*MetadataProfileConfig).EffectiveMovieMetaSources, []string{"tmdb"}},
		{"movie rejects a source another media type owns",
			func(p *MetadataProfileConfig) { p.MovieSources = []string{"kitsu", "tmdb"} },
			(*MetadataProfileConfig).EffectiveMovieMetaSources, []string{"tmdb"}},

		{"series default is the historical TVDB then TMDB pair",
			func(p *MetadataProfileConfig) {},
			(*MetadataProfileConfig).EffectiveSeriesMetaSources, []string{"tvdb", "tmdb"}},
		{"series can be three deep",
			func(p *MetadataProfileConfig) { p.SeriesSources = []string{"cinemeta", "tvdb", "tmdb"} },
			(*MetadataProfileConfig).EffectiveSeriesMetaSources, []string{"cinemeta", "tvdb", "tmdb"}},
		{"series single source has no fallback",
			func(p *MetadataProfileConfig) { p.SeriesSources = []string{"tmdb"} },
			(*MetadataProfileConfig).EffectiveSeriesMetaSources, []string{"tmdb"}},
		{"series casing and padding normalize",
			func(p *MetadataProfileConfig) { p.SeriesSources = []string{" TVDB ", "Cinemeta"} },
			(*MetadataProfileConfig).EffectiveSeriesMetaSources, []string{"tvdb", "cinemeta"}},

		{"anime default is Kitsu then TVDB",
			func(p *MetadataProfileConfig) {},
			(*MetadataProfileConfig).EffectiveAnimeMetaSources, []string{"kitsu", "tvdb"}},
		{"anime order is honored",
			func(p *MetadataProfileConfig) { p.AnimeSources = []string{"tvdb", "kitsu"} },
			(*MetadataProfileConfig).EffectiveAnimeMetaSources, []string{"tvdb", "kitsu"}},
		{"anime rejects Cinemeta, which cannot serve it",
			func(p *MetadataProfileConfig) { p.AnimeSources = []string{"cinemeta", "kitsu"} },
			(*MetadataProfileConfig).EffectiveAnimeMetaSources, []string{"kitsu"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profile := &MetadataProfileConfig{}
			tc.configure(profile)
			if got := tc.want(profile); !slices.Equal(got, tc.expect) {
				t.Fatalf("sources = %v, want %v", got, tc.expect)
			}
		})
	}

	// A nil profile is a valid caller (unbound streams check EffectiveXxx too).
	var nilProfile *MetadataProfileConfig
	if got := nilProfile.EffectiveMovieMetaSources(); !slices.Equal(got, []string{"tmdb"}) {
		t.Fatalf("nil movie sources = %v", got)
	}
	if got := nilProfile.EffectiveSeriesMetaSources(); !slices.Equal(got, []string{"tvdb", "tmdb"}) {
		t.Fatalf("nil series sources = %v", got)
	}
	if got := nilProfile.EffectiveAnimeMetaSource(); got != "kitsu" {
		t.Fatalf("nil anime lead source = %q", got)
	}
}

// A profile saved by a build that still had the primary/backup pair folds into
// the order that replaced it, preserving which source led. The old fields are
// cleared so the next save writes only the list.
func TestMigrateLegacySourcePairs(t *testing.T) {
	profile := &MetadataProfileConfig{
		MovieSource:        "cinemeta",
		MovieBackupSource:  "tmdb",
		SeriesSource:       "tmdb",
		SeriesBackupSource: "cinemeta",
		AnimeBackupSource:  "kitsu",
		AnimeSource:        "tvdb",
	}
	profile.MigrateLegacySourcePairs()
	if !slices.Equal(profile.MovieSources, []string{"cinemeta", "tmdb"}) {
		t.Fatalf("movie = %v", profile.MovieSources)
	}
	if !slices.Equal(profile.SeriesSources, []string{"tmdb", "cinemeta"}) {
		t.Fatalf("series = %v", profile.SeriesSources)
	}
	if !slices.Equal(profile.AnimeSources, []string{"tvdb", "kitsu"}) {
		t.Fatalf("anime = %v", profile.AnimeSources)
	}
	if profile.MovieSource != "" || profile.SeriesBackupSource != "" || profile.AnimeSource != "" {
		t.Fatalf("legacy fields survived migration: %+v", profile)
	}

	// A primary with no explicit backup keeps the fallback the pair implied:
	// series and anime had one, movies never did.
	implicit := &MetadataProfileConfig{SeriesSource: "tmdb", AnimeSource: "tvdb", MovieSource: "cinemeta"}
	implicit.MigrateLegacySourcePairs()
	if !slices.Equal(implicit.SeriesSources, []string{"tmdb", "tvdb"}) {
		t.Fatalf("series lost its implicit TVDB fallback: %v", implicit.SeriesSources)
	}
	if !slices.Equal(implicit.AnimeSources, []string{"tvdb", "kitsu"}) {
		t.Fatalf("anime lost its implicit Kitsu fallback: %v", implicit.AnimeSources)
	}
	if !slices.Equal(implicit.MovieSources, []string{"cinemeta"}) {
		t.Fatalf("movies never had an implicit backup: %v", implicit.MovieSources)
	}

	// An already-migrated profile keeps its list untouched.
	already := &MetadataProfileConfig{SeriesSources: []string{"cinemeta"}, SeriesSource: "tvdb"}
	already.MigrateLegacySourcePairs()
	if !slices.Equal(already.SeriesSources, []string{"cinemeta"}) {
		t.Fatalf("an existing order was overwritten by the legacy pair: %v", already.SeriesSources)
	}
}

// NormalizeSources stores exactly what runs, so the editor never shows an
// order that differs from the one the meta builders walk.
func TestNormalizeSourcesWritesTheEffectiveOrder(t *testing.T) {
	profile := &MetadataProfileConfig{
		// Collapses to ["cinemeta"], which is not the movie default, so the
		// order survives normalization and proves the dropping happened.
		MovieSources:  []string{"cinemeta", "netflix", "cinemeta"},
		SeriesSources: nil,
	}
	profile.NormalizeSources()
	if !slices.Equal(profile.MovieSources, []string{"cinemeta"}) {
		t.Fatalf("unknown and repeated entries survived: %v", profile.MovieSources)
	}
	if profile.SeriesSources != nil {
		t.Fatalf("a list matching the default must stay unset, not be materialized: %v", profile.SeriesSources)
	}
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
	// The legacy global series_source meant "TMDB, then TVDB" — the pair's
	// implicit fallback has to survive as a second entry in the order.
	if !slices.Equal(p.SeriesSources, []string{"tmdb", "tvdb"}) || p.Language != "de-DE" {
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

func TestPosterOverlayURL(t *testing.T) {
	pattern := "https://btttr.cc/poster/imdb/poster-default/{imdb_id}.jpg?lang=de"
	p := &MetadataProfileConfig{PosterURLPattern: pattern}
	if got, want := p.PosterOverlayURL("tt0111161"), "https://btttr.cc/poster/imdb/poster-default/tt0111161.jpg?lang=de"; got != want {
		t.Errorf("PosterOverlayURL = %q, want %q", got, want)
	}
	for name, args := range map[string]struct {
		profile *MetadataProfileConfig
		id      string
	}{
		"nil profile":   {nil, "tt0111161"},
		"no pattern":    {&MetadataProfileConfig{}, "tt0111161"},
		"blank pattern": {&MetadataProfileConfig{PosterURLPattern: "  "}, "tt0111161"},
		"kitsu id":      {p, "kitsu:1376"},
		"tmdb id":       {p, "tmdb:278"},
		"empty id":      {p, ""},
	} {
		if got := args.profile.PosterOverlayURL(args.id); got != "" {
			t.Errorf("%s: PosterOverlayURL = %q, want empty", name, got)
		}
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
