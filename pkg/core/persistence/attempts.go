package persistence

import (
	"database/sql"
	"strings"
	"time"
)

// NZBAttempt represents one recorded play attempt (preload, success, or failure).
type NZBAttempt struct {
	ID            int64     `json:"id"`
	TriedAt       time.Time `json:"tried_at"`
	StreamName    string    `json:"stream_name"`
	ProviderName  string    `json:"provider_name"`
	ContentType   string    `json:"content_type"`
	ContentID     string    `json:"content_id"`
	ContentTitle  string    `json:"content_title"`
	IndexerName   string    `json:"indexer_name"`
	ReleaseTitle  string    `json:"release_title"`
	ReleaseURL    string    `json:"release_url"`
	ReleaseSize   int64     `json:"release_size"`
	ServedFile    string    `json:"served_file,omitempty"`
	MatchType     string    `json:"match_type,omitempty"`
	Success       bool      `json:"success"`
	FailureReason string    `json:"failure_reason,omitempty"`
	AvailStatus   string    `json:"avail_status,omitempty"`
	AvailReason   string    `json:"avail_reason,omitempty"`
	SlotPath      string    `json:"slot_path,omitempty"`
	Preload       bool      `json:"preload"` // true = attempt started, result not yet known
	TTFFMS        int64     `json:"ttff_ms,omitempty"`
}

// PlayedContent is one successful playback row, newest first: what a stream
// actually played, as opposed to what the shared library merely caches.
type PlayedContent struct {
	ContentID    string
	ContentTitle string
}

