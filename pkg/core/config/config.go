package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
)

const (
	defaultAdminPasswordHash                = "8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918"
	DefaultInternalIndexerTimeoutSeconds    = 5
	DefaultAggregatorIndexerTimeoutSeconds  = 10
	DefaultEasynewsIndexerTimeoutSeconds    = 15
	DefaultPlaybackStartupTimeoutSeconds    = 5
	MaxPlaybackStartupTimeoutSeconds        = 60
	DefaultSessionTTLMinutes                = 30
	MinSessionTTLMinutes                    = 1
	MaxSessionTTLMinutes                    = 1440
	DefaultSessionPostPlaybackTTLMinutes    = 240
	MinSessionPostPlaybackTTLMinutes        = 1
	MaxSessionPostPlaybackTTLMinutes        = 1440
	DefaultSpeculativePreProbingCount       = 3
	DefaultSpeculativePreProbingMaxAttempts = 3
	CurrentConfigVersion                    = 2
	StreamModelConfigVersion                = 2
	defaultMigratedStreamID                 = "default"
	SeriesSearchScopeSeasonEpisode          = "season_episode"
	SeriesSearchScopeSeason                 = "season"
	SeriesSearchScopeNone                   = "none"
	// legacySeriesSearchScopeAbsolute was a dedicated scope that queried anime
	// by absolute episode number. Absolute querying is now a per-query
	// supplement (TryAbsoluteEpisode), so the scope migrates to season_episode
	// with the supplement enabled.
	legacySeriesSearchScopeAbsolute     = "absolute"
	legacySeriesSearchScopeEpisodeParam = "episode_param"
	legacySeriesSearchScopeEpisodeQuery = "episode_query"
	legacySeriesSearchScopeSeasonParam  = "season_param"
	legacySeriesSearchScopeSeasonQuery  = "season_query"
)

type Provider struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	UseSSL      bool   `json:"use_ssl"`
	Priority    *int   `json:"priority,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

func ptrBool(b bool) *bool { return &b }

func IsAggregatorIndexerType(indexerType string) bool {
	switch strings.ToLower(strings.TrimSpace(indexerType)) {
	case "aggregator", "nzbhydra", "prowlarr":
		return true
	default:
		return false
	}
}

type IndexerSearchConfig struct {
	SearchResultLimit          int     `json:"search_result_limit,omitempty"`
	EnableSeriesSeasonSearch   *bool   `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool   `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool   `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        *string `json:"search_title_language,omitempty"`
	MovieCategories            *string `json:"movie_categories,omitempty"`
	TVCategories               *string `json:"tv_categories,omitempty"`
	DisableIdSearch            *bool   `json:"disable_id_search,omitempty"`
	DisableStringSearch        *bool   `json:"disable_string_search,omitempty"`
	ContentScope               *string `json:"content_scope,omitempty"`
}

// Indexer content scopes: which requests an indexer participates in.
const (
	IndexerContentScopeAll      = ""
	IndexerContentScopeAnime    = "anime"
	IndexerContentScopeNonAnime = "non_anime"
)

// NormalizeIndexerContentScope maps a raw content scope to "", "anime" or
// "non_anime"; unknown values fall back to all content.
func NormalizeIndexerContentScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case IndexerContentScopeAnime:
		return IndexerContentScopeAnime
	case IndexerContentScopeNonAnime:
		return IndexerContentScopeNonAnime
	}
	return IndexerContentScopeAll
}

type SearchQueryConfig struct {
	Name                   string `json:"name"`
	SearchMode             string `json:"search_mode,omitempty"`
	SearchResultLimit      int    `json:"search_result_limit,omitempty"`
	IncludeYear            *bool  `json:"include_year,omitempty"`
	UseSeasonEpisodeParams *bool  `json:"use_season_episode_params,omitempty"`
	SeriesSearchScope      string `json:"series_search_scope,omitempty"`
	// TryAbsoluteEpisode supplements anime series searches with absolute-
	// numbered text queries ("One Piece 63" for S02E02) and lets validation
	// accept absolute-numbered releases. Nil defaults to enabled; series only.
	TryAbsoluteEpisode            *bool    `json:"try_absolute_episode,omitempty"`
	EnableSeriesSeasonSearch      *bool    `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch    *bool    `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch        *bool    `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage           string   `json:"search_title_language,omitempty"`
	SearchTitleLanguages          []string `json:"search_title_languages,omitempty"`
	LegacyIncludeYearInTextSearch *bool    `json:"include_year_in_text_search,omitempty"`
	MovieCategories               string   `json:"movie_categories,omitempty"`
	TVCategories                  string   `json:"tv_categories,omitempty"`
	DisableIdSearch               *bool    `json:"disable_id_search,omitempty"`
	DisableStringSearch           *bool    `json:"disable_string_search,omitempty"`
}

// FilterProfileConfig is one named filtering/ranking profile. Ranking holds
// jhin's profile verbatim and is what actually decides fetch/reject and score;
// the fields below it are the pre-jhin schema, kept so existing configs keep
// working and read-only after migration (see Synthesize).
type FilterProfileConfig struct {
	Name string `json:"name"`

	// Ranking is jhin's rank.Profile. Nil on configs written before the jhin
	// migration, in which case it is synthesized from the legacy fields on load.
	Ranking *rank.Profile `json:"ranking,omitempty"`

	// LibraryScoreBonus is the ranking bonus added to cached library releases
	// for this profile. Nil uses the default (500); negative disables the bonus.
	LibraryScoreBonus *int `json:"library_score_bonus,omitempty"`

	// Limits bounds releases by NZB attributes (size, age, grabs), keyed by
	// content kind. The "default" entry applies to every kind; entries for
	// "movie", "series", "anime_movie" and "anime_show" override it field by
	// field, non-zero fields winning.
	Limits map[string]*LimitsConfig `json:"limits,omitempty"`

	// BlockPassworded rejects releases the indexer flags as password
	// protected. Nil defaults to true; indexers that never report the flag
	// are unaffected.
	BlockPassworded *bool `json:"block_passworded,omitempty"`

	AllowedResolutions []string `json:"allowed_resolutions,omitempty"`
	BlockedResolutions []string `json:"blocked_resolutions,omitempty"`
	AllowedQualities   []string `json:"allowed_qualities,omitempty"`
	BlockedQualities   []string `json:"blocked_qualities,omitempty"`
	AllowedCodecs      []string `json:"allowed_codecs,omitempty"`
	BlockedCodecs      []string `json:"blocked_codecs,omitempty"`
	RequireHDR         *bool    `json:"require_hdr,omitempty"`
	AllowedHDRs        []string `json:"allowed_hdrs,omitempty"`
	BlockedHDRs        []string `json:"blocked_hdrs,omitempty"`
	RequiredKeywords   []string `json:"required_keywords,omitempty"`
	ExcludedKeywords   []string `json:"excluded_keywords,omitempty"`
	// AllowedLanguages filters releases to those whose parsed Languages include at least
	// one of the listed codes (e.g. "en", "fi"). Alias words like "nordic" are expanded
	// by the parser, so a release titled "...NORDIC..." will match "da","fi","no","sv".
	AllowedLanguages []string `json:"allowed_languages,omitempty"`
	// BlockedLanguages drops releases whose parsed Languages include any of the listed codes.
	BlockedLanguages []string `json:"blocked_languages,omitempty"`
	// PreferredLanguages is used by the "language" sort criterion: releases that include
	// any preferred language rank higher than releases that do not.
	PreferredLanguages []string `json:"preferred_languages,omitempty"`
}

type IndexerConfig struct {
	Name                   string `json:"name"`
	URL                    string `json:"url"`
	APIKey                 string `json:"api_key"`
	APIPath                string `json:"api_path"`
	Type                   string `json:"type"`
	APIHitsDay             int    `json:"api_hits_day"`
	DownloadsDay           int    `json:"downloads_day"`
	RateLimitRPS           int    `json:"rate_limit_rps,omitempty"`
	TimeoutSeconds         int    `json:"timeout_seconds,omitempty"`
	SearchResultsCacheTime int    `json:"search_results_cache_time,omitempty"`
	Enabled                *bool  `json:"enabled,omitempty"`

	Username string `json:"username"`
	Password string `json:"password"`

	MovieCategories            string `json:"movie_categories,omitempty"`
	TVCategories               string `json:"tv_categories,omitempty"`
	SearchResultLimit          int    `json:"search_result_limit,omitempty"`
	EnableSeriesSeasonSearch   *bool  `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool  `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool  `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        string `json:"search_title_language,omitempty"`
	DisableIdSearch            *bool  `json:"disable_id_search,omitempty"`
	DisableStringSearch        *bool  `json:"disable_string_search,omitempty"`

	// ContentScope restricts which requests this indexer participates in:
	// "anime" queries it only for anime content, "non_anime" for everything
	// except anime, ""/"all" for everything. Anime detection: Kitsu-addressed
	// requests are anime by definition, otherwise TMDB metadata decides
	// (animation not originally in English).
	ContentScope string `json:"content_scope,omitempty"`

	// VerifyTLS enables TLS certificate verification for this indexer.
	// Defaults to false (certificates NOT verified) to keep the historical
	// behavior for self-signed private indexers.
	VerifyTLS bool `json:"verify_tls,omitempty"`

	// ProxyURL is an optional HTTP or HTTPS proxy for this indexer (http://host:port or https://...).
	// When empty, HTTP_PROXY / HTTPS_PROXY / NO_PROXY apply via the default proxy resolution.
	ProxyURL string `json:"proxy_url,omitempty"`

	// QueryHeader overrides the global indexer_query_header for search and capability requests to this indexer.
	// Some indexers (e.g. SceneNZBs) gate content by User-Agent; leave empty to use the global setting.
	QueryHeader string `json:"query_header,omitempty"`
	// GrabHeader overrides the global indexer_grab_header for NZB download requests to this indexer.
	// Some indexers (e.g. SceneNZBs) return different NZBs depending on the downloader UA; leave empty to use the global setting.
	GrabHeader string `json:"grab_header,omitempty"`
}

