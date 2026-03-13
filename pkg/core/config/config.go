package config

import (
	"strings"
	"time"

	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
)

const (
	DefaultInternalIndexerTimeoutSeconds   = 5
	DefaultAggregatorIndexerTimeoutSeconds = 10
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

func DefaultFilterConfig() FilterConfig {
	return FilterConfig{
		QualityExcluded: []string{"CAM", "TeleSync", "TeleCine", "SCR"},
		DubbedExcluded:  ptrBool(true),
	}
}

func DefaultSortConfig() SortConfig {
	return SortConfig{
		PreferredResolution: []string{"4k", "1080p", "720p", "sd"},
		PreferredQuality: []string{
			"BluRay REMUX", "REMUX", "BluRay", "BRRip", "BDRip", "UHDRip", "HDRip",
			"WEB-DL", "WEBRip", "WEB-DLRip", "WEB",
			"HDTV", "HDTVRip", "PDTV", "TVRip", "SATRip",
			"DVD", "DVDRip", "PPVRip", "R5", "XviD", "DivX",
		},
		PreferredAvailNZB: []string{"available"},
		SortCriteriaOrder: []string{
			"availnzb", "resolution", "quality", "codec", "visual_tag", "audio", "channels",
			"bit_depth", "container", "languages", "group", "edition", "network", "region",
			"three_d", "size", "keywords", "regex",
		},
		GrabWeight: 0.5,
		AgeWeight:  1.0,
	}
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
	IncludeYearInSearch        *bool   `json:"include_year_in_search,omitempty"`
	EnableSeriesSeasonSearch   *bool   `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool   `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool   `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        *string `json:"search_title_language,omitempty"`
	SearchTitleNormalize       *bool   `json:"search_title_normalize,omitempty"`
	MovieCategories            *string `json:"movie_categories,omitempty"`
	TVCategories               *string `json:"tv_categories,omitempty"`
	ExtraSearchTerms           *string `json:"extra_search_terms,omitempty"`
	UseSeasonEpisodeParams     *bool   `json:"use_season_episode_params,omitempty"`
}

type IndexerConfig struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	APIKey         string `json:"api_key"`
	APIPath        string `json:"api_path"`
	Type           string `json:"type"`
	APIHitsDay     int    `json:"api_hits_day"`
	DownloadsDay   int    `json:"downloads_day"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`

	Username string `json:"username"`
	Password string `json:"password"`

	MovieCategories            string `json:"movie_categories,omitempty"`
	TVCategories               string `json:"tv_categories,omitempty"`
	ExtraSearchTerms           string `json:"extra_search_terms,omitempty"`
	UseSeasonEpisodeParams     *bool  `json:"use_season_episode_params,omitempty"`
	SearchResultLimit          int    `json:"search_result_limit,omitempty"`
	IncludeYearInSearch        *bool  `json:"include_year_in_search,omitempty"`
	EnableSeriesSeasonSearch   *bool  `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool  `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool  `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        string `json:"search_title_language,omitempty"`
	SearchTitleNormalize       *bool  `json:"search_title_normalize,omitempty"`
}

func (ic IndexerConfig) EffectiveTimeoutSeconds() int {
	if ic.TimeoutSeconds > 0 {
		return ic.TimeoutSeconds
	}
	if IsAggregatorIndexerType(ic.Type) {
		return DefaultAggregatorIndexerTimeoutSeconds
	}
	return DefaultInternalIndexerTimeoutSeconds
}

func (ic IndexerConfig) EffectiveTimeout() time.Duration {
	return time.Duration(ic.EffectiveTimeoutSeconds()) * time.Second
}

type Config struct {
	Indexers []IndexerConfig

	AddonPort    int
	AddonBaseURL string
	LogLevel     string

	Providers []Provider

	ProxyPort     int
	ProxyHost     string
	ProxyAuthUser string
	ProxyAuthPass string

	AvailNZBURL    string
	AvailNZBAPIKey string
	TMDBAPIKey     string
	TVDBAPIKey     string

	// MemoryLimitMB sets a soft limit on total Go heap (runtime/debug.SetMemoryLimit). 0 = no limit.
	MemoryLimitMB int

	// KeepLogFiles is how many log files to keep (current streamnzb.log + rotated streamnzb-*.log). Default 9.
	KeepLogFiles int

	// AvailNZBMode controls how the AvailNZB integration behaves.
	// "" or "full"        – fetch availability status AND report playback results (default).
	// "status_only"       – fetch availability status but never report back (leeching).
	// "disabled"          – disable AvailNZB entirely (no GET, no POST).
	AvailNZBMode string
}

func (c *Config) GetIncludeYearInSearch() bool { return true }

func (c *Config) GetSearchTitleLanguage() string { return "" }

func (c *Config) GetSearchTitleNormalize() bool { return false }

func MergeIndexerSearch(ic *IndexerConfig, override *IndexerSearchConfig, global *Config) *IndexerSearchConfig {
	out := &IndexerSearchConfig{}
	const defaultLimit = 1000
	out.SearchResultLimit = defaultLimit
	if ic != nil && ic.SearchResultLimit > 0 {
		out.SearchResultLimit = ic.SearchResultLimit
	}
	if override != nil && override.SearchResultLimit > 0 {
		out.SearchResultLimit = override.SearchResultLimit
	}
	val := true
	if ic != nil && ic.IncludeYearInSearch != nil {
		val = *ic.IncludeYearInSearch
	}
	if override != nil && override.IncludeYearInSearch != nil {
		val = *override.IncludeYearInSearch
	}
	out.IncludeYearInSearch = &val
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
	n := false
	if ic != nil && ic.SearchTitleNormalize != nil {
		n = *ic.SearchTitleNormalize
	}
	if override != nil && override.SearchTitleNormalize != nil {
		n = *override.SearchTitleNormalize
	}
	out.SearchTitleNormalize = &n

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

	et := ""
	if ic != nil {
		et = ic.ExtraSearchTerms
	}
	if override != nil && override.ExtraSearchTerms != nil {
		et = *override.ExtraSearchTerms
	}
	if et != "" {
		out.ExtraSearchTerms = &et
	}

	useSE := true
	if ic != nil && ic.UseSeasonEpisodeParams != nil {
		useSE = *ic.UseSeasonEpisodeParams
	}
	if override != nil && override.UseSeasonEpisodeParams != nil {
		useSE = *override.UseSeasonEpisodeParams
	}
	out.UseSeasonEpisodeParams = &useSE
	return out
}

// Load reads configuration entirely from environment variables.
func Load() *Config {
	v := env.ReadConfig()

	cfg := &Config{
		AddonPort:      v.AddonPort,
		AddonBaseURL:   v.AddonBaseURL,
		LogLevel:       v.LogLevel,
		KeepLogFiles:   v.KeepLogFiles,
		ProxyPort:      v.ProxyPort,
		ProxyHost:      v.ProxyHost,
		ProxyAuthUser:  v.ProxyAuthUser,
		ProxyAuthPass:  v.ProxyAuthPass,
		AvailNZBURL:    v.AvailNZBURL,
		AvailNZBAPIKey: v.AvailNZBAPIKey,
		TMDBAPIKey:     v.TMDBAPIKey,
		TVDBAPIKey:     v.TVDBAPIKey,
		MemoryLimitMB:  v.MemoryLimitMB,
		AvailNZBMode:   v.AvailNZBMode,
	}

	// Convert env providers → config providers
	for i, p := range v.Providers {
		var priority *int
		var enabled *bool
		if p.Priority != nil {
			priority = p.Priority
		} else {
			pri := i + 1
			priority = &pri
		}
		if p.Enabled != nil {
			enabled = p.Enabled
		} else {
			e := true
			enabled = &e
		}
		cfg.Providers = append(cfg.Providers, Provider{
			Name:        p.Name,
			Host:        p.Host,
			Port:        p.Port,
			Username:    p.Username,
			Password:    p.Password,
			Connections: p.Connections,
			UseSSL:      p.UseSSL,
			Priority:    priority,
			Enabled:     enabled,
		})
	}

	// Convert env indexers → config indexers
	for _, idx := range v.Indexers {
		enabled := true
		if idx.Enabled != nil {
			enabled = *idx.Enabled
		}
		cfg.Indexers = append(cfg.Indexers, IndexerConfig{
			Name:    idx.Name,
			URL:     idx.URL,
			APIKey:  idx.APIKey,
			APIPath: idx.APIPath,
			Type:    idx.Type,
			Enabled: &enabled,
		})
	}

	if len(cfg.Providers) == 0 {
		logger.Warn("No NNTP providers configured — set PROVIDER_1_HOST etc. in .env")
	}

	return cfg
}
