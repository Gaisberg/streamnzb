package config

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/env"
	coreenv "streamnzb/pkg/core/env"
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

func TestMergeIndexerSearchCarriesContentScope(t *testing.T) {
	merged := MergeIndexerSearch(&IndexerConfig{ContentScope: "anime"}, nil, &Config{})
	if merged.ContentScope == nil || *merged.ContentScope != IndexerContentScopeAnime {
		t.Fatalf("expected merged content scope %q, got %#v", IndexerContentScopeAnime, merged.ContentScope)
	}

	override := IndexerContentScopeNonAnime
	merged = MergeIndexerSearch(&IndexerConfig{ContentScope: "anime"}, &IndexerSearchConfig{ContentScope: &override}, &Config{})
	if merged.ContentScope == nil || *merged.ContentScope != IndexerContentScopeNonAnime {
		t.Fatalf("expected override content scope to win, got %#v", merged.ContentScope)
	}

	merged = MergeIndexerSearch(&IndexerConfig{ContentScope: "garbage"}, nil, &Config{})
	if merged.ContentScope != nil {
		t.Fatalf("expected unknown content scope to normalize to all content, got %#v", merged.ContentScope)
	}
}

func TestNormalizeIndexerContentScope(t *testing.T) {
	cases := map[string]string{
		"":           IndexerContentScopeAll,
		"all":        IndexerContentScopeAll,
		"anime":      IndexerContentScopeAnime,
		" Anime ":    IndexerContentScopeAnime,
		"non_anime":  IndexerContentScopeNonAnime,
		"NON_ANIME":  IndexerContentScopeNonAnime,
		"whatever":   IndexerContentScopeAll,
		"anime_only": IndexerContentScopeAll,
	}
	for raw, want := range cases {
		if got := NormalizeIndexerContentScope(raw); got != want {
			t.Fatalf("NormalizeIndexerContentScope(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeSeriesSearchScopeDefaultsToSeasonEpisode(t *testing.T) {
	if got := NormalizeSeriesSearchScope(""); got != SeriesSearchScopeSeasonEpisode {
		t.Fatalf("NormalizeSeriesSearchScope() = %q, want %q", got, SeriesSearchScopeSeasonEpisode)
	}
}

func TestNormalizeSeriesSearchScopeMigratesLegacyAbsolute(t *testing.T) {
	if got := NormalizeSeriesSearchScope("Absolute"); got != SeriesSearchScopeSeasonEpisode {
		t.Fatalf("NormalizeSeriesSearchScope() = %q, want %q", got, SeriesSearchScopeSeasonEpisode)
	}
}

func TestTryAbsoluteEpisodeEnabledDefaultsToTrue(t *testing.T) {
	var nilQuery *SearchQueryConfig
	if !nilQuery.TryAbsoluteEpisodeEnabled() {
		t.Fatal("nil query must default to enabled")
	}
	if !(&SearchQueryConfig{}).TryAbsoluteEpisodeEnabled() {
		t.Fatal("unset flag must default to enabled")
	}
	if (&SearchQueryConfig{TryAbsoluteEpisode: ptrBool(false)}).TryAbsoluteEpisodeEnabled() {
		t.Fatal("explicit false must disable the supplement")
	}
}

func TestSeriesSearchScopeRequiresValidation(t *testing.T) {
	if !SeriesSearchScopeRequiresValidation(SeriesSearchScopeSeason) {
		t.Fatalf("expected season scope to require validation")
	}
	if !SeriesSearchScopeRequiresValidation(SeriesSearchScopeNone) {
		t.Fatalf("expected none scope to require validation")
	}
	if SeriesSearchScopeRequiresValidation(SeriesSearchScopeSeasonEpisode) {
		t.Fatalf("did not expect season_episode scope to require validation")
	}
}

// TestDefaultSearchQuerySettingsMatchExpectedModes pins the four stock search
// requests to the settings the UI advertises as the defaults, field by field.
func TestDefaultSearchQuerySettingsMatchExpectedModes(t *testing.T) {
	cfg := &Config{}
	if !cfg.applyStreamModelUpgradeDefaults() {
		t.Fatalf("expected defaults to be applied")
	}

	cases := []struct {
		want SearchQueryConfig
		got  []SearchQueryConfig
		at   int
	}{
		{
			at:  0,
			got: cfg.MovieSearchQueries,
			want: SearchQueryConfig{
				Name:                "DefaultMovieText",
				SearchMode:          "text",
				MovieCategories:     "2000",
				SearchResultLimit:   0,
				SearchTitleLanguage: "en-US",
				IncludeYear:         ptrBool(true),
			},
		},
		{
			at:  1,
			got: cfg.MovieSearchQueries,
			want: SearchQueryConfig{
				Name:                 "DefaultMovieID",
				SearchMode:           "id",
				MovieCategories:      "2000",
				SearchResultLimit:    0,
				SearchTitleLanguages: []string{"en-US", ""},
				IncludeYear:          ptrBool(true),
			},
		},
		{
			at:  0,
			got: cfg.SeriesSearchQueries,
			want: SearchQueryConfig{
				Name:                "DefaultTVText",
				SearchMode:          "text",
				TVCategories:        "5000",
				SearchResultLimit:   0,
				SearchTitleLanguage: "en-US",
				IncludeYear:         ptrBool(false),
				SeriesSearchScope:   SeriesSearchScopeSeasonEpisode,
				TryAbsoluteEpisode:  ptrBool(true),
			},
		},
		{
			at:  1,
			got: cfg.SeriesSearchQueries,
			want: SearchQueryConfig{
				Name:                 "DefaultTVID",
				SearchMode:           "id",
				TVCategories:         "5000",
				SearchResultLimit:    0,
				SearchTitleLanguages: []string{"en-US", ""},
				IncludeYear:          ptrBool(false),
				SeriesSearchScope:    SeriesSearchScopeSeasonEpisode,
				TryAbsoluteEpisode:   ptrBool(true),
			},
		},
	}

	for _, tc := range cases {
		if tc.at >= len(tc.got) {
			t.Fatalf("expected a query at index %d for %s", tc.at, tc.want.Name)
		}
		got := tc.got[tc.at]
		t.Run(tc.want.Name, func(t *testing.T) {
			if got.Name != tc.want.Name {
				t.Fatalf("name = %q, want %q", got.Name, tc.want.Name)
			}
			if got.SearchMode != tc.want.SearchMode {
				t.Errorf("search_mode = %q, want %q", got.SearchMode, tc.want.SearchMode)
			}
			if got.MovieCategories != tc.want.MovieCategories {
				t.Errorf("movie_categories = %q, want %q", got.MovieCategories, tc.want.MovieCategories)
			}
			if got.TVCategories != tc.want.TVCategories {
				t.Errorf("tv_categories = %q, want %q", got.TVCategories, tc.want.TVCategories)
			}
			if got.SearchResultLimit != tc.want.SearchResultLimit {
				t.Errorf("search_result_limit = %d, want %d (max)", got.SearchResultLimit, tc.want.SearchResultLimit)
			}
			if got.SearchTitleLanguage != tc.want.SearchTitleLanguage {
				t.Errorf("search_title_language = %q, want %q", got.SearchTitleLanguage, tc.want.SearchTitleLanguage)
			}
			if !reflect.DeepEqual(got.SearchTitleLanguages, tc.want.SearchTitleLanguages) {
				t.Errorf("search_title_languages = %#v, want %#v", got.SearchTitleLanguages, tc.want.SearchTitleLanguages)
			}
			if got.IncludeYear == nil || *got.IncludeYear != *tc.want.IncludeYear {
				t.Errorf("include_year = %v, want %v", got.IncludeYear, *tc.want.IncludeYear)
			}
			if got.SeriesSearchScope != tc.want.SeriesSearchScope {
				t.Errorf("series_search_scope = %q, want %q", got.SeriesSearchScope, tc.want.SeriesSearchScope)
			}
			if tc.want.TryAbsoluteEpisode == nil {
				if got.TryAbsoluteEpisode != nil {
					t.Errorf("try_absolute_episode = %v, want unset", *got.TryAbsoluteEpisode)
				}
			} else if got.TryAbsoluteEpisode == nil || *got.TryAbsoluteEpisode != *tc.want.TryAbsoluteEpisode {
				t.Errorf("try_absolute_episode = %v, want %v", got.TryAbsoluteEpisode, *tc.want.TryAbsoluteEpisode)
			}
		})
	}
}

// TestBackfillIncludeYearFollowsContentType pins the backfill fallback to the
// same rule ensureDefaultMigrationSearchQueries uses — year for movies, none
// for series — so a config missing include_year lands on the current default
// rather than the retired mode-based one. An explicit legacy value still wins.
func TestBackfillIncludeYearFollowsContentType(t *testing.T) {
	cases := []struct {
		name     string
		query    SearchQueryConfig
		isSeries bool
		want     bool
	}{
		{name: "movie text", query: SearchQueryConfig{SearchMode: "text"}, want: true},
		{name: "movie id", query: SearchQueryConfig{SearchMode: "id"}, want: true},
		{name: "series text", query: SearchQueryConfig{SearchMode: "text"}, isSeries: true, want: false},
		{name: "series id", query: SearchQueryConfig{SearchMode: "id"}, isSeries: true, want: false},
		{
			name:     "series text keeps explicit legacy year",
			query:    SearchQueryConfig{SearchMode: "text", LegacyIncludeYearInTextSearch: ptrBool(true)},
			isSeries: true,
			want:     true,
		},
		{
			name:  "movie text keeps explicit legacy no-year",
			query: SearchQueryConfig{SearchMode: "text", LegacyIncludeYearInTextSearch: ptrBool(false)},
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := tc.query
			backfillLegacySearchQuerySettingsForQuery(&query, tc.isSeries)
			if query.IncludeYear == nil {
				t.Fatal("expected include_year to be backfilled")
			}
			if *query.IncludeYear != tc.want {
				t.Errorf("include_year = %v, want %v", *query.IncludeYear, tc.want)
			}
			if query.LegacyIncludeYearInTextSearch != nil {
				t.Error("expected the legacy year field to be cleared")
			}
		})
	}
}

func TestBackfillLegacySearchQuerySettings(t *testing.T) {
	cfg := &Config{
		MovieSearchQueries: []SearchQueryConfig{
			{Name: "DefaultMovieText", SearchMode: "text"},
			{Name: "DefaultMovieID", SearchMode: "id", LegacyIncludeYearInTextSearch: ptrBool(true), SearchTitleLanguage: "original"},
		},
		SeriesSearchQueries: []SearchQueryConfig{
			{Name: "DefaultTVText", SearchMode: "text", UseSeasonEpisodeParams: ptrBool(false)},
			{Name: "DefaultTVID", SearchMode: "id", LegacyIncludeYearInTextSearch: ptrBool(false)},
			{Name: "AnimeAbsolute", SearchMode: "text", SeriesSearchScope: "absolute"},
		},
	}

	if !cfg.backfillLegacySearchQuerySettings() {
		t.Fatal("expected legacy search query settings to be backfilled")
	}

	if cfg.MovieSearchQueries[0].IncludeYear == nil || !*cfg.MovieSearchQueries[0].IncludeYear {
		t.Fatal("expected DefaultMovieText year enabled after backfill")
	}
	if cfg.MovieSearchQueries[1].IncludeYear == nil || !*cfg.MovieSearchQueries[1].IncludeYear {
		t.Fatal("expected DefaultMovieID year enabled after backfill from legacy year field")
	}
	if got := cfg.MovieSearchQueries[1].SearchTitleLanguages; !reflect.DeepEqual(got, []string{"en-US", ""}) {
		t.Fatalf("expected DefaultMovieID title languages [en-US original] after backfill, got %#v", got)
	}
	if cfg.SeriesSearchQueries[0].IncludeYear == nil || *cfg.SeriesSearchQueries[0].IncludeYear {
		t.Fatal("expected DefaultTVText year disabled after backfill")
	}
	if cfg.SeriesSearchQueries[0].SeriesSearchScope != SeriesSearchScopeSeasonEpisode {
		t.Fatalf("expected DefaultTVText scope %q after legacy backfill, got %q", SeriesSearchScopeSeasonEpisode, cfg.SeriesSearchQueries[0].SeriesSearchScope)
	}
	if cfg.SeriesSearchQueries[1].IncludeYear == nil || *cfg.SeriesSearchQueries[1].IncludeYear {
		t.Fatal("expected DefaultTVID year disabled after backfill")
	}
	if got := cfg.SeriesSearchQueries[1].SearchTitleLanguages; !reflect.DeepEqual(got, []string{"en-US", ""}) {
		t.Fatalf("expected DefaultTVID title languages [en-US original] after backfill, got %#v", got)
	}
	// The retired absolute scope migrates to season_episode with the
	// absolute-episode supplement explicitly enabled.
	if cfg.SeriesSearchQueries[2].SeriesSearchScope != SeriesSearchScopeSeasonEpisode {
		t.Fatalf("expected AnimeAbsolute scope %q after backfill, got %q", SeriesSearchScopeSeasonEpisode, cfg.SeriesSearchQueries[2].SeriesSearchScope)
	}
	if cfg.SeriesSearchQueries[2].TryAbsoluteEpisode == nil || !*cfg.SeriesSearchQueries[2].TryAbsoluteEpisode {
		t.Fatalf("expected AnimeAbsolute to keep the absolute supplement enabled, got %#v", cfg.SeriesSearchQueries[2].TryAbsoluteEpisode)
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
		{name: "easynews", cfg: IndexerConfig{Type: "easynews"}, want: DefaultEasynewsIndexerTimeoutSeconds},
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

func TestValidateIndexerProxyURL(t *testing.T) {
	if err := ValidateIndexerProxyURL(""); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIndexerProxyURL("http://proxy:8888"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIndexerProxyURL("socks5://127.0.0.1:1080"); err == nil {
		t.Fatal("expected error for socks5 scheme")
	}
	if err := ValidateIndexerProxyURL("http://"); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestValidateIndexerProxyReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	if err := ValidateIndexerProxyReachable("http://" + ln.Addr().String()); err != nil {
		t.Fatalf("expected reachable proxy, got err: %v", err)
	}
}

func TestRedactProxyURLForAPI(t *testing.T) {
	got := RedactProxyURLForAPI("http://user:secret@proxy:8888")
	want := "http://proxy:8888"
	if got != want {
		t.Fatalf("RedactProxyURLForAPI = %q, want %q", got, want)
	}
}

func TestRedactDatabaseURLForAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"url form keeps host and database", "postgres://user:secret@db:5432/streamnzb", "postgres://db:5432/streamnzb"},
		{"url form with options", "postgresql://u:p@db:5432/snzb?sslmode=disable", "postgresql://db:5432/snzb?sslmode=disable"},
		// Keyword form hides the password nowhere url.Parse can reach, so the
		// whole string goes rather than leak it.
		{"keyword form is blanked", "host=db user=u password=secret dbname=snzb", ""},
		{"empty stays empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactDatabaseURLForAPI(tc.in)
			if got != tc.want {
				t.Fatalf("RedactDatabaseURLForAPI(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, "secret") {
				t.Fatalf("redacted form still contains the password: %q", got)
			}
		})
	}
}

// TestRedactForAPIStripsDatabasePassword guards the whole-config path the
// settings page actually fetches, not just the helper.
func TestRedactForAPIStripsDatabasePassword(t *testing.T) {
	cfg := &Config{
		DatabaseDriver: "postgres",
		DatabaseURL:    "postgres://user:secret@db:5432/streamnzb",
	}
	out := cfg.RedactForAPI()
	if strings.Contains(out.DatabaseURL, "secret") {
		t.Fatalf("RedactForAPI leaked the database password: %q", out.DatabaseURL)
	}
	if out.DatabaseDriver != "postgres" {
		t.Fatalf("DatabaseDriver should survive redaction, got %q", out.DatabaseDriver)
	}
}

// Provider API keys must never reach a non-admin viewer.
func TestRedactForAPIStripsMetadataKeys(t *testing.T) {
	cfg := &Config{TMDBAPIKey: "tmdb-token", TVDBAPIKey: "tvdb-key", SimklClientID: "simkl-id"}
	out := cfg.RedactForAPI()
	if out.TMDBAPIKey != "" || out.TVDBAPIKey != "" || out.SimklClientID != "" {
		t.Fatalf("RedactForAPI leaked metadata keys: tmdb=%q tvdb=%q simkl=%q", out.TMDBAPIKey, out.TVDBAPIKey, out.SimklClientID)
	}
}

func TestMigrateLegacyIndexersBackfillsEasynewsTimeout(t *testing.T) {
	cfg := &Config{
		Indexers: []IndexerConfig{
			{Name: "Easynews", Type: "easynews"},
			{Name: "SceneNZBs", Type: "newznab"},
		},
	}

	if !cfg.MigrateLegacyIndexers() {
		t.Fatalf("expected legacy indexers to be migrated")
	}
	if cfg.Indexers[0].TimeoutSeconds != DefaultEasynewsIndexerTimeoutSeconds {
		t.Fatalf("Easynews timeout = %d, want %d", cfg.Indexers[0].TimeoutSeconds, DefaultEasynewsIndexerTimeoutSeconds)
	}
	if cfg.Indexers[1].TimeoutSeconds != 0 {
		t.Fatalf("non-Easynews timeout = %d, want 0", cfg.Indexers[1].TimeoutSeconds)
	}
}

func TestMigrateLegacyIndexersKeepsExplicitEasynewsTimeout(t *testing.T) {
	cfg := &Config{
		Indexers: []IndexerConfig{
			{Name: "Easynews", Type: "easynews", TimeoutSeconds: DefaultInternalIndexerTimeoutSeconds, Enabled: ptrBool(true)},
		},
	}

	if cfg.MigrateLegacyIndexers() {
		t.Fatalf("did not expect explicit Easynews timeout to be migrated")
	}
	if cfg.Indexers[0].TimeoutSeconds != DefaultInternalIndexerTimeoutSeconds {
		t.Fatalf("Easynews timeout = %d, want %d", cfg.Indexers[0].TimeoutSeconds, DefaultInternalIndexerTimeoutSeconds)
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

func TestApplyEnvOverridesForcesAdminPasswordResetPrompt(t *testing.T) {
	t.Setenv(coreenv.AdminForcePasswordResetEnv, "true")
	o, keys := coreenv.ReadConfigOverrides()
	cfg := &Config{}

	ApplyEnvOverrides(cfg, o, keys)

	if !cfg.AdminMustChangePassword {
		t.Fatalf("AdminMustChangePassword = false, want true")
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
	if got := NormalizeSeriesSearchScope(cfg.SeriesSearchQueries[0].SeriesSearchScope); got != SeriesSearchScopeSeasonEpisode {
		t.Fatalf("expected DefaultTVText scope season_episode, got %q", got)
	}
	if cfg.SeriesSearchQueries[0].IncludeYear == nil || *cfg.SeriesSearchQueries[0].IncludeYear {
		t.Fatalf("expected DefaultTVText year disabled")
	}
	if got := NormalizeSeriesSearchScope(cfg.SeriesSearchQueries[1].SeriesSearchScope); got != SeriesSearchScopeSeasonEpisode {
		t.Fatalf("expected DefaultTVID scope season_episode, got %q", got)
	}
	if cfg.SeriesSearchQueries[1].IncludeYear == nil || *cfg.SeriesSearchQueries[1].IncludeYear {
		t.Fatalf("expected DefaultTVID year disabled")
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
	if stream.AutoAddProviders == nil || !*stream.AutoAddProviders {
		t.Fatalf("expected default stream auto add providers enabled, got %#v", stream.AutoAddProviders)
	}
	if stream.AutoAddIndexers == nil || !*stream.AutoAddIndexers {
		t.Fatalf("expected default stream auto add indexers enabled, got %#v", stream.AutoAddIndexers)
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

func TestSaveFileDoesNotPersistAvailNZBAPIKey(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	cfg := &Config{
		AddonPort:      7001,
		AvailNZBAPIKey: "secret-should-not-be-written",
	}
	if err := cfg.SaveFile(configPath); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(raw)
	if strings.Contains(content, "availnzb_api_key") {
		t.Fatalf("config.json should not contain availnzb_api_key, got: %s", content)
	}
	if strings.Contains(content, "secret-should-not-be-written") {
		t.Fatalf("config.json should not contain AvailNZBAPIKey value")
	}
}

func TestConfigNormalizeSpeculativePreProbingCount(t *testing.T) {
	cfg := &Config{SpeculativePreProbingMaxAttempts: 6}
	if got := cfg.EffectiveSpeculativePreProbingMaxAttempts(); got != 5 {
		t.Fatalf("expected count normalized to 5, got %d", got)
	}

	cfgNegative := &Config{SpeculativePreProbingMaxAttempts: -1}
	if got := cfgNegative.EffectiveSpeculativePreProbingMaxAttempts(); got != 0 {
		t.Fatalf("expected count normalized to 0, got %d", got)
	}

	cfgDefault := &Config{}
	if got := cfgDefault.EffectiveSpeculativePreProbingMaxAttempts(); got != 0 {
		t.Fatalf("expected count 0 for zero-value count field, got %d", got)
	}

	cfgNil := (*Config)(nil)
	if got := cfgNil.EffectiveSpeculativePreProbingMaxAttempts(); got != DefaultSpeculativePreProbingMaxAttempts {
		t.Fatalf("expected default count %d for nil config, got %d", DefaultSpeculativePreProbingMaxAttempts, got)
	}
}

func TestConfigBootstrapsDefaultFilterProfile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("STREAMNZB_DATA_DIR", tmpDir)
	t.Setenv("LOCALAPPDATA", tmpDir)

	res, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(res.FilterProfiles) != 1 {
		t.Fatalf("expected 1 default filter profile, got %d", len(res.FilterProfiles))
	}

	p := res.FilterProfiles[0]
	t.Logf("bootstrapped profile: %+v", p)
	if p.Name != DefaultFilterProfileName {
		t.Errorf("expected default profile name %q, got %q", DefaultFilterProfileName, p.Name)
	}
	if p.Preset != DefaultPreset {
		t.Errorf("bootstrapped preset = %q, want %q", p.Preset, DefaultPreset)
	}
	// A profile is a preset plus rules; a fresh one carries no spec of its own
	// and nothing to migrate.
	if p.Ranking != nil {
		t.Error("expected the bootstrapped profile to carry no hand-tuned ranking spec")
	}
	if len(p.Rules) != 0 {
		t.Errorf("expected no rules on a fresh profile, got %d", len(p.Rules))
	}
}

// The baseline behind every preset is what all installs start from, so it
// should reject only the CAM-class rips and adult releases and let the rest
// through, demoted rather than dropped.
func TestPresetBaselineBlocksOnlyTrashAndAdult(t *testing.T) {
	profile := PresetSpec(PresetUHD)

	if !profile.Options.RemoveAdult {
		t.Error("adult content should be blocked by default")
	}
	// Leaked copies and deleted-scene reels carry no source of their own, so
	// the per-attribute policies below cannot reach them.
	if !profile.Options.RemoveTrash {
		t.Error("garbage titles should be removed by default")
	}
	if profile.Options.MinRank != noScoreFloor {
		t.Errorf("MinRank = %d, want the score floor disabled (%d)", profile.Options.MinRank, noScoreFloor)
	}

	// Exhaustive in both directions: the SD tiers stay off at every ceiling, so
	// a change there has to fail here rather than quietly widen what the addon
	// streams.
	wantResolutions := map[rank.Resolution]bool{
		rank.Res2160p: true, rank.Res1440p: true, rank.Res1080p: true,
		rank.Res720p: true, rank.ResUnknown: true,
		rank.Res576p: false, rank.Res480p: false,
		rank.Res360p: false, rank.Res240p: false,
	}
	if !reflect.DeepEqual(profile.Resolutions, wantResolutions) {
		t.Errorf("Resolutions = %v, want %v", profile.Resolutions, wantResolutions)
	}

	blocked := map[rank.Attr]bool{}
	for _, attr := range defaultBlockedAttrs {
		blocked[attr] = true
	}
	for attr, policy := range profile.Attributes {
		if blocked[attr] && policy.Fetch {
			t.Errorf("attribute %s should be blocked", attr)
		}
		if !blocked[attr] && !policy.Fetch {
			t.Errorf("attribute %s should be allowed", attr)
		}
	}
	for _, attr := range defaultBlockedAttrs {
		if _, ok := profile.Attributes[attr]; !ok {
			t.Errorf("attribute %s should be written out, not left to the baseline", attr)
		}
	}
}

func TestResolveConfigPathAndLoadWithPath(t *testing.T) {
	tempDir := t.TempDir()
	customFile := filepath.Join(tempDir, "custom.json")

	// 1. Explicit file path
	resolved := ResolveConfigPath(customFile)
	if resolved != customFile {
		t.Errorf("ResolveConfigPath(%q) = %q, want %q", customFile, resolved, customFile)
	}

	// 2. Explicit directory path
	resolvedDir := ResolveConfigPath(tempDir)
	wantDirFile := filepath.Join(tempDir, "config.json")
	if resolvedDir != wantDirFile {
		t.Errorf("ResolveConfigPath(%q) = %q, want %q", tempDir, resolvedDir, wantDirFile)
	}

	// 3. ENV var priority when explicit path is empty
	t.Setenv("CONFIG_PATH", customFile)
	resolvedEnv := ResolveConfigPath("")
	if resolvedEnv != customFile {
		t.Errorf("ResolveConfigPath with CONFIG_PATH = %q, want %q", resolvedEnv, customFile)
	}

	// 4. LoadWithPath creates and loads custom config
	cfg, err := LoadWithPath(customFile)
	if err != nil {
		t.Fatalf("LoadWithPath(%q) error = %v", customFile, err)
	}
	if cfg.LoadedPath != customFile {
		t.Errorf("cfg.LoadedPath = %q, want %q", cfg.LoadedPath, customFile)
	}
}

// TestEnvFieldCopiersCoverEveryKey pins the coverage of the env-override
// table. ApplyEnvOverrides and CopyEnvOverridesFrom used to be two
// hand-maintained lists over the same keys and had already drifted apart; both
// now run off envFieldCopiers, so a key without an entry is silently ignored.
func TestEnvFieldCopiersCoverEveryKey(t *testing.T) {
	// KeyAvailNZBURL is intentionally absent: Config.AvailNZBURL is json:"-"
	// and is supplied directly by main, never carried across a config save.
	skip := map[string]bool{env.KeyAvailNZBURL: true}

	all := []string{
		env.KeyAddonPort, env.KeyAddonBaseURL, env.KeyLogLevel, env.KeyKeepLogFiles,
		env.KeyProxyPort, env.KeyProxyHost, env.KeyProxyEnabled, env.KeyProxyAuthUser,
		env.KeyProxyAuthPass, env.KeyNewznabEnabled, env.KeyNewznabAPIKey,
		env.KeyProviders, env.KeyIndexers, env.KeyAvailNZBURL,
		env.KeyAvailNZBAPIKey, env.KeyTMDBAPIKey, env.KeyTVDBAPIKey, env.KeySimklClientID,
		env.KeyIndexerQueryHeader, env.KeyIndexerGrabHeader, env.KeyProviderHeader,
		env.KeyAdminUsername, env.KeyAdminMustChangePwd,
		env.KeyDatabaseDriver, env.KeyDatabaseURL, env.KeyMetadataEnabled,
	}
	for _, k := range all {
		_, ok := envFieldCopiers[k]
		if skip[k] {
			if ok {
				t.Errorf("key %q is in the skip list but has a copier", k)
			}
			continue
		}
		if !ok {
			t.Errorf("env key %q has no entry in envFieldCopiers", k)
		}
	}
	for k := range envFieldCopiers {
		found := false
		for _, known := range all {
			if k == known {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("envFieldCopiers has entry %q that is not a declared env key", k)
		}
	}
}

// TestCopyEnvOverridesFromDeepCopiesProviders guards against the two configs
// sharing Priority/Enabled/Backup/PipelineDepth pointer storage after a copy.
func TestCopyEnvOverridesFromDeepCopiesProviders(t *testing.T) {
	pri := 3
	on := true
	backup := true
	depth := 4
	src := &Config{Providers: []Provider{{Name: "p", Priority: &pri, Enabled: &on, Backup: &backup, PipelineDepth: &depth}}}
	dst := &Config{}
	copyEnvKeys(dst, src, []string{env.KeyProviders})

	if len(dst.Providers) != 1 || dst.Providers[0].Priority == nil {
		t.Fatalf("provider not copied: %#v", dst.Providers)
	}
	if dst.Providers[0].Priority == src.Providers[0].Priority {
		t.Error("Priority pointer is shared between configs")
	}
	if dst.Providers[0].Enabled == src.Providers[0].Enabled {
		t.Error("Enabled pointer is shared between configs")
	}
	if dst.Providers[0].Backup == nil || *dst.Providers[0].Backup != true {
		t.Errorf("Backup not copied: %#v", dst.Providers[0].Backup)
	}
	if dst.Providers[0].Backup == src.Providers[0].Backup {
		t.Error("Backup pointer is shared between configs")
	}
	if dst.Providers[0].PipelineDepth == nil || *dst.Providers[0].PipelineDepth != depth {
		t.Errorf("PipelineDepth not copied: %#v", dst.Providers[0].PipelineDepth)
	}
	if dst.Providers[0].PipelineDepth == src.Providers[0].PipelineDepth {
		t.Error("PipelineDepth pointer is shared between configs")
	}
}

// TestProxyDefaultsOnlyApplyToFreshInstalls pins both halves of the fix for
// issue #192: a new install must land on a port it can bind without root and
// must not open an unauthenticated relay nobody asked for, while an existing
// config keeps whatever it already had — its downloader is pointed at it.
func TestProxyDefaultsOnlyApplyToFreshInstalls(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		fresh := filepath.Join(t.TempDir(), "config.json")

		cfg, err := LoadWithPath(fresh)
		if err != nil {
			t.Fatalf("LoadWithPath(%q) error = %v", fresh, err)
		}
		if cfg.ProxyPort != DefaultProxyPort {
			t.Errorf("ProxyPort = %d, want %d (unprivileged)", cfg.ProxyPort, DefaultProxyPort)
		}
		if cfg.ProxyEnabled {
			t.Error("ProxyEnabled = true, want false on a fresh install")
		}
	})

	t.Run("existing config", func(t *testing.T) {
		existing := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(existing, []byte(`{"proxy_enabled":true,"proxy_port":119}`), 0644); err != nil {
			t.Fatalf("writing config: %v", err)
		}

		cfg, err := LoadWithPath(existing)
		if err != nil {
			t.Fatalf("LoadWithPath(%q) error = %v", existing, err)
		}
		if cfg.ProxyPort != 119 {
			t.Errorf("ProxyPort = %d, want 119 carried over from the stored config", cfg.ProxyPort)
		}
		if !cfg.ProxyEnabled {
			t.Error("ProxyEnabled = false, want the stored true to survive the default change")
		}
	})
}

func TestNormalizeAvailNZBModeIsOptIn(t *testing.T) {
	cases := map[string]string{
		"":            "off",
		"   ":         "off",
		"off":         "off",
		"disabled":    "off",
		"nonsense":    "off",
		"on":          "on",
		"  ON  ":      "on",
		"full":        "on",
		"status_only": "on",
	}
	for mode, want := range cases {
		if got := NormalizeAvailNZBMode(mode); got != want {
			t.Fatalf("NormalizeAvailNZBMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

// A config written before AvailNZB became opt-in has no availnzb_mode key at
// all; it must land on "off" so an upgrade never registers a key unasked. An
// install that already enabled it keeps its setting.
func TestLoadWithPathAvailNZBModeDefaultsOff(t *testing.T) {
	load := func(t *testing.T, body string) *Config {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte(body), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		cfg, err := LoadWithPath(configPath)
		if err != nil {
			t.Fatalf("LoadWithPath: %v", err)
		}
		return cfg
	}

	if got := load(t, `{"addon_port":7000}`).AvailNZBMode; got != "off" {
		t.Fatalf("AvailNZBMode without the key = %q, want %q", got, "off")
	}
	if got := load(t, `{"addon_port":7000,"availnzb_mode":"on"}`).AvailNZBMode; got != "on" {
		t.Fatalf("AvailNZBMode of an enabled install = %q, want %q", got, "on")
	}
}

// A stream's token is the credential its device authenticates with, so the
// non-admin view of the config must not carry anybody's. This also pins that
// redaction leaves the live config alone: RedactForAPI starts from a shallow
// copy, and Streams holds pointers, so blanking a token without cloning would
// erase it from the running server.
func TestRedactForAPIStripsStreamTokensWithoutTouchingTheLiveConfig(t *testing.T) {
	cfg := &Config{
		Streams: map[string]*StreamEntry{
			"living-room": {Username: "living-room", Token: "living-room-secret", AddonName: "Lounge"},
			"phone":       {Username: "phone", Token: "phone-secret"},
		},
	}

	out := cfg.RedactForAPI()

	for name, entry := range out.Streams {
		if entry.Token != "" {
			t.Fatalf("RedactForAPI leaked the token for stream %q: %q", name, entry.Token)
		}
	}
	if out.Streams["living-room"].AddonName != "Lounge" {
		t.Fatalf("non-secret stream fields should survive redaction, got %#v", out.Streams["living-room"])
	}
	if cfg.Streams["living-room"].Token != "living-room-secret" || cfg.Streams["phone"].Token != "phone-secret" {
		t.Fatalf("RedactForAPI mutated the live config: %#v", cfg.Streams)
	}
}

// A nil entry is not something the loader produces, but the map is decoded
// straight from user-editable JSON, where `"phone": null` is legal.
func TestRedactForAPIToleratesANilStreamEntry(t *testing.T) {
	cfg := &Config{Streams: map[string]*StreamEntry{"phone": nil}}
	out := cfg.RedactForAPI()
	if entry, ok := out.Streams["phone"]; !ok || entry != nil {
		t.Fatalf("expected the nil entry to survive as nil, got %#v", out.Streams)
	}
}
