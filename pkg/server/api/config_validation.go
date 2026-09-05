package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"streamnzb/pkg/auth"
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
	"streamnzb/pkg/search/rules"
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
	validateDefineLibraries        bool
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
		validateDefineLibraries:        true,
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
	"preload_article_census":              true,
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
	"filter_profiles":  true,
	"define_libraries": true,
	"availnzb_mode":    true,
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
			plan.changedProviderIndexes = changedDialTargets(currentCfg.Providers, nextCfg.Providers)
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
	if _, ok := raw["define_libraries"]; ok {
		plan.validateDefineLibraries = true
		// A library edit can rename or remove a define that profiles
		// reference, so every profile recompiles against the new library set
		// — refusing the save beats silently skipping broken profiles at the
		// next reload.
		plan.validateFilterProfiles = true
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

// changedDialTargets picks out the providers whose server or account moved.
// Only those are worth a validation dial: a priority, backup or connection
// count edit changes nothing the server would answer differently, and the dial
// itself is a connection the provider counts against the account while
// playback may already be at the limit.
func changedDialTargets(current, next []config.Provider) map[int]bool {
	changed := make(map[int]bool)
	for i := range next {
		if i >= len(current) || !current[i].SameDialTarget(next[i]) {
			changed[i] = true
		}
	}
	return changed
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
	// Always checked: a bad entry would silently switch the feature off at
	// runtime, and the save is the only moment the admin is looking.
	if len(cfg.TrustedProxies) > 0 || strings.TrimSpace(cfg.TrustedProxyAuthHeader) != "" {
		if _, err := auth.NewProxyAuth(cfg.TrustedProxyAuthHeader, cfg.TrustedProxies); err != nil {
			errors["trusted_proxies"] = err.Error()
		} else if strings.TrimSpace(cfg.TrustedProxyAuthHeader) == "" {
			errors["trusted_proxy_auth_header"] = "Set the header the proxy sends (for example Remote-User), or clear trusted_proxies"
		} else if len(cfg.TrustedProxies) == 0 {
			errors["trusted_proxies"] = "List the proxy's address or network, or clear trusted_proxy_auth_header"
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
			// A plan is valid when every attempt in it is: one well-formed
			// question each, and at least one to ask. There are no cross-field
			// rules left to break — an attempt carries its own address,
			// target, language and year.
			isSeries := prefix == "series_search_queries"
			if len(query.Attempts) == 0 {
				errors[fmt.Sprintf("%s.%d.attempts", prefix, i)] = "Add at least one search attempt"
			}
			for j, attempt := range query.Attempts {
				field := fmt.Sprintf("%s.%d.attempts.%d", prefix, i, j)
				switch strings.ToLower(strings.TrimSpace(attempt.Address)) {
				case config.SearchAddressID, config.SearchAddressTitle:
				default:
					errors[field+".address"] = "Address must be id or title"
				}
				if !isSeries {
					if strings.TrimSpace(attempt.Target) != "" {
						errors[field+".target"] = "Movie attempts carry no target"
					}
					continue
				}
				switch strings.ToLower(strings.TrimSpace(attempt.Target)) {
				case config.SearchTargetEpisode, config.SearchTargetSeason,
					config.SearchTargetSeries, config.SearchTargetAbsolute:
				default:
					errors[field+".target"] = "Target must be episode, season, series, or absolute"
				}
				if config.NormalizeSearchAddress(attempt.Address) == config.SearchAddressID &&
					config.NormalizeSearchTarget(attempt.Target) == config.SearchTargetAbsolute {
					errors[field+".target"] = "An absolute episode number is a title query, not an id"
				}
			}
			switch strings.ToLower(strings.TrimSpace(query.Stop)) {
			case "", config.SearchStopFirstHit, config.SearchStopAll:
			case config.SearchStopEnoughHits:
				if query.MinHits < 1 {
					errors[fmt.Sprintf("%s.%d.min_hits", prefix, i)] = "Minimum hits must be at least 1"
				}
			default:
				errors[fmt.Sprintf("%s.%d.stop", prefix, i)] = "Stop must be first_hit, enough_hits, or all"
			}
			switch strings.ToLower(strings.TrimSpace(query.Order)) {
			case "", config.SearchOrderAsListed, config.SearchOrderAdaptiveSeason:
			default:
				errors[fmt.Sprintf("%s.%d.order", prefix, i)] = "Order must be as_listed or adaptive_season"
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
		library := config.DefineLibraryRules(cfg.DefineLibraries)
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
			if _, err := ranking.Compile(fp, library...); err != nil {
				errors[fmt.Sprintf("filter_profiles.%d.ranking", i)] = err.Error()
			}
			if err := fp.Source.Validate(config.FilterShareCodePrefix); err != nil {
				errors[fmt.Sprintf("filter_profiles.%d.source", i)] = err.Error()
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

	if plan.validateDefineLibraries {
		seenLibs := make(map[string]bool)
		// Define names are one namespace across every library: a profile's
		// matched("Name") could not say which library it meant, so a name two
		// libraries share is refused here, naming both sides.
		defineOwner := make(map[string]string)
		for i, lib := range cfg.DefineLibraries {
			name := strings.TrimSpace(lib.Name)
			if name == "" {
				errors[fmt.Sprintf("define_libraries.%d.name", i)] = "Name is required"
			} else {
				key := strings.ToLower(name)
				if seenLibs[key] {
					errors[fmt.Sprintf("define_libraries.%d.name", i)] = "Name must be unique"
				}
				seenLibs[key] = true
			}
			for _, rc := range lib.Rules {
				path := fmt.Sprintf("define_libraries.%d.rules", i)
				// A library is data for profiles to reference, never policy: a
				// score or reject rule riding in on a refresh would change what
				// every profile does without any profile saying so.
				if rc.EffectiveAction() != config.RuleActionDefine {
					errors[path] = fmt.Sprintf("Rule %q is a %s rule; a define library may only contain define rules", rc.Name, rc.EffectiveAction())
					break
				}
				ruleName := strings.ToLower(strings.TrimSpace(rc.Name))
				if ruleName == "" {
					errors[path] = "Every define needs a name; an unnamed one can never be referenced"
					break
				}
				if owner, taken := defineOwner[ruleName]; taken {
					if owner == name {
						errors[path] = fmt.Sprintf("More than one define is named %q", rc.Name)
					} else {
						errors[path] = fmt.Sprintf("Define %q is already in library %q", rc.Name, owner)
					}
					break
				}
				defineOwner[ruleName] = name
			}
			// A library compiles on its own: every define is validated whether
			// or not anything references it yet, and a define referencing a
			// profile's rules fails here — a library must stay self-contained
			// to serve profiles that do not have that rule.
			if _, err := rules.Compile(lib.Rules); err != nil {
				path := fmt.Sprintf("define_libraries.%d.rules", i)
				if _, taken := errors[path]; !taken {
					errors[path] = err.Error()
				}
			}
			if err := lib.Source.Validate(config.DefineShareCodePrefix); err != nil {
				errors[fmt.Sprintf("define_libraries.%d.source", i)] = err.Error()
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
			if pattern := strings.TrimSpace(mp.PosterURLPattern); pattern != "" {
				if !strings.Contains(pattern, "{imdb_id}") {
					errors[fmt.Sprintf("metadata_profiles.%d.poster_url_pattern", i)] = "Must contain the {imdb_id} placeholder"
				} else if u, err := url.Parse(strings.ReplaceAll(pattern, "{imdb_id}", "tt0000001")); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					errors[fmt.Sprintf("metadata_profiles.%d.poster_url_pattern", i)] = "Not a valid http(s) URL"
				}
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
			if err := fp.Source.Validate(config.FormatShareCodePrefix); err != nil {
				errors[fmt.Sprintf("format_profiles.%d.source", i)] = err.Error()
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
			// A nil map means "no diff computed — validate everything" (the
			// full plan); an empty map means the patch changed nothing, and
			// dialing every provider over an unrelated save is wasted traffic.
			if plan.changedProviderIndexes != nil && !plan.changedProviderIndexes[i] {
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
				// Registered under the same name the streaming pool would use,
				// so the dashboard folds this connection into the right row.
				pool.TrackAux(provider.PoolName())
				// Shutdown, not just Validate: without it the validation pool
				// leaked its reaper goroutine and held its idle connection —
				// a connection the provider counts against the account.
				defer pool.Shutdown()
				if err := pool.Validate(); err != nil {
					setErr(fmt.Sprintf("providers.%d.host", idx), err.Error())
				}
			}(i, p)
		}
	}

	if plan.validateIndexers && !plan.indexerDeletionOnly {
		for i, idx := range cfg.Indexers {
			// Same nil-vs-empty distinction as the providers above — every ping
			// here spends a real API hit at the indexer, so an unchanged entry
			// must not be probed just because the patch carried the array.
			if plan.changedIndexerIndexes != nil && !plan.changedIndexerIndexes[i] {
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