// ValidateIndexerProxyURL returns nil if raw is empty or a valid http(s) proxy URL.
func ValidateIndexerProxyURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("proxy URL scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("proxy URL must include a host")
	}
	return nil
}

// ValidateIndexerProxyReachable performs a lightweight TCP dial check to ensure
// the proxy endpoint is reachable.
func ValidateIndexerProxyReachable(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return fmt.Errorf("proxy URL must include a host")
	}
	port := strings.TrimSpace(u.Port())
	if port == "" {
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("proxy is unreachable at %s: %w", addr, err)
	}
	_ = conn.Close()
	return nil
}

// RedactProxyURLForAPI strips userinfo from a proxy URL for non-admin API responses.
func RedactProxyURLForAPI(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

// RedactDatabaseURLForAPI strips the credentials from a Postgres DSN for
// display. URL-form DSNs keep their host and database so the settings page can
// show what is configured; keyword-form DSNs (host=... password=...) are
// blanked wholesale, because there the secret is not confined to a userinfo
// section url.Parse can remove.
func RedactDatabaseURLForAPI(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "postgres://") && !strings.HasPrefix(s, "postgresql://") {
		return ""
	}
	return RedactProxyURLForAPI(s)
}

func (ic IndexerConfig) EffectiveTimeoutSeconds() int {
	if ic.TimeoutSeconds > 0 {
		return ic.TimeoutSeconds
	}
	if strings.EqualFold(strings.TrimSpace(ic.Type), "easynews") {
		return DefaultEasynewsIndexerTimeoutSeconds
	}
	if IsAggregatorIndexerType(ic.Type) {
		return DefaultAggregatorIndexerTimeoutSeconds
	}
	return DefaultInternalIndexerTimeoutSeconds
}

func (ic IndexerConfig) EffectiveTimeout() time.Duration {
	return time.Duration(ic.EffectiveTimeoutSeconds()) * time.Second
}

func normalizePlaybackStartupTimeoutSeconds(timeout int) int {
	if timeout < 1 || timeout > MaxPlaybackStartupTimeoutSeconds {
		return DefaultPlaybackStartupTimeoutSeconds
	}
	return timeout
}

func (c *Config) EffectivePlaybackStartupTimeoutSeconds() int {
	if c != nil {
		return normalizePlaybackStartupTimeoutSeconds(c.PlaybackStartupTimeoutSeconds)
	}
	return DefaultPlaybackStartupTimeoutSeconds
}

func (c *Config) EffectivePlaybackStartupTimeout() time.Duration {
	return time.Duration(c.EffectivePlaybackStartupTimeoutSeconds()) * time.Second
}

func (c *Config) EffectiveSpeculativePreProbingMaxAttempts() int {
	if c != nil {
		if c.SpeculativePreProbingMaxAttempts != 0 {
			return normalizeSpeculativePreProbingCount(c.SpeculativePreProbingMaxAttempts)
		}
		return normalizeSpeculativePreProbingCount(c.SpeculativePreProbingCount)
	}
	return DefaultSpeculativePreProbingMaxAttempts
}

func normalizeSessionTTLMinutes(ttl int) int {
	if ttl < MinSessionTTLMinutes || ttl > MaxSessionTTLMinutes {
		return DefaultSessionTTLMinutes
	}
	return ttl
}

func normalizeSessionPostPlaybackTTLMinutes(ttl int) int {
	if ttl < MinSessionPostPlaybackTTLMinutes || ttl > MaxSessionPostPlaybackTTLMinutes {
		return DefaultSessionPostPlaybackTTLMinutes
	}
	return ttl
}

func normalizeSpeculativePreProbingCount(count int) int {
	if count < 0 {
		return 0
	}
	if count > 5 {
		return 5
	}
	return count
}

func (c *Config) EffectiveSessionTTLSeconds() int {
	if c != nil {
		return normalizeSessionTTLMinutes(c.SessionTTLMinutes) * 60
	}
	return DefaultSessionTTLMinutes * 60
}

func (c *Config) EffectiveSessionPostPlaybackTTLSeconds() int {
	if c != nil {
		return normalizeSessionPostPlaybackTTLMinutes(c.SessionPostPlaybackTTLMinutes) * 60
	}
	return DefaultSessionPostPlaybackTTLMinutes * 60
}

const (
	DefaultLibrarySearchMode     = "combine"
	DefaultLibraryMaxItems       = 5000
	DefaultLibraryMaxSizeMB      = 250
	DefaultLibraryVerifyTTLHours = 168 // 7 days
	DefaultBadReleaseTTLHours    = 336 // 14 days
)

func (c *Config) EffectiveLibrarySearchMode() string {
	if c != nil && strings.TrimSpace(c.LibrarySearchMode) != "" {
		mode := strings.ToLower(strings.TrimSpace(c.LibrarySearchMode))
		switch mode {
		case "library_first", "combine", "fallback_only", "disabled":
			return mode
		}
	}
	return DefaultLibrarySearchMode
}

func (c *Config) EffectiveLibraryMaxItems() int {
	if c != nil && c.LibraryMaxItems > 0 {
		return c.LibraryMaxItems
	}
	return DefaultLibraryMaxItems
}

func (c *Config) EffectiveLibraryMaxSizeMB() int {
	if c != nil && c.LibraryMaxSizeMB > 0 {
		return c.LibraryMaxSizeMB
	}
	return DefaultLibraryMaxSizeMB
}

func (c *Config) EffectiveLibraryAutoSave() bool {
	if c != nil && c.LibraryAutoSave != nil {
		return *c.LibraryAutoSave
	}
	return true
}

// EffectiveLibraryVerifyTTL is the age past which a cached release is re-STATed
// by the background freshness sweep. Returns 0 when disabled (config < 0).
func (c *Config) EffectiveLibraryVerifyTTL() time.Duration {
	hours := DefaultLibraryVerifyTTLHours
	if c != nil && c.LibraryVerifyTTLHours != 0 {
		if c.LibraryVerifyTTLHours < 0 {
			return 0 // disabled
		}
		hours = c.LibraryVerifyTTLHours
	}
	return time.Duration(hours) * time.Hour
}

// EffectiveBadReleaseTTL is how long a definitive bad-release verdict (article
// hole / corruption) keeps that release filtered from search results before it
// is eligible for retry. Returns 0 when disabled (config < 0).
func (c *Config) EffectiveBadReleaseTTL() time.Duration {
	hours := DefaultBadReleaseTTLHours
	if c != nil && c.BadReleaseTTLHours != 0 {
		if c.BadReleaseTTLHours < 0 {
			return 0 // disabled
		}
		hours = c.BadReleaseTTLHours
	}
	return time.Duration(hours) * time.Hour
}

