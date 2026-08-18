package stremio

import "streamnzb/pkg/core/config"

// CatalogDef is one catalog the addon can serve. The registry is the single
// source of truth for which catalogs exist: the manifest, the catalog handler,
// and the frontend Metadata page (via /api/metadata/catalogs) all derive from
// it, so config toggles referencing unknown ids are simply ignored.
type CatalogDef struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Name           string `json:"name"`
	Provider       string `json:"provider"` // "tmdb" | "tvdb" | "kitsu" | "local"
	SupportsSearch bool   `json:"supports_search"`
	SupportsSkip   bool   `json:"supports_skip"`
	DefaultEnabled bool   `json:"default_enabled"`
	// Kind selects the provider listing endpoint (trending, popular,
	// top_rated, discover, ...). Dispatch detail, not part of the API payload.
	Kind string `json:"-"`
	// DiscoverGenres is the TMDB with_genres filter for Kind "discover"
	// catalogs (comma-separated genre ids).
	DiscoverGenres string `json:"-"`
	// CertCeiling caps the catalog at an age certification by default, as an
	// id from certification.Options — what makes "Family Movies" family-safe
	// even on an uncapped profile. A capped profile tightens it further
	// (effective ceiling = min of the two); "" means no built-in ceiling.
	CertCeiling string `json:"-"`
}

// catalogRegistry lists every browse catalog the addon can serve, in default
// display order. IDs are stable config keys — renaming one orphans user
// toggles.
//
// None of these declare search: search rides the hidden searchCatalogs below,
// so a profile's search coverage never depends on which browse rows it picks.
//
// Defaults stay suppressed to one flagship trending row per media type, so a
// fresh install gets a clean board rather than every row at once; the rest
// are opt-in from the Metadata page.
var catalogRegistry = []CatalogDef{
	{ID: "tmdb.trending.movie", Type: "movie", Name: "Trending Movies", Provider: "tmdb", Kind: "trending", SupportsSkip: true, DefaultEnabled: true},
	{ID: "tmdb.trending.series", Type: "series", Name: "Trending Series", Provider: "tmdb", Kind: "trending", SupportsSkip: true, DefaultEnabled: true},
	{ID: "tmdb.popular.movie", Type: "movie", Name: "Popular Movies", Provider: "tmdb", Kind: "popular", SupportsSkip: true},
	{ID: "tmdb.popular.series", Type: "series", Name: "Popular Series", Provider: "tmdb", Kind: "popular", SupportsSkip: true},
	{ID: "tmdb.top_rated.movie", Type: "movie", Name: "Top Rated Movies", Provider: "tmdb", Kind: "top_rated", SupportsSkip: true},
	{ID: "tmdb.top_rated.series", Type: "series", Name: "Top Rated Series", Provider: "tmdb", Kind: "top_rated", SupportsSkip: true},
	{ID: "tmdb.now_playing.movie", Type: "movie", Name: "Now Playing in Theaters", Provider: "tmdb", Kind: "now_playing", SupportsSkip: true},
	{ID: "tmdb.upcoming.movie", Type: "movie", Name: "Upcoming Movies", Provider: "tmdb", Kind: "upcoming", SupportsSkip: true},
	{ID: "tmdb.on_the_air.series", Type: "series", Name: "Currently Airing Series", Provider: "tmdb", Kind: "on_the_air", SupportsSkip: true},
	// Discover-backed rows are filtered server-side (genre + certification),
	// so they stay dense under a rating cap instead of thinning out the way
	// post-filtered general rows do. TMDB genre ids: 10751 family, 16
	// animation, 10762 kids TV.
	{ID: "tmdb.family.movie", Type: "movie", Name: "Family Movies", Provider: "tmdb", Kind: "discover", DiscoverGenres: "10751", CertCeiling: "7", SupportsSkip: true},
	{ID: "tmdb.animated.movie", Type: "movie", Name: "Animated Movies", Provider: "tmdb", Kind: "discover", DiscoverGenres: "16", SupportsSkip: true},
	{ID: "tmdb.family.series", Type: "series", Name: "Family Series", Provider: "tmdb", Kind: "discover", DiscoverGenres: "10751", SupportsSkip: true},
	{ID: "tmdb.kids.series", Type: "series", Name: "Kids TV", Provider: "tmdb", Kind: "discover", DiscoverGenres: "10762", SupportsSkip: true},
	{ID: "tvdb.popular.series", Type: "series", Name: "Popular on TVDB", Provider: "tvdb", Kind: "popular", SupportsSkip: true},
	{ID: "tvdb.new.series", Type: "series", Name: "New on TVDB", Provider: "tvdb", Kind: "new", SupportsSkip: true},
	{ID: "kitsu.trending.anime", Type: "anime", Name: "Trending Anime", Provider: "kitsu", Kind: "trending", DefaultEnabled: true},
	{ID: "kitsu.top_rated.anime", Type: "anime", Name: "Top Rated Anime", Provider: "kitsu", Kind: "top_rated", SupportsSkip: true},
	{ID: "kitsu.popular.anime", Type: "anime", Name: "Most Popular Anime", Provider: "kitsu", Kind: "popular", SupportsSkip: true},
	// Kitsu filters age ratings server-side (filter[ageRating]), so this row
	// stays dense under a cap; the ceiling tightens from G,PG to G when the
	// profile caps below 7.
	{ID: "kitsu.kids.anime", Type: "anime", Name: "Kids Anime", Provider: "kitsu", Kind: "kids", CertCeiling: "7", SupportsSkip: true},
	{ID: "streamnzb.continue-watching.movie", Type: "movie", Name: "Continue Watching", Provider: "local", Kind: "continue-watching", SupportsSkip: true},
	{ID: "streamnzb.continue-watching.series", Type: "series", Name: "Continue Watching", Provider: "local", Kind: "continue-watching", SupportsSkip: true},
	{ID: "streamnzb.because-you-watched.movie", Type: "movie", Name: "Because You Watched", Provider: "local", Kind: "because-you-watched", SupportsSkip: true},
	{ID: "streamnzb.because-you-watched.series", Type: "series", Name: "Because You Watched", Provider: "local", Kind: "because-you-watched", SupportsSkip: true},
}

