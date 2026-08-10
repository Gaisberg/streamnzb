package persistence

import (
	"database/sql"
	"time"
)

// SearchDiagnostic is one persisted search-funnel record: everything one
// uncached playlist build learned about where results came from and where they
// were dropped. Payload is the diag.Snapshot JSON, opaque to this layer.
type SearchDiagnostic struct {
	ID           int64     `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	StreamName   string    `json:"stream_name"`
	ContentType  string    `json:"content_type"`
	ContentID    string    `json:"content_id"`
	ContentTitle string    `json:"content_title,omitempty"`
	Payload      string    `json:"payload"`
}

// searchDiagnosticsKeepRows bounds the table. Diagnostics are a debugging aid
// for recent requests, not an archive; a few hundred rows outlives everything
// the history page can still show attempts for.
const searchDiagnosticsKeepRows = 500

// RecordSearchDiagnostic inserts one diagnostics row and prunes the oldest
// rows past the retention bound. Safe on a nil receiver.
func (m *StateManager) RecordSearchDiagnostic(d SearchDiagnostic) {
	if m == nil || m.db == nil || d.Payload == "" {
		return
	}
	createdAt := d.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_ = m.withWriteLock(func(db *connRef) error {
		if _, err := db.Exec(`INSERT INTO search_diagnostics (created_at, stream_name, content_type, content_id, content_title, payload)
			VALUES (?, ?, ?, ?, ?, ?)`,
			createdAt.UnixMilli(),
			d.StreamName,
			d.ContentType,
			d.ContentID,
			d.ContentTitle,
			d.Payload,
		); err != nil {
			return err
		}
		// Prune by id ordering: ids are monotonic and this avoids a second
		// timestamp index scan on every insert.
		_, err := db.Exec(`DELETE FROM search_diagnostics WHERE id NOT IN (
			SELECT id FROM search_diagnostics ORDER BY id DESC LIMIT ?)`,
			searchDiagnosticsKeepRows,
		)
		return err
	})
}

// ListSearchDiagnosticsOptions filters diagnostics rows; zero values mean "any".
type ListSearchDiagnosticsOptions struct {
	StreamName  string
	ContentType string
	ContentID   string
	Limit       int
}

// ListSearchDiagnostics returns diagnostics newest first. Limit default 50,
// max 200.
func (m *StateManager) ListSearchDiagnostics(opts ListSearchDiagnosticsOptions) ([]SearchDiagnostic, error) {
	if m == nil || m.db == nil {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	query := `SELECT id, created_at, stream_name, content_type, content_id, content_title, payload
		FROM search_diagnostics WHERE 1=1`
	args := []interface{}{}
	if opts.StreamName != "" {
		query += ` AND stream_name = ?`
		args = append(args, opts.StreamName)
	}
	if opts.ContentType != "" {
		query += ` AND content_type = ?`
		args = append(args, opts.ContentType)
	}
	if opts.ContentID != "" {
		query += ` AND content_id = ?`
		args = append(args, opts.ContentID)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []SearchDiagnostic
	for rows.Next() {
		var d SearchDiagnostic
		var createdAtMs int64
		var streamName, contentTitle sql.NullString
		if err := rows.Scan(&d.ID, &createdAtMs, &streamName, &d.ContentType, &d.ContentID, &contentTitle, &d.Payload); err != nil {
			return nil, err
		}
		d.CreatedAt = time.UnixMilli(createdAtMs)
		d.StreamName = streamName.String
		d.ContentTitle = contentTitle.String
		list = append(list, d)
	}
	return list, rows.Err()
}
