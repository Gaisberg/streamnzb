package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"

	_ "modernc.org/sqlite"
)

const (
	dbFilename = "streamnzb.db"

	kvSchema = `CREATE TABLE IF NOT EXISTS kv (
		key TEXT PRIMARY KEY,
		value BLOB NOT NULL,
		updated_at INTEGER
	);`

	nzbAttemptsSchema = `CREATE TABLE IF NOT EXISTS nzb_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		tried_at INTEGER NOT NULL,
		stream_name TEXT,
		provider_name TEXT,
		content_type TEXT NOT NULL,
		content_id TEXT NOT NULL,
		content_title TEXT,
		indexer_name TEXT,
		release_title TEXT NOT NULL,
		release_url TEXT,
		release_size INTEGER,
		served_file TEXT,
		match_type TEXT,
		success INTEGER NOT NULL,
		failure_reason TEXT,
		avail_status TEXT,
		avail_reason TEXT,
		slot_path TEXT,
		preload INTEGER NOT NULL DEFAULT 0,
		ttff_ms INTEGER NOT NULL DEFAULT 0
	);`

	nzbAttemptsIndexTried    = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_tried_at ON nzb_attempts(tried_at DESC);`
	nzbAttemptsIndexContent  = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_content ON nzb_attempts(content_type, content_id);`
	nzbAttemptsIndexStream   = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_stream_name ON nzb_attempts(stream_name);`
	nzbAttemptsIndexProvider = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_provider_name ON nzb_attempts(provider_name);`
	nzbAttemptsIndexIndexer  = `CREATE INDEX IF NOT EXISTS idx_nzb_attempts_indexer_name ON nzb_attempts(indexer_name);`

	providerMetricsSchema = `CREATE TABLE IF NOT EXISTS provider_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		collected_at INTEGER NOT NULL,
		provider_name TEXT NOT NULL,
		host TEXT,
		active_conns INTEGER NOT NULL DEFAULT 0,
		idle_conns INTEGER NOT NULL DEFAULT 0,
		max_conns INTEGER NOT NULL DEFAULT 0,
		current_speed_mbps REAL NOT NULL DEFAULT 0,
		downloaded_mb REAL NOT NULL DEFAULT 0,
		usage_percent REAL NOT NULL DEFAULT 0,
		article_available_count INTEGER NOT NULL DEFAULT 0,
		article_missing_count INTEGER NOT NULL DEFAULT 0
	);`
	providerMetricsIndexTime = `CREATE INDEX IF NOT EXISTS idx_provider_metrics_collected_at ON provider_metrics(collected_at DESC);`
	providerMetricsIndexName = `CREATE INDEX IF NOT EXISTS idx_provider_metrics_name_time ON provider_metrics(provider_name, collected_at DESC);`

	indexerMetricsSchema = `CREATE TABLE IF NOT EXISTS indexer_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		collected_at INTEGER NOT NULL,
		indexer_name TEXT NOT NULL,
		api_hits_used INTEGER NOT NULL DEFAULT 0,
		api_hits_limit INTEGER NOT NULL DEFAULT 0,
		downloads_used INTEGER NOT NULL DEFAULT 0,
		downloads_limit INTEGER NOT NULL DEFAULT 0,
		searches_count INTEGER NOT NULL DEFAULT 0,
		unique_hits_count INTEGER NOT NULL DEFAULT 0,
		avg_response_ms REAL NOT NULL DEFAULT 0.0,
		avail_available_count INTEGER NOT NULL DEFAULT 0,
		avail_discarded_count INTEGER NOT NULL DEFAULT 0
	);`
	indexerMetricsIndexTime = `CREATE INDEX IF NOT EXISTS idx_indexer_metrics_collected_at ON indexer_metrics(collected_at DESC);`
	indexerMetricsIndexName = `CREATE INDEX IF NOT EXISTS idx_indexer_metrics_name_time ON indexer_metrics(indexer_name, collected_at DESC);`

	performanceMetricsSchema = `CREATE TABLE IF NOT EXISTS performance_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		collected_at INTEGER NOT NULL,
		metric_type TEXT NOT NULL,
		sample_count INTEGER NOT NULL DEFAULT 0,
		min_ms REAL NOT NULL DEFAULT 0.0,
		max_ms REAL NOT NULL DEFAULT 0.0,
		avg_ms REAL NOT NULL DEFAULT 0.0,
		p50_ms REAL NOT NULL DEFAULT 0.0,
		p95_ms REAL NOT NULL DEFAULT 0.0,
		p99_ms REAL NOT NULL DEFAULT 0.0
	);`
	performanceMetricsIndexTime = `CREATE INDEX IF NOT EXISTS idx_performance_metrics_collected_at ON performance_metrics(collected_at DESC);`
	performanceMetricsIndexType = `CREATE INDEX IF NOT EXISTS idx_performance_metrics_type_time ON performance_metrics(metric_type, collected_at DESC);`

	streamApiSamplesSchema = `CREATE TABLE IF NOT EXISTS stream_api_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp INTEGER NOT NULL,
		content_type TEXT,
		content_id TEXT,
		total_duration_ms INTEGER NOT NULL DEFAULT 0,
		metadata_duration_ms INTEGER NOT NULL DEFAULT 0,
		search_duration_ms INTEGER NOT NULL DEFAULT 0,
		ranking_duration_ms INTEGER NOT NULL DEFAULT 0,
		avail_nzb_duration_ms INTEGER NOT NULL DEFAULT 0,
		candidate_count INTEGER NOT NULL DEFAULT 0,
		result_count INTEGER NOT NULL DEFAULT 0
	);`
	streamApiSamplesIndexTime = `CREATE INDEX IF NOT EXISTS idx_stream_api_samples_timestamp ON stream_api_samples(timestamp DESC);`

	playbackTtffSamplesSchema = `CREATE TABLE IF NOT EXISTS playback_ttff_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp INTEGER NOT NULL,
		session_id TEXT,
		provider_name TEXT,
		ttff_ms INTEGER NOT NULL DEFAULT 0,
		session_resolution_ms INTEGER NOT NULL DEFAULT 0,
		nzb_fetch_duration_ms INTEGER NOT NULL DEFAULT 0,
		nntp_connect_duration_ms INTEGER NOT NULL DEFAULT 0,
		probe_duration_ms INTEGER NOT NULL DEFAULT 0,
		first_byte_duration_ms INTEGER NOT NULL DEFAULT 0,
		is_cache_hit INTEGER NOT NULL DEFAULT 0
	);`
	playbackTtffSamplesIndexTime = `CREATE INDEX IF NOT EXISTS idx_playback_ttff_samples_timestamp ON playback_ttff_samples(timestamp DESC);`
)

func openDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, dbFilename)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	// Enable WAL for better concurrent read/write.
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	return db, nil
}

const (
	libraryNZBsSchema = `CREATE TABLE IF NOT EXISTS library_nzbs (
		id TEXT PRIMARY KEY,
		content_type TEXT NOT NULL,
		content_id TEXT NOT NULL,
		season INTEGER NOT NULL DEFAULT 0,
		episode INTEGER NOT NULL DEFAULT 0,
		release_title TEXT NOT NULL,
		details_url TEXT,
		indexer_name TEXT,
		size_bytes INTEGER NOT NULL DEFAULT 0,
		nzb_data BLOB NOT NULL,
		created_at INTEGER NOT NULL,
		last_accessed_at INTEGER NOT NULL,
		pinned INTEGER NOT NULL DEFAULT 0
	);`
	libraryNZBsIndexContent  = `CREATE INDEX IF NOT EXISTS idx_library_nzbs_content ON library_nzbs(content_type, content_id, season, episode);`
	libraryNZBsIndexAccessed = `CREATE INDEX IF NOT EXISTS idx_library_nzbs_accessed ON library_nzbs(last_accessed_at DESC);`

	libraryBlueprintsSchema = `CREATE TABLE IF NOT EXISTS library_blueprints (
		nzb_id TEXT PRIMARY KEY,
		blueprint_json TEXT NOT NULL,
		media_file_name TEXT NOT NULL,
		media_file_size INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);`

	badReleasesSchema = `CREATE TABLE IF NOT EXISTS bad_releases (
		details_url TEXT PRIMARY KEY,
		release_title TEXT,
		reason TEXT,
		reported_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);`
	badReleasesIndexExpires = `CREATE INDEX IF NOT EXISTS idx_bad_releases_expires ON bad_releases(expires_at);`
)

