package persistence

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// JellyfinPlaystate is one stream's progress on one Jellyfin item. Ticks are
// Jellyfin's unit (100 ns), stored as reported so the layer never converts
// twice.
type JellyfinPlaystate struct {
	StreamName    string
	ItemID        string
	ContentType   string
	ContentID     string
	PositionTicks int64
	RuntimeTicks  int64
	Played        bool
	PlayCount     int
	LastPlayedAt  time.Time
	// IsFavorite is owned by SetFavorite: Upsert never writes it, so a
	// progress report built from a stale read cannot undo a favourite.
	IsFavorite bool
}

// JellyfinPlaystateStore persists playback progress reported by Jellyfin
// clients. Progress arrives every few seconds during playback, so callers
// debounce; the store itself is a plain upsert.
type JellyfinPlaystateStore struct {
	db  *connRef // shared read handle
	wdb *connRef // write handle
	mu  sync.RWMutex
}

func NewJellyfinPlaystateStore(db, wdb *connRef) *JellyfinPlaystateStore {
	return &JellyfinPlaystateStore{db: db, wdb: wdb}
}

// Upsert writes the row for (stream, item). A zero LastPlayedAt is stamped
// with now. PlayCount is taken as-is, so callers increment it themselves when
// a play starts.
func (ps *JellyfinPlaystateStore) Upsert(state JellyfinPlaystate) error {
	if ps == nil || ps.db == nil {
		return nil
	}
	state.StreamName = strings.TrimSpace(state.StreamName)
	state.ItemID = strings.TrimSpace(state.ItemID)
	if state.StreamName == "" || state.ItemID == "" {
		return nil
	}
	if state.LastPlayedAt.IsZero() {
		state.LastPlayedAt = time.Now()
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	_, err := ps.wdb.Exec(`
		INSERT INTO jellyfin_playstate
			(stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(stream_name, item_id) DO UPDATE SET
			content_type = excluded.content_type,
			content_id = excluded.content_id,
			position_ticks = excluded.position_ticks,
			runtime_ticks = excluded.runtime_ticks,
			played = excluded.played,
			play_count = excluded.play_count,
			last_played_at = excluded.last_played_at
	`, state.StreamName, state.ItemID, state.ContentType, state.ContentID,
		state.PositionTicks, state.RuntimeTicks, boolToInt(state.Played), state.PlayCount,
		state.LastPlayedAt.UnixMilli())
	return err
}

// Get returns the row for (stream, item), or false when none exists.
func (ps *JellyfinPlaystateStore) Get(streamName, itemID string) (JellyfinPlaystate, bool) {
	if ps == nil || ps.db == nil {
		return JellyfinPlaystate{}, false
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rows, err := ps.db.Query(`
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at, is_favorite
		FROM jellyfin_playstate WHERE stream_name = ? AND item_id = ?`,
		strings.TrimSpace(streamName), strings.TrimSpace(itemID))
	if err != nil {
		return JellyfinPlaystate{}, false
	}
	defer rows.Close()
	states := scanPlaystates(rows)
	if len(states) == 0 {
		return JellyfinPlaystate{}, false
	}
	return states[0], true
}

// ListResume returns in-progress rows (position recorded, not played) for a
// stream, most recently played first.
func (ps *JellyfinPlaystateStore) ListResume(streamName string, limit int) []JellyfinPlaystate {
	if ps == nil || ps.db == nil || limit <= 0 {
		return nil
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rows, err := ps.db.Query(`
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at, is_favorite
		FROM jellyfin_playstate
		WHERE stream_name = ? AND played = 0 AND position_ticks > 0
		ORDER BY last_played_at DESC LIMIT ?`,
		strings.TrimSpace(streamName), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaystates(rows)
}

// ListForStream returns every row for a stream, most recently played first.
// It backs UserData lookups for whole catalog pages in one query.
func (ps *JellyfinPlaystateStore) ListForStream(streamName string) []JellyfinPlaystate {
	if ps == nil || ps.db == nil {
		return nil
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rows, err := ps.db.Query(`
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at, is_favorite
		FROM jellyfin_playstate WHERE stream_name = ? ORDER BY last_played_at DESC`,
		strings.TrimSpace(streamName))
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaystates(rows)
}

// SetPlayed marks an item watched (position reset) or unwatched (row kept so
// the play count survives, position cleared).
func (ps *JellyfinPlaystateStore) SetPlayed(streamName, itemID, contentType, contentID string, played bool) error {
	if ps == nil || ps.db == nil {
		return nil
	}
	current, ok := ps.Get(streamName, itemID)
	if !ok {
		current = JellyfinPlaystate{StreamName: streamName, ItemID: itemID, ContentType: contentType, ContentID: contentID}
	}
	current.Played = played
	current.PositionTicks = 0
	if played && current.PlayCount == 0 {
		current.PlayCount = 1
	}
	current.LastPlayedAt = time.Now()
	return ps.Upsert(current)
}

// MarkPlayedMany marks items watched as SetPlayed marks one, in one
// transaction stamped with one time, so a season or series marked at once is
// either all marked or left as it was. Each item carries its ItemID,
// ContentType and ContentID. An item already played is left as it was, date
// and play count included; the rows missing are created. The favourite flag
// is never written.
func (ps *JellyfinPlaystateStore) MarkPlayedMany(streamName string, items []JellyfinPlaystate) error {
	if ps == nil || ps.db == nil {
		return nil
	}
	streamName = strings.TrimSpace(streamName)
	if streamName == "" || len(items) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	// Not the request's context: once begun, the marks land together or not
	// at all, whatever the client does meanwhile.
	tx, err := ps.wdb.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	// Rows go a chunk to a statement rather than one each: the store is locked
	// meanwhile, and on Postgres every statement is a round trip. An id is
	// written once, since one Postgres upsert cannot touch a row twice.
	const chunk = 500
	seen := make(map[string]bool, len(items))
	for start := 0; start < len(items); start += chunk {
		var values []string
		var args []any
		for _, item := range items[start:min(start+chunk, len(items))] {
			itemID := strings.TrimSpace(item.ItemID)
			if itemID == "" {
				return errors.New("jellyfin playstate: empty item id")
			}
			if seen[itemID] {
				continue
			}
			seen[itemID] = true
			values = append(values, "(?, ?, ?, ?, 1, 1, ?)")
			args = append(args, streamName, itemID, item.ContentType, item.ContentID, now)
		}
		if len(values) == 0 {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO jellyfin_playstate (stream_name, item_id, content_type, content_id, played, play_count, last_played_at)
			VALUES `+strings.Join(values, ", ")+`
			ON CONFLICT(stream_name, item_id) DO UPDATE SET
				played = 1,
				position_ticks = 0,
				play_count = CASE WHEN jellyfin_playstate.play_count = 0 THEN 1 ELSE jellyfin_playstate.play_count END,
				last_played_at = excluded.last_played_at
			WHERE jellyfin_playstate.played = 0`, args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// prefixRange is the WHERE clause, and its arguments, admitting the item ids
// that start with prefix, a lowercase hex one, as every item id is (so LIKE
// has no wildcard to misread). The range lets the primary key find the rows
// without reading the rest of the stream. Neither half alone is exact on
// both backends, which compare text differently: Postgres under a glibc
// collation such as en_US orders case aside, so the range admits "05AB…"
// under "05ab", and SQLite's LIKE ignores case. Together they admit exactly
// the ids that start with prefix and go on in hex.
func prefixRange(prefix string) (string, []any, error) {
	if prefix == "" || strings.Trim(prefix, "0123456789abcdef") != "" {
		return "", nil, errors.New("jellyfin playstate: item id prefix must be lowercase hex")
	}
	return `item_id >= ? AND item_id < ? AND item_id LIKE ?`, []any{prefix, prefix + "g", prefix + "%"}, nil
}

// ListPrefix returns a stream's rows whose item id starts with prefix: a
// series' or season's episodes, without the rest of the stream. Unlike the
// listings above it reports a failed read, which a caller deciding what to
// change must not take for no rows.
func (ps *JellyfinPlaystateStore) ListPrefix(streamName, prefix string) ([]JellyfinPlaystate, error) {
	if ps == nil || ps.db == nil {
		return nil, nil
	}
	where, args, err := prefixRange(prefix)
	if err != nil {
		return nil, err
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rows, err := ps.db.Query(`
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at, is_favorite
		FROM jellyfin_playstate WHERE stream_name = ? AND `+where,
		append([]any{strings.TrimSpace(streamName)}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JellyfinPlaystate
	for rows.Next() {
		st, err := scanPlaystate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ClearPlayedPrefix marks unwatched every row of a stream whose item id
// starts with prefix, its position cleared, its play count and favourite
// kept, in one statement, and reports how many it changed: what it clears is
// decided as it writes, so a row played before it lands is cleared too
// rather than missed. A write landing after it wins, as after any mark: a
// progress report that read its row before the clear writes that row's old
// played flag back. Rows with nothing to clear are left as they are, and
// none is created.
func (ps *JellyfinPlaystateStore) ClearPlayedPrefix(streamName, prefix string) (int64, error) {
	if ps == nil || ps.db == nil {
		return 0, nil
	}
	where, args, err := prefixRange(prefix)
	if err != nil {
		return 0, err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	res, err := ps.wdb.Exec(`
		UPDATE jellyfin_playstate SET played = 0, position_ticks = 0, last_played_at = ?
		WHERE stream_name = ? AND (played = 1 OR position_ticks > 0) AND `+where,
		append([]any{time.Now().UnixMilli(), strings.TrimSpace(streamName)}, args...)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetFavorite marks or clears an item as a favourite. It writes is_favorite
// alone, so progress and last_played_at are never touched. Marking creates a
// row when there is none; clearing only updates, and never deletes the row,
// whose progress outlives the favourite.
func (ps *JellyfinPlaystateStore) SetFavorite(streamName, itemID, contentType, contentID string, favorite bool) error {
	if ps == nil || ps.db == nil {
		return nil
	}
	streamName = strings.TrimSpace(streamName)
	itemID = strings.TrimSpace(itemID)
	if streamName == "" || itemID == "" {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if !favorite {
		_, err := ps.wdb.Exec(`UPDATE jellyfin_playstate SET is_favorite = 0 WHERE stream_name = ? AND item_id = ?`, streamName, itemID)
		return err
	}
	_, err := ps.wdb.Exec(`
		INSERT INTO jellyfin_playstate (stream_name, item_id, content_type, content_id, is_favorite)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(stream_name, item_id) DO UPDATE SET is_favorite = excluded.is_favorite
	`, streamName, itemID, contentType, contentID)
	return err
}

// ListFavorites returns a stream's favourites in a stable order, so a client
// paging through them sees each once.
func (ps *JellyfinPlaystateStore) ListFavorites(streamName string) []JellyfinPlaystate {
	if ps == nil || ps.db == nil {
		return nil
	}
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	rows, err := ps.db.Query(`
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at, is_favorite
		FROM jellyfin_playstate WHERE stream_name = ? AND is_favorite = 1 ORDER BY item_id`,
		strings.TrimSpace(streamName))
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanPlaystates(rows)
}

// DeleteForStream drops every row for a stream; used when the stream is
// deleted.
func (ps *JellyfinPlaystateStore) DeleteForStream(streamName string) error {
	if ps == nil || ps.db == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	_, err := ps.wdb.Exec(`DELETE FROM jellyfin_playstate WHERE stream_name = ?`, strings.TrimSpace(streamName))
	return err
}

func scanPlaystates(rows interface {
	Next() bool
	Scan(dest ...any) error
}) []JellyfinPlaystate {
	var out []JellyfinPlaystate
	for rows.Next() {
		st, err := scanPlaystate(rows)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	return out
}

// scanPlaystate reads the row rows is on. The listings skip a row it cannot
// read; ListPrefix, whose rows decide a watched state, fails on it instead.
func scanPlaystate(rows interface{ Scan(dest ...any) error }) (JellyfinPlaystate, error) {
	var st JellyfinPlaystate
	var played, favorite int
	var lastPlayed int64
	if err := rows.Scan(&st.StreamName, &st.ItemID, &st.ContentType, &st.ContentID,
		&st.PositionTicks, &st.RuntimeTicks, &played, &st.PlayCount, &lastPlayed, &favorite); err != nil {
		return JellyfinPlaystate{}, err
	}
	st.Played = played != 0
	st.IsFavorite = favorite != 0
	// A favourite-only row was never played; 0 must read as zero time,
	// not as 1970.
	if lastPlayed > 0 {
		st.LastPlayedAt = time.UnixMilli(lastPlayed)
	}
	return st, nil
}
