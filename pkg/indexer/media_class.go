package indexer

import (
	"context"
	"strings"
)

// Media header classes. An *arr stack talks to an indexer as two different
// programs: Sonarr for series, Radarr for films, and each of them both
// searches and fetches the NZB itself. StreamNZB can present the same split,
// so the class a request is about has to travel with it down to the HTTP
// call, including the NZB download that happens long after the search.
const (
	MediaClassSeries = "series"
	MediaClassMovie  = "movie"
)

type mediaClassKey struct{}

// WithMediaClass records which kind of content a request is about, for the
// indexer clients to pick the matching User-Agent. Anything that is not a
// film or a series (direct-play NZB URLs, unknown content) leaves the
// context untouched and the clients fall back to the plain headers.
func WithMediaClass(ctx context.Context, class string) context.Context {
	if c := MediaClassOf(class); c != "" {
		return context.WithValue(ctx, mediaClassKey{}, c)
	}
	return ctx
}

// MediaClassFromContext returns the class stored by WithMediaClass, or "".
func MediaClassFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	c, _ := ctx.Value(mediaClassKey{}).(string)
	return c
}

// MediaClassOf folds the vocabularies in use across the code base — search
// classes ("movie", "tv", "tv_anime"), Stremio content types ("movie",
// "series"), profile kinds ("anime_show", "anime_movie") — into the two the
// headers distinguish. Unknown words map to "".
func MediaClassOf(class string) string {
	switch strings.ToLower(strings.TrimSpace(class)) {
	case "movie", "movies", "anime_movie", "film":
		return MediaClassMovie
	case "series", "tv", "tv_anime", "show", "anime_show", "episode", "anime":
		return MediaClassSeries
	}
	return ""
}
