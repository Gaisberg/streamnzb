package persistence

import (
	"database/sql"
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
	Success       bool      `json:"success"`
	FailureReason string    `json:"failure_reason,omitempty"`
	AvailStatus   string    `json:"avail_status,omitempty"`
	AvailReason   string    `json:"avail_reason,omitempty"`
	SlotPath      string    `json:"slot_path,omitempty"`
	Preload       bool      `json:"preload"` // true = attempt started, result not yet known
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
	Success       bool
	FailureReason string
	AvailStatus   string
	AvailReason   string
	SlotPath      string
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
	_ = m.withWriteLock(func(db *sql.DB) error {
		_, err := db.Exec(`INSERT INTO nzb_attempts (tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, success, failure_reason, avail_status, avail_reason, slot_path, preload)
				SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', '', '', ?, 1
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
	_ = m.withWriteLock(func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE nzb_attempts
			SET served_file = COALESCE(NULLIF(?, ''), served_file),
				indexer_name = COALESCE(NULLIF(?, ''), indexer_name),
				stream_name = COALESCE(NULLIF(?, ''), stream_name),
				provider_name = COALESCE(NULLIF(?, ''), provider_name),
				content_title = COALESCE(NULLIF(?, ''), content_title),
				failure_reason = COALESCE(NULLIF(?, ''), failure_reason),
				avail_status = COALESCE(NULLIF(?, ''), avail_status),
				avail_reason = COALESCE(NULLIF(?, ''), avail_reason)
			WHERE slot_path = ? AND preload = 1`,
			p.ServedFile, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.FailureReason, p.AvailStatus, p.AvailReason, p.SlotPath)
		return err
	})
}

// RecordAttempt writes one NZB attempt row, or updates an existing preload row by slot_path. Safe to call with nil receiver (no-op).
func (m *StateManager) RecordAttempt(p RecordAttemptParams) {
	if m == nil || m.db == nil {
		return
	}
	success := 0
	if p.Success {
		success = 1
	}
	err := m.withWriteLock(func(db *sql.DB) error {
		if p.SlotPath != "" {
			// Only update the currently-pending preload row (preload=1). Historical resolved rows
			// for the same slot_path (previous plays) must not be mutated.
			res, err := db.Exec(`UPDATE nzb_attempts
				SET preload = 0,
					success = ?,
					failure_reason = ?,
					served_file = ?,
					indexer_name = COALESCE(NULLIF(?, ''), indexer_name),
					stream_name = COALESCE(NULLIF(?, ''), stream_name),
					provider_name = COALESCE(NULLIF(?, ''), provider_name),
					content_title = COALESCE(NULLIF(?, ''), content_title),
					avail_status = COALESCE(NULLIF(?, ''), avail_status),
					avail_reason = COALESCE(NULLIF(?, ''), avail_reason)
				WHERE slot_path = ? AND preload = 1`,
				success, p.FailureReason, p.ServedFile, p.IndexerName, p.StreamName, p.ProviderName, p.ContentTitle, p.AvailStatus, p.AvailReason, p.SlotPath)
			if err == nil {
				affected, _ := res.RowsAffected()
				if affected > 0 {
					return nil
				}
			}
		}

		_, err := db.Exec(`INSERT INTO nzb_attempts (tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, success, failure_reason, avail_status, avail_reason, slot_path, preload)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
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
			success,
			p.FailureReason,
			p.AvailStatus,
			p.AvailReason,
			p.SlotPath,
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

	query := `SELECT id, tried_at, stream_name, provider_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, success, failure_reason, avail_status, avail_reason, slot_path, COALESCE(preload, 0)
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
		var releaseURL, servedFile, failureReason, availStatus, availReason, slotPath, indexerName, streamName, providerName sql.NullString
		var contentTitle sql.NullString
		var releaseSize sql.NullInt64
		err := rows.Scan(&a.ID, &triedAtMs, &streamName, &providerName, &a.ContentType, &a.ContentID, &contentTitle, &indexerName, &a.ReleaseTitle, &releaseURL, &releaseSize, &servedFile, &success, &failureReason, &availStatus, &availReason, &slotPath, &preload)
		if err != nil {
			return nil, err
		}
		a.TriedAt = time.UnixMilli(triedAtMs)
		a.Success = success != 0
		a.Preload = preload != 0
		if releaseURL.Valid {
			a.ReleaseURL = releaseURL.String
		}
		if servedFile.Valid {
			a.ServedFile = servedFile.String
		}
		if failureReason.Valid {
			a.FailureReason = failureReason.String
		}
		if availStatus.Valid {
			a.AvailStatus = availStatus.String
		}
		if availReason.Valid {
			a.AvailReason = availReason.String
		}
		if slotPath.Valid {
			a.SlotPath = slotPath.String
		}
		if contentTitle.Valid {
			a.ContentTitle = contentTitle.String
		}
		if indexerName.Valid {
			a.IndexerName = indexerName.String
		}
		if streamName.Valid {
			a.StreamName = streamName.String
		}
		if providerName.Valid {
			a.ProviderName = providerName.String
		}
		if releaseSize.Valid {
			a.ReleaseSize = releaseSize.Int64
		}
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
	err := m.withWriteLock(func(db *sql.DB) error {
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
