package persistence

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"streamnzb/pkg/core/logger"
	"testing"
)

func TestStateManager(t *testing.T) {
	logger.Init("DEBUG")
	tempDir, err := os.MkdirTemp("", "state_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	defer mgr.Close()

	key := "test_key"
	value := map[string]string{"foo": "bar"}
	if err := mgr.Set(key, value); err != nil {
		t.Fatalf("failed to set value: %v", err)
	}

	var retrieved map[string]string
	found, err := mgr.Get(key, &retrieved)
	if err != nil {
		t.Fatalf("failed to get value: %v", err)
	}
	if !found {
		t.Fatal("value not found")
	}
	if retrieved["foo"] != "bar" {
		t.Errorf("expected bar, got %s", retrieved["foo"])
	}

	if err := mgr.Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	mgr.Close()
	globalManager = nil
	mgr2, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to reload manager: %v", err)
	}
	defer mgr2.Close()

	var retrieved2 map[string]string
	found2, err := mgr2.Get(key, &retrieved2)
	if err != nil {
		t.Fatalf("failed to get value after reload: %v", err)
	}
	if !found2 {
		t.Fatal("value not found after reload")
	}
	if retrieved2["foo"] != "bar" {
		t.Errorf("expected bar after reload, got %s", retrieved2["foo"])
	}
}

func TestMigration(t *testing.T) {
	logger.Init("DEBUG")
	tempDir, err := os.MkdirTemp("", "migration_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	usageData := map[string]interface{}{
		"indexer1": map[string]interface{}{
			"api_hits_used":  10,
			"last_reset_day": "2024-01-01",
		},
	}
	usagePath := filepath.Join(tempDir, "usage.json")
	data, _ := json.Marshal(usageData)
	os.WriteFile(usagePath, data, 0644)

	globalManager = nil
	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	defer mgr.Close()

	var migratedUsage map[string]interface{}
	found, err := mgr.Get("indexer_usage", &migratedUsage)
	if err != nil || !found {
		t.Fatalf("migration failed: %v", err)
	}

	indexer1, ok := migratedUsage["indexer1"].(map[string]interface{})
	if !ok {
		t.Fatal("indexer1 not found in migrated data")
	}
	if indexer1["api_hits_used"].(float64) != 10 {
		t.Errorf("expected 10, got %v", indexer1["api_hits_used"])
	}

	if _, err := os.Stat(usagePath); !os.IsNotExist(err) {
		t.Error("usage.json should have been deleted after migration")
	}
}

func TestGetManagerMergesMisplacedSiblingDatabase(t *testing.T) {
	logger.Init("DEBUG")
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	sourceDB, err := sql.Open("sqlite", filepath.Join(rootDir, dbFilename))
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	if _, err := sourceDB.Exec(`CREATE TABLE kv (
		key TEXT PRIMARY KEY,
		value BLOB NOT NULL,
		updated_at INTEGER
	);`); err != nil {
		t.Fatalf("create kv: %v", err)
	}
	if _, err := sourceDB.Exec(`CREATE TABLE nzb_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		tried_at INTEGER NOT NULL,
		content_type TEXT NOT NULL,
		content_id TEXT NOT NULL,
		content_title TEXT,
		release_title TEXT NOT NULL,
		release_url TEXT,
		release_size INTEGER,
		served_file TEXT,
		success INTEGER NOT NULL,
		failure_reason TEXT,
		slot_path TEXT,
		preload INTEGER NOT NULL DEFAULT 0,
		indexer_name TEXT
	);`); err != nil {
		t.Fatalf("create attempts: %v", err)
	}
	if _, err := sourceDB.Exec(`INSERT INTO nzb_attempts
		(tried_at, content_type, content_id, content_title, release_title, release_url, release_size, served_file, success, failure_reason, slot_path, preload, indexer_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1740000000000, "movie", "tt123", "Example", "Example.Release", "https://example.invalid", 1234, "movie.mkv", 1, "", "slot-1", 0, "Indexer"); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	if err := setKV(sourceDB, "provider_usage", []byte(`{"example":1}`)); err != nil {
		t.Fatalf("insert kv: %v", err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatalf("close source db: %v", err)
	}

	globalManager = nil
	mgr, err := GetManager(dataDir)
	if err != nil {
		t.Fatalf("GetManager: %v", err)
	}
	defer mgr.Close()

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 merged attempt, got %d", len(list))
	}
	if got := list[0].ReleaseTitle; got != "Example.Release" {
		t.Fatalf("ReleaseTitle = %q, want %q", got, "Example.Release")
	}

	var providerUsage map[string]int
	found, err := mgr.Get("provider_usage", &providerUsage)
	if err != nil {
		t.Fatalf("Get(provider_usage): %v", err)
	}
	if !found {
		t.Fatal("expected provider_usage to be merged")
	}
	if providerUsage["example"] != 1 {
		t.Fatalf("provider_usage = %#v", providerUsage)
	}

	if _, err := os.Stat(filepath.Join(rootDir, dbFilename)); !os.IsNotExist(err) {
		t.Fatalf("expected misplaced db to be archived, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, dbFilename+".merged")); err != nil {
		t.Fatalf("expected archived misplaced db to exist: %v", err)
	}
}