// searchCatalogs are the hidden per-type search carriers. The Stremio
// protocol has no standalone search resource — search rides catalogs — so
// these declare their search extra as REQUIRED, which tells clients to use
// them for search but never render them as board rows. One per content type:
// each declaring catalog adds a separate row on the client's search screen,
// and search results come from the provider's general search endpoint, not
// from any browse listing. Kept out of catalogRegistry so they never appear
// in the Metadata page, profile toggles, or cross-catalog dedup.
var searchCatalogs = []CatalogDef{
	{ID: "tmdb.search.movie", Type: "movie", Name: "Search Movies", Provider: "tmdb", Kind: "search", SupportsSearch: true},
	{ID: "tmdb.search.series", Type: "series", Name: "Search Series", Provider: "tmdb", Kind: "search", SupportsSearch: true},
	{ID: "kitsu.search.anime", Type: "anime", Name: "Search Anime", Provider: "kitsu", Kind: "search", SupportsSearch: true},
}

func searchCatalogDefByID(id string) (CatalogDef, bool) {
	for _, def := range searchCatalogs {
		if def.ID == id {
			return def, true
		}
	}
	return CatalogDef{}, false
}

// CatalogRegistry returns the browse-catalog registry for the frontend/API
// layer. The hidden search carriers are deliberately absent — they are not
// user-configurable.
func CatalogRegistry() []CatalogDef {
	out := make([]CatalogDef, len(catalogRegistry))
	copy(out, catalogRegistry)
	return out
}

func catalogDefByID(id string) (CatalogDef, bool) {
	for _, def := range catalogRegistry {
		if def.ID == id {
			return def, true
		}
	}
	return CatalogDef{}, false
}

// enabledCatalogDefs resolves a profile's toggles against the registry:
// profile order wins and unknown ids are dropped. A nil toggle list means
// "never configured" and gets the registry defaults, so a fresh profile works
// without visiting the catalog list; an explicitly saved empty list means
// none — a meta-only profile is legitimate. A nil profile (stream with no
// binding, or kill-switch down) has no catalogs at all.
func enabledCatalogDefs(profile *config.MetadataProfileConfig) []CatalogDef {
	if profile == nil {
		return nil
	}
	toggles := profile.Catalogs
	if toggles == nil {
		var defs []CatalogDef
		for _, def := range catalogRegistry {
			if def.DefaultEnabled {
				defs = append(defs, def)
			}
		}
		return defs
	}
	var defs []CatalogDef
	seen := make(map[string]bool, len(toggles))
	for _, t := range toggles {
		if !t.Enabled || seen[t.ID] {
			continue
		}
		if def, ok := catalogDefByID(t.ID); ok {
			seen[t.ID] = true
			defs = append(defs, def)
		}
	}
	return defs
}

// enabledCatalogs renders the profile's manifest entries: the enabled browse
// catalogs (with their skip extras), plus the hidden search carriers — their
// required search extra keeps them off the client's board while making every
// content type searchable regardless of which browse rows the profile picked.
func enabledCatalogs(profile *config.MetadataProfileConfig) []Catalog {
	if profile == nil {
		return nil
	}
	defs := enabledCatalogDefs(profile)
	catalogs := make([]Catalog, 0, len(defs)+len(searchCatalogs))
	for _, def := range defs {
		cat := Catalog{Type: def.Type, ID: def.ID, Name: def.Name}
		if def.SupportsSkip {
			cat.Extra = append(cat.Extra, CatalogExtra{Name: "skip"})
		}
		catalogs = append(catalogs, cat)
	}
	for _, def := range searchCatalogs {
		catalogs = append(catalogs, Catalog{
			Type:  def.Type,
			ID:    def.ID,
			Name:  def.Name,
			Extra: []CatalogExtra{{Name: "search", IsRequired: true}},
		})
	}
	return catalogs
}
