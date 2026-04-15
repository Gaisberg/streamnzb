package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeIndexerSearchDefaultsSeriesSeasonAndCompleteSearchOn(t *testing.T) {
	merged := MergeIndexerSearch(&IndexerConfig{}, nil, &Config{})
	if merged.EnableSeriesSeasonSearch == nil || !*merged.EnableSeriesSeasonSearch {
		t.Fatalf("expected EnableSeriesSeasonSearch default true, got %#v", merged.EnableSeriesSeasonSearch)
	}
	if merged.EnableSeriesCompleteSearch == nil || !*merged.EnableSeriesCompleteSearch {
		t.Fatalf("expected EnableSeriesCompleteSearch default true, got %#v", merged.EnableSeriesCompleteSearch)
	}
}

func TestMergeIndexerSearchLegacySeriesPackSearchAppliesToSeasonAndComplete(t *testing.T) {
	merged := MergeIndexerSearch(
		&IndexerConfig{EnableSeriesPackSearch: ptrBool(false)},
		nil,
		&Config{},
	)
	if merged.EnableSeriesSeasonSearch == nil || *merged.EnableSeriesSeasonSearch {
		t.Fatalf("expected legacy pack search setting to disable season search, got %#v", merged.EnableSeriesSeasonSearch)
	}
	if merged.EnableSeriesCompleteSearch == nil || *merged.EnableSeriesCompleteSearch {
		t.Fatalf("expected legacy pack search setting to disable complete search, got %#v", merged.EnableSeriesCompleteSearch)
	}
}

func TestMergeIndexerSearchExplicitSeriesSearchOverridesWin(t *testing.T) {
	merged := MergeIndexerSearch(
		&IndexerConfig{
			EnableSeriesPackSearch:     ptrBool(false),
			EnableSeriesSeasonSearch:   ptrBool(true),
			EnableSeriesCompleteSearch: ptrBool(false),
		},
		&IndexerSearchConfig{
			EnableSeriesSeasonSearch:   ptrBool(false),
			EnableSeriesCompleteSearch: ptrBool(true),
		},
		&Config{},
	)
	if merged.EnableSeriesSeasonSearch == nil || *merged.EnableSeriesSeasonSearch {
		t.Fatalf("expected explicit season override to win, got %#v", merged.EnableSeriesSeasonSearch)
	}
	if merged.EnableSeriesCompleteSearch == nil || !*merged.EnableSeriesCompleteSearch {
		t.Fatalf("expected explicit complete override to win, got %#v", merged.EnableSeriesCompleteSearch)
	}
}

func TestIndexerConfigEffectiveTimeoutDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  IndexerConfig
		want int
	}{
		{name: "default newznab", cfg: IndexerConfig{}, want: DefaultInternalIndexerTimeoutSeconds},
		{name: "aggregator", cfg: IndexerConfig{Type: "aggregator"}, want: DefaultAggregatorIndexerTimeoutSeconds},
		{name: "nzbhydra", cfg: IndexerConfig{Type: "nzbhydra"}, want: DefaultAggregatorIndexerTimeoutSeconds},
		{name: "prowlarr", cfg: IndexerConfig{Type: "prowlarr"}, want: DefaultAggregatorIndexerTimeoutSeconds},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveTimeoutSeconds(); got != tt.want {
				t.Fatalf("EffectiveTimeoutSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIndexerConfigEffectiveTimeoutHonorsExplicitOverride(t *testing.T) {
	cfg := IndexerConfig{Type: "aggregator", TimeoutSeconds: 7}

	if got := cfg.EffectiveTimeoutSeconds(); got != 7 {
		t.Fatalf("EffectiveTimeoutSeconds() = %d, want 7", got)
	}
	if got := cfg.EffectiveTimeout(); got != 7*time.Second {
		t.Fatalf("EffectiveTimeout() = %v, want %v", got, 7*time.Second)
	}
}

func TestConfigEffectivePlaybackStartupTimeoutDefaults(t *testing.T) {
	cfg := &Config{}

	if got := cfg.EffectivePlaybackStartupTimeoutSeconds(); got != DefaultPlaybackStartupTimeoutSeconds {
		t.Fatalf("EffectivePlaybackStartupTimeoutSeconds() = %d, want %d", got, DefaultPlaybackStartupTimeoutSeconds)
	}
	if got := cfg.EffectivePlaybackStartupTimeout(); got != time.Duration(DefaultPlaybackStartupTimeoutSeconds)*time.Second {
		t.Fatalf("EffectivePlaybackStartupTimeout() = %v", got)
	}
}

func TestConfigEffectivePlaybackStartupTimeoutHonorsExplicitOverride(t *testing.T) {
	cfg := &Config{PlaybackStartupTimeoutSeconds: 12}

	if got := cfg.EffectivePlaybackStartupTimeoutSeconds(); got != 12 {
		t.Fatalf("EffectivePlaybackStartupTimeoutSeconds() = %d, want 12", got)
	}
	if got := cfg.EffectivePlaybackStartupTimeout(); got != 12*time.Second {
		t.Fatalf("EffectivePlaybackStartupTimeout() = %v, want %v", got, 12*time.Second)
	}
}

func TestConfigEffectivePlaybackStartupTimeoutRejectsOutOfRangeValues(t *testing.T) {
	cfg := &Config{PlaybackStartupTimeoutSeconds: 61}

	if got := cfg.EffectivePlaybackStartupTimeoutSeconds(); got != DefaultPlaybackStartupTimeoutSeconds {
		t.Fatalf("EffectivePlaybackStartupTimeoutSeconds() = %d, want %d", got, DefaultPlaybackStartupTimeoutSeconds)
	}
}

func TestApplyStreamModelUpgradeDefaultsCreatesQueriesAndDefaultStream(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{
			{Host: "news.newshosting.com"},
			{Name: "eweka", Host: "news.eweka.nl"},
		},
		Indexers: []IndexerConfig{
			{Name: "Indexer A"},
			{Name: "Indexer B"},
		},
	}

	if !cfg.ApplyProviderDefaults() {
		t.Fatalf("expected provider defaults to derive provider names")
	}

	if !cfg.applyStreamModelUpgradeDefaults() {
		t.Fatalf("expected stream model upgrade defaults to change config")
	}

	if len(cfg.MovieSearchQueries) != 2 {
		t.Fatalf("expected 2 movie queries, got %d", len(cfg.MovieSearchQueries))
	}
	if len(cfg.SeriesSearchQueries) != 2 {
		t.Fatalf("expected 2 series queries, got %d", len(cfg.SeriesSearchQueries))
	}

	stream := cfg.Streams[defaultMigratedStreamID]
	if stream == nil {
		t.Fatalf("expected migrated default stream to be created")
	}
	if stream.Token == "" {
		t.Fatalf("expected migrated default stream token to be populated")
	}
	if stream.IndexerMode != "combine" {
		t.Fatalf("expected default stream indexer mode combine, got %q", stream.IndexerMode)
	}
	if stream.FilterSortingMode != "aiostreams" {
		t.Fatalf("expected default stream filter sorting mode aiostreams, got %q", stream.FilterSortingMode)
	}
	if stream.ResultsMode != "display_all" {
		t.Fatalf("expected default stream results mode display_all, got %q", stream.ResultsMode)
	}
	if stream.EnableFailover == nil || !*stream.EnableFailover {
		t.Fatalf("expected default stream failover enabled, got %#v", stream.EnableFailover)
	}
	if len(stream.ProviderSelections) != 2 || stream.ProviderSelections[0] != "newshosting" {
		t.Fatalf("unexpected provider selections: %#v", stream.ProviderSelections)
	}
	if len(stream.IndexerSelections) != 2 {
		t.Fatalf("unexpected indexer selections: %#v", stream.IndexerSelections)
	}
	if len(stream.MovieSearchQueries) != 2 || stream.MovieSearchQueries[0] != "DefaultMovieText" {
		t.Fatalf("unexpected movie search queries: %#v", stream.MovieSearchQueries)
	}
	if len(stream.SeriesSearchQueries) != 2 || stream.SeriesSearchQueries[0] != "DefaultTVText" {
		t.Fatalf("unexpected series search queries: %#v", stream.SeriesSearchQueries)
	}

	if cfg.applyStreamModelUpgradeDefaults() {
		t.Fatalf("expected second upgrade application to be a no-op")
	}
}

func TestLoadFilePreservesLoadedPath(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"addon_port":7001}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := &Config{}
	if err := cfg.LoadFile(configPath); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if cfg.LoadedPath != configPath {
		t.Fatalf("LoadedPath = %q, want %q", cfg.LoadedPath, configPath)
	}
}

func TestSaveFileUpdatesLoadedPath(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	cfg := &Config{AddonPort: 7001}
	if err := cfg.SaveFile(configPath); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	if cfg.LoadedPath != configPath {
		t.Fatalf("LoadedPath = %q, want %q", cfg.LoadedPath, configPath)
	}
}
