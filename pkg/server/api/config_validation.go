package api

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/indexer/easynews"
	"streamnzb/pkg/indexer/newznab"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/services/metadata/certification"
	"streamnzb/pkg/usenet/nntp"
)

func (s *Server) validateConfig(cfg *config.Config) map[string]string {
	return s.validateConfigWithPlan(cfg, fullConfigValidationPlan())
}

type configValidationPlan struct {
	validateKeepLogFiles           bool
	validateNZBHistoryRetention    bool
	validatePlaybackStartupTimeout bool
	validateIndexerProxyURL        bool
	validateMovieSearchQueries     bool
	validateSeriesSearchQueries    bool
	validateDeviceAssignments      bool
	validateProviders              bool
	validateIndexers               bool
	providerDeletionOnly           bool
	indexerDeletionOnly            bool
	changedProviderIndexes         map[int]bool
	changedIndexerIndexes          map[int]bool
	validateTMDBAPIKey             bool
	validateTVDBAPIKey             bool
	validateSpeculativePreProbing  bool
	validateFilterProfiles         bool
	validateMetadataProfiles       bool
	validateFormatProfiles         bool
	validateDatabase               bool
}

func fullConfigValidationPlan() configValidationPlan {
	return configValidationPlan{
		validateKeepLogFiles:           true,
		validateNZBHistoryRetention:    true,
		validatePlaybackStartupTimeout: true,
		validateIndexerProxyURL:        true,
		validateMovieSearchQueries:     true,
		validateSeriesSearchQueries:    true,
		validateDeviceAssignments:      true,
		validateProviders:              true,
		validateIndexers:               true,
		validateTMDBAPIKey:             true,
		validateTVDBAPIKey:             true,
		validateSpeculativePreProbing:  true,
		validateFilterProfiles:         true,
		validateMetadataProfiles:       true,
		validateFormatProfiles:         true,
		validateDatabase:               true,
	}
}

type cacheClearScope int

const (
	cacheClearNone cacheClearScope = iota
	cacheClearPlaylist
	cacheClearSearch
)

// patchKeysNoCacheImpact are config fields that affect neither raw indexer
// results nor the filtered/sorted playlists built from them, so saving them
// invalidates nothing.
var patchKeysNoCacheImpact = map[string]bool{
	"log_level":                           true,
	"verbose_nntp_logging":                true,
	"search_debug_stream":                 true,
	"keep_log_files":                      true,
	"nzb_history_retention_days":          true,
	"playback_startup_timeout_seconds":    true,
	"session_ttl_minutes":                 true,
	"session_post_playback_ttl_minutes":   true,
	"speculative_preprobing_max_attempts": true,
	"speculative_pre_probing_count":       true,
	"memory_limit_mb":                     true,
	"mute_error_video":                    true,
	"admin_username":                      true,
	"proxy_enabled":                       true,
	"proxy_host":                          true,
	"proxy_port":                          true,
	"proxy_auth_user":                     true,
	"proxy_auth_pass":                     true,
	"newznab_enabled":                     true,
	"newznab_api_key":                     true,
	"indexer_query_header":                true,
	"indexer_grab_header":                 true,
	"provider_header":                     true,
	"library_auto_save":                   true,
	"library_max_items":                   true,
	"library_max_size_mb":                 true,
	"ffprobe_path":                        true,
	"database_driver":                     true,
	"database_url":                        true,
	// Result formatting is applied when a response is rendered from the
	// cached playlist, so profile edits need no cache invalidation.
	"format_profiles": true,
}

// patchKeysPlaylistOnly are config fields that change how cached raw results
// are filtered, sorted, or annotated — the playlists must be rebuilt but the
// raw indexer results stay valid.
//
// metadata_profiles is deliberately NOT here: cached rawSearchResults embed
// the per-stream certification-gate outcome, so a profile save must fall
// through to the full search-cache clear or a tightened cap could keep
// serving pre-cap results for the cache TTL.
var patchKeysPlaylistOnly = map[string]bool{
	"filter_profiles": true,
	"availnzb_mode":   true,
}