// RecentPlayedContent lists successful (non-preload) playback rows for one
// content type, newest first. streamName narrows to one stream's history;
// empty means any stream. Episodes of one series come back as separate rows —
// the caller collapses them to series granularity.
func (m *StateManager) RecentPlayedContent(streamName, contentType string, limit int) ([]PlayedContent, error) {
	if m == nil || m.db == nil || limit <= 0 {
		return nil, nil
	}
	query := `SELECT content_id, content_title FROM nzb_attempts
		WHERE success = 1 AND preload = 0 AND content_type = ?`
	args := []any{contentType}
	if streamName != "" {
		query += ` AND stream_name = ?`
		args = append(args, streamName)
	}
	query += ` ORDER BY tried_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var played []PlayedContent
	for rows.Next() {
		var p PlayedContent
		var title sql.NullString
		if err := rows.Scan(&p.ContentID, &title); err != nil {
			return nil, err
		}
		p.ContentTitle = title.String
		played = append(played, p)
	}
	return played, rows.Err()
}

// RecordAttemptParams holds the fields needed to record an NZB attempt.
type RecordAttemptParams struct {
	StreamName    string
	ProviderName  string
	ContentType   string // "movie" or "series"
	ContentID     string // e.g. "tt123" or "tmdb:123:1:2"
	ContentTitle  string
	IndexerName   string
	ReleaseTitle  string
	ReleaseURL    string
	ReleaseSize   int64
	ServedFile    string
	MatchType     string
	Success       bool
	FailureReason string
	AvailStatus   string
	AvailReason   string
	SlotPath      string
	TTFFMS        int64
	// ID and Gen name the attempt's row, as RecordPreloadAttempt returned them
	// (an AttemptRef). Updates select by ID: the slot path is reused by the
	// next session in the slot, so it cannot tell one attempt's row from
	// another's. Gen confines them to the database the row was written in.
	ID  int64
	Gen int64
}

// AttemptRef names one attempt's row: its id in the database of generation
// Gen. After a Reopen the same id can name an unrelated row in the new
// database, so a write whose Gen is not the current one never selects by ID.
type AttemptRef struct {
	ID  int64
	Gen int64
}

// RecordPreloadAttempt inserts a pending (preload) row for an attempt whose
// result is not known yet, and returns its ref (zero when nothing was
// written). It always inserts: the caller keeps one row per attempt, and a
// pending row that another attempt left for the same slot is not this one's.
// Safe to call with nil receiver (no-op).
func (m *StateManager) RecordPreloadAttempt(p RecordAttemptParams) AttemptRef {
	if m == nil || m.db == nil || p.SlotPath == "" {
		return AttemptRef{}
	}
	var ref AttemptRef
	err := m.withWriteLock(func(db *connRef) error {
		ref.Gen = m.gen
		return db.QueryRow(`INSERT INTO nzb_attempts (tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, match_type, success, failure_reason, avail_status, avail_reason, slot_path, preload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', '', '', ?, 1)
			RETURNING id`,
			time.Now().UnixMilli(),
			p.StreamName,
			p.ProviderName,
			p.ContentType,
			p.ContentID,
			p.ContentTitle,
			p.IndexerName,
			p.ReleaseTitle,
			p.ReleaseURL,
			p.ReleaseSize,
			p.ServedFile,
			p.MatchType,
			p.SlotPath,
		).Scan(&ref.ID)
	})
	if err != nil {
		return AttemptRef{}
	}
	return ref
}

// ownsRow reports whether p's row is in the database now in use. Called
// under the write lock, which Reopen holds while it switches databases.
func (m *StateManager) ownsRow(p RecordAttemptParams) bool {
	return p.ID > 0 && p.Gen == m.gen
}

// UpdatePendingAttempt refreshes the attempt's row (p.ID) while it is still
// pending; a finalized row is left alone. Used when playback ended too early to
// classify the release as good or bad.
func (m *StateManager) UpdatePendingAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil {
		return
	}
	_ = m.withWriteLock(func(db *connRef) error {
		if !m.ownsRow(p) {
			return nil
		}
		_, err := db.Exec(`UPDATE nzb_attempts
			SET served_file = COALESCE(NULLIF(CAST(? AS TEXT), ''), served_file),
				match_type = COALESCE(NULLIF(CAST(? AS TEXT), ''), match_type),
				indexer_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), indexer_name),
				stream_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), stream_name),
				provider_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), provider_name),
				content_title = COALESCE(NULLIF(CAST(? AS TEXT), ''), content_title),
				failure_reason = COALESCE(NULLIF(CAST(? AS TEXT), ''), failure_reason),
				avail_status = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_status),
				avail_reason = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_reason),
				ttff_ms = CASE WHEN CAST(? AS BIGINT) > 0 THEN CAST(? AS BIGINT) ELSE ttff_ms END
			WHERE id = ? AND preload = 1`,
			p.ServedFile, p.MatchType, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.FailureReason, p.AvailStatus, p.AvailReason, p.TTFFMS, p.TTFFMS, p.ID)
		return err
	})
}

// ResolvePendingAttempt finalizes the attempt's row (p.ID) if it is still
// pending; a row that already has a verdict keeps it, and nothing is inserted.
// A reason already on the row (an early stop's) is kept over p.FailureReason.
func (m *StateManager) ResolvePendingAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil {
		return
	}
	success := boolToInt(p.Success)
	_ = m.withWriteLock(func(db *connRef) error {
		if !m.ownsRow(p) {
			return nil
		}
		_, err := db.Exec(`UPDATE nzb_attempts
			SET preload = 0,
				success = ?,
				failure_reason = COALESCE(NULLIF(failure_reason, ''), CAST(? AS TEXT)),
				served_file = COALESCE(NULLIF(CAST(? AS TEXT), ''), served_file),
				match_type = COALESCE(NULLIF(CAST(? AS TEXT), ''), match_type),
				indexer_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), indexer_name),
				stream_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), stream_name),
				provider_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), provider_name),
				content_title = COALESCE(NULLIF(CAST(? AS TEXT), ''), content_title),
				avail_status = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_status),
				avail_reason = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_reason),
				ttff_ms = CASE WHEN CAST(? AS BIGINT) > 0 THEN CAST(? AS BIGINT) ELSE ttff_ms END
			WHERE id = ? AND preload = 1`,
			success, p.FailureReason, p.ServedFile, p.MatchType, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.AvailStatus, p.AvailReason, p.TTFFMS, p.TTFFMS, p.ID)
		return err
	})
}

// RecordAttempt writes the verdict on the attempt's row (p.ID) while it is
// pending or was settled without one (a short play, a close; success = 0). A
// recorded success stands: a later verdict, such as a delayed failure, changes
// nothing. The caller records at most one verdict per attempt. Without an id,
// when the row is in a database since switched away from (Reopen), or when the
// row no longer exists (History was cleared mid-play), it inserts a finalized
// row.
// Safe to call with nil receiver (no-op).
func (m *StateManager) RecordAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil {
		return
	}
	success := boolToInt(p.Success)
	err := m.withWriteLock(func(db *connRef) error {
		if m.ownsRow(p) {
			res, err := db.Exec(`UPDATE nzb_attempts
				SET preload = 0,
					success = ?,
					failure_reason = ?,
					served_file = ?,
					match_type = COALESCE(NULLIF(CAST(? AS TEXT), ''), match_type),
					indexer_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), indexer_name),
					stream_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), stream_name),
					provider_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), provider_name),
					content_title = COALESCE(NULLIF(CAST(? AS TEXT), ''), content_title),
					avail_status = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_status),
					avail_reason = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_reason),
					ttff_ms = CASE WHEN CAST(? AS BIGINT) > 0 THEN CAST(? AS BIGINT) ELSE ttff_ms END
				WHERE id = ? AND (preload = 1 OR success = 0)`,
				success, p.FailureReason, p.ServedFile, p.MatchType, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.AvailStatus, p.AvailReason, p.TTFFMS, p.TTFFMS, p.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				return nil
			}
			var exists int
			if err := db.QueryRow(`SELECT COUNT(*) FROM nzb_attempts WHERE id = ?`, p.ID).Scan(&exists); err != nil || exists > 0 {
				return err // finalized already: its verdict stands
			}
		}

		_, err := db.Exec(`INSERT INTO nzb_attempts (tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, match_type, success, failure_reason, avail_status, avail_reason, slot_path, preload, ttff_ms)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			time.Now().UnixMilli(),
			p.StreamName,
			p.ProviderName,
			p.ContentType,
			p.ContentID,
			p.ContentTitle,
			p.IndexerName,
			p.ReleaseTitle,
			p.ReleaseURL,
			p.ReleaseSize,
			p.ServedFile,
			p.MatchType,
			success,
			p.FailureReason,
			p.AvailStatus,
			p.AvailReason,
			p.SlotPath,
			p.TTFFMS,
		)
		return err
	})
	if err != nil {
		// Best-effort; don't fail playback
		return
	}
}

