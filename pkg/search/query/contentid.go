package query

import (
	"strconv"
	"strings"
)

// ContentID is everything a Stremio content id says on its own, before any
// metadata provider is asked anything.
//
// Stremio addresses content with a colon-separated id whose shape depends on
// which id space it came from, and the arities differ: a series carries a
// season and an episode, a movie does not, and a Kitsu entry numbers episodes
// within itself rather than within a season. Reading that wrong sends the whole
// search after the wrong episode, which is why it lives here — pure string
// work, testable without a server or a metadata client.
type ContentID struct {
	IMDbID  string
	TMDBID  string
	TVDBID  string
	KitsuID string
	// KitsuEpisode is the episode number as the Kitsu entry counts it, which is
	// not the series episode number until anime-lists has placed the entry.
	// Kept separate from Episode for that reason.
	KitsuEpisode string
	Season       string
	Episode      string
}

// ParseContentID reads a Stremio content id.
//
// The forms, all of which appear in practice:
//
//	tt1234567             a movie by IMDb id
//	tt1234567:2:3         a series episode by IMDb id
//	tmdb:1234             / tmdb:1234:2:3
//	tvdb:1234             / tvdb:1234:2:3
//	kitsu:1234            / kitsu:1234:7      (entry-relative episode)
//	1234                  a bare number is a TMDB id
//
// It never fails. An id that matches nothing recognisable comes back empty
// rather than as an error, which is the behaviour the search path has always
// had: an unusable id yields a search with no identifiers rather than a 500,
// and the layers above already handle finding nothing.
func ParseContentID(id string) ContentID {
	var out ContentID
	searchID := id

	switch {
	case strings.HasPrefix(id, "kitsu:"):
		parts := strings.Split(id, ":")
		if len(parts) >= 3 {
			out.KitsuID = parts[1]
			out.KitsuEpisode = parts[2]
			// Carried into Episode as well: an unmapped entry is searched by
			// the number the request actually gave.
			out.Episode = parts[2]
		} else if len(parts) >= 2 {
			out.KitsuID = parts[1]
		}
		// A kitsu id names no other id space, so the tt/numeric fallback below
		// must not run against the raw string.
		searchID = ""

	case strings.HasPrefix(id, "tvdb:"):
		parts := strings.Split(id, ":")
		if len(parts) >= 4 {
			out.TVDBID, searchID = parts[1], parts[1]
			out.Season, out.Episode = parts[2], parts[3]
		} else if len(parts) >= 2 {
			out.TVDBID, searchID = parts[1], parts[1]
		}

	case strings.HasPrefix(id, "tmdb:"):
		parts := strings.Split(id, ":")
		if len(parts) >= 4 {
			out.TMDBID, searchID = parts[1], parts[1]
			out.Season, out.Episode = parts[2], parts[3]
		} else if len(parts) >= 2 {
			out.TMDBID, searchID = parts[1], parts[1]
		}

	case strings.Contains(id, ":"):
		// No prefix but colons: an IMDb or bare-numeric id with a season and
		// episode hung off it.
		parts := strings.Split(id, ":")
		searchID = parts[0]
		if len(parts) >= 3 {
			out.Season, out.Episode = parts[1], parts[2]
		}
	}

	// The leading segment decides the id space it belongs to. The TVDB guard
	// matters: a tvdb id is numeric too, and without it a tvdb request would
	// also claim to carry the same number as a TMDB id.
	if strings.HasPrefix(searchID, "tt") {
		out.IMDbID = searchID
	} else if out.TVDBID == "" && isNumericID(searchID) {
		out.TMDBID = searchID
	}

	return out
}

// isNumericID reports whether value is a bare number, the shape a TMDB id takes
// when an id carries no prefix to say so.
func isNumericID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	_, err := strconv.Atoi(value)
	return err == nil
}