// validationPlanFromPatch narrows validation to the fields a patch actually
// touches; unparseable or full-config saves validate everything.
func validationPlanFromPatch(body []byte, currentCfg, nextCfg *config.Config) configValidationPlan {
	plan := fullConfigValidationPlan()
	if len(body) == 0 || currentCfg == nil || nextCfg == nil {
		return plan
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || len(raw) == 0 {
		return plan
	}

	plan = configValidationPlan{}

	if _, ok := raw["keep_log_files"]; ok {
		plan.validateKeepLogFiles = true
	}
	if _, ok := raw["nzb_history_retention_days"]; ok {
		plan.validateNZBHistoryRetention = true
	}
	if _, ok := raw["playback_startup_timeout_seconds"]; ok {
		plan.validatePlaybackStartupTimeout = true
	}
	if _, ok := raw["speculative_pre_probing_count"]; ok {
		plan.validateSpeculativePreProbing = true
	}
	if _, ok := raw["indexer_proxy_url"]; ok {
		plan.validateIndexerProxyURL = true
	}
	_, patchedDriver := raw["database_driver"]
	_, patchedDatabaseURL := raw["database_url"]
	if patchedDriver || patchedDatabaseURL {
		plan.validateDatabase = true
	}
	if _, ok := raw["movie_search_queries"]; ok {
		plan.validateMovieSearchQueries = true
		plan.validateDeviceAssignments = true
	}
	if _, ok := raw["series_search_queries"]; ok {
		plan.validateSeriesSearchQueries = true
		plan.validateDeviceAssignments = true
	}
	if _, ok := raw["providers"]; ok {
		plan.validateProviders = true
		if len(nextCfg.Providers) < len(currentCfg.Providers) {
			plan.providerDeletionOnly = true
		} else {
			plan.changedProviderIndexes = changedIndexes(currentCfg.Providers, nextCfg.Providers)
		}
	}
	if _, ok := raw["indexers"]; ok {
		plan.validateIndexers = true
		if len(nextCfg.Indexers) < len(currentCfg.Indexers) {
			plan.indexerDeletionOnly = true
		} else {
			plan.changedIndexerIndexes = changedIndexes(currentCfg.Indexers, nextCfg.Indexers)
		}
	}
	if _, ok := raw["tmdb_api_key"]; ok {
		plan.validateTMDBAPIKey = true
	}
	if _, ok := raw["tvdb_api_key"]; ok {
		plan.validateTVDBAPIKey = true
	}
	if _, ok := raw["filter_profiles"]; ok {
		plan.validateFilterProfiles = true
		plan.validateDeviceAssignments = true
	}
	if _, ok := raw["metadata_profiles"]; ok {
		plan.validateMetadataProfiles = true
		plan.validateDeviceAssignments = true
	}
	if _, ok := raw["format_profiles"]; ok {
		plan.validateFormatProfiles = true
		plan.validateDeviceAssignments = true
	}

	return plan
}

// databaseVerifyTimeout bounds the connection probe so a save against an
// unreachable host fails fast instead of hanging the request.
const databaseVerifyTimeout = 5 * time.Second

// validateDatabaseSettings checks the persistence settings and dials the server
// when Postgres is selected, so an unusable connection string is rejected at
// save time rather than stopping the next startup. It returns the field the
// error belongs to.
func validateDatabaseSettings(cfg *config.Config) (string, error) {
	settings := persistence.Settings{Backend: cfg.DatabaseDriver, DSN: cfg.DatabaseURL}
	if err := persistence.ValidateSettings(settings); err != nil {
		if strings.TrimSpace(cfg.DatabaseURL) == "" {
			return "database_url", err
		}
		return "database_driver", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseVerifyTimeout)
	defer cancel()
	if err := persistence.VerifySettings(ctx, settings); err != nil {
		return "database_url", err
	}
	return "", nil
}

func changedIndexes[T any](current, next []T) map[int]bool {
	changed := make(map[int]bool)
	for i := range next {
		if i >= len(current) || !reflect.DeepEqual(current[i], next[i]) {
			changed[i] = true
		}
	}
	return changed
}

func pingIndexerWithTimeout(indexerCfg config.IndexerConfig) error {
	pingTimeout := indexerCfg.EffectiveTimeout()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), pingTimeout)
	defer pingCancel()
	ping := func() error {
		if strings.EqualFold(indexerCfg.Type, "easynews") {
			client, err := easynews.NewClient(
				indexerCfg.Username,
				indexerCfg.Password,
				indexerCfg.Name,
				"",
				indexerCfg.APIHitsDay,
				indexerCfg.DownloadsDay,
				indexerCfg.RateLimitRPS,
				indexerCfg.EffectiveTimeoutSeconds(),
				indexerCfg.ProxyURL,
				indexerCfg.QueryHeader,
				indexerCfg.GrabHeader,
				nil,
			)
			if err != nil {
				return err
			}
			return client.Ping(pingCtx)
		}
		return newznab.NewClient(indexerCfg, nil).Ping(pingCtx)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- ping() }()
	select {
	case err := <-errCh:
		return err
	case <-time.After(pingTimeout):
		return fmt.Errorf("connection timeout after %v", pingTimeout)
	}
}