func (c *Config) IsErrorVideoMuted() bool {
	if c != nil && c.MuteErrorVideo != nil {
		return *c.MuteErrorVideo
	}
	return false
}

func NormalizeAvailNZBMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "full", "status_only", "on":
		return "on"
	case "disabled", "off":
		return "off"
	default:
		return "on"
	}
}

func NormalizeSearchTitleLanguage(language string) string {
	trimmed := strings.TrimSpace(language)
	if strings.EqualFold(trimmed, "original") {
		return ""
	}
	return trimmed
}

func NormalizeSearchTitleLanguages(languages []string) []string {
	if len(languages) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(languages))
	seen := make(map[string]bool, len(languages))
	for _, language := range languages {
		value := NormalizeSearchTitleLanguage(language)
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func DefaultIDSearchTitleLanguages() []string {
	return []string{"en-US", ""}
}

func NormalizeSeriesSearchScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case SeriesSearchScopeSeasonEpisode,
		SeriesSearchScopeSeason,
		SeriesSearchScopeNone:
		return strings.ToLower(strings.TrimSpace(scope))
	case legacySeriesSearchScopeAbsolute,
		legacySeriesSearchScopeEpisodeParam,
		legacySeriesSearchScopeEpisodeQuery:
		return SeriesSearchScopeSeasonEpisode
	case legacySeriesSearchScopeSeasonParam,
		legacySeriesSearchScopeSeasonQuery:
		return SeriesSearchScopeSeason
	}
	return SeriesSearchScopeSeasonEpisode
}

func normalizeSeriesSearchScopeFromLegacy(scope string, useSeasonEpisodeParams *bool) string {
	normalizedScope := strings.ToLower(strings.TrimSpace(scope))
	if normalizedScope != "" {
		return NormalizeSeriesSearchScope(normalizedScope)
	}
	if useSeasonEpisodeParams != nil {
		return SeriesSearchScopeSeasonEpisode
	}
	return normalizedScope
}

func SeriesSearchScopeUsesSeasonParams(scope, searchMode string) bool {
	if !strings.EqualFold(strings.TrimSpace(searchMode), "id") {
		return false
	}
	switch NormalizeSeriesSearchScope(scope) {
	case SeriesSearchScopeSeasonEpisode, SeriesSearchScopeSeason:
		return true
	default:
		return false
	}
}

func SeriesSearchScopeSearchTarget(scope, searchMode, season, episode string) (string, string) {
	if !SeriesSearchScopeUsesSeasonParams(scope, searchMode) {
		return "", ""
	}
	switch NormalizeSeriesSearchScope(scope) {
	case SeriesSearchScopeSeasonEpisode:
		return strings.TrimSpace(season), strings.TrimSpace(episode)
	case SeriesSearchScopeSeason:
		return strings.TrimSpace(season), ""
	default:
		return "", ""
	}
}

func SeriesSearchScopeRequiresValidation(scope string) bool {
	switch NormalizeSeriesSearchScope(scope) {
	case SeriesSearchScopeSeason, SeriesSearchScopeNone:
		return true
	default:
		return false
	}
}

