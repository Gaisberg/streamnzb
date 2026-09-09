package persistence

import (
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
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at
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
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at
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
		SELECT stream_name, item_id, content_type, content_id, position_ticks, runtime_ticks, played, play_count, last_played_at
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
		var st JellyfinPlaystate
		var played int
		var lastPlayed int64
		if err := rows.Scan(&st.StreamName, &st.ItemID, &st.ContentType, &st.ContentID,
			&st.PositionTicks, &st.RuntimeTicks, &played, &st.PlayCount, &lastPlayed); err != nil {
			continue
		}
		st.Played = played != 0
		st.LastPlayedAt = time.UnixMilli(lastPlayed)
		out = append(out, st)
	}
	return out
}
