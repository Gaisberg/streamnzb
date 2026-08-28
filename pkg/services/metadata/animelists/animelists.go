// Package animelists resolves Kitsu anime ids to the TVDB/IMDb/TMDB ids and the
// season/episode numbering that Usenet releases actually use.
//
// Kitsu addresses anime per entry, and an entry is usually one season — often
// one cour. "Fire Force Season 3 Part 2" episode 3 is kitsu:49016:3, while the
// release is named S03E15. Kitsu's own mappings do not carry that offset (most
// modern entries map only to AniList/MAL), so searching from Kitsu data alone
// finds nothing. Fribb's anime-lists publishes the missing pieces: the series
// ids plus the aired season and the episode offset of each entry.
//
// The list is imported into the database rather than cached as a file, so
// everything StreamNZB persists stays in one place.
package animelists

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
)

const (
	// sourceURL is Fribb's combined list. It is regenerated daily, which is why
	// the list is refreshed rather than embedded as a snapshot: currently
	// airing anime — exactly the content that needs the offset — is what
	// changes.
	sourceURL = "https://raw.githubusercontent.com/Fribb/anime-lists/master/anime-list-full.json"

	// refreshInterval bounds staleness of the imported list. An import younger
	// than this is left alone, so a restart costs no download.
	refreshInterval = 24 * time.Hour
)

// Mapping is one anime-lists entry, reduced to the fields that shape a search.
type Mapping struct {
	KitsuID int
	IMDbID  string
	TVDBID  string
	TMDBID  string

	// Season is the aired season this entry maps onto, and HasSeason reports
	// whether the source published one at all. The distinction matters: no
	// season means the entry spans the whole series and numbers its episodes
	// absolutely, which is not the same as season 0 (specials).
	Season    int
	HasSeason bool

	// EpisodeOffset is how many episodes of the season aired before this entry
	// starts — 12 for a second cour that opens on episode 13.
	EpisodeOffset int

	Type string

	// AniListID keys external anime services (SeaDex); 0 when the source
	// published none.
	AniListID int

	// MALID keys MAL-based sources (Simkl anime watchlists); 0 when the source
	// published none.
	MALID int
}

// SpansSeries reports whether the entry covers the whole series rather than one
// season, which makes its episode numbers the series' absolute numbers.
func (m *Mapping) SpansSeries() bool {
	return m != nil && !m.HasSeason
}

// ResolveEpisode maps an entry-local episode number onto the aired season and
// episode that releases are named by. ok is false when the entry carries no
// usable season: specials (season 0), or an entry spanning the series, where
// the requested number is already absolute and SpansSeries covers it.
func (m *Mapping) ResolveEpisode(entryEpisode int) (season, episode int, ok bool) {
	if m == nil || !m.HasSeason || m.Season < 1 || entryEpisode < 1 {
		return 0, 0, false
	}
	return m.Season, entryEpisode + m.EpisodeOffset, true
}

// rawEntry mirrors the upstream JSON. The id fields are irregular by design of
// the source: imdb is a list, themoviedb is an object keyed by media kind, and
// season/episode_offset are objects keyed by provider.
type rawEntry struct {
	Type      string   `json:"type"`
	KitsuID   *int     `json:"kitsu_id"`
	IMDbID    []string `json:"imdb_id"`
	TVDBID    *int     `json:"tvdb_id"`
	AniListID *int     `json:"anilist_id"`
	MALID     *int     `json:"mal_id"`
	TheMovie  *struct {
		TV    *int  `json:"tv"`
		Movie []int `json:"movie"`
	} `json:"themoviedb_id"`
	Season *struct {
		TVDB *int `json:"tvdb"`
		TMDB *int `json:"tmdb"`
	} `json:"season"`
	EpisodeOffset *struct {
		TVDB *int `json:"tvdb"`
		TMDB *int `json:"tmdb"`
	} `json:"episode_offset"`
}

func (r *rawEntry) toMapping() *persistence.AnimeMapping {
	if r.KitsuID == nil || *r.KitsuID <= 0 {
		return nil
	}
	m := &persistence.AnimeMapping{KitsuID: *r.KitsuID, EntryType: strings.TrimSpace(r.Type)}
	if r.AniListID != nil && *r.AniListID > 0 {
		m.AniListID = *r.AniListID
	}
	if r.MALID != nil && *r.MALID > 0 {
		m.MALID = *r.MALID
	}
	if len(r.IMDbID) > 0 {
		m.IMDbID = strings.TrimSpace(r.IMDbID[0])
	}
	if r.TVDBID != nil && *r.TVDBID > 0 {
		m.TVDBID = strconv.Itoa(*r.TVDBID)
	}
	if r.TheMovie != nil {
		if r.TheMovie.TV != nil && *r.TheMovie.TV > 0 {
			m.TMDBID = strconv.Itoa(*r.TheMovie.TV)
		} else if len(r.TheMovie.Movie) > 0 && r.TheMovie.Movie[0] > 0 {
			m.TMDBID = strconv.Itoa(r.TheMovie.Movie[0])
		}
	}
	// TVDB numbering is what release names follow; TMDB is the fallback only
	// because the two agree in almost every entry that publishes both.
	if r.Season != nil {
		if r.Season.TVDB != nil {
			m.Season, m.HasSeason = *r.Season.TVDB, true
		} else if r.Season.TMDB != nil {
			m.Season, m.HasSeason = *r.Season.TMDB, true
		}
	}
	if r.EpisodeOffset != nil {
		if r.EpisodeOffset.TVDB != nil {
			m.EpisodeOffset = *r.EpisodeOffset.TVDB
		} else if r.EpisodeOffset.TMDB != nil {
			m.EpisodeOffset = *r.EpisodeOffset.TMDB
		}
	}
	return m
}

