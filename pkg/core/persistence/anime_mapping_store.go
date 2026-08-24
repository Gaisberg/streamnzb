package persistence

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// animeMappingsUpdatedKey records when the list was last replaced, so a restart
// inside the refresh window costs no download.
const animeMappingsUpdatedKey = "anime_mappings_updated_at"

// AnimeMapping is one row of the anime id/numbering list. Season is only
// meaningful when HasSeason is set; see animeMappingsSchema.
type AnimeMapping struct {
	KitsuID       int
	IMDbID        string
	TVDBID        string
	TMDBID        string
	Season        int
	HasSeason     bool
	EpisodeOffset int
	EntryType     string
	AniListID     int
}

// animeMappingInsertChunk is how many rows go into one multi-row INSERT. The
// list is ~22k rows and a row-at-a-time loop costs a round trip each, which is
// cheap in-process and expensive over a socket — 11s against Postgres, against
// well under a second batched on either backend. The chunk stays small enough
// that 9 columns fit Postgres's 65535-parameter ceiling many times over, and
// SQLite's default 32766 likewise.
const animeMappingInsertChunk = 500

// AnimeMappingStore holds the anime id mappings that turn a Kitsu entry and its
// entry-local episode number into the series ids and aired season/episode that
// releases are named by.
type AnimeMappingStore struct {
	db  *connRef // shared read handle
	wdb *connRef // write handle
	mgr *StateManager
}

func NewAnimeMappingStore(mgr *StateManager, db, wdb *connRef) *AnimeMappingStore {
	return &AnimeMappingStore{db: db, wdb: wdb, mgr: mgr}
}

// Replace swaps the whole list in one transaction. The list is a snapshot of an
// upstream file where entries are merged and retired as well as added, so a
// wholesale replace is what keeps it faithful — an upsert-only pass would leave
// retired entries behind forever.
func (as *AnimeMappingStore) Replace(mappings []AnimeMapping, updatedAt time.Time) error {
	if as == nil || as.wdb == nil {
		return nil
	}
	return as.mgr.withTx(as.wdb, func(tx *txn) error {
		if _, err := tx.Exec(`DELETE FROM anime_mappings`); err != nil {
			return err
		}
		for start := 0; start < len(mappings); start += animeMappingInsertChunk {
			end := min(start+animeMappingInsertChunk, len(mappings))
			if err := insertAnimeMappings(tx, mappings[start:end]); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`
			INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				updated_at = excluded.updated_at`,
			animeMappingsUpdatedKey,
			[]byte(strconv.FormatInt(updatedAt.UnixMilli(), 10)),
			time.Now().UnixMilli())
		return err
	})
}

// insertAnimeMappings writes one chunk as a single multi-row INSERT.
func insertAnimeMappings(tx *txn, chunk []AnimeMapping) error {
	const cols = 9
	var sb strings.Builder
	sb.WriteString(`INSERT INTO anime_mappings
		(kitsu_id, imdb_id, tvdb_id, tmdb_id, has_season, season, episode_offset, entry_type, anilist_id) VALUES `)
	args := make([]any, 0, len(chunk)*cols)
	for i, m := range chunk {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, m.KitsuID, m.IMDbID, m.TVDBID, m.TMDBID,
			boolToInt(m.HasSeason), m.Season, m.EpisodeOffset, m.EntryType, m.AniListID)
	}
	_, err := tx.Exec(sb.String(), args...)
	return err
}

// LookupKitsu resolves one Kitsu anime id. Missing rows are the normal case for
// an entry too new for the list, so absence is not an error.
func (as *AnimeMappingStore) LookupKitsu(kitsuID int) (*AnimeMapping, bool) {
	if as == nil || as.db == nil || kitsuID <= 0 {
		return nil, false
	}
	var (
		m         AnimeMapping
		imdb      sql.NullString
		tvdb      sql.NullString
		tmdb      sql.NullString
		entryType sql.NullString
		hasSeason int
	)
	err := as.db.QueryRow(`
		SELECT kitsu_id, imdb_id, tvdb_id, tmdb_id, has_season, season, episode_offset, entry_type, anilist_id
		FROM anime_mappings WHERE kitsu_id = ?`, kitsuID).
		Scan(&m.KitsuID, &imdb, &tvdb, &tmdb, &hasSeason, &m.Season, &m.EpisodeOffset, &entryType, &m.AniListID)
	if err != nil {
		return nil, false
	}
	m.IMDbID, m.TVDBID, m.TMDBID = imdb.String, tvdb.String, tmdb.String
	m.EntryType = entryType.String
	m.HasSeason = hasSeason != 0
	return &m, true
}

// LastUpdated reports when the list was last replaced. A zero time means it has
// never been imported.
func (as *AnimeMappingStore) LastUpdated() time.Time {
	if as == nil || as.db == nil {
		return time.Time{}
	}
	raw, ok, err := getKV(as.db, animeMappingsUpdatedKey)
	if err != nil || !ok {
		return time.Time{}
	}
	millis, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(millis)
}

// Count reports how many mappings are stored.
func (as *AnimeMappingStore) Count() int {
	if as == nil || as.db == nil {
		return 0
	}
	var n int
	if err := as.db.QueryRow(`SELECT COUNT(*) FROM anime_mappings`).Scan(&n); err != nil {
		return 0
	}
	return n
}
