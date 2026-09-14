package jellyfin

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"streamnzb/pkg/search/query"
)

// Jellyfin clients require item ids to parse as GUIDs and treat them as
// opaque, so every id here is 16 bytes rendered as 32 hex digits, and the
// bytes are the item's identity rather than a lookup key: nothing has to be
// remembered between the request that handed an id out and the one that hands
// it back. A restart, a second instance behind a load balancer or a client
// that kept an id for a month all decode the same way.
//
// Layout:
//
//	[0]      kind
//	[1]      content type (movie / series / anime)
//	[2]      id scheme (tt / tmdb / tvdb / kitsu)
//	[3..8]   numeric id, big-endian (48 bits; no scheme is near that)
//	[9..10]  slot index (source kind)
//	[11..12] season
//	[13..14] episode
//	[15]     checksum
//
// A view (a library folder) has no numeric identity: [1..14] hold a hash of
// the catalog id, resolved by scanning the registry.

type itemKind byte

const (
	kindView itemKind = iota + 1
	kindMovie
	kindSeries
	kindSeason
	kindEpisode
	// kindSource is a media source: one playable candidate of a movie or
	// episode. It carries the movie's or episode's id plus the slot index.
	kindSource
)

type idScheme byte

const (
	schemeIMDb idScheme = iota + 1
	schemeTMDB
	schemeTVDB
	schemeKitsu
)

const maxNumber = 1<<48 - 1

var contentTypeCodes = map[string]byte{"movie": 1, "series": 2, "anime": 3}
var contentTypeNames = map[byte]string{1: "movie", 2: "series", 3: "anime"}

var errBadItemID = errors.New("jellyfin: not an item id")

// itemID is a decoded id.
type itemID struct {
	Kind        itemKind
	ContentType string
	Scheme      idScheme
	Number      uint64
	Slot        int
	Season      int
	Episode     int
	// CatalogID is set for views only.
	CatalogID string
}

// checksum keeps a random GUID — a client probing, or an id from a real
// Jellyfin server — from decoding into some item.
func checksum(b []byte) byte {
	sum := byte(0x5a)
	for i, v := range b[:15] {
		sum ^= v + byte(i)
	}
	return sum
}

func (id itemID) encode() string {
	var b [16]byte
	b[0] = byte(id.Kind)
	if id.Kind == kindView {
		h := sha1.Sum([]byte(id.CatalogID))
		copy(b[1:15], h[:14])
	} else {
		b[1] = contentTypeCodes[id.ContentType]
		b[2] = byte(id.Scheme)
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], id.Number)
		copy(b[3:9], n[2:])
		binary.BigEndian.PutUint16(b[9:11], uint16(id.Slot))
		binary.BigEndian.PutUint16(b[11:13], uint16(id.Season))
		binary.BigEndian.PutUint16(b[13:15], uint16(id.Episode))
	}
	b[15] = checksum(b[:])
	return hex.EncodeToString(b[:])
}

// decodeItemID reads an id back. Dashes are tolerated because some clients
// render GUIDs in their canonical hyphenated form.
func decodeItemID(s string) (itemID, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 || checksum(b) != b[15] {
		return itemID{}, errBadItemID
	}
	id := itemID{Kind: itemKind(b[0])}
	if id.Kind == kindView {
		for _, def := range allCatalogs() {
			if viewID(def.ID) == hex.EncodeToString(b) {
				id.CatalogID = def.ID
				return id, nil
			}
		}
		// Profile-owned external catalog IDs are not part of the static
		// registry, so retain their opaque view id. The request-specific
		// catalog resolver verifies it against the stream's enabled rows before
		// it can be opened.
		id.CatalogID = hex.EncodeToString(b)
		return id, nil
	}
	if id.Kind < kindMovie || id.Kind > kindSource {
		return itemID{}, errBadItemID
	}
	id.ContentType = contentTypeNames[b[1]]
	id.Scheme = idScheme(b[2])
	if id.ContentType == "" || id.Scheme < schemeIMDb || id.Scheme > schemeKitsu {
		return itemID{}, errBadItemID
	}
	var n [8]byte
	copy(n[2:], b[3:9])
	id.Number = binary.BigEndian.Uint64(n[:])
	id.Slot = int(binary.BigEndian.Uint16(b[9:11]))
	id.Season = int(binary.BigEndian.Uint16(b[11:13]))
	id.Episode = int(binary.BigEndian.Uint16(b[13:15]))
	return id, nil
}