type Config struct {
	ConfigVersion int `json:"config_version,omitempty"`

	Indexers []IndexerConfig `json:"indexers"`

	AddonPort          int    `json:"addon_port"`
	AddonBaseURL       string `json:"addon_base_url"`
	LogLevel           string `json:"log_level"`
	VerboseNNTPLogging bool   `json:"verbose_nntp_logging,omitempty"`
	// SearchDebugStream prepends a non-playable-looking debug row to every
	// stream response summarizing the search funnel (per-indexer timings and
	// filter drops). Selecting it plays the top real result via redirect, so
	// autoplay clients lose nothing.
	SearchDebugStream bool `json:"search_debug_stream,omitempty"`

	AdminUsername           string `json:"admin_username"`
	AdminPasswordHash       string `json:"admin_password_hash"`
	AdminMustChangePassword bool   `json:"admin_must_change_password"`
	AdminToken              string `json:"admin_token"`

	Providers []Provider `json:"providers"`

	ProxyPort     int    `json:"proxy_port"`
	ProxyHost     string `json:"proxy_host"`
	ProxyEnabled  bool   `json:"proxy_enabled"`
	ProxyAuthUser string `json:"proxy_auth_user"`
	ProxyAuthPass string `json:"proxy_auth_pass"`

	AvailNZBURL    string `json:"-"`
	AvailNZBAPIKey string `json:"-"`

	TMDBAPIKey         string `json:"tmdb_api_key,omitempty"`
	IndexerQueryHeader string `json:"indexer_query_header,omitempty"`
	IndexerGrabHeader  string `json:"indexer_grab_header,omitempty"`
	ProviderHeader     string `json:"provider_header,omitempty"`
	IndexerProxyURL    string `json:"indexer_proxy_url,omitempty"`

	TVDBAPIKey string `json:"tvdb_api_key,omitempty"`

	// DatabaseDriver selects the persistence backend: "sqlite" (default,
	// <data dir>/streamnzb.db) or "postgres". DatabaseURL is the Postgres
	// connection string. Changing either is applied by a config reload —
	// see initialization.ReloadDatabase — not only at startup.
	DatabaseDriver string `json:"database_driver,omitempty"`
	DatabaseURL    string `json:"database_url,omitempty"`
	// DatabaseSkipMigration suppresses carrying data across when the backend
	// changes. The migration copies in whichever direction the switch goes and
	// leaves the database being left untouched, so the default is to run it.
	DatabaseSkipMigration bool `json:"database_skip_migration,omitempty"`

	Streams map[string]*StreamEntry `json:"streams,omitempty"`

	MovieSearchQueries  []SearchQueryConfig   `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []SearchQueryConfig   `json:"series_search_queries,omitempty"`
	FilterProfiles      []FilterProfileConfig `json:"filter_profiles,omitempty"`
	// MetadataProfiles are the named metadata profiles streams bind by name.
	// Deliberately not omitempty: nil means "never migrated" (the one-shot
	// legacy conversion runs), while a persisted [] means the user deleted
	// every profile and must stay that way.
	MetadataProfiles []MetadataProfileConfig `json:"metadata_profiles"`

	// MemoryLimitMB sets a soft limit on total Go heap (runtime/debug.SetMemoryLimit). 0 = no limit.
	// When set, segment cache is automatically 80% of this limit.
	// Use this to stop memory climbing; the runtime will GC more aggressively to stay under the limit.
	MemoryLimitMB int `json:"memory_limit_mb,omitempty"`

	// KeepLogFiles is how many log files to keep (current streamnzb.log + rotated streamnzb-*.log). Default 9.
	// Where those files live is set by -log-file / LOG_PATH — see env.LogPath.
	KeepLogFiles int `json:"keep_log_files,omitempty"`

	// NZBHistoryRetentionDays controls how many days NZB attempt history is kept. Default 90.
	NZBHistoryRetentionDays int `json:"nzb_history_retention_days,omitempty"`

	// FFprobePath specifies an optional custom path to the ffprobe binary.
	FFprobePath string `json:"ffprobe_path,omitempty"`

	// PlaybackStartupTimeoutSeconds bounds probe/open work before the first playable response is ready. Default 5.
	PlaybackStartupTimeoutSeconds int `json:"playback_startup_timeout_seconds,omitempty"`
	// SessionTTLMinutes controls how long a deferred/inactive stream session is kept in memory. Default 30.
	SessionTTLMinutes int `json:"session_ttl_minutes,omitempty"`
	// SessionPostPlaybackTTLMinutes controls how long a session stays in memory after playback ends (paused/stopped). Default 240 (4 hours).
	SessionPostPlaybackTTLMinutes int `json:"session_post_playback_ttl_minutes,omitempty"`

	// SpeculativePreProbingMaxAttempts controls how many top candidate streams to speculatively probe sequentially until a working candidate is verified. Default 3 (max 5).
	SpeculativePreProbingMaxAttempts int `json:"speculative_preprobing_max_attempts,omitempty"`
	// SpeculativePreProbingCount is kept for backward compatibility with legacy configs.
	SpeculativePreProbingCount int `json:"speculative_pre_probing_count,omitempty"`

	// LibrarySearchMode controls SQLite library search priority ("library_first", "combine", "fallback_only", "disabled"). Default "combine".
	LibrarySearchMode string `json:"library_search_mode,omitempty"`
	// LibraryMaxItems controls maximum number of entries cached in SQLite library. Default 5000.
	LibraryMaxItems int `json:"library_max_items,omitempty"`
	// LibraryMaxSizeMB controls maximum storage size in MB for SQLite library. Default 250.
	LibraryMaxSizeMB int `json:"library_max_size_mb,omitempty"`
	// LibraryVerifyTTLHours: a background sweep re-STATs cached releases older than this many hours and prunes dead ones. Default 168 (7 days); negative disables.
	LibraryVerifyTTLHours int `json:"library_verify_ttl_hours,omitempty"`
	// BadReleaseTTLHours: releases with a definitive bad verdict (missing/corrupt articles) are filtered from results for this many hours. Default 336 (14 days); negative disables.
	BadReleaseTTLHours int `json:"bad_release_ttl_hours,omitempty"`
	// LibraryAutoSave automatically persists successful NZBs and blueprints to SQLite. Default true.
	LibraryAutoSave *bool `json:"library_auto_save,omitempty"`
	// LibraryScoreBonus is the legacy global library ranking bonus.
	// Deprecated: superseded by FilterProfileConfig.LibraryScoreBonus; kept only
	// so old configs unmarshal and migrate their value into profiles on load.
	LibraryScoreBonus int `json:"library_score_bonus,omitempty"`

	// AvailNZBMode controls how the AvailNZB integration behaves.
	// "on"  - fetch availability status and report playback results.
	// "off" - disable AvailNZB entirely (no GET, no POST).
	AvailNZBMode string `json:"availnzb_mode,omitempty"`

	// MuteErrorVideo controls whether the "Failed to start video" playback error stream is muted.
	MuteErrorVideo *bool `json:"mute_error_video,omitempty"`

	// Metadata is the legacy global metadata-provider section.
	// Deprecated: superseded by MetadataProfiles + per-stream bindings; kept so
	// old configs unmarshal and migrate on load (seedMetadataProfiles). Only
	// the Enabled field is still live, as the METADATA_ENABLED env kill-switch
	// target read by EffectiveMetadataEnabled.
	Metadata MetadataConfig `json:"metadata,omitempty"`

	LoadedPath string `json:"-"`

	ResetLegacyStreamState bool `json:"-"`
}

// MetadataConfig is the metadata-provider section. Catalogs lists the enabled
// catalogs from the built-in registry in display order. nil means "never
// configured" (registry defaults apply); an explicitly saved empty list means
// none — the tag must not be omitempty or the two collapse on save. Unknown
// ids are ignored read-side so a stale config never breaks the manifest.
type MetadataConfig struct {
	// Enabled is the master switch, on by default — nil means on, so only an
	// explicit false turns the provider off (and survives saves).
	Enabled  *bool           `json:"enabled,omitempty"`
	Catalogs []CatalogToggle `json:"catalogs"`

	// Per-media-type meta sources. Empty means the default; unknown values
	// normalize to the default read-side. Today only series has a real
	// choice (TVDB default, TMDB alternative) — movies are TMDB-only and
	// anime Kitsu-only, the fields exist so future sources slot in without
	// a config migration.
	MovieSource  string `json:"movie_source,omitempty"`
	SeriesSource string `json:"series_source,omitempty"`
	AnimeSource  string `json:"anime_source,omitempty"`

	// TVMazeAirDates lets TVMaze override episode air dates (and drive the
	// unaired-episode gate). nil means enabled.
	TVMazeAirDates *bool `json:"tvmaze_air_dates,omitempty"`

	// Language is the display language for meta responses and catalog rows,
	// as a TMDB-style tag ("de-DE"). Empty means English. TMDB content
	// localizes fully; TVDB series overlay translated names and overviews
	// where TheTVDB has them, falling back to the default record.
	Language string `json:"language,omitempty"`
}

// EffectiveMetadataEnabled reports whether the metadata provider (meta +
// catalog resources) may serve at all. Default true. Since the profile
// migration this is a global kill-switch (METADATA_ENABLED env) layered above
// the per-stream profile bindings, which are what actually enable metadata
// for a stream; the migration reads the persisted value once, then clears it.
func (c *Config) EffectiveMetadataEnabled() bool {
	if c == nil || c.Metadata.Enabled == nil {
		return true
	}
	return *c.Metadata.Enabled
}

// EffectiveSeriesMetaSource returns the primary series meta source: "tvdb"
// (default) or "tmdb". Whichever is not primary stays the fallback.
func (c *Config) EffectiveSeriesMetaSource() string {
	if c != nil && c.Metadata.SeriesSource == "tmdb" {
		return "tmdb"
	}
	return "tvdb"
}

// EffectiveMetadataLanguage returns the configured metadata display language
// tag, or "" for the English default. en-US normalizes to "" — it is what the
// sources serve without a language anyway, so the default path stays
// parameter-free (and cache keys stay stable).
func (c *Config) EffectiveMetadataLanguage() string {
	if c == nil {
		return ""
	}
	lang := strings.TrimSpace(c.Metadata.Language)
	if strings.EqualFold(lang, "en-US") || strings.EqualFold(lang, "en") {
		return ""
	}
	return lang
}

// EffectiveTVMazeAirDates reports whether TVMaze air-date overlays and gating
// are enabled. Default true.
func (c *Config) EffectiveTVMazeAirDates() bool {
	if c == nil || c.Metadata.TVMazeAirDates == nil {
		return true
	}
	return *c.Metadata.TVMazeAirDates
}

// CatalogToggle enables or disables one registry catalog by id.
type CatalogToggle struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type StreamEntry struct {
	Username           string   `json:"username"`
	Token              string   `json:"token"`
	Order              int      `json:"order,omitempty"`
	FilterSortingMode  string   `json:"filter_sorting_mode,omitempty"`
	IndexerMode        string   `json:"indexer_mode,omitempty"`
	UseAvailNZB        *bool    `json:"use_availnzb,omitempty"`
	FilterAvailNZB     *bool    `json:"filter_availnzb,omitempty"`
	CombineResults     *bool    `json:"combine_results,omitempty"`
	EnableFailover     *bool    `json:"enable_failover,omitempty"`
	ResultsMode        string   `json:"results_mode,omitempty"`
	AutoAddProviders   *bool    `json:"auto_add_providers,omitempty"`
	AutoAddIndexers    *bool    `json:"auto_add_indexers,omitempty"`
	ProviderSelections []string `json:"provider_selections,omitempty"`
	// ProviderConnectionLimits caps how many of a provider's connections this
	// stream may hold at once during playback, by provider name. A missing or
	// zero entry means uncapped. It is a ceiling, not a reservation: it stops
	// one stream monopolizing a provider, but does not hold connections back
	// for anyone.
	ProviderConnectionLimits map[string]int `json:"provider_connection_limits,omitempty"`
	// DisabledProviders are selected providers this stream currently does not
	// use. Disabling rather than removing is what makes a stream-level opinion
	// survive automatic sync: sync owns which providers are in the list, this
	// owns which of them are active.
	DisabledProviders   []string                       `json:"disabled_providers,omitempty"`
	IndexerSelections   []string                       `json:"indexer_selections,omitempty"`
	IndexerOverrides    map[string]IndexerSearchConfig `json:"indexer_overrides,omitempty"`
	MovieSearchQueries  []string                       `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []string                       `json:"series_search_queries,omitempty"`
	FilterProfileName   string                         `json:"filter_profile_name,omitempty"`
	// FilterProfileByType selects a profile per content kind — "movie",
	// "series", "anime_movie" or "anime_show" — falling back to
	// FilterProfileName when a kind has no entry. Anime is matched first when
	// the request resolved via Kitsu.
	FilterProfileByType map[string]string `json:"filter_profile_by_type,omitempty"`
	// MetadataProfileName binds a metadata profile by name. Empty means
	// metadata off for this stream — the manifest stays stream-only.
	MetadataProfileName string `json:"metadata_profile_name,omitempty"`
	MuteErrorVideo      *bool  `json:"mute_error_video,omitempty"`
	// ResultNameTemplate and ResultDescriptionTemplate customize how this
	// stream's Stremio results render (Go text/template over the result's
	// FormatContext). Empty uses the built-in format.
	ResultNameTemplate        string `json:"result_name_template,omitempty"`
	ResultDescriptionTemplate string `json:"result_description_template,omitempty"`
	// AddonName replaces the manifest name this stream reports to clients.
	// Empty keeps the default, which is the service name plus the stream name.
	AddonName string `json:"addon_name,omitempty"`
}

