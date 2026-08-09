package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

const saveDebounceInterval = 2 * time.Second

// Settings selects which database the manager opens.
type Settings struct {
	// Backend is "sqlite" (the default) or "postgres".
	Backend string
	// DSN is the Postgres connection string; unused by the sqlite backend,
	// which always lives at <dataDir>/streamnzb.db.
	DSN string
	// MigrateData carries data into the database being opened: from the
	// streamnzb.db file at startup, and from whichever database is currently
	// live on a Reopen — so a backend switch takes the data with it either way.
	MigrateData bool
}

// ValidateSettings reports whether these settings are internally coherent,
// without touching the network.
func ValidateSettings(s Settings) error {
	backend := normalizeBackend(s.Backend)
	if _, err := dialectFor(backend); err != nil {
		return err
	}
	if backend == BackendPostgres && strings.TrimSpace(s.DSN) == "" {
		return fmt.Errorf("a connection string is required when the database driver is postgres")
	}
	return nil
}

// VerifySettings dials the configured database. Callers use it at save time so
// a bad connection string surfaces immediately, rather than at the next start
// where it would stop the app from booting.
func VerifySettings(ctx context.Context, s Settings) error {
	if err := ValidateSettings(s); err != nil {
		return err
	}
	if normalizeBackend(s.Backend) != BackendPostgres {
		return nil
	}
	db, err := sql.Open("pgx", s.DSN)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	return nil
}

type StateManager struct {
	db              *connRef // shared read handle
	wdb             *connRef // write handle; all writes go here
	backend         string
	libraryStore    *LibraryStore
	badReleaseStore *BadReleaseStore
	animeMappings   *AnimeMappingStore
	mu              sync.RWMutex
	saveTimer       *time.Timer
	saveMu          sync.Mutex
}

var globalManager *StateManager
var managerMu sync.Mutex
var managerDataDir string
var configuredSettings Settings

// Configure selects the backend the next GetManager call opens. It must run
// before the first GetManager — once the manager exists it is bound to the
// settings it was opened with, and a later call here is ignored.
func Configure(s Settings) {
	managerMu.Lock()
	defer managerMu.Unlock()
	if globalManager != nil {
		logger.Warn("persistence.Configure called after the database was already opened; ignoring",
			"requested_backend", s.Backend, "active_backend", globalManager.backend)
		return
	}
	configuredSettings = s
}

func GetManager(dataDir string) (*StateManager, error) {
	managerMu.Lock()
	defer managerMu.Unlock()

	if globalManager != nil {
		// The manager is a process-wide singleton bound to the first dataDir it
		// was opened with; a different argument here means two call sites
		// resolved the data directory differently — surface it instead of
		// silently using the wrong database.
		if dataDir != "" && managerDataDir != "" && dataDir != managerDataDir {
			logger.Warn("persistence.GetManager called with a different data dir than the one in use",
				"requested", dataDir, "active", managerDataDir)
		}
		return globalManager, nil
	}

	m, err := newManager(configuredSettings, dataDir)
	if err != nil {
		return nil, err
	}
	globalManager = m
	managerDataDir = dataDir
	return m, nil
}