// ListAttemptsOptions filters and paginates NZB attempts.
type ListAttemptsOptions struct {
	ContentType string
	ContentID   string
	Limit       int
	Offset      int
	Since       *time.Time
}

// ListAttempts returns attempts newest first. Limit default 100, max 500.
func (m *StateManager) ListAttempts(opts ListAttemptsOptions) ([]NZBAttempt, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	query := `SELECT id, tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, match_type, success, failure_reason, avail_status, avail_reason, slot_path, COALESCE(preload, 0), COALESCE(ttff_ms, 0)
		FROM nzb_attempts WHERE 1=1`
	args := []interface{}{}
	if opts.ContentType != "" {
		query += ` AND content_type = ?`
		args = append(args, opts.ContentType)
	}
	if opts.ContentID != "" {
		query += ` AND content_id = ?`
		args = append(args, opts.ContentID)
	}
	if opts.Since != nil {
		query += ` AND tried_at >= ?`
		args = append(args, opts.Since.UnixMilli())
	}
	query += ` ORDER BY tried_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []NZBAttempt
	for rows.Next() {
		var a NZBAttempt
		var triedAtMs int64
		var success int
		var preload int
		var ttffMs int64
		var releaseURL, servedFile, matchType, failureReason, availStatus, availReason, slotPath, indexerName, streamName, providerName sql.NullString
		var contentTitle sql.NullString
		var releaseSize sql.NullInt64
		err := rows.Scan(&a.ID, &triedAtMs, &streamName, &providerName, &a.ContentType, &a.ContentID, &contentTitle, &indexerName, &a.ReleaseTitle, &releaseURL, &releaseSize, &servedFile, &matchType, &success, &failureReason, &availStatus, &availReason, &slotPath, &preload, &ttffMs)
		if err != nil {
			return nil, err
		}
		a.TriedAt = time.UnixMilli(triedAtMs)
		a.Success = success != 0
		a.Preload = preload != 0
		a.TTFFMS = ttffMs
		a.ReleaseURL = releaseURL.String
		a.ServedFile = servedFile.String
		a.MatchType = matchType.String
		a.FailureReason = failureReason.String
		a.AvailStatus = availStatus.String
		a.AvailReason = availReason.String
		a.SlotPath = slotPath.String
		a.ContentTitle = contentTitle.String
		a.IndexerName = indexerName.String
		a.StreamName = streamName.String
		a.ProviderName = providerName.String
		a.ReleaseSize = releaseSize.Int64
		list = append(list, a)
	}
	return list, rows.Err()
}

// DeleteAttemptsBefore removes NZB attempts older than the provided cutoff.
func (m *StateManager) DeleteAttemptsBefore(cutoff time.Time) (int64, error) {
	if m == nil || m.db == nil {
		return 0, nil
	}
	var deleted int64
	err := m.withWriteLock(func(db *connRef) error {
		res, err := db.Exec(`DELETE FROM nzb_attempts WHERE tried_at < ?`, cutoff.UnixMilli())
		if err != nil {
			return err
		}
		deleted, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// DefaultStreamLabel is the name the history UI shows for rows written before
// streams were named, whose stream_name is empty. Scoping a delete to it has to
// cover those rows too, or clearing "default" would leave them behind.
const DefaultStreamLabel = "default"

// streamScopeClause builds the WHERE fragment restricting a history delete to
// one stream. An empty name means every stream, so it yields no restriction.
func streamScopeClause(streamName string) (string, []any) {
	streamName = strings.TrimSpace(streamName)
	switch {
	case streamName == "":
		return "", nil
	case streamName == DefaultStreamLabel:
		return ` WHERE stream_name = ? OR stream_name IS NULL OR stream_name = ''`, []any{streamName}
	default:
		return ` WHERE stream_name = ?`, []any{streamName}
	}
}

// DeleteAttempts removes NZB attempts for one stream, or every attempt when
// streamName is empty.
func (m *StateManager) DeleteAttempts(streamName string) (int64, error) {
	if m == nil || m.db == nil {
		return 0, nil
	}
	where, args := streamScopeClause(streamName)
	var deleted int64
	err := m.withWriteLock(func(db *connRef) error {
		res, err := db.Exec(`DELETE FROM nzb_attempts`+where, args...)
		if err != nil {
			return err
		}
		deleted, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// RenameStreamReferences repoints history rows at a stream's new name.
//
// The NZB attempt log, the search diagnostics and the Jellyfin play state all
// store the stream name as plain text, and the history UI filters on it —
// leaving the old value behind would make a renamed stream look like it had
// never played anything, while its past runs sat under a name that no longer
// exists. The stream's Jellyfin user configuration moves with them, but is
// left out of the count returned, which is of history rows.
func (m *StateManager) RenameStreamReferences(oldName, newName string) (int64, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if m == nil || m.db == nil || oldName == "" || newName == "" || oldName == newName {
		return 0, nil
	}
	var updated int64
	err := m.withWriteLock(func(db *connRef) error {
		return m.withTx(db, func(tx *txn) error {
			// jellyfin_playstate is keyed (stream_name, item_id), so rows
			// already sitting under the new name collide with the ones being
			// moved and fail the whole rename. They are stale by definition —
			// a stream holding that name now is the one being renamed to it —
			// so the incoming progress wins. nzb_attempts and
			// search_diagnostics have no such key and just carry over.
			if _, err := tx.Exec(`DELETE FROM jellyfin_playstate WHERE stream_name = ? AND item_id IN (
				SELECT item_id FROM jellyfin_playstate WHERE stream_name = ?
			)`, newName, oldName); err != nil {
				return err
			}
			// jellyfin_user_config holds one row per stream, so whatever sits
			// under the new name is stale for the same reason; it goes even
			// when the renamed stream has no row of its own, which leaves it
			// on the defaults rather than inheriting a deleted stream's. The
			// moved row is a setting, not history, so it is not counted.
			if _, err := tx.Exec(`DELETE FROM jellyfin_user_config WHERE stream_name = ?`, newName); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE jellyfin_user_config SET stream_name = ? WHERE stream_name = ?`, newName, oldName); err != nil {
				return err
			}
			for _, table := range []string{"nzb_attempts", "search_diagnostics", "jellyfin_playstate"} {
				res, err := tx.Exec(`UPDATE `+table+` SET stream_name = ? WHERE stream_name = ?`, newName, oldName)
				if err != nil {
					return err
				}
				affected, _ := res.RowsAffected()
				updated += affected
			}
			return nil
		})
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}