// DefaultLibraryScoreBonus is the ranking bonus added to cached library
// releases when a profile does not set its own value.
const DefaultLibraryScoreBonus = 500

// EffectiveLibraryScoreBonus is the ranking bonus added to cached library
// releases for this profile. Nil (unset) uses the default; negative disables
// the bonus entirely.
func (fp *FilterProfileConfig) EffectiveLibraryScoreBonus() int {
	if fp == nil || fp.LibraryScoreBonus == nil {
		return DefaultLibraryScoreBonus
	}
	if *fp.LibraryScoreBonus < 0 {
		return 0
	}
	return *fp.LibraryScoreBonus
}

// TryAbsoluteEpisodeEnabled reports whether the query supplements anime series
// searches with absolute-numbered queries. Defaults to enabled when unset.
func (sq *SearchQueryConfig) TryAbsoluteEpisodeEnabled() bool {
	if sq == nil || sq.TryAbsoluteEpisode == nil {
		return true
	}
	return *sq.TryAbsoluteEpisode
}

func (sq *SearchQueryConfig) AsIndexerSearchConfig() *IndexerSearchConfig {
	if sq == nil {
		return nil
	}
	out := &IndexerSearchConfig{
		SearchResultLimit:          sq.SearchResultLimit,
		EnableSeriesSeasonSearch:   sq.EnableSeriesSeasonSearch,
		EnableSeriesCompleteSearch: sq.EnableSeriesCompleteSearch,
		EnableSeriesPackSearch:     sq.EnableSeriesPackSearch,
	}
	mode := strings.ToLower(strings.TrimSpace(sq.SearchMode))
	switch mode {
	case "id":
		disableID := false
		disableString := true
		out.DisableIdSearch = &disableID
		out.DisableStringSearch = &disableString
	case "text":
		disableID := true
		disableString := false
		out.DisableIdSearch = &disableID
		out.DisableStringSearch = &disableString
	default:
		out.DisableIdSearch = sq.DisableIdSearch
		out.DisableStringSearch = sq.DisableStringSearch
	}
	if s := NormalizeSearchTitleLanguage(sq.SearchTitleLanguage); s != "" {
		out.SearchTitleLanguage = &s
	}
	if sq.MovieCategories != "" {
		s := sq.MovieCategories
		out.MovieCategories = &s
	}
	if sq.TVCategories != "" {
		s := sq.TVCategories
		out.TVCategories = &s
	}
	return out
}

func (c *Config) GetSearchQueryByName(contentType, name string) *SearchQueryConfig {
	if c == nil || name == "" {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(name))
	var queries []SearchQueryConfig
	if contentType == "movie" {
		queries = c.MovieSearchQueries
	} else {
		queries = c.SeriesSearchQueries
	}
	for i := range queries {
		if strings.ToLower(strings.TrimSpace(queries[i].Name)) == target {
			return &queries[i]
		}
	}
	return nil
}

func MergeIndexerSearch(ic *IndexerConfig, override *IndexerSearchConfig, global *Config) *IndexerSearchConfig {
	out := &IndexerSearchConfig{}
	const defaultLimit = 0
	out.SearchResultLimit = defaultLimit
	if ic != nil && ic.SearchResultLimit > 0 {
		out.SearchResultLimit = ic.SearchResultLimit
	}
	if override != nil && override.SearchResultLimit > 0 {
		out.SearchResultLimit = override.SearchResultLimit
	}
	seriesSeasonSearch := true
	if ic != nil && ic.EnableSeriesPackSearch != nil {
		seriesSeasonSearch = *ic.EnableSeriesPackSearch
	}
	if ic != nil && ic.EnableSeriesSeasonSearch != nil {
		seriesSeasonSearch = *ic.EnableSeriesSeasonSearch
	}
	if override != nil && override.EnableSeriesPackSearch != nil {
		seriesSeasonSearch = *override.EnableSeriesPackSearch
	}
	if override != nil && override.EnableSeriesSeasonSearch != nil {
		seriesSeasonSearch = *override.EnableSeriesSeasonSearch
	}
	out.EnableSeriesSeasonSearch = &seriesSeasonSearch

	seriesCompleteSearch := true
	if ic != nil && ic.EnableSeriesPackSearch != nil {
		seriesCompleteSearch = *ic.EnableSeriesPackSearch
	}
	if ic != nil && ic.EnableSeriesCompleteSearch != nil {
		seriesCompleteSearch = *ic.EnableSeriesCompleteSearch
	}
	if override != nil && override.EnableSeriesPackSearch != nil {
		seriesCompleteSearch = *override.EnableSeriesPackSearch
	}
	if override != nil && override.EnableSeriesCompleteSearch != nil {
		seriesCompleteSearch = *override.EnableSeriesCompleteSearch
	}
	out.EnableSeriesCompleteSearch = &seriesCompleteSearch
	s := ""
	if ic != nil && ic.SearchTitleLanguage != "" {
		s = ic.SearchTitleLanguage
	}
	if override != nil && override.SearchTitleLanguage != nil {
		s = *override.SearchTitleLanguage
	}
	out.SearchTitleLanguage = &s

	mc := ""
	if ic != nil {
		mc = ic.MovieCategories
	}
	if override != nil && override.MovieCategories != nil {
		mc = *override.MovieCategories
	}
	if mc != "" {
		out.MovieCategories = &mc
	}

	tc := ""
	if ic != nil {
		tc = ic.TVCategories
	}
	if override != nil && override.TVCategories != nil {
		tc = *override.TVCategories
	}
	if tc != "" {
		out.TVCategories = &tc
	}

	disableID := false
	if ic != nil && ic.DisableIdSearch != nil {
		disableID = *ic.DisableIdSearch
	}
	if override != nil && override.DisableIdSearch != nil {
		disableID = *override.DisableIdSearch
	}
	out.DisableIdSearch = &disableID

	disableString := false
	if ic != nil && ic.DisableStringSearch != nil {
		disableString = *ic.DisableStringSearch
	}
	if override != nil && override.DisableStringSearch != nil {
		disableString = *override.DisableStringSearch
	}
	out.DisableStringSearch = &disableString

	scope := IndexerContentScopeAll
	if ic != nil {
		scope = NormalizeIndexerContentScope(ic.ContentScope)
	}
	if override != nil && override.ContentScope != nil {
		scope = NormalizeIndexerContentScope(*override.ContentScope)
	}
	if scope != IndexerContentScopeAll {
		out.ContentScope = &scope
	}

	return out
}

func (c *Config) GetAdminUsername() string {
	if c != nil && c.AdminUsername != "" {
		return c.AdminUsername
	}
	return "admin"
}

func ResolveConfigPath(explicitPath string) string {
	target := strings.TrimSpace(explicitPath)
	if target == "" {
		target = strings.TrimSpace(os.Getenv(env.ConfigPath))
	}
	if target == "" {
		dataDir := paths.GetDataDir()
		return filepath.Join(dataDir, "config.json")
	}

	target = filepath.Clean(target)
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		return filepath.Join(target, "config.json")
	}
	if strings.HasSuffix(explicitPath, "/") || strings.HasSuffix(explicitPath, "\\") {
		return filepath.Join(target, "config.json")
	}
	return target
}

func Load() (*Config, error) {
	return LoadWithPath("")
}

