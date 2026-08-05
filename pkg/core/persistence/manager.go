package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

const saveDebounceInterval = 2 * time.Second

type StateManager struct {
	db              *sql.DB // shared read pool
	wdb             *sql.DB // single-connection write handle; all writes go here
	libraryStore    *LibraryStore
	badReleaseStore *BadReleaseStore
	mu              sync.RWMutex
	saveTimer       *time.Timer
	saveMu          sync.Mutex
}

var globalManager *StateManager
var managerMu sync.Mutex

func GetManager(dataDir string) (*StateManager, error) {
	managerMu.Lock()
	defer managerMu.Unlock()

	if globalManager != nil {
		return globalManager, nil
	}

	db, wdb, err := openDB(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	closeBoth := func() {
		db.Close()
		wdb.Close()
	}
	if err := initSchema(wdb); err != nil {
		closeBoth()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := migrateFromStateJSON(wdb, dataDir); err != nil {
		closeBoth()
		return nil, fmt.Errorf("migrate state: %w", err)
	}
	mergeMisplacedDatabases(wdb, dataDir)

	m := &StateManager{
		db:              db,
		wdb:             wdb,
		libraryStore:    NewLibraryStore(db, wdb),
		badReleaseStore: NewBadReleaseStore(db, wdb),
	}
	globalManager = m
	return m, nil
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

func mergeMisplacedDatabases(target *sql.DB, dataDir string) {
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

func mergeDatabaseFile(target *sql.DB, sourcePath string) (int64, int64, error) {
	// The stray database may belong to a still-running instance; busy_timeout
	// keeps the merge from failing on a transient lock.
	source, err := sql.Open("sqlite", sqliteDSN(sourcePath))
	if err != nil {
		return 0, 0, err
	}
	defer source.Close()
	if err := source.Ping(); err != nil {
		return 0, 0, err
	}

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

func tableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func tableColumns(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]struct{})
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			defaultV  sql.NullString
			primaryKV int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultV, &primaryKV); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, rows.Err()
}

func mergeKVRows(target, source *sql.DB) (int64, error) {
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
		res, err := target.Exec(`INSERT OR IGNORE INTO kv (key, value, updated_at) VALUES (?, ?, ?)`, key, value, updatedAt)
		if err != nil {
			return merged, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			merged += affected
		}
	}
	return merged, rows.Err()
}

func mergeAttemptRows(target, source *sql.DB) (int64, error) {
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

		res, err := target.Exec(`INSERT INTO nzb_attempts (
			tried_at, stream_name, provider_name, content_type, content_id, content_title,
			indexer_name, release_title, release_url, release_size, served_file, match_type, success,
			failure_reason, slot_path, preload
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
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
			a.TriedAt.UnixMilli(), a.ContentType, a.ContentID, a.ReleaseTitle, a.ReleaseURL, a.SlotPath, boolToInt(a.Preload), boolToInt(a.Success), a.MatchType, a.FailureReason,
		)
		if err != nil {
			return merged, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			merged += affected
		}
	}
	return merged, rows.Err()
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

func (m *StateManager) withWriteLock(fn func(*sql.DB) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m.wdb)
}

func (m *StateManager) Set(key string, value interface{}) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	err = m.withWriteLock(func(db *sql.DB) error {
		return setKV(db, key, raw)
	})
	if err != nil {
		return err
	}
	m.scheduleSave()
	return nil
}

func (m *StateManager) Delete(key string) error {
	err := m.withWriteLock(func(db *sql.DB) error {
		return deleteKV(db, key)
	})
	if err != nil {
		return err
	}
	m.scheduleSave()
	return nil
}

func (m *StateManager) Save() error {
	// KV writes are immediate (no dirty buffer); Save is a no-op except for debounce.
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
		// No-op for SQLite; kept for API compatibility.
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
	if m.db != nil {
		err = m.db.Close()
		m.db = nil
	}
	if m.wdb != nil {
		if werr := m.wdb.Close(); err == nil {
			err = werr
		}
		m.wdb = nil
	}

	if globalManager == m {
		globalManager = nil
	}
	return err
}