// newManager opens a database and builds a manager over it, without touching
// the process-wide singleton. Tests use it directly to get isolated databases.
func newManager(s Settings, dataDir string) (*StateManager, error) {
	backend := normalizeBackend(s.Backend)
	db, wdb, err := openBackend(s, dataDir)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	closeBoth := func() {
		db.Close()
		if wdb != db {
			wdb.Close()
		}
	}
	if err := initSchema(wdb); err != nil {
		closeBoth()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrateFromStateJSON(wdb, dataDir); err != nil {
		closeBoth()
		return nil, fmt.Errorf("migrate state: %w", err)
	}
	if backend == BackendSQLite {
		// Merging stray database files is inherently about SQLite's one-file-per
		// -database model; there is no equivalent on a shared server.
		mergeMisplacedDatabases(wdb, dataDir)
	} else if s.MigrateData {
		if err := importFromSQLite(wdb, dataDir); err != nil {
			logger.Warn("SQLite import failed; continuing with the configured database", "err", err)
		}
	}

	mgr := &StateManager{
		db:              db,
		wdb:             wdb,
		backend:         backend,
		libraryStore:    NewLibraryStore(db, wdb),
		badReleaseStore: NewBadReleaseStore(db, wdb),
	}
	mgr.animeMappings = NewAnimeMappingStore(mgr, db, wdb)
	return mgr, nil
}

// openPools opens the configured database and returns the read and write
// pools. They are the same pool on Postgres, which has no single-writer
// constraint to work around.
func openPools(s Settings, dataDir string) (readPool, writePool *sql.DB, d Dialect, err error) {
	backend := normalizeBackend(s.Backend)
	d, err = dialectFor(backend)
	if err != nil {
		return nil, nil, nil, err
	}
	if backend == BackendPostgres {
		pool, err := openPostgres(s.DSN)
		if err != nil {
			return nil, nil, nil, err
		}
		return pool, pool, d, nil
	}
	readPool, writePool, err = openSQLite(dataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	return readPool, writePool, d, nil
}

// openBackend wraps openPools in the connRefs the manager and stores hold.
func openBackend(s Settings, dataDir string) (db, wdb *connRef, err error) {
	readPool, writePool, d, err := openPools(s, dataDir)
	if err != nil {
		return nil, nil, err
	}
	if readPool == writePool {
		ref := newConnRef(readPool, d)
		return ref, ref, nil
	}
	return newConnRef(readPool, d), newConnRef(writePool, d), nil
}

// Reopen points the manager at a different database without replacing the
// *StateManager itself. Every holder keeps its pointer and the stores keep
// their connRefs, so nothing has to be rewired — the swap is what the caller
// sees, not a new object graph.
//
// It returns a function that closes the pools that were in use. Callers must
// run it only once no in-flight query can still hold them; closing inline
// would block, because sql.DB.Close waits on in-use connections.
func (m *StateManager) Reopen(s Settings, dataDir string) (closeOld func(), err error) {
	if m == nil {
		return func() {}, nil
	}
	backend := normalizeBackend(s.Backend)
	m.mu.RLock()
	source := m.db
	m.mu.RUnlock()

	readPool, writePool, d, err := openPools(s, dataDir)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	discard := func() {
		readPool.Close()
		if writePool != readPool {
			writePool.Close()
		}
	}

	writeRef := newConnRef(writePool, d)
	if err := initSchema(writeRef); err != nil {
		discard()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	// The database being left is the live one, so it is the migration source —
	// which makes this work in either direction, not just off the SQLite file.
	if s.MigrateData && source != nil {
		if err := migrateInto(writeRef, source); err != nil {
			logger.Warn("Data migration failed; continuing with the new database", "err", err)
		}
	}

	m.mu.Lock()
	oldRead := m.db.swap(readPool, d)
	oldWrite := m.wdb.swap(writePool, d)
	m.backend = backend
	m.mu.Unlock()

	return func() {
		if oldRead != nil {
			_ = oldRead.Close()
		}
		if oldWrite != nil && oldWrite != oldRead {
			_ = oldWrite.Close()
		}
	}, nil
}

// Backend reports which database backend is in use ("sqlite" or "postgres").
func (m *StateManager) Backend() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.backend
}

func (m *StateManager) LibraryStore() *LibraryStore {
	if m == nil {
		return nil
	}
	return m.libraryStore
}

func (m *StateManager) BadReleaseStore() *BadReleaseStore {
	if m == nil {
		return nil
	}
	return m.badReleaseStore
}

func (m *StateManager) AnimeMappingStore() *AnimeMappingStore {
	if m == nil {
		return nil
	}
	return m.animeMappings
}

func mergeMisplacedDatabases(target *connRef, dataDir string) {
	for _, sourcePath := range misplacedDatabasePaths(dataDir) {
		mergedAttempts, mergedKV, err := mergeDatabaseFile(target, sourcePath)
		if err != nil {
			logger.Warn("Failed to merge misplaced sqlite database", "path", sourcePath, "err", err)
			continue
		}
		if mergedAttempts > 0 || mergedKV > 0 {
			logger.Info("Merged misplaced sqlite database", "path", sourcePath, "attempts", mergedAttempts, "kv", mergedKV)
			if err := archiveMergedDatabase(sourcePath); err != nil {
				logger.Warn("Failed to archive merged sqlite database", "path", sourcePath, "err", err)
			}
		}
	}
}

func archiveMergedDatabase(sourcePath string) error {
	archivedPath := sourcePath + ".merged"
	_ = os.Remove(archivedPath)
	return os.Rename(sourcePath, archivedPath)
}

func misplacedDatabasePaths(dataDir string) []string {
	targetPath, _ := filepath.Abs(filepath.Join(dataDir, dbFilename))
	seen := map[string]struct{}{}
	var candidates []string

	add := func(path string) {
		if path == "" {
			return
		}
		absPath, err := filepath.Abs(path)
		if err != nil || absPath == targetPath {
			return
		}
		if _, ok := seen[absPath]; ok {
			return
		}
		if _, err := os.Stat(absPath); err != nil {
			return
		}
		seen[absPath] = struct{}{}
		candidates = append(candidates, absPath)
	}

	if filepath.Base(dataDir) == "data" {
		add(filepath.Join(filepath.Dir(dataDir), dbFilename))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, dbFilename))
	}
	return candidates
}

