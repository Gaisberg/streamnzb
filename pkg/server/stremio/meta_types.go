package stremio

// MetaVideo is one entry in a meta object's videos array — an episode for
// series-like content. ID is the exact id a client sends back on the stream
// request, so it must match what the stream handler parses: "<id>:<s>:<e>" for
// tt/tmdb series, "kitsu:<id>:<ep>" (season-less) for anime.
type MetaVideo struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	Released  string `json:"released,omitempty"` // ISO 8601
	Overview  string `json:"overview,omitempty"`
	Thumbnail string `json:"thumbnail,omitempty"`
}

// MetaObject is a Stremio meta resource object.
type MetaObject struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	Poster      string      `json:"poster,omitempty"`
	Background  string      `json:"background,omitempty"`
	Logo        string      `json:"logo,omitempty"`
	Description string      `json:"description,omitempty"`
	ReleaseInfo string      `json:"releaseInfo,omitempty"`
	IMDBRating  string      `json:"imdbRating,omitempty"`
	Runtime     string      `json:"runtime,omitempty"`
	Genres      []string    `json:"genres,omitempty"`
	Videos      []MetaVideo `json:"videos,omitempty"`
}

// MetaResponse is the /meta/{type}/{id}.json envelope. CacheMaxAge and
// StaleRevalidate are Stremio's client-side caching hints, in seconds.
type MetaResponse struct {
	Meta            *MetaObject `json:"meta"`
	CacheMaxAge     int         `json:"cacheMaxAge,omitempty"`
	StaleRevalidate int         `json:"staleRevalidate,omitempty"`
}

// MetaPreview is a catalog row entry.
type MetaPreview struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Poster      string `json:"poster,omitempty"`
	Description string `json:"description,omitempty"`
}

// CatalogResponse is the /catalog/{type}/{id}.json envelope.
type CatalogResponse struct {
	Metas       []MetaPreview `json:"metas"`
	CacheMaxAge int           `json:"cacheMaxAge,omitempty"`
}
