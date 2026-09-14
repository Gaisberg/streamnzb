package config

import (
	"slices"
	"strings"
)

// DefaultMetadataProfileName is the profile seeded by the one-shot migration
// from the legacy global metadata section, and the admin token's fallback.
const DefaultMetadataProfileName = "Default"

// MetadataProfileConfig is one named metadata profile. Streams bind a profile
// by name (StreamEntry.MetadataProfileName); a stream with no binding serves
// the classic stream-only manifest — binding is what enables the meta and
// catalog resources for that stream. Profiles are global: the stream decides
// which one to use, the profile carries no stream scoping.
type MetadataProfileConfig struct {
	Name string `json:"name"`

	// Catalogs lists the enabled registry catalogs in display order. nil means
	// "never configured" (registry defaults apply); an explicitly saved empty
	// list means none — the tag must not be omitempty or the two collapse on
	// save. Unknown ids are ignored read-side.
	Catalogs []CatalogToggle `json:"catalogs"`

	// ExternalCatalogs are selected browse rows from public Stremio manifests.
	// They deliberately carry no credentials and do not import remote search,
	// stream, subtitle, or configuration resources.
	ExternalCatalogs []ExternalCatalogConfig `json:"external_catalogs,omitempty"`

	// Per-media-type meta sources, in priority order: the first one that can
	// serve a title is used, and the rest are fallbacks tried in order. Movies,
	// series and anime are fully independent — choosing sources for one never
	// affects the others. Empty means that media type's default order; unknown
	// and duplicate entries are dropped read-side.
	//
	// Cinemeta is opt-in only: it appears in no default order, so a profile
	// that never sets these fields behaves exactly as before Cinemeta support
	// existed (TMDB-only movies, TVDB then TMDB series, Kitsu then TVDB anime).
	MovieSources  []string `json:"movie_sources,omitempty"`
	SeriesSources []string `json:"series_sources,omitempty"`
	AnimeSources  []string `json:"anime_sources,omitempty"`

	// Deprecated: the primary/backup pair these lists replaced, read once on
	// load to migrate a profile saved by a pre-list build and never written
	// back. Only ever written by unreleased builds, so this can be deleted
	// after one release.
	MovieSource        string `json:"movie_source,omitempty"`
	MovieBackupSource  string `json:"movie_backup_source,omitempty"`
	SeriesSource       string `json:"series_source,omitempty"`
	SeriesBackupSource string `json:"series_backup_source,omitempty"`
	AnimeSource        string `json:"anime_source,omitempty"`
	AnimeBackupSource  string `json:"anime_backup_source,omitempty"`

	// TVMazeAirDates lets TVMaze override episode air dates (and drive the
	// unaired-episode gate). nil means enabled.
	TVMazeAirDates *bool `json:"tvmaze_air_dates,omitempty"`

	// Language is the display language for meta responses and catalog rows,
	// as a TMDB-style tag ("de-DE"). Empty means English.
	Language string `json:"language,omitempty"`

	// PosterURLPattern, when set, swaps poster URLs for an overlay service's
	// (BetterPosters, RatingPosterDB): an http(s) URL template whose {imdb_id}
	// placeholder is substituted per title. Overlay services key on IMDb ids,
	// so titles resolvable only to kitsu:/tvdb:/tmdb: ids keep their source
	// artwork. Empty means posters come from the metadata sources untouched.
	PosterURLPattern string `json:"poster_url_pattern,omitempty"`

	// MaxCertification caps content by age certification, as an id from
	// certification.Options ("0", "7", "13", "16", "18"). Empty means no cap.
	// The cap gates catalogs, meta pages and stream resolution.
	MaxCertification string `json:"max_certification,omitempty"`
	// AllowUnrated lets content with no known certification through a capped
	// profile. nil means false: unknown certifications fail closed, because
	// this is a parental control — the deliberate opposite of the fail-open
	// doctrine release limits follow.
	AllowUnrated *bool `json:"allow_unrated,omitempty"`

	// HideIncompleteMetadata drops rows no source could describe well enough
	// to render — in practice, the ones with no artwork at all. nil means
	// enabled: an unrenderable row is noise in every client.
	HideIncompleteMetadata *bool `json:"hide_incomplete_metadata,omitempty"`

	// UnreleasedWindowDays is how far ahead of today a title may be scheduled
	// and still appear in catalog rows and search results. nil means the
	// default window; 0 shows only what has been released. A title whose
	// source publishes no date at all is never hidden by this — the window
	// filters announcements, not records with missing data.
	UnreleasedWindowDays *int `json:"unreleased_window_days,omitempty"`
}