func LoadWithPath(explicitPath string) (*Config, error) {
	configPath := ResolveConfigPath(explicitPath)
	dataDir := filepath.Dir(configPath)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Warn("Failed to create data directory", "dir", dataDir, "err", err)
	}

	cfg := &Config{
		AddonPort:                        7000,
		AddonBaseURL:                     "http://localhost:7000",
		LogLevel:                         "INFO",
		VerboseNNTPLogging:               false,
		AdminUsername:                    "admin",
		ProxyPort:                        119,
		ProxyHost:                        "0.0.0.0",
		ProxyEnabled:                     true,
		MemoryLimitMB:                    512,
		KeepLogFiles:                     9,
		NZBHistoryRetentionDays:          90,
		PlaybackStartupTimeoutSeconds:    DefaultPlaybackStartupTimeoutSeconds,
		SessionTTLMinutes:                DefaultSessionTTLMinutes,
		SessionPostPlaybackTTLMinutes:    DefaultSessionPostPlaybackTTLMinutes,
		SpeculativePreProbingMaxAttempts: DefaultSpeculativePreProbingMaxAttempts,
		SpeculativePreProbingCount:       DefaultSpeculativePreProbingCount,
		LoadedPath:                       configPath,
	}

	if err := cfg.LoadFile(configPath); err != nil {
		if os.IsNotExist(err) {
			logger.Info("No config found, creating new one", "path", configPath)
		} else {
			logger.Warn("Failed to load config, using defaults", "path", configPath, "err", err)
		}
	} else {
		logger.Info("Loaded configuration", "path", configPath)
	}
	needSave := false
	streamModelUpgrade := cfg.ConfigVersion < StreamModelConfigVersion
	if streamModelUpgrade {
		if len(cfg.Streams) > 0 {
			logger.Warn("Resetting legacy stream entries from config for stream-model upgrade", "count", len(cfg.Streams), "from_version", cfg.ConfigVersion, "to_version", CurrentConfigVersion)
		} else {
			logger.Info("Applying stream-model upgrade defaults", "from_version", cfg.ConfigVersion, "to_version", CurrentConfigVersion)
		}
		cfg.Streams = make(map[string]*StreamEntry)
		cfg.ResetLegacyStreamState = true
		needSave = true
	}
	if cfg.ConfigVersion < CurrentConfigVersion {
		cfg.ConfigVersion = CurrentConfigVersion
		needSave = true
	}
	if len(cfg.FilterProfiles) == 0 {
		cfg.FilterProfiles = []FilterProfileConfig{DefaultFilterProfile()}
		needSave = true
	}
	// Carry pre-jhin profiles onto rank.Profile once and write it back, so the
	// stored profile is what the UI edits and what ranking compiles.
	for i := range cfg.FilterProfiles {
		if cfg.FilterProfiles[i].Ranking == nil {
			ranking := Synthesize(cfg.FilterProfiles[i])
			cfg.FilterProfiles[i].Ranking = &ranking
			needSave = true
		}
		// Migrate the legacy global library bonus into profiles that have no
		// value of their own, so an explicitly tuned bonus keeps applying.
		if cfg.FilterProfiles[i].LibraryScoreBonus == nil && cfg.LibraryScoreBonus != 0 {
			bonus := cfg.LibraryScoreBonus
			cfg.FilterProfiles[i].LibraryScoreBonus = &bonus
			needSave = true
		}
	}
	if cfg.KeepLogFiles < 1 {
		cfg.KeepLogFiles = 9
	}
	if cfg.NZBHistoryRetentionDays < 1 {
		cfg.NZBHistoryRetentionDays = 90
		needSave = true
	}
	if normalized := normalizePlaybackStartupTimeoutSeconds(cfg.PlaybackStartupTimeoutSeconds); normalized != cfg.PlaybackStartupTimeoutSeconds {
		cfg.PlaybackStartupTimeoutSeconds = normalized
		needSave = true
	}
	if normalized := normalizeSessionTTLMinutes(cfg.SessionTTLMinutes); normalized != cfg.SessionTTLMinutes {
		cfg.SessionTTLMinutes = normalized
		needSave = true
	}
	if normalized := normalizeSessionPostPlaybackTTLMinutes(cfg.SessionPostPlaybackTTLMinutes); normalized != cfg.SessionPostPlaybackTTLMinutes {
		cfg.SessionPostPlaybackTTLMinutes = normalized
		needSave = true
	}
	if normalized := normalizeSpeculativePreProbingCount(cfg.SpeculativePreProbingCount); normalized != cfg.SpeculativePreProbingCount {
		cfg.SpeculativePreProbingCount = normalized
		needSave = true
	}
	if normalizedMode := NormalizeAvailNZBMode(cfg.AvailNZBMode); normalizedMode != cfg.AvailNZBMode {
		cfg.AvailNZBMode = normalizedMode
		needSave = true
	}

	// Metadata-profile migration, phase one: capture the persisted master
	// switch and seed the profile list before env overrides can touch either.
	// Phase two (stream binding) runs after applyStreamModelUpgradeDefaults so
	// a fresh install's seeded default stream is bound too.
	persistedMetadataEnabled := cfg.EffectiveMetadataEnabled()
	migratedMetadataProfiles := cfg.seedMetadataProfiles()
	if migratedMetadataProfiles {
		needSave = true
	}

	overrides, keys := env.ReadConfigOverrides()
	ApplyEnvOverrides(cfg, overrides, keys)

	if cfg.MigrateLegacyIndexers() {
		needSave = true
	}

	if cfg.ApplyProviderDefaults() {
		needSave = true
	}
	if cfg.backfillLegacySearchQuerySettings() {
		needSave = true
	}
	if streamModelUpgrade && cfg.applyStreamModelUpgradeDefaults() {
		needSave = true
	}
	// Metadata-profile migration, phase two: every stream exists by now.
	if migratedMetadataProfiles && persistedMetadataEnabled {
		cfg.bindDefaultMetadataProfile()
	}

	if cfg.AdminToken == "" {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err == nil {
			hash := sha256.Sum256(bytes)
			cfg.AdminToken = hex.EncodeToString(hash[:])
			needSave = true
		}
	}
	if cfg.AdminPasswordHash == "" {
		cfg.AdminPasswordHash = defaultAdminPasswordHash
		cfg.AdminMustChangePassword = true
		needSave = true
	}
	if needSave {
		logger.Info("Set default admin token/password in config")
	}

	if err := cfg.Save(); err != nil {
		logger.Warn("Failed to save config on startup", "err", err)
	} else {
		logger.Info("Saved merged configuration", "path", configPath)
	}

	if len(cfg.Providers) == 0 {
		logger.Warn("No NNTP providers configured. Add some via the web UI")
	}

	return cfg, nil
}

func (c *Config) applyStreamModelUpgradeDefaults() bool {
	changed := false
	if c.ensureDefaultMigrationSearchQueries() {
		changed = true
	}
	if c.ensureDefaultMigratedStream() {
		changed = true
	}
	return changed
}

// ensureDefaultMigrationSearchQueries seeds the four stock search requests.
// Movie queries carry the year because movie releases are named with one;
// series queries do not, because scene TV releases are named
// "Title.S01E01.1080p..." — a year token narrows the indexer query to nothing
// and arms year validation against results that can never carry one.
func (c *Config) ensureDefaultMigrationSearchQueries() bool {
	changed := false
	if c.ensureMovieSearchQuery(SearchQueryConfig{
		Name:                "DefaultMovieText",
		SearchMode:          "text",
		SearchResultLimit:   0,
		MovieCategories:     "2000",
		IncludeYear:         ptrBool(true),
		SearchTitleLanguage: "en-US",
	}) {
		changed = true
	}
	if c.ensureMovieSearchQuery(SearchQueryConfig{
		Name:                 "DefaultMovieID",
		SearchMode:           "id",
		SearchResultLimit:    0,
		MovieCategories:      "2000",
		IncludeYear:          ptrBool(true),
		SearchTitleLanguages: DefaultIDSearchTitleLanguages(),
	}) {
		changed = true
	}
	if c.ensureSeriesSearchQuery(SearchQueryConfig{
		Name:                "DefaultTVText",
		SearchMode:          "text",
		SearchResultLimit:   0,
		TVCategories:        "5000",
		IncludeYear:         ptrBool(false),
		SeriesSearchScope:   SeriesSearchScopeSeasonEpisode,
		TryAbsoluteEpisode:  ptrBool(true),
		SearchTitleLanguage: "en-US",
	}) {
		changed = true
	}
	if c.ensureSeriesSearchQuery(SearchQueryConfig{
		Name:                 "DefaultTVID",
		SearchMode:           "id",
		SearchResultLimit:    0,
		TVCategories:         "5000",
		IncludeYear:          ptrBool(false),
		SeriesSearchScope:    SeriesSearchScopeSeasonEpisode,
		TryAbsoluteEpisode:   ptrBool(true),
		SearchTitleLanguages: DefaultIDSearchTitleLanguages(),
	}) {
		changed = true
	}
	return changed
}