func verifyGlobalIndexerProxy(cfg *config.Config) error {
	if cfg == nil || strings.TrimSpace(cfg.IndexerProxyURL) == "" {
		return nil
	}

	type probeResult struct {
		name string
		err  error
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempted int
	samples := make([]string, 0, 3)
	resultCh := make(chan probeResult, len(cfg.Indexers))
	var wg sync.WaitGroup

	for _, idx := range cfg.Indexers {
		if idx.Enabled != nil && !*idx.Enabled {
			continue
		}
		if strings.EqualFold(idx.Type, "easynews") {
			if strings.TrimSpace(idx.Username) == "" || strings.TrimSpace(idx.Password) == "" {
				continue
			}
		} else {
			if strings.TrimSpace(idx.URL) == "" || strings.Contains(idx.APIPath, "{indexer_id}") {
				continue
			}
		}

		testCfg := idx
		testCfg.ProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
		attempted++
		wg.Add(1)
		go func(testCfg config.IndexerConfig) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			default:
			}

			err := pingIndexerWithTimeout(testCfg)
			name := strings.TrimSpace(testCfg.Name)
			if name == "" {
				if strings.EqualFold(testCfg.Type, "easynews") {
					name = "easynews"
				} else {
					name = testCfg.URL
				}
			}
			select {
			case resultCh <- probeResult{name: name, err: err}:
			case <-ctx.Done():
			}
		}(testCfg)
	}

	if attempted == 0 {
		return nil
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for result := range resultCh {
		if result.err == nil {
			cancel()
			wg.Wait()
			return nil
		}
		if len(samples) < 3 {
			samples = append(samples, fmt.Sprintf("%s: %v", result.name, result.err))
		}
	}
	return fmt.Errorf("global proxy could not reach any enabled indexer (%s)", strings.Join(samples, " | "))
}

