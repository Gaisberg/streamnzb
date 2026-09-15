// Package scrobble is the vocabulary the watch-tracking integrations share:
// one played title, addressed every way a target might need it, plus the
// interface the playback layer reports it through.
//
// The addressings are not interchangeable. Simkl places anime per MyAnimeList
// entry, numbering episodes the way the entry does; MDBList places everything
// by the aired series and its aired numbering. A session can resolve one and
// not the other, so an Item carries both and every target says for itself
// which items it can place.
package scrobble

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
)

// The verbs Target.Scrobble accepts: start marks the item watching-now, stop
// ends the session at the progress reached. Both services mark an item watched
// past 80% and keep a resume position below that, so there is no third verb
// for "finished".
const (
	VerbStart = "start"
	VerbStop  = "stop"
)

// Item is one played title, addressed for every target at once.
type Item struct {
	// ContentType is "movie", "series" or "anime". Anime is its own kind
	// because MAL-keyed targets address it per Kitsu/MAL entry rather than by
	// the aired series.
	ContentType string
	Title       string

	// The aired-series addressing: the ids and the season/episode numbers
	// releases are named by. For anime these are what anime-lists resolved the
	// Kitsu entry onto, which is all a target without MAL addressing can use.
	IMDbID  string
	TMDBID  string
	TVDBID  string
	Season  int
	Episode int

	// AnimeMovie marks an anime entry that is a film rather than a season, so
	// a target with no per-entry anime addressing places it as a movie instead
	// of looking for an episode it has no numbers for.
	AnimeMovie bool

	// MALID and MALEpisode address anime the way MAL-keyed targets do: per
	// entry, with the entry-local episode number MAL shares with Kitsu.
	MALID      string
	MALEpisode int
}

// HasMovieIDs reports whether the item can be placed as a film.
func (i Item) HasMovieIDs() bool { return i.IMDbID != "" || i.TMDBID != "" }

// HasAiredEpisode reports whether the item carries the aired-series addressing
// a target needs to place one episode.
func (i Item) HasAiredEpisode() bool {
	return (i.IMDbID != "" || i.TMDBID != "" || i.TVDBID != "") && i.Season >= 1 && i.Episode >= 1
}

// Addressable reports whether any target at all could place the item — a
// direct NZB play, or anime nothing has a mapping for, is addressable nowhere.
// A target still says for itself whether it can place this particular one.
func (i Item) Addressable() bool {
	switch i.ContentType {
	case "movie":
		return i.HasMovieIDs()
	case "anime":
		return i.MALID != "" || (i.AnimeMovie && i.HasMovieIDs()) || i.HasAiredEpisode()
	default:
		return i.HasAiredEpisode()
	}
}

// Target is one watch-tracking service playback is reported to.
type Target interface {
	// Name labels the target in logs.
	Name() string
	// Supports reports whether the target can address the item at all, so the
	// playback layer skips it silently instead of sending a report that was
	// never going to land.
	Supports(item Item) bool
	// Scrobble reports playback state for the item at the given progress
	// percentage. Implementations must be safe to call concurrently.
	Scrobble(ctx context.Context, verb string, item Item, progress float64) error
}

// ClampProgress folds a served-progress percentage into the 0–100 range both
// APIs document, at the two decimals they keep.
func ClampProgress(progress float64) float64 {
	return math.Round(math.Min(100, math.Max(0, progress))*100) / 100
}

// IDValue renders one id for a payload: both APIs' examples send numeric ids
// as numbers, so parseable ones go out that way.
func IDValue(id string) any {
	if n, err := strconv.Atoi(id); err == nil {
		return n
	}
	return id
}

// CredentialFingerprint pins a stored token to the client id it was authorized
// for, without persisting the id itself. Both services mint tokens per app, so
// a token left behind by a replaced client id cannot speak for the one in use
// now — it has to be re-linked rather than silently failing later.
func CredentialFingerprint(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:])
}