// viewID is the id of the library folder for a catalog.
func viewID(catalogID string) string {
	return itemID{Kind: kindView, CatalogID: catalogID}.encode()
}

// itemIDFor builds the id of a movie or series from its Stremio content id.
// An id the layer cannot encode is reported rather than turned into something
// that decodes to nothing.
func itemIDFor(contentType, stremioID string) (itemID, error) {
	parsed := query.ParseContentID(stremioID)
	id := itemID{ContentType: contentType, Kind: kindSeries}
	if contentType == "movie" {
		id.Kind = kindMovie
	}
	if _, ok := contentTypeCodes[contentType]; !ok {
		return itemID{}, fmt.Errorf("jellyfin: unsupported content type %q", contentType)
	}
	var number string
	switch {
	case parsed.KitsuID != "":
		id.Scheme, number = schemeKitsu, parsed.KitsuID
	case parsed.IMDbID != "":
		id.Scheme, number = schemeIMDb, strings.TrimPrefix(parsed.IMDbID, "tt")
	case parsed.TVDBID != "":
		id.Scheme, number = schemeTVDB, parsed.TVDBID
	case parsed.TMDBID != "":
		id.Scheme, number = schemeTMDB, parsed.TMDBID
	default:
		return itemID{}, fmt.Errorf("jellyfin: unsupported content id %q", stremioID)
	}
	n, err := strconv.ParseUint(number, 10, 64)
	if err != nil || n > maxNumber {
		return itemID{}, fmt.Errorf("jellyfin: unsupported content id %q", stremioID)
	}
	id.Number = n
	return id, nil
}

// series returns the series-level id of a season, episode or source.
func (id itemID) series() itemID {
	out := id
	out.Kind = kindSeries
	out.Slot, out.Season, out.Episode = 0, 0, 0
	return out
}

func (id itemID) season(season int) itemID {
	out := id
	out.Kind = kindSeason
	out.Slot, out.Season, out.Episode = 0, season, 0
	return out
}

func (id itemID) episode(season, episode int) itemID {
	out := id
	out.Kind = kindEpisode
	out.Slot, out.Season, out.Episode = 0, season, episode
	return out
}

// source is the media-source id of one candidate of a movie or episode.
func (id itemID) source(index int) itemID {
	out := id
	out.Kind = kindSource
	out.Slot = index
	return out
}

// playable returns the movie or episode a source belongs to; other kinds are
// returned as they are.
func (id itemID) playable() itemID {
	if id.Kind != kindSource {
		return id
	}
	out := id
	out.Slot = 0
	out.Kind = kindEpisode
	if id.ContentType == "movie" {
		out.Kind = kindMovie
	}
	return out
}

// baseStremioID is the Stremio id of the movie or series: "tt0000123",
// "tmdb:123", "tvdb:123" or "kitsu:123".
func (id itemID) baseStremioID() string {
	n := strconv.FormatUint(id.Number, 10)
	switch id.Scheme {
	case schemeIMDb:
		return fmt.Sprintf("tt%07d", id.Number)
	case schemeTMDB:
		return "tmdb:" + n
	case schemeTVDB:
		return "tvdb:" + n
	case schemeKitsu:
		return "kitsu:" + n
	}
	return ""
}

// playStremioID is the id the addon plays: the movie's own id, or the
// episode form "<series>:<season>:<episode>" ("kitsu:N:<ep>" for anime, whose
// numbering is entry-relative and season-less). An anime with no episodes at
// all — a Kitsu movie — is rendered as an episode too (see synthesizeVideos),
// and plays under the bare entry id.
func (id itemID) playStremioID() string {
	base := id.baseStremioID()
	if id.Kind != kindEpisode {
		return base
	}
	if id.Scheme == schemeKitsu {
		if id.Episode == 0 {
			return base
		}
		return base + ":" + strconv.Itoa(id.Episode)
	}
	return base + ":" + strconv.Itoa(id.Season) + ":" + strconv.Itoa(id.Episode)
}