func (s *Server) validateConfigWithPlan(cfg *config.Config, plan configValidationPlan) map[string]string {
	errors := make(map[string]string)
	if plan.validateKeepLogFiles && (cfg.KeepLogFiles < 1 || cfg.KeepLogFiles > 50) {
		errors["keep_log_files"] = "Must be between 1 and 50"
	}
	if plan.validateNZBHistoryRetention && (cfg.NZBHistoryRetentionDays < 0 || cfg.NZBHistoryRetentionDays > 3650) {
		errors["nzb_history_retention_days"] = "Must be between 0 and 3650 days"
	}
	if plan.validatePlaybackStartupTimeout && (cfg.PlaybackStartupTimeoutSeconds < 1 || cfg.PlaybackStartupTimeoutSeconds > config.MaxPlaybackStartupTimeoutSeconds) {
		errors["playback_startup_timeout_seconds"] = "Must be between 1 and 60 seconds"
	}
	if plan.validateSpeculativePreProbing {
		count := cfg.EffectiveSpeculativePreProbingMaxAttempts()
		if count < 0 || count > 5 {
			errors["speculative_preprobing_max_attempts"] = "Must be between 0 and 5"
		}
	}
	if plan.validateDatabase {
		if field, err := validateDatabaseSettings(cfg); err != nil {
			errors[field] = err.Error()
		}
	}
	if plan.validateIndexerProxyURL {
		if err := config.ValidateIndexerProxyURL(cfg.IndexerProxyURL); err != nil {
			errors["indexer_proxy_url"] = err.Error()
		} else if err := verifyGlobalIndexerProxy(cfg); err != nil {
			errors["indexer_proxy_url"] = err.Error()
		}
	}
	validateSearchQueries := func(prefix string, queries []config.SearchQueryConfig) {
		seen := make(map[string]bool)
		for i, query := range queries {
			name := strings.TrimSpace(query.Name)
			if name == "" {
				errors[fmt.Sprintf("%s.%d.name", prefix, i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seen[key] {
					errors[fmt.Sprintf("%s.%d.name", prefix, i)] = "Name must be unique"
				}
				seen[key] = true
			}
			mode := strings.ToLower(strings.TrimSpace(query.SearchMode))
			if mode != "id" && mode != "text" {
				errors[fmt.Sprintf("%s.%d.search_mode", prefix, i)] = "Search mode must be id or text"
			}
			titleLanguages := config.NormalizeSearchTitleLanguages(query.SearchTitleLanguages)
			if mode == "id" && len(titleLanguages) == 0 {
				if rawSingle := strings.TrimSpace(query.SearchTitleLanguage); rawSingle != "" {
					titleLanguages = []string{config.NormalizeSearchTitleLanguage(rawSingle)}
				}
			}
			if mode == "text" && len(titleLanguages) > 1 {
				errors[fmt.Sprintf("%s.%d.search_title_languages", prefix, i)] = "Text search can use only one title language"
			}
			if mode == "id" && len(titleLanguages) == 0 {
				errors[fmt.Sprintf("%s.%d.search_title_languages", prefix, i)] = "Add at least one title language"
			}
			if prefix == "series_search_queries" {
				rawScope := strings.ToLower(strings.TrimSpace(query.SeriesSearchScope))
				if rawScope != "" {
					normalizedScope := config.NormalizeSeriesSearchScope(rawScope)
					switch rawScope {
					case normalizedScope,
						"absolute",
						"episode_param",
						"episode_query",
						"season_param",
						"season_query":
					default:
						errors[fmt.Sprintf("%s.%d.series_search_scope", prefix, i)] = "TV scope must be season_episode, season, or none"
					}
				}
			}
		}
	}

	if plan.validateMovieSearchQueries {
		validateSearchQueries("movie_search_queries", cfg.MovieSearchQueries)
	}
	if plan.validateSeriesSearchQueries {
		validateSearchQueries("series_search_queries", cfg.SeriesSearchQueries)
	}

	if plan.validateFilterProfiles {
		seen := make(map[string]bool)
		for i, fp := range cfg.FilterProfiles {
			name := strings.TrimSpace(fp.Name)
			if name == "" {
				errors[fmt.Sprintf("filter_profiles.%d.name", i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seen[key] {
					errors[fmt.Sprintf("filter_profiles.%d.name", i)] = "Name must be unique"
				}
				seen[key] = true
			}
			// A profile that fails to compile would be skipped by the ranking
			// reload, silently leaving its streams unfiltered — reject the save
			// instead so the bad pattern never reaches the config.
			if _, err := ranking.Compile(fp); err != nil {
				errors[fmt.Sprintf("filter_profiles.%d.ranking", i)] = err.Error()
			}
			for key, lim := range fp.Limits {
				if lim == nil {
					continue
				}
				path := fmt.Sprintf("filter_profiles.%d.limits.%s", i, key)
				if lim.MinSizeGB < 0 || lim.MaxSizeGB < 0 {
					errors[path] = "Size limits cannot be negative"
				} else if lim.MaxAgeDays < 0 {
					errors[path] = "Max age cannot be negative"
				} else if lim.MinGrabs < 0 {
					errors[path] = "Min grabs cannot be negative"
				}
			}
			// Bounds merge default-then-kind, so a contradiction can span two
			// entries — check what each kind actually resolves to.
			for _, kind := range config.LimitKinds {
				resolved := fp.LimitsForKind(kind)
				if resolved.MinSizeGB > 0 && resolved.MaxSizeGB > 0 && resolved.MinSizeGB > resolved.MaxSizeGB {
					errors[fmt.Sprintf("filter_profiles.%d.limits.%s", i, kind)] = "Min size cannot exceed max size"
				}
			}
		}
	}

	if plan.validateMetadataProfiles {
		// Search needs no per-profile validation: it rides hidden search-only
		// catalogs appended to every manifest, never the browse rows a
		// profile toggles here.
		knownCatalogIDs := make(map[string]bool)
		for _, def := range stremio.CatalogRegistry() {
			knownCatalogIDs[def.ID] = true
		}
		knownCertIDs := make(map[string]bool)
		for _, opt := range certification.Options() {
			knownCertIDs[opt.ID] = true
		}
		seen := make(map[string]bool)
		for i, mp := range cfg.MetadataProfiles {
			name := strings.TrimSpace(mp.Name)
			if name == "" {
				errors[fmt.Sprintf("metadata_profiles.%d.name", i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seen[key] {
					errors[fmt.Sprintf("metadata_profiles.%d.name", i)] = "Name must be unique"
				}
				seen[key] = true
			}
			if lang := strings.TrimSpace(mp.Language); lang != "" {
				if _, err := language.Parse(lang); err != nil {
					errors[fmt.Sprintf("metadata_profiles.%d.language", i)] = "Not a valid language tag"
				}
			}
			switch mp.SeriesSource {
			case "", "tvdb", "tmdb":
			default:
				errors[fmt.Sprintf("metadata_profiles.%d.series_source", i)] = "Unknown series source"
			}
			for j, toggle := range mp.Catalogs {
				if !knownCatalogIDs[toggle.ID] {
					errors[fmt.Sprintf("metadata_profiles.%d.catalogs.%d", i, j)] = "Unknown catalog id"
				}
			}
			if mp.MaxCertification != "" && !knownCertIDs[mp.MaxCertification] {
				errors[fmt.Sprintf("metadata_profiles.%d.max_certification", i)] = "Unknown rating limit"
			}
		}
	}

	if plan.validateFormatProfiles {
		seen := make(map[string]bool)
		for i, fp := range cfg.FormatProfiles {
			name := strings.TrimSpace(fp.Name)
			if name == "" {
				errors[fmt.Sprintf("format_profiles.%d.name", i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seen[key] {
					errors[fmt.Sprintf("format_profiles.%d.name", i)] = "Name must be unique"
				}
				seen[key] = true
			}
			// A template that fails to parse would silently render the
			// built-in format — reject the save instead.
			if err := stremio.ValidateResultTemplates(fp.ResultNameTemplate, fp.ResultDescriptionTemplate); err != nil {
				errors[fmt.Sprintf("format_profiles.%d.templates", i)] = err.Error()
			}
		}
	}

	if plan.validateDeviceAssignments {
		movieQueryNames := lowerNameSet(cfg.MovieSearchQueries, func(x config.SearchQueryConfig) string { return x.Name })
		seriesQueryNames := lowerNameSet(cfg.SeriesSearchQueries, func(x config.SearchQueryConfig) string { return x.Name })
		filterProfileNames := lowerNameSet(cfg.FilterProfiles, func(x config.FilterProfileConfig) string { return x.Name })
		metadataProfileNames := lowerNameSet(cfg.MetadataProfiles, func(x config.MetadataProfileConfig) string { return x.Name })
		formatProfileNames := lowerNameSet(cfg.FormatProfiles, func(x config.FormatProfileConfig) string { return x.Name })
		for username, stream := range cfg.Streams {
			if stream == nil {
				continue
			}
			for i, name := range stream.MovieSearchQueries {
				if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" && !movieQueryNames[normalized] {
					errors[fmt.Sprintf("streams.%s.movie_search_queries.%d", username, i)] = "Assigned movie search query does not exist"
				}
			}
			for i, name := range stream.SeriesSearchQueries {
				if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" && !seriesQueryNames[normalized] {
					errors[fmt.Sprintf("streams.%s.series_search_queries.%d", username, i)] = "Assigned show search query does not exist"
				}
			}
			if fpName := strings.ToLower(strings.TrimSpace(stream.FilterProfileName)); fpName != "" && !filterProfileNames[fpName] {
				errors[fmt.Sprintf("streams.%s.filter_profile_name", username)] = "Assigned filter profile does not exist"
			}
			for kind, name := range stream.FilterProfileByType {
				if fpName := strings.ToLower(strings.TrimSpace(name)); fpName != "" && !filterProfileNames[fpName] {
					errors[fmt.Sprintf("streams.%s.filter_profile_by_type.%s", username, kind)] = "Assigned filter profile does not exist"
				}
			}
			if mpName := strings.ToLower(strings.TrimSpace(stream.MetadataProfileName)); mpName != "" && !metadataProfileNames[mpName] {
				errors[fmt.Sprintf("streams.%s.metadata_profile_name", username)] = "Assigned metadata profile does not exist"
			}
			if fpName := strings.ToLower(strings.TrimSpace(stream.FormatProfileName)); fpName != "" && !formatProfileNames[fpName] {
				errors[fmt.Sprintf("streams.%s.format_profile_name", username)] = "Assigned format profile does not exist"
			}
		}
	}

	var mu sync.Mutex
	setErr := func(key, msg string) {
		mu.Lock()
		errors[key] = msg
		mu.Unlock()
	}
	var wg sync.WaitGroup

	if plan.validateProviders && !plan.providerDeletionOnly {
		for i, p := range cfg.Providers {
			if len(plan.changedProviderIndexes) > 0 && !plan.changedProviderIndexes[i] {
				continue
			}
			wg.Add(1)
			go func(idx int, provider config.Provider) {
				defer wg.Done()
				if provider.Enabled != nil && !*provider.Enabled {
					return
				}
				if provider.Host == "" {
					setErr(fmt.Sprintf("providers.%d.host", idx), "Host is required")
					return
				}
				pool := nntp.NewClientPool(provider.Host, provider.Port, provider.UseSSL, provider.Username, provider.Password, 1)
				if err := pool.Validate(); err != nil {
					setErr(fmt.Sprintf("providers.%d.host", idx), err.Error())
				}
			}(i, p)
		}
	}

	if plan.validateIndexers && !plan.indexerDeletionOnly {
		for i, idx := range cfg.Indexers {
			if len(plan.changedIndexerIndexes) > 0 && !plan.changedIndexerIndexes[i] {
				continue
			}
			wg.Add(1)
			go func(index int, indexerCfg config.IndexerConfig) {
				defer wg.Done()
				if indexerCfg.Enabled != nil && !*indexerCfg.Enabled {
					return
				}
				if strings.EqualFold(indexerCfg.Type, "easynews") {
					if indexerCfg.Username == "" {
						setErr(fmt.Sprintf("indexers.%d.username", index), "Username is required")
					}
					if indexerCfg.Password == "" {
						setErr(fmt.Sprintf("indexers.%d.password", index), "Password is required")
					}
					if err := config.ValidateIndexerProxyURL(indexerCfg.ProxyURL); err != nil {
						setErr(fmt.Sprintf("indexers.%d.proxy_url", index), err.Error())
						return
					}
					testCfg := indexerCfg
					effectiveProxyURL := strings.TrimSpace(indexerCfg.ProxyURL)
					if effectiveProxyURL == "" && plan.validateIndexerProxyURL {
						effectiveProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
					}
					testCfg.ProxyURL = effectiveProxyURL
					if err := pingIndexerWithTimeout(testCfg); err != nil {
						setErr(fmt.Sprintf("indexers.%d.username", index), err.Error())
					}
					return
				}
				if indexerCfg.URL == "" {
					setErr(fmt.Sprintf("indexers.%d.url", index), "URL is required")
					return
				}
				if strings.Contains(indexerCfg.APIPath, "{indexer_id}") {
					setErr(fmt.Sprintf("indexers.%d.api_path", index), "Replace {indexer_id} with the Prowlarr indexer ID (for example 1/api)")
					return
				}
				if err := config.ValidateIndexerProxyURL(indexerCfg.ProxyURL); err != nil {
					setErr(fmt.Sprintf("indexers.%d.proxy_url", index), err.Error())
					return
				}
				testCfg := indexerCfg
				effectiveProxyURL := strings.TrimSpace(indexerCfg.ProxyURL)
				if effectiveProxyURL == "" && plan.validateIndexerProxyURL {
					effectiveProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
				}
				testCfg.ProxyURL = effectiveProxyURL
				err := pingIndexerWithTimeout(testCfg)
				if err != nil {
					setErr(fmt.Sprintf("indexers.%d.url", index), err.Error())
				}
			}(i, idx)
		}
	}

	if plan.validateTMDBAPIKey && strings.TrimSpace(cfg.TMDBAPIKey) != "" {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			client := tmdb.NewClient(key)
			if err := client.Ping(); err != nil {
				setErr("tmdb_api_key", err.Error())
			}
		}(strings.TrimSpace(cfg.TMDBAPIKey))
	}

	if plan.validateTVDBAPIKey && strings.TrimSpace(cfg.TVDBAPIKey) != "" {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			client := tvdb.NewClient(key, paths.GetDataDir())
			if err := client.Ping(); err != nil {
				setErr("tvdb_api_key", err.Error())
			}
		}(strings.TrimSpace(cfg.TVDBAPIKey))
	}

	wg.Wait()
	return errors
}