// migrateLibraryBlueprintsMediaCaps adds the media capabilities column for existing DBs (no-op if already present).
func migrateLibraryBlueprintsMediaCaps(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE library_blueprints ADD COLUMN media_caps_json TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate library_blueprints.media_caps_json: %w", err)
	}
	return nil
}

// migrateLibraryMultiID adds per-scheme content-id columns so a cached release
// can be found regardless of which id scheme (imdb/tmdb/tvdb/kitsu) a later
// request carries. Rows written before this migration keep only content_id.
func migrateLibraryMultiID(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE library_nzbs ADD COLUMN imdb_id TEXT`,
		`ALTER TABLE library_nzbs ADD COLUMN tmdb_id TEXT`,
		`ALTER TABLE library_nzbs ADD COLUMN tvdb_id TEXT`,
		`ALTER TABLE library_nzbs ADD COLUMN kitsu_id TEXT`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate library_nzbs multi-id: %w", err)
		}
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_library_nzbs_imdb ON library_nzbs(imdb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_library_nzbs_tmdb ON library_nzbs(tmdb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_library_nzbs_tvdb ON library_nzbs(tvdb_id)`,
		`CREATE INDEX IF NOT EXISTS idx_library_nzbs_kitsu ON library_nzbs(kitsu_id)`,
	} {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("index library_nzbs multi-id: %w", err)
		}
	}
	return nil
}

// migrateLibraryStatus adds the release status lifecycle columns. Rows written
// before this migration only ever existed after successful validation, so they
// default to 'good'; new rows are written 'pending' as soon as the NZB and
// blueprint are known, then marked 'good'/'bad' once a verdict exists.
func migrateLibraryStatus(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE library_nzbs ADD COLUMN status TEXT NOT NULL DEFAULT 'good'`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate library_nzbs.status: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE library_nzbs ADD COLUMN status_reason TEXT`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate library_nzbs.status_reason: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_library_nzbs_status ON library_nzbs(status)`); err != nil {
		return fmt.Errorf("index library_nzbs.status: %w", err)
	}
	return nil
}

// migrateLibraryLastVerified adds the freshness timestamp used by the background
// re-verification sweep. Legacy rows get 0 (treated as never verified → stale).
func migrateLibraryLastVerified(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE library_nzbs ADD COLUMN last_verified_at INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate library_nzbs.last_verified_at: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_library_nzbs_verified ON library_nzbs(last_verified_at)`); err != nil {
		return fmt.Errorf("index library_nzbs.last_verified_at: %w", err)
	}
	return nil
}