// openSQLiteFile opens one existing SQLite file as a read source. The
// busy_timeout in the DSN keeps a still-running instance's lock from failing
// the read outright.
func openSQLiteFile(path string) (*connRef, func(), error) {
	source, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, nil, err
	}
	if err := source.Ping(); err != nil {
		source.Close()
		return nil, nil, err
	}
	return newConnRef(source, sqliteDialect{}), func() { source.Close() }, nil
}

func mergeDatabaseFile(target *connRef, sourcePath string) (int64, int64, error) {
	source, closeSource, err := openSQLiteFile(sourcePath)
	if err != nil {
		return 0, 0, err
	}
	defer closeSource()

	mergedKV, err := mergeKVRows(target, source)
	if err != nil {
		return 0, 0, err
	}
	mergedAttempts, err := mergeAttemptRows(target, source)
	if err != nil {
		return 0, mergedKV, err
	}
	return mergedAttempts, mergedKV, nil
}

func mergeKVRows(target, source *connRef) (int64, error) {
	ok, err := tableExists(source, "kv")
	if err != nil || !ok {
		return 0, err
	}
	rows, err := source.Query(`SELECT key, value, updated_at FROM kv`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var merged int64
	for rows.Next() {
		var (
			key       string
			value     []byte
			updatedAt sql.NullInt64
		)
		if err := rows.Scan(&key, &value, &updatedAt); err != nil {
			return merged, err
		}
		res, err := target.Exec(
			`INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO NOTHING`,
			key, value, updatedAt)
		if err != nil {
			return merged, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			merged += affected
		}
	}
	return merged, rows.Err()
}

func mergeAttemptRows(target, source *connRef) (int64, error) {
	ok, err := tableExists(source, "nzb_attempts")
	if err != nil || !ok {
		return 0, err
	}
	cols, err := tableColumns(source, "nzb_attempts")
	if err != nil {
		return 0, err
	}

	colOr := func(name, fallback string) string {
		if _, ok := cols[name]; ok {
			return name
		}
		return fallback + " AS " + name
	}

	query := fmt.Sprintf(`SELECT
		tried_at,
		%s,
		%s,
		content_type,
		content_id,
		%s,
		%s,
		release_title,
		%s,
		%s,
		%s,
		%s,
		success,
		%s,
		%s,
		%s
		FROM nzb_attempts`,
		colOr("stream_name", "''"),
		colOr("provider_name", "''"),
		colOr("content_title", "''"),
		colOr("indexer_name", "''"),
		colOr("release_url", "''"),
		colOr("release_size", "0"),
		colOr("served_file", "''"),
		colOr("match_type", "''"),
		colOr("failure_reason", "''"),
		colOr("slot_path", "''"),
		colOr("preload", "0"),
	)

	rows, err := source.Query(query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var merged int64
	for rows.Next() {
		var a NZBAttempt
		var triedAtMs int64
		var success int
		var preload int
		if err := rows.Scan(
			&triedAtMs,
			&a.StreamName,
			&a.ProviderName,
			&a.ContentType,
			&a.ContentID,
			&a.ContentTitle,
			&a.IndexerName,
			&a.ReleaseTitle,
			&a.ReleaseURL,
			&a.ReleaseSize,
			&a.ServedFile,
			&a.MatchType,
			&success,
			&a.FailureReason,
			&a.SlotPath,
			&preload,
		); err != nil {
			return merged, err
		}
		a.TriedAt = time.UnixMilli(triedAtMs)
		a.Success = success != 0
		a.Preload = preload != 0

		inserted, err := insertAttemptIfAbsent(target, a)
		if err != nil {
			return merged, err
		}
		merged += inserted
	}
	return merged, rows.Err()
}

// insertAttemptIfAbsent writes one attempt row unless an identical one is
// already present. The CASTs make the parameter types explicit: a bare
// SELECT ?, ?, ... has no column context for Postgres to infer them from.
func insertAttemptIfAbsent(target *connRef, a NZBAttempt) (int64, error) {
	res, err := target.Exec(`INSERT INTO nzb_attempts (
		tried_at, stream_name, provider_name, content_type, content_id, content_title,
		indexer_name, release_title, release_url, release_size, served_file, match_type, success,
		failure_reason, slot_path, preload
	)
	SELECT CAST(? AS BIGINT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT),
	       CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT), CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT),
	       CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT)
	WHERE NOT EXISTS (
		SELECT 1 FROM nzb_attempts
		WHERE tried_at = ?
		  AND content_type = ?
		  AND content_id = ?
		  AND release_title = ?
		  AND COALESCE(release_url, '') = ?
		  AND COALESCE(slot_path, '') = ?
		  AND COALESCE(preload, 0) = ?
		  AND success = ?
		  AND COALESCE(match_type, '') = ?
		  AND COALESCE(failure_reason, '') = ?
	)`,
		a.TriedAt.UnixMilli(), a.StreamName, a.ProviderName, a.ContentType, a.ContentID, a.ContentTitle,
		a.IndexerName, a.ReleaseTitle, a.ReleaseURL, a.ReleaseSize, a.ServedFile, a.MatchType, boolToInt(a.Success),
		a.FailureReason, a.SlotPath, boolToInt(a.Preload),
		a.TriedAt.UnixMilli(), a.ContentType, a.ContentID, a.ReleaseTitle, a.ReleaseURL, a.SlotPath,
		boolToInt(a.Preload), boolToInt(a.Success), a.MatchType, a.FailureReason,
	)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (m *StateManager) Get(key string, target interface{}) (bool, error) {
	m.mu.RLock()
	value, ok, err := getKV(m.db, key)
	m.mu.RUnlock()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(value, target); err != nil {
		return true, err
	}
	return true, nil
}

func (m *StateManager) withWriteLock(fn func(*connRef) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m.wdb)
}

func (m *StateManager) Set(key string, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	err = m.withWriteLock(func(c *connRef) error {
		return setKV(c, key, raw)
	})
	if err != nil {
		return err
	}
	m.scheduleSave()
	return nil
}

func (m *StateManager) Delete(key string) error {
	err := m.withWriteLock(func(c *connRef) error {
		return deleteKV(c, key)
	})
	if err != nil {
		return err
	}
	m.scheduleSave()
	return nil
}

func (m *StateManager) scheduleSave() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if m.saveTimer != nil {
		m.saveTimer.Stop()
	}
	m.saveTimer = time.AfterFunc(saveDebounceInterval, func() {
		m.saveMu.Lock()
		m.saveTimer = nil
		m.saveMu.Unlock()
		// No-op; writes already landed. Kept for API compatibility.
	})
}

func (m *StateManager) Flush() error {
	m.saveMu.Lock()
	if m.saveTimer != nil {
		m.saveTimer.Stop()
		m.saveTimer = nil
	}
	m.saveMu.Unlock()
	return nil
}

func (m *StateManager) Close() error {
	managerMu.Lock()
	defer managerMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.saveMu.Lock()
	if m.saveTimer != nil {
		m.saveTimer.Stop()
		m.saveTimer = nil
	}
	m.saveMu.Unlock()

	var err error
	// On Postgres both roles share one pool, so close it exactly once. Compare
	// the pools rather than the refs: a Reopen can leave two distinct refs
	// pointing at the same pool.
	sharedPool := m.db != nil && m.wdb != nil && m.db.pool() == m.wdb.pool()
	if m.db != nil {
		err = m.db.Close()
		m.db = nil
	}
	if m.wdb != nil {
		if !sharedPool {
			if werr := m.wdb.Close(); err == nil {
				err = werr
			}
		}
		m.wdb = nil
	}

	if globalManager == m {
		globalManager = nil
		managerDataDir = ""
	}
	return err
}