// DefaultUnreleasedWindowDays is the out-of-the-box horizon: a month of
// upcoming titles, which covers what is about to air without filling a board
// with announcements years out. MaxUnreleasedWindowDays is a year, the far
// end of the editor's slider.
const (
	DefaultUnreleasedWindowDays = 30
	MaxUnreleasedWindowDays     = 365
)

// EffectiveHideIncompleteMetadata reports whether rows without enough
// metadata to render are dropped. Unset means on.
func (p *MetadataProfileConfig) EffectiveHideIncompleteMetadata() bool {
	return p == nil || p.HideIncompleteMetadata == nil || *p.HideIncompleteMetadata
}

// EffectiveUnreleasedWindowDays is the profile's horizon in days, never
// negative.
func (p *MetadataProfileConfig) EffectiveUnreleasedWindowDays() int {
	if p == nil || p.UnreleasedWindowDays == nil {
		return DefaultUnreleasedWindowDays
	}
	return min(max(*p.UnreleasedWindowDays, 0), MaxUnreleasedWindowDays)
}

// ExternalCatalogConfig records one chosen catalog row rather than an entire
// addon. ID is a locally generated stable key; ManifestURL is always the
// public manifest URL the administrator pasted; RemoteType and RemoteID are
// the exact resource coordinates declared by that manifest.
type ExternalCatalogConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	// SourceLabel is the human-facing identity of the pasted source. Kind is
	// only a dispatch detail and must not appear in the editor as "external".
	SourceLabel string `json:"source_label,omitempty"`
	ManifestURL string `json:"manifest_url"`
	RemoteType  string `json:"remote_type"`
	RemoteID    string `json:"remote_id"`
	// SupportsSkip records the pasted manifest's explicit pagination contract.
	// Nil is a legacy row saved before this field existed and retains the old
	// behavior until it is re-added through the inspected-source flow.
	SupportsSkip *bool `json:"supports_skip,omitempty"`
}

// Metadata source orders. Each list is the media type's sources in priority
// order; the first that can serve a title wins and the rest are fallbacks.
// The defaults reproduce the behavior that predates per-profile selection, and
// deliberately contain no Cinemeta: it carries no age-rating data, so a title
// served from it is hidden under a rating limit unless the profile allows
// unrated content. Nothing may opt a profile into it but the profile itself.
var (
	movieMetaSources  = []string{"tmdb", "cinemeta"}
	seriesMetaSources = []string{"tvdb", "tmdb", "cinemeta"}
	animeMetaSources  = []string{"kitsu", "tvdb"}

	defaultMovieMetaSources  = []string{"tmdb"}
	defaultSeriesMetaSources = []string{"tvdb", "tmdb"}
	defaultAnimeMetaSources  = []string{"kitsu", "tvdb"}
)

// MovieMetaSourceOptions, SeriesMetaSourceOptions and AnimeMetaSourceOptions
// are the sources each media type may list, for validation and for the editor.
func MovieMetaSourceOptions() []string { return append([]string(nil), movieMetaSources...) }

// MetaSourceOptions is the same lists addressed by media type, for callers
// that iterate every type.
func MetaSourceOptions(mediaType string) []string {
	switch mediaType {
	case "movie":
		return MovieMetaSourceOptions()
	case "anime":
		return AnimeMetaSourceOptions()
	default:
		return SeriesMetaSourceOptions()
	}
}
func SeriesMetaSourceOptions() []string { return append([]string(nil), seriesMetaSources...) }
func AnimeMetaSourceOptions() []string  { return append([]string(nil), animeMetaSources...) }

// effectiveMetaSources filters a configured order down to the recognized
// sources, dropping unknown entries and repeats while keeping the user's
// order, and falls back to fallback when nothing usable is left. A profile
// listing only sources this build does not know still serves metadata rather
// than none.
func effectiveMetaSources(configured, allowed, fallback []string) []string {
	out := make([]string, 0, len(configured))
	seen := make(map[string]bool, len(configured))
	for _, raw := range configured {
		source := strings.ToLower(strings.TrimSpace(raw))
		if seen[source] || !slices.Contains(allowed, source) {
			continue
		}
		seen[source] = true
		out = append(out, source)
	}
	if len(out) == 0 {
		return append([]string(nil), fallback...)
	}
	return out
}

// EffectiveMovieMetaSources returns the movie meta providers in priority
// order, defaulting to TMDB alone.
func (p *MetadataProfileConfig) EffectiveMovieMetaSources() []string {
	if p == nil {
		return append([]string(nil), defaultMovieMetaSources...)
	}
	return effectiveMetaSources(p.MovieSources, movieMetaSources, defaultMovieMetaSources)
}