// migrateLibraryCapColumns promotes key ffprobe capability fields to indexed
// columns so the library can be filtered/sorted by codec/resolution/HDR in the
// Library UI and ranking, without parsing media_caps_json. NOT used to gate or
// reorder by client — codec compatibility is the client decoder's job.
func migrateLibraryCapColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE library_blueprints ADD COLUMN video_codec TEXT`,
		`ALTER TABLE library_blueprints ADD COLUMN height INTEGER`,
		`ALTER TABLE library_blueprints ADD COLUMN bit_depth INTEGER`,
		`ALTER TABLE library_blueprints ADD COLUMN hdr TEXT`,
		`ALTER TABLE library_blueprints ADD COLUMN dolby_vision INTEGER`,
		`ALTER TABLE library_blueprints ADD COLUMN audio_codec TEXT`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate library_blueprints cap columns: %w", err)
		}
	}
	return nil
}

func initSchema(db *sql.DB) error {
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
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	if err := migrateProviderMetricsArticleAvailableCount(db); err != nil {
		return err
	}
	_, _ = db.Exec("ALTER TABLE nzb_attempts ADD COLUMN ttff_ms INTEGER NOT NULL DEFAULT 0;")
	if err := migrateProviderMetricsArticleMissingCount(db); err != nil {
		return err
	}
	if err := migrateIndexerMetricsUniqueHitsCount(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsPreload(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsServedFile(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsMatchType(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsIndexerName(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsStreamName(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsProviderName(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsAvailStatus(db); err != nil {
		return err
	}
	if err := migrateNzbAttemptsAvailReason(db); err != nil {
		return err
	}
	if err := migrateLibraryBlueprintsMediaCaps(db); err != nil {
		return err
	}
	if err := migrateLibraryMultiID(db); err != nil {
		return err
	}
	if err := migrateLibraryCapColumns(db); err != nil {
		return err
	}
	if err := migrateLibraryLastVerified(db); err != nil {
		return err
	}
	if err := migrateLibraryStatus(db); err != nil {
		return err
	}
	for _, stmt := range []string{nzbAttemptsIndexStream, nzbAttemptsIndexProvider, nzbAttemptsIndexIndexer} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	return nil
}

// migrateNzbAttemptsPreload adds preload column for existing DBs (no-op if already present).
func migrateNzbAttemptsPreload(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN preload INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.preload: %w", err)
	}
	return nil
}

func migrateNzbAttemptsServedFile(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN served_file TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.served_file: %w", err)
	}
	return nil
}

func migrateNzbAttemptsMatchType(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN match_type TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.match_type: %w", err)
	}
	return nil
}

func migrateNzbAttemptsIndexerName(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN indexer_name TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.indexer_name: %w", err)
	}
	return nil
}

func migrateNzbAttemptsStreamName(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN stream_name TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.stream_name: %w", err)
	}
	return nil
}

func migrateNzbAttemptsProviderName(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN provider_name TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.provider_name: %w", err)
	}
	return nil
}

func migrateNzbAttemptsAvailStatus(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN avail_status TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.avail_status: %w", err)
	}
	return nil
}

func migrateNzbAttemptsAvailReason(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE nzb_attempts ADD COLUMN avail_reason TEXT`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate nzb_attempts.avail_reason: %w", err)
	}
	return nil
}

func migrateProviderMetricsArticleAvailableCount(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE provider_metrics ADD COLUMN article_available_count INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate provider_metrics.article_available_count: %w", err)
	}
	return nil
}

func migrateProviderMetricsArticleMissingCount(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE provider_metrics ADD COLUMN article_missing_count INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate provider_metrics.article_missing_count: %w", err)
	}
	return nil
}

func migrateIndexerMetricsUniqueHitsCount(db *sql.DB) error {
	_, err := db.Exec(`ALTER TABLE indexer_metrics ADD COLUMN unique_hits_count INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate indexer_metrics.unique_hits_count: %w", err)
	}
	return nil
}

// migrateFromStateJSON reads state.json (and optionally usage.json) into the kv table, then removes the file(s).
func migrateFromStateJSON(db *sql.DB, dataDir string) error {
	statePath := filepath.Join(dataDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Legacy: migrate usage.json into kv as indexer_usage
			usagePath := filepath.Join(dataDir, "usage.json")
			if u, uErr := os.ReadFile(usagePath); uErr == nil {
				logger.Info("Migrating usage.json to database")
				if err := setKV(db, "indexer_usage", u); err != nil {
					return err
				}
				os.Remove(usagePath)
			}
			return nil
		}
		return err
	}

	var kv map[string]json.RawMessage
	if err := json.Unmarshal(data, &kv); err != nil {
		return fmt.Errorf("parse state.json: %w", err)
	}
	logger.Info("Migrating state.json to database", "keys", len(kv))
	for k, v := range kv {
		if err := setKV(db, k, v); err != nil {
			return fmt.Errorf("migrate key %s: %w", k, err)
		}
	}
	if err := os.Remove(statePath); err != nil {
		logger.Warn("Could not remove state.json after migration", "err", err)
	}
	return nil
}

func setKV(db *sql.DB, key string, value []byte) error {
	_, err := db.Exec("INSERT OR REPLACE INTO kv (key, value, updated_at) VALUES (?, ?, ?)",
		key, value, time.Now().UnixMilli())
	return err
}

func getKV(db *sql.DB, key string) ([]byte, bool, error) {
	var value []byte
	err := db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func deleteKV(db *sql.DB, key string) error {
	_, err := db.Exec("DELETE FROM kv WHERE key = ?", key)
	return err
}
