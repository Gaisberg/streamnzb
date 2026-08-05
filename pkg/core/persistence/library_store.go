package persistence

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Library release status lifecycle: items are stored 'pending' as soon as the
// NZB and blueprint are known, then marked 'good' (validated / sustained play)
// or 'bad' (definitive article hole / corruption) once a verdict exists.
const (
	LibraryStatusPending = "pending"
	LibraryStatusGood    = "good"
	LibraryStatusBad     = "bad"
)

type LibraryItem struct {
	ID          string `json:"id"`
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id"`
	// Per-scheme content ids, stored so the item is reachable by any of them.
	ImdbID  string `json:"imdb_id,omitempty"`
	TmdbID  string `json:"tmdb_id,omitempty"`
	TvdbID  string `json:"tvdb_id,omitempty"`
	KitsuID string `json:"kitsu_id,omitempty"`

	Season       int    `json:"season"`
	Episode      int    `json:"episode"`
	ReleaseTitle string `json:"release_title"`
	DetailsURL   string `json:"details_url"`
	IndexerName  string `json:"indexer_name"`
	SizeBytes    int64  `json:"size_bytes"`
	NZBData      []byte `json:"-"`
	// NZBSizeBytes is the stored (compressed) NZB payload size. Listing queries
	// populate it via length(nzb_data) WITHOUT loading the blob, so "has NZB"
	// checks work on listings where NZBData is intentionally not fetched.
	NZBSizeBytes   int64     `json:"nzb_size_bytes,omitempty"`
	BlueprintJSON  string    `json:"blueprint_json,omitempty"`
	MediaFileName  string    `json:"media_file_name,omitempty"`
	MediaFileSize  int64     `json:"media_file_size,omitempty"`
	MediaCapsJSON  string    `json:"media_caps_json,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessedAt time.Time `json:"last_accessed_at"`
	LastVerifiedAt time.Time `json:"last_verified_at"`
	Pinned         bool      `json:"pinned"`
	Status         string    `json:"status"`
	StatusReason   string    `json:"status_reason,omitempty"`

	// Indexed capability fields (informational / filtering / ranking only).
	VideoCodec  string `json:"video_codec,omitempty"`
	Height      int    `json:"height,omitempty"`
	BitDepth    int    `json:"bit_depth,omitempty"`
	HDR         string `json:"hdr,omitempty"`
	DolbyVision bool   `json:"dolby_vision,omitempty"`
	AudioCodec  string `json:"audio_codec,omitempty"`
}

type LibraryStats struct {
	TotalItems      int64 `json:"total_items"`
	TotalNZBBytes   int64 `json:"total_nzb_bytes"`
	TotalMediaBytes int64 `json:"total_media_bytes"`
	TotalSizeBytes  int64 `json:"total_size_bytes"`
	PinnedItems     int64 `json:"pinned_items"`
	GoodItems       int64 `json:"good_items"`
	PendingItems    int64 `json:"pending_items"`
	BadItems        int64 `json:"bad_items"`
}

type LibraryStore struct {
	db  *sql.DB // shared read pool
	wdb *sql.DB // single-connection write handle
	mu  sync.RWMutex
}

func NewLibraryStore(db, wdb *sql.DB) *LibraryStore {
	return &LibraryStore{db: db, wdb: wdb}
}

func compressNZB(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecompressNZB(compressed []byte) ([]byte, error) {
	if len(compressed) == 0 {
		return nil, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
}

func buildLibraryItemID(contentType, contentID string, season, episode int, releaseTitle string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	contentID = strings.ToLower(strings.TrimSpace(contentID))
	releaseTitle = strings.TrimSpace(releaseTitle)
	if season <= 0 && episode <= 0 {
		return fmt.Sprintf("%s:%s:%s", contentType, contentID, releaseTitle)
	}
	return fmt.Sprintf("%s:%s:%d:%d:%s", contentType, contentID, season, episode, releaseTitle)
}

func (ls *LibraryStore) StoreItem(item *LibraryItem) error {
	if ls == nil || ls.db == nil || item == nil {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()

	item.ContentType = strings.ToLower(strings.TrimSpace(item.ContentType))
	item.ContentID = strings.ToLower(strings.TrimSpace(item.ContentID))
	item.ImdbID = strings.ToLower(strings.TrimSpace(item.ImdbID))
	item.TmdbID = strings.ToLower(strings.TrimSpace(item.TmdbID))
	item.TvdbID = strings.ToLower(strings.TrimSpace(item.TvdbID))
	item.KitsuID = strings.ToLower(strings.TrimSpace(item.KitsuID))
	if item.ID == "" {
		item.ID = buildLibraryItemID(item.ContentType, item.ContentID, item.Season, item.Episode, item.ReleaseTitle)
	}
	now := time.Now().Unix()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Unix(now, 0)
	}
	if item.LastAccessedAt.IsZero() {
		item.LastAccessedAt = time.Unix(now, 0)
	}

	compressedNZB, err := compressNZB(item.NZBData)
	if err != nil {
		return fmt.Errorf("compress nzb data: %w", err)
	}

	tx, err := ls.wdb.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	pinnedInt := 0
	if item.Pinned {
		pinnedInt = 1
	}

	// Status defaults to pending; a re-save can promote to good/bad but a later
	// pending save must never downgrade an existing verdict.
	status := strings.ToLower(strings.TrimSpace(item.Status))
	if status == "" {
		status = LibraryStatusPending
	}
	res, err := tx.Exec(`
		INSERT INTO library_nzbs (
			id, content_type, content_id, imdb_id, tmdb_id, tvdb_id, kitsu_id, season, episode, release_title, details_url, indexer_name, size_bytes, nzb_data, created_at, last_accessed_at, last_verified_at, pinned, status, status_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			imdb_id = CASE WHEN excluded.imdb_id != '' THEN excluded.imdb_id ELSE library_nzbs.imdb_id END,
			tmdb_id = CASE WHEN excluded.tmdb_id != '' THEN excluded.tmdb_id ELSE library_nzbs.tmdb_id END,
			tvdb_id = CASE WHEN excluded.tvdb_id != '' THEN excluded.tvdb_id ELSE library_nzbs.tvdb_id END,
			kitsu_id = CASE WHEN excluded.kitsu_id != '' THEN excluded.kitsu_id ELSE library_nzbs.kitsu_id END,
			release_title = excluded.release_title,
			details_url = excluded.details_url,
			indexer_name = excluded.indexer_name,
			size_bytes = excluded.size_bytes,
			nzb_data = CASE WHEN length(excluded.nzb_data) > 0 THEN excluded.nzb_data ELSE library_nzbs.nzb_data END,
			last_accessed_at = excluded.last_accessed_at,
			last_verified_at = excluded.last_verified_at,
			pinned = CASE WHEN excluded.pinned = 1 THEN 1 ELSE library_nzbs.pinned END,
			status = CASE WHEN excluded.status IN ('good','bad') THEN excluded.status ELSE library_nzbs.status END,
			status_reason = CASE WHEN excluded.status IN ('good','bad') THEN excluded.status_reason ELSE library_nzbs.status_reason END
	`,
		item.ID, item.ContentType, item.ContentID, item.ImdbID, item.TmdbID, item.TvdbID, item.KitsuID,
		item.Season, item.Episode, item.ReleaseTitle, item.DetailsURL, item.IndexerName, item.SizeBytes,
		compressedNZB, item.CreatedAt.Unix(), item.LastAccessedAt.Unix(), now, pinnedInt, status, item.StatusReason,
	)
	if err != nil {
		return fmt.Errorf("insert library_nzbs: %w", err)
	}
	_ = res

	hasBlueprintRow := item.BlueprintJSON != "" || item.MediaCapsJSON != "" ||
		item.MediaFileName != "" || item.VideoCodec != "" || item.Height > 0 ||
		item.BitDepth > 0 || item.HDR != "" || item.DolbyVision || item.AudioCodec != ""
	if hasBlueprintRow {
		dvInt := 0
		if item.DolbyVision {
			dvInt = 1
		}
		_, err = tx.Exec(`
			INSERT INTO library_blueprints (
				nzb_id, blueprint_json, media_file_name, media_file_size, media_caps_json,
				video_codec, height, bit_depth, hdr, dolby_vision, audio_codec, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(nzb_id) DO UPDATE SET
				blueprint_json = excluded.blueprint_json,
				media_file_name = excluded.media_file_name,
				media_file_size = excluded.media_file_size,
				media_caps_json = CASE WHEN length(excluded.media_caps_json) > 0 THEN excluded.media_caps_json ELSE library_blueprints.media_caps_json END,
				video_codec = CASE WHEN excluded.video_codec != '' THEN excluded.video_codec ELSE library_blueprints.video_codec END,
				height = CASE WHEN excluded.height > 0 THEN excluded.height ELSE library_blueprints.height END,
				bit_depth = CASE WHEN excluded.bit_depth > 0 THEN excluded.bit_depth ELSE library_blueprints.bit_depth END,
				hdr = CASE WHEN excluded.hdr != '' THEN excluded.hdr ELSE library_blueprints.hdr END,
				dolby_vision = CASE WHEN excluded.dolby_vision = 1 THEN 1 ELSE library_blueprints.dolby_vision END,
				audio_codec = CASE WHEN excluded.audio_codec != '' THEN excluded.audio_codec ELSE library_blueprints.audio_codec END
		`, item.ID, item.BlueprintJSON, item.MediaFileName, item.MediaFileSize, item.MediaCapsJSON,
			item.VideoCodec, item.Height, item.BitDepth, item.HDR, dvInt, item.AudioCodec, item.CreatedAt.Unix())
		if err != nil {
			return fmt.Errorf("insert library_blueprints: %w", err)
		}
	}

	return tx.Commit()
}

// candidatesSelectSQL selects every candidate column joined with its blueprint.
// The WHERE clause and ORDER are appended by the query helpers.
const candidatesSelectSQL = `
	SELECT n.id, n.content_type, n.content_id, n.imdb_id, n.tmdb_id, n.tvdb_id, n.kitsu_id,
	       n.season, n.episode, n.release_title, n.details_url, n.indexer_name, n.size_bytes,
	       n.nzb_data, n.created_at, n.last_accessed_at, n.last_verified_at, n.pinned,
	       n.status, n.status_reason,
	       b.blueprint_json, b.media_file_name, b.media_file_size, b.media_caps_json,
	       b.video_codec, b.height, b.bit_depth, b.hdr, b.dolby_vision, b.audio_codec
	FROM library_nzbs n
	LEFT JOIN library_blueprints b ON n.id = b.nzb_id
	WHERE `

// GetCandidates returns cached releases for a single content id (back-compat).
func (ls *LibraryStore) GetCandidates(contentType, contentID string, season, episode int) ([]*LibraryItem, error) {
	contentID = strings.ToLower(strings.TrimSpace(contentID))
	if contentID == "" {
		return nil, nil
	}
	return ls.queryCandidates(contentType, "n.content_id = ?", []any{contentID}, season, episode)
}

// GetCandidatesByIDs returns cached releases reachable by ANY of the supplied
// per-scheme content ids, so a request addressed by a different id scheme than
// the one under which the item was originally stored still hits. Rows written
// before the multi-id migration carry only content_id, which is also matched.
func (ls *LibraryStore) GetCandidatesByIDs(contentType, imdb, tmdb, tvdb, kitsu string, season, episode int) ([]*LibraryItem, error) {
	var ors []string
	var args []any
	addCol := func(col, val string) {
		val = strings.ToLower(strings.TrimSpace(val))
		if val == "" {
			return
		}
		ors = append(ors, "n."+col+" = ?")
		args = append(args, val)
	}
	addCol("imdb_id", imdb)
	addCol("tmdb_id", tmdb)
	addCol("tvdb_id", tvdb)
	addCol("kitsu_id", kitsu)
	// Back-compat: legacy rows only populated content_id (the canonical id), so
	// match it against every provided id too.
	for _, v := range []string{imdb, tmdb, tvdb, kitsu} {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			ors = append(ors, "n.content_id = ?")
			args = append(args, v)
		}
	}
	if len(ors) == 0 {
		return nil, nil
	}
	return ls.queryCandidates(contentType, strings.Join(ors, " OR "), args, season, episode)
}

func (ls *LibraryStore) queryCandidates(contentType, idWhere string, idArgs []any, season, episode int) ([]*LibraryItem, error) {
	if ls == nil || ls.db == nil {
		return nil, nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	contentType = strings.ToLower(strings.TrimSpace(contentType))
	// Bad releases stay stored (visible in the Library UI) but are never offered
	// as playback candidates.
	query := candidatesSelectSQL + "n.status != 'bad' AND n.content_type = ? AND (" + idWhere + ")"
	args := append([]any{contentType}, idArgs...)
	if contentType == "series" && (season > 0 || episode > 0) {
		if season > 0 {
			query += " AND (n.season = ? OR n.season = 0)"
			args = append(args, season)
		}
		if episode > 0 {
			query += " AND (n.episode = ? OR n.episode = 0)"
			args = append(args, episode)
		}
	}
	query += " ORDER BY n.pinned DESC, n.last_accessed_at DESC"

	rows, err := ls.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*LibraryItem
	for rows.Next() {
		item, err := scanCandidateRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanCandidateRow(rows *sql.Rows) (*LibraryItem, error) {
	var item LibraryItem
	var createdUnix, accessedUnix, verifiedUnix int64
	var pinnedInt int
	var compressedNZB []byte
	var imdbID, tmdbID, tvdbID, kitsuID, statusStr, statusReason sql.NullString
	var bpJSON, mediaName, capsJSON, videoCodec, hdr, audioCodec sql.NullString
	var mediaSize, height, bitDepth sql.NullInt64
	var dolbyVision sql.NullInt64

	if err := rows.Scan(
		&item.ID, &item.ContentType, &item.ContentID, &imdbID, &tmdbID, &tvdbID, &kitsuID,
		&item.Season, &item.Episode, &item.ReleaseTitle, &item.DetailsURL, &item.IndexerName, &item.SizeBytes,
		&compressedNZB, &createdUnix, &accessedUnix, &verifiedUnix, &pinnedInt,
		&statusStr, &statusReason,
		&bpJSON, &mediaName, &mediaSize, &capsJSON,
		&videoCodec, &height, &bitDepth, &hdr, &dolbyVision, &audioCodec,
	); err != nil {
		return nil, err
	}
	item.Status = statusStr.String
	item.StatusReason = statusReason.String
	item.CreatedAt = time.Unix(createdUnix, 0)
	item.LastAccessedAt = time.Unix(accessedUnix, 0)
	if verifiedUnix > 0 {
		item.LastVerifiedAt = time.Unix(verifiedUnix, 0)
	}
	item.Pinned = pinnedInt == 1
	item.ImdbID = imdbID.String
	item.TmdbID = tmdbID.String
	item.TvdbID = tvdbID.String
	item.KitsuID = kitsuID.String
	if bpJSON.Valid {
		item.BlueprintJSON = bpJSON.String
	}
	if mediaName.Valid {
		item.MediaFileName = mediaName.String
	}
	if mediaSize.Valid {
		item.MediaFileSize = mediaSize.Int64
	}
	if capsJSON.Valid {
		item.MediaCapsJSON = capsJSON.String
	}
	item.VideoCodec = videoCodec.String
	item.Height = int(height.Int64)
	item.BitDepth = int(bitDepth.Int64)
	item.HDR = hdr.String
	item.DolbyVision = dolbyVision.Int64 == 1
	item.AudioCodec = audioCodec.String
	if len(compressedNZB) > 0 {
		if decompressed, err := DecompressNZB(compressedNZB); err == nil {
			item.NZBData = decompressed
		}
	}
	return &item, nil
}

// GetItem returns a single library item by id, including its decompressed NZB
// payload, or nil when not found.
func (ls *LibraryStore) GetItem(id string) (*LibraryItem, error) {
	if ls == nil || ls.db == nil || strings.TrimSpace(id) == "" {
		return nil, nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	rows, err := ls.db.Query(candidatesSelectSQL+"n.id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	return scanCandidateRow(rows)
}

// markStatusWhere applies a status verdict to all rows matching the WHERE
// clause. A good verdict also refreshes last_verified_at: sustained playback is
// proof the articles exist, so the freshness sweep need not re-check soon.
func (ls *LibraryStore) markStatusWhere(where, status, reason string, matchArg any) {
	if ls == nil || ls.db == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if status == LibraryStatusGood {
		_, _ = ls.wdb.Exec(`UPDATE library_nzbs SET status = ?, status_reason = ?, last_verified_at = ? WHERE `+where, status, reason, time.Now().Unix(), matchArg)
		return
	}
	_, _ = ls.wdb.Exec(`UPDATE library_nzbs SET status = ?, status_reason = ? WHERE `+where, status, reason, matchArg)
}

// MarkStatus sets the status verdict (good/bad/pending) with a reason on one item.
func (ls *LibraryStore) MarkStatus(id, status, reason string) {
	if id == "" {
		return
	}
	ls.markStatusWhere("id = ?", status, reason, id)
}

// MarkStatusByDetailsURL sets the status verdict on every item with the given details URL.
func (ls *LibraryStore) MarkStatusByDetailsURL(detailsURL, status, reason string) {
	detailsURL = strings.TrimSpace(detailsURL)
	if detailsURL == "" {
		return
	}
	ls.markStatusWhere("details_url = ?", status, reason, detailsURL)
}

// MarkStatusByTitle sets the status verdict on every item with the given release title.
func (ls *LibraryStore) MarkStatusByTitle(title, status, reason string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	ls.markStatusWhere("release_title = ?", status, reason, title)
}

// StaleItems returns up to limit non-pinned items last verified before `before`
// (oldest first), including their NZBData, for the background freshness sweep.
func (ls *LibraryStore) StaleItems(before time.Time, limit int) ([]*LibraryItem, error) {
	if ls == nil || ls.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	query := candidatesSelectSQL + "n.pinned = 0 AND n.status != 'bad' AND n.last_verified_at < ? ORDER BY n.last_verified_at ASC LIMIT ?"
	rows, err := ls.db.Query(query, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*LibraryItem
	for rows.Next() {
		item, err := scanCandidateRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// MarkVerified refreshes an item's freshness timestamp after a successful re-STAT.
func (ls *LibraryStore) MarkVerified(id string, t time.Time) {
	if ls == nil || ls.db == nil || id == "" {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	_, _ = ls.wdb.Exec(`UPDATE library_nzbs SET last_verified_at = ? WHERE id = ?`, t.Unix(), id)
}

func (ls *LibraryStore) TouchItem(id string) {
	if ls == nil || ls.db == nil || id == "" {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	_, _ = ls.wdb.Exec(`UPDATE library_nzbs SET last_accessed_at = ? WHERE id = ?`, time.Now().Unix(), id)
}

func (ls *LibraryStore) GetFilteredItems(search, contentType string, pinnedOnly bool, statusFilter string, offset, limit int) ([]*LibraryItem, int, error) {
	if ls == nil || ls.db == nil {
		return nil, 0, nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	search = strings.TrimSpace(search)
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	statusFilter = strings.ToLower(strings.TrimSpace(statusFilter))

	whereClauses := []string{"1=1"}
	var args []any

	if contentType != "" && contentType != "all" {
		whereClauses = append(whereClauses, "n.content_type = ?")
		args = append(args, contentType)
	}
	if pinnedOnly {
		whereClauses = append(whereClauses, "n.pinned = 1")
	}
	switch statusFilter {
	case "", "all":
		// no status restriction
	case LibraryStatusGood, LibraryStatusBad, LibraryStatusPending:
		whereClauses = append(whereClauses, "n.status = ?")
		args = append(args, statusFilter)
	}
	if search != "" {
		likePattern := "%" + strings.ToLower(search) + "%"
		whereClauses = append(whereClauses, "(LOWER(n.release_title) LIKE ? OR LOWER(n.content_id) LIKE ? OR LOWER(n.indexer_name) LIKE ?)")
		args = append(args, likePattern, likePattern, likePattern)
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	countQuery := "SELECT COUNT(*) FROM library_nzbs n WHERE " + whereSQL
	var total int
	if err := ls.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT n.id, n.content_type, n.content_id, n.season, n.episode, n.release_title,
		       n.details_url, n.indexer_name, n.size_bytes, COALESCE(length(n.nzb_data), 0), n.created_at,
		       n.last_accessed_at, n.last_verified_at, n.pinned, n.status, n.status_reason,
		       b.blueprint_json, b.media_file_name, b.media_file_size,
		       b.video_codec, b.height, b.bit_depth, b.hdr, b.dolby_vision, b.audio_codec
		FROM library_nzbs n
		LEFT JOIN library_blueprints b ON n.id = b.nzb_id
		WHERE ` + whereSQL + `
		ORDER BY n.pinned DESC, n.last_accessed_at DESC
		LIMIT ? OFFSET ?
	`
	queryArgs := append(append([]any(nil), args...), limit, offset)

	rows, err := ls.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []*LibraryItem
	for rows.Next() {
		var item LibraryItem
		var createdUnix, accessedUnix, verifiedUnix int64
		var pinnedInt int
		var statusStr, statusReason sql.NullString
		var bpJSON, mediaName, videoCodec, hdr, audioCodec sql.NullString
		var mediaSize, height, bitDepth, dolbyVision sql.NullInt64

		err := rows.Scan(
			&item.ID, &item.ContentType, &item.ContentID, &item.Season, &item.Episode,
			&item.ReleaseTitle, &item.DetailsURL, &item.IndexerName, &item.SizeBytes, &item.NZBSizeBytes,
			&createdUnix, &accessedUnix, &verifiedUnix, &pinnedInt, &statusStr, &statusReason,
			&bpJSON, &mediaName, &mediaSize,
			&videoCodec, &height, &bitDepth, &hdr, &dolbyVision, &audioCodec,
		)
		if err != nil {
			return nil, 0, err
		}
		item.CreatedAt = time.Unix(createdUnix, 0)
		item.LastAccessedAt = time.Unix(accessedUnix, 0)
		if verifiedUnix > 0 {
			item.LastVerifiedAt = time.Unix(verifiedUnix, 0)
		}
		item.Pinned = pinnedInt == 1
		item.Status = statusStr.String
		item.StatusReason = statusReason.String
		if bpJSON.Valid {
			item.BlueprintJSON = bpJSON.String
		}
		if mediaName.Valid {
			item.MediaFileName = mediaName.String
		}
		if mediaSize.Valid {
			item.MediaFileSize = mediaSize.Int64
		}
		item.VideoCodec = videoCodec.String
		item.Height = int(height.Int64)
		item.BitDepth = int(bitDepth.Int64)
		item.HDR = hdr.String
		item.DolbyVision = dolbyVision.Int64 == 1
		item.AudioCodec = audioCodec.String
		items = append(items, &item)
	}

	return items, total, rows.Err()
}

func (ls *LibraryStore) SetPinned(id string, pinned bool) error {
	if ls == nil || ls.db == nil || id == "" {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	pinnedInt := 0
	if pinned {
		pinnedInt = 1
	}
	_, err := ls.wdb.Exec(`UPDATE library_nzbs SET pinned = ? WHERE id = ?`, pinnedInt, id)
	return err
}

func (ls *LibraryStore) DeleteItem(id string) error {
	if ls == nil || ls.db == nil || id == "" {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	_, _ = ls.wdb.Exec(`DELETE FROM library_blueprints WHERE nzb_id = ?`, id)
	_, err := ls.wdb.Exec(`DELETE FROM library_nzbs WHERE id = ?`, id)
	return err
}

func (ls *LibraryStore) DeleteByDetailsURL(detailsURL string) error {
	detailsURL = strings.TrimSpace(detailsURL)
	if ls == nil || ls.db == nil || detailsURL == "" {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()

	var ids []string
	rows, err := ls.db.Query(`SELECT id FROM library_nzbs WHERE details_url = ?`, detailsURL)
	if err == nil {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	for _, id := range ids {
		_, _ = ls.wdb.Exec(`DELETE FROM library_blueprints WHERE nzb_id = ?`, id)
		_, _ = ls.wdb.Exec(`DELETE FROM library_nzbs WHERE id = ?`, id)
	}
	return nil
}

func (ls *LibraryStore) DeleteByTitle(title string) error {
	title = strings.TrimSpace(title)
	if ls == nil || ls.db == nil || title == "" {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()

	var ids []string
	rows, err := ls.db.Query(`SELECT id FROM library_nzbs WHERE release_title = ?`, title)
	if err == nil {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	for _, id := range ids {
		_, _ = ls.wdb.Exec(`DELETE FROM library_blueprints WHERE nzb_id = ?`, id)
		_, _ = ls.wdb.Exec(`DELETE FROM library_nzbs WHERE id = ?`, id)
	}
	return nil
}

func (ls *LibraryStore) GetStats() (LibraryStats, error) {
	var stats LibraryStats
	if ls == nil || ls.db == nil {
		return stats, nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	_ = ls.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(length(nzb_data)), 0),
		       COALESCE(SUM(size_bytes), 0)
		FROM library_nzbs
	`).Scan(&stats.TotalItems, &stats.TotalNZBBytes, &stats.TotalSizeBytes)

	_ = ls.db.QueryRow(`
		SELECT COALESCE(SUM(media_file_size), 0) FROM library_blueprints
	`).Scan(&stats.TotalMediaBytes)

	_ = ls.db.QueryRow(`SELECT COUNT(*) FROM library_nzbs WHERE pinned = 1`).Scan(&stats.PinnedItems)
	_ = ls.db.QueryRow(`SELECT COUNT(*) FROM library_nzbs WHERE status = 'good'`).Scan(&stats.GoodItems)
	_ = ls.db.QueryRow(`SELECT COUNT(*) FROM library_nzbs WHERE status = 'pending'`).Scan(&stats.PendingItems)
	_ = ls.db.QueryRow(`SELECT COUNT(*) FROM library_nzbs WHERE status = 'bad'`).Scan(&stats.BadItems)
	return stats, nil
}

func (ls *LibraryStore) EnforceQuota(maxItems int, maxSizeMB int) error {
	if ls == nil || ls.db == nil {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()

	// 1. Count-based: evict the oldest non-pinned rows beyond maxItems.
	if maxItems > 0 {
		var count int
		_ = ls.db.QueryRow(`SELECT COUNT(*) FROM library_nzbs WHERE pinned = 0`).Scan(&count)
		if count > maxItems {
			ls.deleteItemsLocked(ls.oldestNonPinnedIDsLocked(count - maxItems))
		}
	}

	// 2. Size-based: evict oldest non-pinned rows until the stored footprint
	//    (compressed nzb_data bytes) is within budget. Pinned rows are never
	//    evicted, so if they alone exceed the budget we stop early.
	if maxSizeMB > 0 {
		limit := int64(maxSizeMB) * 1024 * 1024
		var total int64
		_ = ls.db.QueryRow(`SELECT COALESCE(SUM(length(nzb_data)), 0) FROM library_nzbs`).Scan(&total)
		if total > limit {
			rows, err := ls.db.Query(`SELECT id, length(nzb_data) FROM library_nzbs WHERE pinned = 0 ORDER BY last_accessed_at ASC`)
			if err == nil {
				var ids []string
				for total > limit && rows.Next() {
					var id string
					var sz int64
					if err := rows.Scan(&id, &sz); err == nil {
						ids = append(ids, id)
						total -= sz
					}
				}
				rows.Close()
				ls.deleteItemsLocked(ids)
			}
		}
	}
	return nil
}

// oldestNonPinnedIDsLocked returns up to n non-pinned ids, oldest-accessed first.
// The caller must hold ls.mu.
func (ls *LibraryStore) oldestNonPinnedIDsLocked(n int) []string {
	if n <= 0 {
		return nil
	}
	rows, err := ls.db.Query(`SELECT id FROM library_nzbs WHERE pinned = 0 ORDER BY last_accessed_at ASC LIMIT ?`, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// deleteItemsLocked removes each id from both library tables (no orphaned
// blueprint rows). The caller must hold ls.mu.
func (ls *LibraryStore) deleteItemsLocked(ids []string) {
	for _, id := range ids {
		_, _ = ls.wdb.Exec(`DELETE FROM library_blueprints WHERE nzb_id = ?`, id)
		_, _ = ls.wdb.Exec(`DELETE FROM library_nzbs WHERE id = ?`, id)
	}
}
