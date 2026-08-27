package config

import "strings"

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

	// Per-media-type meta sources. Empty means the default; unknown values
	// normalize to the default read-side. Today only series has a real choice
	// (TVDB default, TMDB alternative).
	MovieSource  string `json:"movie_source,omitempty"`
	SeriesSource string `json:"series_source,omitempty"`
	AnimeSource  string `json:"anime_source,omitempty"`

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
}

// EffectiveSeriesMetaSource returns the primary series meta source: "tvdb"
// (default) or "tmdb". Whichever is not primary stays the fallback.
func (p *MetadataProfileConfig) EffectiveSeriesMetaSource() string {
	if p != nil && p.SeriesSource == "tmdb" {
		return "tmdb"
	}
	return "tvdb"
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
