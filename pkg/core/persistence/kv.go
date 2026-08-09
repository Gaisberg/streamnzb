package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"streamnzb/pkg/core/logger"
)

func setKV(c *connRef, key string, value []byte) error {
	_, err := c.Exec(`
		INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`,
		key, value, time.Now().UnixMilli())
	return err
}

func getKV(c *connRef, key string) ([]byte, bool, error) {
	var value []byte
	err := c.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func deleteKV(c *connRef, key string) error {
	_, err := c.Exec("DELETE FROM kv WHERE key = ?", key)
	return err
}

// migrateFromStateJSON reads state.json (and optionally usage.json) into the kv
// table, then removes the file(s). It is backend-independent: the files are a
// legacy on-disk format, not a SQLite artifact, so the import runs whichever
// database is configured.
func migrateFromStateJSON(c *connRef, dataDir string) error {
	statePath := filepath.Join(dataDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Legacy: migrate usage.json into kv as indexer_usage
			usagePath := filepath.Join(dataDir, "usage.json")
			if u, uErr := os.ReadFile(usagePath); uErr == nil {
				logger.Info("Migrating usage.json to database")
				if err := setKV(c, "indexer_usage", u); err != nil {
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
		if err := setKV(c, k, v); err != nil {
			return fmt.Errorf("migrate key %s: %w", k, err)
		}
	}
	if err := os.Remove(statePath); err != nil {
		logger.Warn("Could not remove state.json after migration", "err", err)
	}
	return nil
}