func backfillLegacySearchQuerySettingsForQuery(query *SearchQueryConfig, isSeries bool) bool {
	if query == nil {
		return false
	}
	changed := false
	if query.IncludeYear == nil {
		if query.LegacyIncludeYearInTextSearch != nil {
			query.IncludeYear = ptrBool(*query.LegacyIncludeYearInTextSearch)
		} else {
			// Same rule the stock queries use: movie releases are named with a
			// year, series releases are not.
			query.IncludeYear = ptrBool(!isSeries)
		}
		changed = true
	}
	if query.LegacyIncludeYearInTextSearch != nil {
		query.LegacyIncludeYearInTextSearch = nil
		changed = true
	}
	normalizedSingleLanguage := NormalizeSearchTitleLanguage(query.SearchTitleLanguage)
	if query.SearchTitleLanguage != normalizedSingleLanguage {
		query.SearchTitleLanguage = normalizedSingleLanguage
		changed = true
	}
	normalizedLanguages := NormalizeSearchTitleLanguages(query.SearchTitleLanguages)
	if len(query.SearchTitleLanguages) != len(normalizedLanguages) || strings.Join(query.SearchTitleLanguages, "\x00") != strings.Join(normalizedLanguages, "\x00") {
		query.SearchTitleLanguages = normalizedLanguages
		changed = true
	}
	if strings.EqualFold(strings.TrimSpace(query.SearchMode), "id") && len(query.SearchTitleLanguages) == 0 {
		if query.SearchTitleLanguage == "" {
			query.SearchTitleLanguages = DefaultIDSearchTitleLanguages()
		} else {
			query.SearchTitleLanguages = NormalizeSearchTitleLanguages([]string{query.SearchTitleLanguage})
		}
		changed = true
	}
	if isSeries {
		// The retired "absolute" scope becomes season_episode with the
		// absolute-episode supplement explicitly enabled.
		if strings.EqualFold(strings.TrimSpace(query.SeriesSearchScope), legacySeriesSearchScopeAbsolute) && query.TryAbsoluteEpisode == nil {
			query.TryAbsoluteEpisode = ptrBool(true)
			changed = true
		}
		normalizedScope := normalizeSeriesSearchScopeFromLegacy(query.SeriesSearchScope, query.UseSeasonEpisodeParams)
		if query.SeriesSearchScope != normalizedScope {
			query.SeriesSearchScope = normalizedScope
			changed = true
		}
	} else {
		if query.SeriesSearchScope != "" {
			query.SeriesSearchScope = ""
			changed = true
		}
		if query.TryAbsoluteEpisode != nil {
			query.TryAbsoluteEpisode = nil
			changed = true
		}
	}
	if query.UseSeasonEpisodeParams != nil {
		query.UseSeasonEpisodeParams = nil
		changed = true
	}
	return changed
}

func (c *Config) backfillLegacySearchQuerySettings() bool {
	changed := false
	for i := range c.MovieSearchQueries {
		if backfillLegacySearchQuerySettingsForQuery(&c.MovieSearchQueries[i], false) {
			changed = true
		}
	}
	for i := range c.SeriesSearchQueries {
		if backfillLegacySearchQuerySettingsForQuery(&c.SeriesSearchQueries[i], true) {
			changed = true
		}
	}
	return changed
}

func (c *Config) ensureMovieSearchQuery(query SearchQueryConfig) bool {
	for _, existing := range c.MovieSearchQueries {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(query.Name)) {
			return false
		}
	}
	c.MovieSearchQueries = append(c.MovieSearchQueries, query)
	return true
}

func (c *Config) ensureSeriesSearchQuery(query SearchQueryConfig) bool {
	for _, existing := range c.SeriesSearchQueries {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(query.Name)) {
			return false
		}
	}
	c.SeriesSearchQueries = append(c.SeriesSearchQueries, query)
	return true
}

func (c *Config) ensureDefaultMigratedStream() bool {
	if c.Streams == nil {
		c.Streams = make(map[string]*StreamEntry)
	}
	if _, exists := c.Streams[defaultMigratedStreamID]; exists {
		return false
	}
	token, err := generateConfigToken()
	if err != nil {
		logger.Warn("Failed to generate token for migrated default stream", "err", err)
		return false
	}
	c.Streams[defaultMigratedStreamID] = &StreamEntry{
		Username:            defaultMigratedStreamID,
		Token:               token,
		Order:               1,
		FilterSortingMode:   "aiostreams",
		IndexerMode:         "combine",
		UseAvailNZB:         ptrBool(true),
		CombineResults:      ptrBool(true),
		EnableFailover:      ptrBool(true),
		ResultsMode:         "display_all",
		AutoAddProviders:    ptrBool(true),
		AutoAddIndexers:     ptrBool(true),
		IndexerOverrides:    make(map[string]IndexerSearchConfig),
		ProviderSelections:  allProviderNames(c.Providers),
		IndexerSelections:   allIndexerNames(c.Indexers),
		MovieSearchQueries:  allSearchQueryNames(c.MovieSearchQueries),
		SeriesSearchQueries: allSearchQueryNames(c.SeriesSearchQueries),
	}
	return true
}

func generateConfigToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