// EffectiveSeriesMetaSources returns the series meta providers in priority
// order, defaulting to TVDB then TMDB — the historical pair.
func (p *MetadataProfileConfig) EffectiveSeriesMetaSources() []string {
	if p == nil {
		return append([]string(nil), defaultSeriesMetaSources...)
	}
	return effectiveMetaSources(p.SeriesSources, seriesMetaSources, defaultSeriesMetaSources)
}

// EffectiveAnimeMetaSources returns the anime meta providers in priority
// order, defaulting to Kitsu then TVDB. Whichever serves the show's metadata,
// Kitsu keeps the playback identity: episode ids stay kitsu:<id>:<ep>.
func (p *MetadataProfileConfig) EffectiveAnimeMetaSources() []string {
	if p == nil {
		return append([]string(nil), defaultAnimeMetaSources...)
	}
	return effectiveMetaSources(p.AnimeSources, animeMetaSources, defaultAnimeMetaSources)
}

// EffectiveSeriesMetaSource returns just the series source that serves first,
// for callers that only need to know which one leads.
func (p *MetadataProfileConfig) EffectiveSeriesMetaSource() string {
	return p.EffectiveSeriesMetaSources()[0]
}

// EffectiveAnimeMetaSource returns just the anime source that serves first.
func (p *MetadataProfileConfig) EffectiveAnimeMetaSource() string {
	return p.EffectiveAnimeMetaSources()[0]
}

// MigrateLegacySourcePairs converts the primary/backup fields that preceded
// the priority lists into the order that replaced them, then clears them so
// the next save writes only the list. A profile that already has lists keeps
// them.
//
// The pair carried an implicit fallback the list has to spell out: a legacy
// series_source of "tmdb" meant "TMDB, then TVDB", so migrating it to ["tmdb"]
// alone would quietly delete a fallback the profile has always had. Each media
// type below therefore reproduces the exact defaulting its old accessor did.
func (p *MetadataProfileConfig) MigrateLegacySourcePairs() {
	if p == nil {
		return
	}
	if p.MovieSource == "" && p.MovieBackupSource == "" &&
		p.SeriesSource == "" && p.SeriesBackupSource == "" &&
		p.AnimeSource == "" && p.AnimeBackupSource == "" {
		return
	}

	pick := func(value string, allowed []string, fallback string) string {
		if value = strings.ToLower(strings.TrimSpace(value)); slices.Contains(allowed, value) {
			return value
		}
		return fallback
	}
	order := func(primary, backup string) []string {
		if backup == "" || backup == primary {
			return []string{primary}
		}
		return []string{primary, backup}
	}

	if len(p.MovieSources) == 0 {
		// Movies never had an implicit backup: Cinemeta only ever ran when the
		// profile explicitly named it.
		primary := pick(p.MovieSource, movieMetaSources, "tmdb")
		p.MovieSources = order(primary, pick(p.MovieBackupSource, movieMetaSources, ""))
	}
	if len(p.SeriesSources) == 0 {
		primary := pick(p.SeriesSource, seriesMetaSources, "tvdb")
		// The implicit backup was whichever of the historical tvdb/tmdb pair
		// was not primary.
		implicit := "tmdb"
		if primary == "tmdb" {
			implicit = "tvdb"
		}
		p.SeriesSources = order(primary, pick(p.SeriesBackupSource, seriesMetaSources, implicit))
	}
	if len(p.AnimeSources) == 0 {
		primary := pick(p.AnimeSource, animeMetaSources, "kitsu")
		implicit := "tvdb"
		if primary == "tvdb" {
			implicit = "kitsu"
		}
		p.AnimeSources = order(primary, pick(p.AnimeBackupSource, animeMetaSources, implicit))
	}
	p.MovieSource, p.MovieBackupSource = "", ""
	p.SeriesSource, p.SeriesBackupSource = "", ""
	p.AnimeSource, p.AnimeBackupSource = "", ""
}

