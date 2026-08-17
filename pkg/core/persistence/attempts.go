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
}

// RecordPreloadAttempt inserts a preload row for a slot (attempt started, result not yet known).
// It is idempotent: if an unresolved preload row already exists for the same slot_path it is a no-op,
// so Stremio's multiple automatic requests to the same play URL don't create duplicate rows.
// Safe to call with nil receiver (no-op).
func (m *StateManager) RecordPreloadAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil || p.SlotPath == "" {
		return
	}
	// INSERT only when no active (preload=1) row exists for this slot yet.
	_ = m.withWriteLock(func(db *connRef) error {
		// The CASTs are load-bearing: a bare SELECT ?, ?, ... gives Postgres no
		// column context to infer parameter types from, so it refuses the query.
		_, err := db.Exec(`INSERT INTO nzb_attempts (tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, match_type, success, failure_reason, avail_status, avail_reason, slot_path, preload)
				SELECT CAST(? AS BIGINT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT),
				       CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT), CAST(? AS TEXT), CAST(? AS TEXT),
				       0, '', '', '', CAST(? AS TEXT), 1
			WHERE NOT EXISTS (SELECT 1 FROM nzb_attempts WHERE slot_path = ? AND preload = 1)`,
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
			p.SlotPath, // for the NOT EXISTS sub-query
		)
		return err
	})
}

// UpdatePendingAttempt refreshes the currently unresolved preload row for a slot while keeping it pending.
// Used when playback ended too early to classify the release as good or bad.
func (m *StateManager) UpdatePendingAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil || p.SlotPath == "" {
		return
	}
	_ = m.withWriteLock(func(db *connRef) error {
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
			WHERE slot_path = ? AND preload = 1`,
			p.ServedFile, p.MatchType, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.FailureReason, p.AvailStatus, p.AvailReason, p.TTFFMS, p.TTFFMS, p.SlotPath)
		return err
	})
}

// ResolvePendingAttempt finalizes the currently unresolved preload row for a slot without inserting
// a new row when no pending preload exists anymore.
func (m *StateManager) ResolvePendingAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil || p.SlotPath == "" {
		return
	}
	success := boolToInt(p.Success)
	_ = m.withWriteLock(func(db *connRef) error {
		_, err := db.Exec(`UPDATE nzb_attempts
			SET preload = 0,
				success = ?,
				failure_reason = ?,
				served_file = COALESCE(NULLIF(CAST(? AS TEXT), ''), served_file),
				match_type = COALESCE(NULLIF(CAST(? AS TEXT), ''), match_type),
				indexer_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), indexer_name),
				stream_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), stream_name),
				provider_name = COALESCE(NULLIF(CAST(? AS TEXT), ''), provider_name),
				content_title = COALESCE(NULLIF(CAST(? AS TEXT), ''), content_title),
				avail_status = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_status),
				avail_reason = COALESCE(NULLIF(CAST(? AS TEXT), ''), avail_reason),
				ttff_ms = CASE WHEN CAST(? AS BIGINT) > 0 THEN CAST(? AS BIGINT) ELSE ttff_ms END
			WHERE slot_path = ? AND preload = 1`,
			success, p.FailureReason, p.ServedFile, p.MatchType, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.AvailStatus, p.AvailReason, p.TTFFMS, p.TTFFMS, p.SlotPath)
		return err
	})
}

// RecordAttempt writes one NZB attempt row, or updates an existing preload row by slot_path. Safe to call with nil receiver (no-op).
func (m *StateManager) RecordAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil {
		return
	}
	success := boolToInt(p.Success)
	err := m.withWriteLock(func(db *connRef) error {
		if p.SlotPath != "" {
			// Only update the currently-pending preload row (preload=1). Historical resolved rows
			// for the same slot_path (previous plays) must not be mutated.
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
				WHERE slot_path = ? AND preload = 1`,
				success, p.FailureReason, p.ServedFile, p.MatchType, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.AvailStatus, p.AvailReason, p.TTFFMS, p.TTFFMS, p.SlotPath)
			if err == nil {
				affected, _ := res.RowsAffected()
				if affected > 0 {
					return nil
				}
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

// RenameStreamReferences repoints history rows at a stream's new name.
//
// Both the NZB attempt log and the search diagnostics store the stream name as
// plain text, and the history UI filters on it — leaving the old value behind
// would make a renamed stream look like it had never played anything, while its
// past runs sat under a name that no longer exists.
func (m *StateManager) RenameStreamReferences(oldName, newName string) (int64, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if m == nil || m.db == nil || oldName == "" || newName == "" || oldName == newName {
		return 0, nil
	}
	var updated int64
	err := m.withWriteLock(func(db *connRef) error {
		return m.withTx(db, func(tx *txn) error {
			for _, table := range []string{"nzb_attempts", "search_diagnostics"} {
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
