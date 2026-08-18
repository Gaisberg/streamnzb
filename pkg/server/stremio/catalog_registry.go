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
	// top_rated, ...). Dispatch detail, not part of the API payload.
	Kind string `json:"-"`
}

// catalogRegistry lists every catalog the addon can serve, in default display
// order. IDs are stable config keys — renaming one orphans user toggles.
//
// SupportsSearch stays on one catalog per type: each declared search extra adds
// a separate row on the client's search screen, so more would answer every
// search three times.
//
// Defaults stay suppressed to one flagship row per media type — the three
// search-carrying trending catalogs — so a fresh install gets a clean board
// (and working search) rather than every row at once; the rest are opt-in
// from the Metadata page.
var catalogRegistry = []CatalogDef{
	{ID: "tmdb.trending.movie", Type: "movie", Name: "Trending Movies", Provider: "tmdb", Kind: "trending", SupportsSearch: true, SupportsSkip: true, DefaultEnabled: true},
	{ID: "tmdb.trending.series", Type: "series", Name: "Trending Series", Provider: "tmdb", Kind: "trending", SupportsSearch: true, SupportsSkip: true, DefaultEnabled: true},
	{ID: "tmdb.popular.movie", Type: "movie", Name: "Popular Movies", Provider: "tmdb", Kind: "popular", SupportsSkip: true},
	{ID: "tmdb.popular.series", Type: "series", Name: "Popular Series", Provider: "tmdb", Kind: "popular", SupportsSkip: true},
	{ID: "tmdb.top_rated.movie", Type: "movie", Name: "Top Rated Movies", Provider: "tmdb", Kind: "top_rated", SupportsSkip: true},
	{ID: "tmdb.top_rated.series", Type: "series", Name: "Top Rated Series", Provider: "tmdb", Kind: "top_rated", SupportsSkip: true},
	{ID: "tmdb.now_playing.movie", Type: "movie", Name: "Now Playing in Theaters", Provider: "tmdb", Kind: "now_playing", SupportsSkip: true},
	{ID: "tmdb.upcoming.movie", Type: "movie", Name: "Upcoming Movies", Provider: "tmdb", Kind: "upcoming", SupportsSkip: true},
	{ID: "tmdb.on_the_air.series", Type: "series", Name: "Currently Airing Series", Provider: "tmdb", Kind: "on_the_air", SupportsSkip: true},
	{ID: "tvdb.popular.series", Type: "series", Name: "Popular on TVDB", Provider: "tvdb", Kind: "popular", SupportsSkip: true},
	{ID: "tvdb.new.series", Type: "series", Name: "New on TVDB", Provider: "tvdb", Kind: "new", SupportsSkip: true},
	{ID: "kitsu.trending.anime", Type: "anime", Name: "Trending Anime", Provider: "kitsu", Kind: "trending", SupportsSearch: true, DefaultEnabled: true},
	{ID: "kitsu.top_rated.anime", Type: "anime", Name: "Top Rated Anime", Provider: "kitsu", Kind: "top_rated", SupportsSkip: true},
	{ID: "kitsu.popular.anime", Type: "anime", Name: "Most Popular Anime", Provider: "kitsu", Kind: "popular", SupportsSkip: true},
	{ID: "streamnzb.continue-watching.movie", Type: "movie", Name: "Continue Watching", Provider: "local", Kind: "continue-watching", SupportsSkip: true},
	{ID: "streamnzb.continue-watching.series", Type: "series", Name: "Continue Watching", Provider: "local", Kind: "continue-watching", SupportsSkip: true},
	{ID: "streamnzb.because-you-watched.movie", Type: "movie", Name: "Because You Watched", Provider: "local", Kind: "because-you-watched", SupportsSkip: true},
	{ID: "streamnzb.because-you-watched.series", Type: "series", Name: "Because You Watched", Provider: "local", Kind: "because-you-watched", SupportsSkip: true},
}

// CatalogRegistry returns the registry for the frontend/API layer.
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

// enabledCatalogs renders the enabled catalogs as manifest entries, declaring
// the search/skip extras clients need to route search and pagination here.
func enabledCatalogs(profile *config.MetadataProfileConfig) []Catalog {
	defs := enabledCatalogDefs(profile)
	catalogs := make([]Catalog, 0, len(defs))
	for _, def := range defs {
		cat := Catalog{Type: def.Type, ID: def.ID, Name: def.Name}
		if def.SupportsSearch {
			cat.Extra = append(cat.Extra, CatalogExtra{Name: "search"})
		}
		if def.SupportsSkip {
			cat.Extra = append(cat.Extra, CatalogExtra{Name: "skip"})
		}
		catalogs = append(catalogs, cat)
	}
	return catalogs
}