// Store keeps the anime-lists import current and answers lookups from it.
type Store struct {
	url        string
	mappings   *persistence.AnimeMappingStore
	httpClient *http.Client
	startOnce  sync.Once
}

var (
	sharedOnce sync.Once
	shared     *Store
)

// GetStore returns the process-wide store, starting its background refresh on
// the first call. The list has no config inputs, so a config reload keeps the
// already-imported copy instead of re-fetching it.
func GetStore(mappings *persistence.AnimeMappingStore) *Store {
	sharedOnce.Do(func() {
		shared = NewStore(mappings)
		shared.Start()
	})
	return shared
}

func NewStore(mappings *persistence.AnimeMappingStore) *Store {
	url := sourceURL
	if envURL := strings.TrimSpace(os.Getenv("STREAMNZB_ANIME_LISTS_URL")); envURL != "" {
		url = envURL
	}
	return &Store{
		url:        url,
		mappings:   mappings,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Start keeps the list refreshed in the background. It never blocks a request:
// until the first import lands, lookups miss and callers fall back to whatever
// Kitsu itself provides.
func (s *Store) Start() {
	if s == nil || s.mappings == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.refreshLoop()
	})
}

func (s *Store) refreshLoop() {
	s.Refresh()
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.Refresh()
	}
}

// Refresh imports the list unless the stored copy is still current. A failed
// download leaves the previous import in place — mappings from yesterday map
// everything that is not brand new, which beats no mappings at all.
func (s *Store) Refresh() {
	if s == nil || s.mappings == nil {
		return
	}
	if age := time.Since(s.mappings.LastUpdated()); age < refreshInterval && s.mappings.Count() > 0 {
		logger.Debug("Anime lists already current", "age", age.Truncate(time.Minute), "entries", s.mappings.Count())
		return
	}
	if err := s.importList(); err != nil {
		logger.Warn("Anime lists refresh failed", "url", s.url, "entries", s.mappings.Count(), "err", err)
	}
}

func (s *Store) importList() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "StreamNZB/1.0")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("anime lists returned status %d", resp.StatusCode)
	}

	return s.Load(resp.Body)
}

// Load parses an anime-lists document and replaces the stored mappings with it,
// for callers that hold their own copy rather than letting the store fetch one.
//
// It decodes element by element: the document is several megabytes of entries
// that are mostly fields we drop, so streaming keeps peak memory to the
// projection rather than the whole document as well.
func (s *Store) Load(r io.Reader) error {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return fmt.Errorf("anime lists: expected a JSON array, got %v", tok)
	}

	mappings := make([]persistence.AnimeMapping, 0, 24000)
	for dec.More() {
		var raw rawEntry
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if m := raw.toMapping(); m != nil {
			mappings = append(mappings, *m)
		}
	}
	if len(mappings) == 0 {
		return fmt.Errorf("anime lists: no kitsu entries found")
	}
	if err := s.mappings.Replace(mappings, time.Now()); err != nil {
		return fmt.Errorf("anime lists: store import: %w", err)
	}
	logger.Info("Anime lists imported", "entries", len(mappings))
	return nil
}

// LookupKitsu resolves a Kitsu anime id. It misses — rather than waiting —
// while the first import is still in flight.
func (s *Store) LookupKitsu(kitsuID string) (*Mapping, bool) {
	if s == nil || s.mappings == nil {
		return nil, false
	}
	id, err := strconv.Atoi(strings.TrimSpace(kitsuID))
	if err != nil || id <= 0 {
		return nil, false
	}
	row, ok := s.mappings.LookupKitsu(id)
	if !ok {
		return nil, false
	}
	return mappingFromRow(row), true
}

// LookupMAL resolves a MyAnimeList anime id to its entry, for MAL-keyed
// sources (Simkl anime watchlists). Same miss semantics as LookupKitsu.
func (s *Store) LookupMAL(malID string) (*Mapping, bool) {
	if s == nil || s.mappings == nil {
		return nil, false
	}
	id, err := strconv.Atoi(strings.TrimSpace(malID))
	if err != nil || id <= 0 {
		return nil, false
	}
	row, ok := s.mappings.LookupMAL(id)
	if !ok {
		return nil, false
	}
	return mappingFromRow(row), true
}

func mappingFromRow(row *persistence.AnimeMapping) *Mapping {
	return &Mapping{
		KitsuID:       row.KitsuID,
		IMDbID:        row.IMDbID,
		TVDBID:        row.TVDBID,
		TMDBID:        row.TMDBID,
		Season:        row.Season,
		HasSeason:     row.HasSeason,
		EpisodeOffset: row.EpisodeOffset,
		Type:          row.EntryType,
		AniListID:     row.AniListID,
		MALID:         row.MALID,
	}
}

// Ready reports whether a list has been imported.
func (s *Store) Ready() bool {
	return s != nil && s.mappings != nil && s.mappings.Count() > 0
}