func allProviderNames(providers []Provider) []string {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		name := strings.TrimSpace(provider.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func allIndexerNames(indexers []IndexerConfig) []string {
	names := make([]string, 0, len(indexers))
	for _, indexer := range indexers {
		name := strings.TrimSpace(indexer.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func allSearchQueryNames(queries []SearchQueryConfig) []string {
	names := make([]string, 0, len(queries))
	for _, query := range queries {
		name := strings.TrimSpace(query.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (c *Config) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	type configAlias Config
	var raw struct {
		configAlias
		LegacyDevices map[string]*StreamEntry `json:"devices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = Config(raw.configAlias)
	if c.Streams == nil && raw.LegacyDevices != nil {
		c.Streams = raw.LegacyDevices
	}
	c.LoadedPath = path
	return nil
}

func (c *Config) ApplyProviderDefaults() bool {
	changed := false
	usedNames := make(map[string]bool, len(c.Providers))
	for i := range c.Providers {
		name := strings.TrimSpace(c.Providers[i].Name)
		if name == "" {
			continue
		}
		usedNames[strings.ToLower(name)] = true
	}
	for i := range c.Providers {
		p := &c.Providers[i]

		if strings.TrimSpace(p.Name) == "" {
			p.Name = uniqueProviderNameFromHost(p.Host, usedNames)
			changed = true
		}
		if trimmedName := strings.TrimSpace(p.Name); trimmedName != "" {
			usedNames[strings.ToLower(trimmedName)] = true
		}

		if p.Priority == nil {
			priority := i + 1
			p.Priority = &priority
			enabled := true
			p.Enabled = &enabled
			changed = true
		} else if p.Enabled == nil {

			enabled := true
			p.Enabled = &enabled
			changed = true
		}

	}
	return changed
}

func uniqueProviderNameFromHost(host string, usedNames map[string]bool) string {
	base := providerNameFromHost(host)
	name := base
	for suffix := 2; usedNames[strings.ToLower(name)]; suffix++ {
		name = base + "-" + strconv.Itoa(suffix)
	}
	return name
}

func providerNameFromHost(host string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(host)), ".")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) >= 2 {
		return filtered[len(filtered)-2]
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return "provider"
}

func (c *Config) MigrateLegacyIndexers() bool {
	changed := false
	for i := range c.Indexers {
		if c.Indexers[i].Enabled == nil {
			enabled := true
			c.Indexers[i].Enabled = &enabled
			changed = true
		}
		if strings.EqualFold(strings.TrimSpace(c.Indexers[i].Type), "easynews") {
			if c.Indexers[i].TimeoutSeconds <= 0 {
				c.Indexers[i].TimeoutSeconds = DefaultEasynewsIndexerTimeoutSeconds
				changed = true
			}
		}
	}
	return changed
}

// Save writes the config back to the file it was loaded from. It deliberately
// does not fall back to a relative "config.json": that silently wrote a config
// into whatever the working directory happened to be (tests littered package
// directories this way). Callers that legitimately have no loaded path must
// pick one explicitly via SaveFile.
func (c *Config) Save() error {
	path := strings.TrimSpace(c.LoadedPath)
	if path == "" {
		return errors.New("config: cannot save, no config path is set (use SaveFile with an explicit path)")
	}
	return c.SaveFile(path)
}

func (c *Config) SaveFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(c); err != nil {
		return err
	}
	c.LoadedPath = path
	return nil
}

func keySet(list []string, s string) bool {
	for _, k := range list {
		if k == s {
			return true
		}
	}
	return false
}

// envFieldCopiers maps each env-override key to the single config field it
// drives. ApplyEnvOverrides and CopyEnvOverridesFrom both run off this table,
// so a new override is one entry instead of two hand-maintained mirrors that
// can (and did) drift apart.
var envFieldCopiers = map[string]func(dst, src *Config){
	env.KeyAddonPort:          func(d, s *Config) { d.AddonPort = s.AddonPort },
	env.KeyAddonBaseURL:       func(d, s *Config) { d.AddonBaseURL = s.AddonBaseURL },
	env.KeyLogLevel:           func(d, s *Config) { d.LogLevel = s.LogLevel },
	env.KeyKeepLogFiles:       func(d, s *Config) { d.KeepLogFiles = s.KeepLogFiles },
	env.KeyAvailNZBAPIKey:     func(d, s *Config) { d.AvailNZBAPIKey = s.AvailNZBAPIKey },
	env.KeyTMDBAPIKey:         func(d, s *Config) { d.TMDBAPIKey = s.TMDBAPIKey },
	env.KeyTVDBAPIKey:         func(d, s *Config) { d.TVDBAPIKey = s.TVDBAPIKey },
	env.KeyIndexerQueryHeader: func(d, s *Config) { d.IndexerQueryHeader = s.IndexerQueryHeader },
	env.KeyIndexerGrabHeader:  func(d, s *Config) { d.IndexerGrabHeader = s.IndexerGrabHeader },
	env.KeyProviderHeader:     func(d, s *Config) { d.ProviderHeader = s.ProviderHeader },
	env.KeyProxyPort:          func(d, s *Config) { d.ProxyPort = s.ProxyPort },
	env.KeyProxyHost:          func(d, s *Config) { d.ProxyHost = s.ProxyHost },
	env.KeyProxyEnabled:       func(d, s *Config) { d.ProxyEnabled = s.ProxyEnabled },
	env.KeyProxyAuthUser:      func(d, s *Config) { d.ProxyAuthUser = s.ProxyAuthUser },
	env.KeyProxyAuthPass:      func(d, s *Config) { d.ProxyAuthPass = s.ProxyAuthPass },
	env.KeyAdminUsername:      func(d, s *Config) { d.AdminUsername = s.AdminUsername },
	env.KeyAdminMustChangePwd: func(d, s *Config) { d.AdminMustChangePassword = s.AdminMustChangePassword },
	env.KeyProviders:          func(d, s *Config) { d.Providers = cloneProviders(s.Providers) },
	env.KeyIndexers:           func(d, s *Config) { d.Indexers = cloneIndexers(s.Indexers) },
	env.KeyDatabaseDriver:     func(d, s *Config) { d.DatabaseDriver = s.DatabaseDriver },
	env.KeyDatabaseURL:        func(d, s *Config) { d.DatabaseURL = s.DatabaseURL },
	env.KeyMetadataEnabled:    func(d, s *Config) { d.Metadata.Enabled = s.Metadata.Enabled },
}

// cloneProviders deep-copies the pointer fields so the two configs never share
// Priority/Enabled storage.
func cloneProviders(in []Provider) []Provider {
	out := make([]Provider, len(in))
	for i, p := range in {
		out[i] = p
		if p.Priority != nil {
			v := *p.Priority
			out[i].Priority = &v
		}
		if p.Enabled != nil {
			v := *p.Enabled
			out[i].Enabled = &v
		}
	}
	return out
}

func cloneIndexers(in []IndexerConfig) []IndexerConfig {
	out := make([]IndexerConfig, len(in))
	for i, idx := range in {
		out[i] = idx
		if idx.Enabled != nil {
			v := *idx.Enabled
			out[i].Enabled = &v
		}
	}
	return out
}

// copyEnvKeys copies just the named env-driven fields from src to dst.
func copyEnvKeys(dst, src *Config, keys []string) {
	for _, k := range keys {
		if copyField := envFieldCopiers[k]; copyField != nil {
			copyField(dst, src)
		}
	}
}

// envOverridesAsConfig projects the raw env overrides onto a Config, applying
// the normalizations that only make sense for env-declared entries (indexers
// declared via env are always newznab and default to enabled).
func envOverridesAsConfig(o env.ConfigOverrides) *Config {
	cfg := &Config{
		AddonPort:               o.AddonPort,
		AddonBaseURL:            o.AddonBaseURL,
		LogLevel:                o.LogLevel,
		KeepLogFiles:            o.KeepLogFiles,
		AvailNZBAPIKey:          o.AvailNZBAPIKey,
		TMDBAPIKey:              o.TMDBAPIKey,
		TVDBAPIKey:              o.TVDBAPIKey,
		IndexerQueryHeader:      o.IndexerQueryHeader,
		IndexerGrabHeader:       o.IndexerGrabHeader,
		ProviderHeader:          o.ProviderHeader,
		ProxyPort:               o.ProxyPort,
		ProxyHost:               o.ProxyHost,
		ProxyEnabled:            o.ProxyEnabled,
		ProxyAuthUser:           o.ProxyAuthUser,
		ProxyAuthPass:           o.ProxyAuthPass,
		AdminUsername:           o.AdminUsername,
		AdminMustChangePassword: o.AdminMustChangePwd,
		DatabaseDriver:          o.DatabaseDriver,
		DatabaseURL:             o.DatabaseURL,
		Metadata:                MetadataConfig{Enabled: &o.MetadataEnabled},
	}
	cfg.Providers = make([]Provider, len(o.Providers))
	for i, p := range o.Providers {
		cfg.Providers[i] = Provider{
			Name:        p.Name,
			Host:        p.Host,
			Port:        p.Port,
			Username:    p.Username,
			Password:    p.Password,
			Connections: p.Connections,
			UseSSL:      p.UseSSL,
			Priority:    p.Priority,
			Enabled:     p.Enabled,
		}
	}
	cfg.Indexers = make([]IndexerConfig, len(o.Indexers))
	for i, idx := range o.Indexers {
		enabled := true
		if idx.Enabled != nil {
			enabled = *idx.Enabled
		}
		cfg.Indexers[i] = IndexerConfig{
			Name:    idx.Name,
			URL:     idx.URL,
			APIKey:  idx.APIKey,
			Type:    "newznab",
			Enabled: &enabled,
		}
	}
	return cfg
}

// ApplyEnvOverrides writes the env-declared values for keys onto cfg.
func ApplyEnvOverrides(cfg *Config, o env.ConfigOverrides, keys []string) {
	copyEnvKeys(cfg, envOverridesAsConfig(o), keys)
}

// CopyEnvOverridesFrom carries env-driven fields across a config replacement,
// so a UI save cannot clobber a value the environment owns.
func CopyEnvOverridesFrom(src, dst *Config) {
	if src == nil || dst == nil {
		return
	}
	copyEnvKeys(dst, src, env.OverrideKeys())
}

func GetEnvOverrideKeys() []string {
	return env.OverrideKeys()
}

func (c *Config) RedactForAPI() Config {
	out := *c
	out.AdminPasswordHash = ""
	out.AdminToken = ""
	out.ProxyAuthUser = ""
	out.ProxyAuthPass = ""
	out.IndexerQueryHeader = ""
	out.IndexerGrabHeader = ""
	out.ProviderHeader = ""
	out.IndexerProxyURL = RedactProxyURLForAPI(c.IndexerProxyURL)
	out.AvailNZBAPIKey = ""
	out.TMDBAPIKey = ""
	out.TVDBAPIKey = ""
	out.DatabaseURL = RedactDatabaseURLForAPI(c.DatabaseURL)
	out.Providers = make([]Provider, len(c.Providers))
	for i, provider := range c.Providers {
		redactedProvider := provider
		redactedProvider.Username = ""
		redactedProvider.Password = ""
		out.Providers[i] = redactedProvider
	}
	out.Indexers = make([]IndexerConfig, len(c.Indexers))
	for i, indexer := range c.Indexers {
		redactedIndexer := indexer
		redactedIndexer.APIKey = ""
		redactedIndexer.Username = ""
		redactedIndexer.Password = ""
		redactedIndexer.ProxyURL = RedactProxyURLForAPI(indexer.ProxyURL)
		out.Indexers[i] = redactedIndexer
	}
	return out
}

func (c *Config) EffectiveFFprobePath() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.FFprobePath)
}
