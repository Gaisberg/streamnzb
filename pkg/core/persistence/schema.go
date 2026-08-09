package persistence

import (
	"fmt"
)

// Schema DDL is written once with canonical type tokens (see dialect.go) and
// rendered per backend. Anything integral is {INT}, which is 64-bit on both
// backends — several columns (tried_at holds UnixMilli) would overflow a
// 32-bit Postgres INTEGER.
//
// Booleans are stored as integers and converted with boolToInt rather than
// mapped to a native BOOLEAN, so one set of queries and scans serves both
// backends.
const (
	kvSchema = `CREATE TABLE IF NOT EXISTS kv (
		key {TEXT} PRIMARY KEY,
		value {BLOB} NOT NULL,
		updated_at {INT}
	);`

	// anime_mappings is a cache of an external list (Fribb's anime-lists), not
	// user state: it is replaced wholesale on refresh and can be rebuilt by
	// re-downloading. It lives here anyway so everything StreamNZB persists
	// stays in one place rather than beside the database as its own file.
	//
	// has_season distinguishes "no season published" — the entry spans the
	// whole series and numbers episodes absolutely — from season 0, which means
	// specials.
	animeMappingsSchema = `CREATE TABLE IF NOT EXISTS anime_mappings (
		kitsu_id {INT} PRIMARY KEY,
		imdb_id {TEXT},
		tvdb_id {TEXT},
		tmdb_id {TEXT},
		has_season {INT} NOT NULL DEFAULT 0,
		season {INT} NOT NULL DEFAULT 0,
		episode_offset {INT} NOT NULL DEFAULT 0,
		entry_type {TEXT}
	);`

	nzbAttemptsSchema = `CREATE TABLE IF NOT EXISTS nzb_attempts (
		id {ID},
		tried_at {INT} NOT NULL,
		stream_name {TEXT},
		provider_name {TEXT},
		content_type {TEXT} NOT NULL,
		content_id {TEXT} NOT NULL,
		content_title {TEXT},
		indexer_name {TEXT},
		release_title {TEXT} NOT NULL,
		release_url {TEXT},
		release_size {INT},
		served_file {TEXT},
		match_type {TEXT},
		success {INT} NOT NULL,
		failure_reason {TEXT},
		avail_status {TEXT},
		avail_reason {TEXT},
		slot_path {TEXT},
		preload {INT} NOT NULL DEFAULT 0,
		ttff_ms {INT} NOT NULL DEFAULT 0
	);`

	nzbAttemptsIndexTried    = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_tried_at ON nzb_attempts(tried_at DESC);`
	nzbAttemptsIndexContent  = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_content ON nzb_attempts(content_type, content_id);`
	nzbAttemptsIndexStream   = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_stream_name ON nzb_attempts(stream_name);`
	nzbAttemptsIndexProvider = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_provider_name ON nzb_attempts(provider_name);`
	nzbAttemptsIndexIndexer  = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_indexer_name ON nzb_attempts(indexer_name);`

	providerMetricsSchema = `CREATE TABLE IF NOT EXISTS provider_metrics (
		id {ID},
		collected_at {INT} NOT NULL,
		provider_name {TEXT} NOT NULL,
		host {TEXT},
		active_conns {INT} NOT NULL DEFAULT 0,
		idle_conns {INT} NOT NULL DEFAULT 0,
		max_conns {INT} NOT NULL DEFAULT 0,
		current_speed_mbps {REAL} NOT NULL DEFAULT 0,
		downloaded_mb {REAL} NOT NULL DEFAULT 0,
		usage_percent {REAL} NOT NULL DEFAULT 0,
		article_available_count {INT} NOT NULL DEFAULT 0,
		article_missing_count {INT} NOT NULL DEFAULT 0
	);`
	providerMetricsIndexTime = `CREATE INDEX IF NOT EXISTS idx_provider_metrics_collected_at ON provider_metrics(collected_at DESC);`
	providerMetricsIndexName = `CREATE INDEX IF NOT EXISTS idx_provider_metrics_name_time ON provider_metrics(provider_name, collected_at DESC);`

	indexerMetricsSchema = `CREATE TABLE IF NOT EXISTS indexer_metrics (
		id {ID},
		collected_at {INT} NOT NULL,
		indexer_name {TEXT} NOT NULL,
		api_hits_used {INT} NOT NULL DEFAULT 0,
		api_hits_limit {INT} NOT NULL DEFAULT 0,
		downloads_used {INT} NOT NULL DEFAULT 0,
		downloads_limit {INT} NOT NULL DEFAULT 0,
		searches_count {INT} NOT NULL DEFAULT 0,
		unique_hits_count {INT} NOT NULL DEFAULT 0,
		avg_response_ms {REAL} NOT NULL DEFAULT 0.0,
		avail_available_count {INT} NOT NULL DEFAULT 0,
		avail_discarded_count {INT} NOT NULL DEFAULT 0
	);`
	indexerMetricsIndexTime = `CREATE INDEX IF NOT EXISTS idx_indexer_metrics_collected_at ON indexer_metrics(collected_at DESC);`
	indexerMetricsIndexName = `CREATE INDEX IF NOT EXISTS idx_indexer_metrics_name_time ON indexer_metrics(indexer_name, collected_at DESC);`

	performanceMetricsSchema = `CREATE TABLE IF NOT EXISTS performance_metrics (
		id {ID},
		collected_at {INT} NOT NULL,
		metric_type {TEXT} NOT NULL,
		sample_count {INT} NOT NULL DEFAULT 0,
		min_ms {REAL} NOT NULL DEFAULT 0.0,
		max_ms {REAL} NOT NULL DEFAULT 0.0,
		avg_ms {REAL} NOT NULL DEFAULT 0.0,
		p50_ms {REAL} NOT NULL DEFAULT 0.0,
		p95_ms {REAL} NOT NULL DEFAULT 0.0,
		p99_ms {REAL} NOT NULL DEFAULT 0.0
	);`
	performanceMetricsIndexTime = `CREATE INDEX IF NOT EXISTS idx_performance_metrics_collected_at ON performance_metrics(collected_at DESC);`
	performanceMetricsIndexType = `CREATE INDEX IF NOT EXISTS idx_performance_metrics_type_time ON performance_metrics(metric_type, collected_at DESC);`

	// "timestamp" is quoted because Postgres treats the bare word as a type-name
	// keyword; the quoted form is an ordinary identifier on both backends.
	streamApiSamplesSchema = `CREATE TABLE IF NOT EXISTS stream_api_samples (
		id {ID},
		"timestamp" {INT} NOT NULL,
		content_type {TEXT},
		content_id {TEXT},
		total_duration_ms {INT} NOT NULL DEFAULT 0,
		metadata_duration_ms {INT} NOT NULL DEFAULT 0,
		search_duration_ms {INT} NOT NULL DEFAULT 0,
		ranking_duration_ms {INT} NOT NULL DEFAULT 0,
		avail_nzb_duration_ms {INT} NOT NULL DEFAULT 0,
		candidate_count {INT} NOT NULL DEFAULT 0,
		result_count {INT} NOT NULL DEFAULT 0
	);`
	streamApiSamplesIndexTime = `CREATE INDEX IF NOT EXISTS idx_stream_api_samples_timestamp ON stream_api_samples("timestamp" DESC);`

	playbackTtffSamplesSchema = `CREATE TABLE IF NOT EXISTS playback_ttff_samples (
		id {ID},
		"timestamp" {INT} NOT NULL,
		session_id {TEXT},
		provider_name {TEXT},
		ttff_ms {INT} NOT NULL DEFAULT 0,
		session_resolution_ms {INT} NOT NULL DEFAULT 0,
		nzb_fetch_duration_ms {INT} NOT NULL DEFAULT 0,
		nntp_connect_duration_ms {INT} NOT NULL DEFAULT 0,
		probe_duration_ms {INT} NOT NULL DEFAULT 0,
		first_byte_duration_ms {INT} NOT NULL DEFAULT 0,
		is_cache_hit {INT} NOT NULL DEFAULT 0
	);`
	playbackTtffSamplesIndexTime = `CREATE INDEX IF NOT EXISTS idx_playback_ttff_samples_timestamp ON playback_ttff_samples("timestamp" DESC);`

	libraryNZBsSchema = `CREATE TABLE IF NOT EXISTS library_nzbs (
		id {TEXT} PRIMARY KEY,
		content_type {TEXT} NOT NULL,
		content_id {TEXT} NOT NULL,
		season {INT} NOT NULL DEFAULT 0,
		episode {INT} NOT NULL DEFAULT 0,
		release_title {TEXT} NOT NULL,
		details_url {TEXT},
		indexer_name {TEXT},
		size_bytes {INT} NOT NULL DEFAULT 0,
		nzb_data {BLOB} NOT NULL,
		created_at {INT} NOT NULL,
		last_accessed_at {INT} NOT NULL,
		pinned {INT} NOT NULL DEFAULT 0
	);`
	libraryNZBsIndexContent  = `CREATE INDEX IF NOT EXISTS idx_library_nzbs_content ON library_nzbs(content_type, content_id, season, episode);`
	libraryNZBsIndexAccessed = `CREATE INDEX IF NOT EXISTS idx_library_nzbs_accessed ON library_nzbs(last_accessed_at DESC);`

	libraryBlueprintsSchema = `CREATE TABLE IF NOT EXISTS library_blueprints (
		nzb_id {TEXT} PRIMARY KEY,
		blueprint_json {TEXT} NOT NULL,
		media_file_name {TEXT} NOT NULL,
		media_file_size {INT} NOT NULL DEFAULT 0,
		created_at {INT} NOT NULL
	);`

	badReleasesSchema = `CREATE TABLE IF NOT EXISTS bad_releases (
		details_url {TEXT} PRIMARY KEY,
		release_title {TEXT},
		reason {TEXT},
		reported_at {INT} NOT NULL,
		expires_at {INT} NOT NULL
	);`
	badReleasesIndexExpires = `CREATE INDEX IF NOT EXISTS idx_bad_releases_expires ON bad_releases(expires_at);`
)

// addedColumn is one idempotent ALTER TABLE ADD COLUMN migration. Existing
// databases get the column; new ones already have it from the CREATE TABLE.
type addedColumn struct {
	table  string
	column string
	decl   string
}

// addedColumns are applied in order on every startup. Order does not matter —
// each is independent and idempotent — so new columns just get appended.
var addedColumns = []addedColumn{
	{"library_blueprints", "media_caps_json", "{TEXT}"},
	// Per-scheme content ids, so a cached release is findable regardless of
	// which id scheme (imdb/tmdb/tvdb/kitsu) a later request carries. Rows
	// written before this keep only content_id.
	{"library_nzbs", "imdb_id", "{TEXT}"},
	{"library_nzbs", "tmdb_id", "{TEXT}"},
	{"library_nzbs", "tvdb_id", "{TEXT}"},
	{"library_nzbs", "kitsu_id", "{TEXT}"},
	// Release status lifecycle. Rows written before this only ever existed
	// after successful validation, so they default to 'good'; new rows are
	// written 'pending' as soon as the NZB and blueprint are known.
	{"library_nzbs", "status", "{TEXT} NOT NULL DEFAULT 'good'"},
	{"library_nzbs", "status_reason", "{TEXT}"},
	// Freshness timestamp for the background re-verification sweep. Legacy
	// rows get 0, which reads as never verified -> stale.
	{"library_nzbs", "last_verified_at", "{INT} NOT NULL DEFAULT 0"},
	// ffprobe capability fields promoted to indexed columns so the Library UI
	// and ranking can filter/sort without parsing media_caps_json. NOT used to
	// gate or reorder by client — codec compatibility is the client decoder's
	// job.
	{"library_blueprints", "video_codec", "{TEXT}"},
	{"library_blueprints", "height", "{INT}"},
	{"library_blueprints", "bit_depth", "{INT}"},
	{"library_blueprints", "hdr", "{TEXT}"},
	{"library_blueprints", "dolby_vision", "{INT}"},
	{"library_blueprints", "audio_codec", "{TEXT}"},
	{"provider_metrics", "article_available_count", "{INT} NOT NULL DEFAULT 0"},
	{"provider_metrics", "article_missing_count", "{INT} NOT NULL DEFAULT 0"},
	{"indexer_metrics", "unique_hits_count", "{INT} NOT NULL DEFAULT 0"},
	{"nzb_attempts", "preload", "{INT} NOT NULL DEFAULT 0"},
	{"nzb_attempts", "served_file", "{TEXT}"},
	{"nzb_attempts", "match_type", "{TEXT}"},
	{"nzb_attempts", "indexer_name", "{TEXT}"},
	{"nzb_attempts", "stream_name", "{TEXT}"},
	{"nzb_attempts", "provider_name", "{TEXT}"},
	{"nzb_attempts", "avail_status", "{TEXT}"},
	{"nzb_attempts", "avail_reason", "{TEXT}"},
	{"nzb_attempts", "ttff_ms", "{INT} NOT NULL DEFAULT 0"},
}

// migratedIndexes are created after the column migrations, since several index
// columns only exist once addedColumns has run.
var migratedIndexes = []string{
	`CREATE INDEX IF NOT EXISTS idx_library_nzbs_imdb ON library_nzbs(imdb_id)`,
	`CREATE INDEX IF NOT EXISTS idx_library_nzbs_tmdb ON library_nzbs(tmdb_id)`,
	`CREATE INDEX IF NOT EXISTS idx_library_nzbs_tvdb ON library_nzbs(tvdb_id)`,
	`CREATE INDEX IF NOT EXISTS idx_library_nzbs_kitsu ON library_nzbs(kitsu_id)`,
	`CREATE INDEX IF NOT EXISTS idx_library_nzbs_status ON library_nzbs(status)`,
	`CREATE INDEX IF NOT EXISTS idx_library_nzbs_verified ON library_nzbs(last_verified_at)`,
}

// addColumn applies one ALTER TABLE ADD COLUMN, tolerating the column already
// existing.
func addColumn(c *connRef, col addedColumn) error {
	d := c.dialect()
	stmt := d.AddColumnSQL(col.table, col.column, d.ExpandDDL(col.decl))
	if _, err := c.Exec(stmt); err != nil && !d.IsDuplicateColumn(err) {
		return fmt.Errorf("migrate %s.%s: %w", col.table, col.column, err)
	}
	return nil
}

func initSchema(c *connRef) error {
	d := c.dialect()
	for _, stmt := range []string{
		kvSchema,
		nzbAttemptsSchema,
		nzbAttemptsIndexTried,
		nzbAttemptsIndexContent,
		providerMetricsSchema,
		providerMetricsIndexTime,
		providerMetricsIndexName,
		indexerMetricsSchema,
		indexerMetricsIndexTime,
		indexerMetricsIndexName,
		performanceMetricsSchema,
		performanceMetricsIndexTime,
		performanceMetricsIndexType,
		streamApiSamplesSchema,
		streamApiSamplesIndexTime,
		playbackTtffSamplesSchema,
		playbackTtffSamplesIndexTime,
		libraryNZBsSchema,
		libraryNZBsIndexContent,
		libraryNZBsIndexAccessed,
		libraryBlueprintsSchema,
		badReleasesSchema,
		badReleasesIndexExpires,
		animeMappingsSchema,
	} {
		if _, err := c.Exec(d.ExpandDDL(stmt)); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	for _, col := range addedColumns {
		if err := addColumn(c, col); err != nil {
			return err
		}
	}
	for _, stmt := range migratedIndexes {
		if _, err := c.Exec(stmt); err != nil {
			return fmt.Errorf("schema index: %w", err)
		}
	}
	for _, stmt := range []string{nzbAttemptsIndexStream, nzbAttemptsIndexProvider, nzbAttemptsIndexIndexer} {
		if _, err := c.Exec(stmt); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	return nil
}

func tableExists(c *connRef, table string) (bool, error) {
	query, args := c.dialect().TableExistsQuery(table)
	rows, err := c.Query(query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return true, rows.Err()
	}
	return false, rows.Err()
}

func tableColumns(c *connRef, table string) (map[string]struct{}, error) {
	query, args := c.dialect().TableColumnsQuery(table)
	rows, err := c.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, rows.Err()
}