// NormalizeSources rewrites each order to exactly what the profile resolves
// to, dropping unknown entries and repeats, and clears a list that matches its
// media type's default.
//
// Storing "unset" rather than a materialized default is what makes the default
// mean something: a profile nobody has customized follows whatever the default
// order is in the build it runs on, instead of being frozen at the one that
// happened to be current when it was last saved. It also keeps the editor and
// the stored config agreeing on what an untouched profile looks like.
func (p *MetadataProfileConfig) NormalizeSources() {
	if p == nil {
		return
	}
	p.MigrateLegacySourcePairs()
	unsetIfDefault := func(effective, fallback []string) []string {
		if slices.Equal(effective, fallback) {
			return nil
		}
		return effective
	}
	p.MovieSources = unsetIfDefault(p.EffectiveMovieMetaSources(), defaultMovieMetaSources)
	p.SeriesSources = unsetIfDefault(p.EffectiveSeriesMetaSources(), defaultSeriesMetaSources)
	p.AnimeSources = unsetIfDefault(p.EffectiveAnimeMetaSources(), defaultAnimeMetaSources)
}

// EffectiveLanguage returns the profile's display language tag, or "" for the
// English default. en-US normalizes to "" — it is what the sources serve
// without a language anyway, so the default path stays parameter-free (and
// cache keys stay stable).
func (p *MetadataProfileConfig) EffectiveLanguage() string {
	if p == nil {
		return ""
	}
	lang := strings.TrimSpace(p.Language)
	if strings.EqualFold(lang, "en-US") || strings.EqualFold(lang, "en") {
		return ""
	}
	return lang
}

// EffectiveTVMazeAirDates reports whether TVMaze air-date overlays and gating
// are enabled for this profile. Default true.
func (p *MetadataProfileConfig) EffectiveTVMazeAirDates() bool {
	if p == nil || p.TVMazeAirDates == nil {
		return true
	}
	return *p.TVMazeAirDates
}

// PosterOverlayURL returns the overlay poster URL for one title, or "" when
// the profile has no pattern configured or the id is not an IMDb id — the
// caller keeps the source artwork in that case.
func (p *MetadataProfileConfig) PosterOverlayURL(imdbID string) string {
	if p == nil || !strings.HasPrefix(imdbID, "tt") {
		return ""
	}
	pattern := strings.TrimSpace(p.PosterURLPattern)
	if pattern == "" {
		return ""
	}
	return strings.ReplaceAll(pattern, "{imdb_id}", imdbID)
}

// EffectiveAllowUnrated reports whether unrated content passes this profile's
// certification cap. Default false (fail closed).
func (p *MetadataProfileConfig) EffectiveAllowUnrated() bool {
	return p != nil && p.AllowUnrated != nil && *p.AllowUnrated
}

// MetadataProfileByName finds a profile by name, case-insensitively. Returns
// nil when the name is empty or unknown.
func (c *Config) MetadataProfileByName(name string) *MetadataProfileConfig {
	if c == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	for i := range c.MetadataProfiles {
		if strings.EqualFold(c.MetadataProfiles[i].Name, name) {
			return &c.MetadataProfiles[i]
		}
	}
	return nil
}

// seedMetadataProfiles is the one-shot conversion of the legacy global
// metadata section into the profile list. nil MetadataProfiles means "never
// migrated" (the field has no omitempty, so once written it round-trips as []
// even when the user deletes every profile — that state is left alone).
//
// It runs before ApplyEnvOverrides so clearing the persisted master switch
// cannot clobber a METADATA_ENABLED override; stream binding is the separate
// bindDefaultMetadataProfile phase because the fresh-install default stream is
// only created later in LoadWithPath (applyStreamModelUpgradeDefaults).
func (c *Config) seedMetadataProfiles() bool {
	if c.MetadataProfiles != nil {
		return false
	}
	c.MetadataProfiles = []MetadataProfileConfig{{
		Name:           DefaultMetadataProfileName,
		Catalogs:       c.Metadata.Catalogs,
		MovieSource:    c.Metadata.MovieSource,
		SeriesSource:   c.Metadata.SeriesSource,
		AnimeSource:    c.Metadata.AnimeSource,
		TVMazeAirDates: c.Metadata.TVMazeAirDates,
		Language:       c.Metadata.Language,
	}}
	// The master switch's intent now lives in the bindings; clearing it keeps
	// EffectiveMetadataEnabled as a pure env kill-switch.
	c.Metadata.Enabled = nil
	return true
}

// bindDefaultMetadataProfile binds the seeded Default profile to every stream
// with no binding. Called only when the migration ran and the stored config
// had metadata effectively on — a config with metadata.enabled=false keeps
// its stream-only manifests while the settings stay recoverable in the seeded
// profile.
func (c *Config) bindDefaultMetadataProfile() {
	for _, entry := range c.Streams {
		if entry != nil && strings.TrimSpace(entry.MetadataProfileName) == "" {
			entry.MetadataProfileName = DefaultMetadataProfileName
		}
	}
}
