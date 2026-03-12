package main

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/persistence"
)

func TestExistingConfigAdminToken(t *testing.T) {
	cfg := &config.Config{
		LoadedPath: filepath.Join(t.TempDir(), "config.json"),
		AdminToken: "persisted-admin-token",
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if got := existingConfigAdminToken(cfg.LoadedPath); got != "persisted-admin-token" {
		t.Fatalf("expected persisted admin token, got %q", got)
	}
}

func TestMigrateLegacyAuthConfig(t *testing.T) {
	dataDir := t.TempDir()
	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		t.Fatalf("GetManager: %v", err)
	}
	t.Cleanup(func() {
		closeStateManagerDB(t, stateMgr)
	})
	configPath := filepath.Join(dataDir, "config.json")

	t.Run("migrates legacy admin token and devices", func(t *testing.T) {
		resetLegacyAuthState(t, stateMgr)
		cfg := &config.Config{LoadedPath: configPath, AdminToken: "generated-token"}
		if err := stateMgr.Set("admin", map[string]any{"password_hash": "legacy-hash", "must_change_password": true}); err != nil {
			t.Fatalf("Set admin: %v", err)
		}
		if err := stateMgr.Set("devices", map[string]*auth.Device{
			"admin":       &auth.Device{Username: "admin", Token: "legacy-admin-token"},
			"living-room": &auth.Device{Username: "living-room", Token: "device-token", StreamIDs: []string{"movie-stream"}},
		}); err != nil {
			t.Fatalf("Set devices: %v", err)
		}

		migrateLegacyAuthConfig(cfg, stateMgr, false)

		if cfg.AdminToken != "legacy-admin-token" {
			t.Fatalf("expected migrated admin token, got %q", cfg.AdminToken)
		}
		if cfg.AdminPasswordHash != "legacy-hash" || !cfg.AdminMustChangePassword {
			t.Fatalf("expected migrated admin credentials, got hash=%q mustChange=%v", cfg.AdminPasswordHash, cfg.AdminMustChangePassword)
		}
		if got := existingConfigAdminToken(configPath); got != "legacy-admin-token" {
			t.Fatalf("expected persisted migrated admin token, got %q", got)
		}
		if len(cfg.Devices) != 1 || cfg.Devices["living-room"] == nil {
			t.Fatalf("expected one migrated non-admin device, got %#v", cfg.Devices)
		}
		if _, ok := cfg.Devices["admin"]; ok {
			t.Fatalf("expected admin device to be skipped, got %#v", cfg.Devices)
		}

		assertStateKeyMissing(t, stateMgr, "admin")
		assertStateKeyMissing(t, stateMgr, "devices")
	})

	t.Run("prefers existing config token", func(t *testing.T) {
		resetLegacyAuthState(t, stateMgr)
		cfg := &config.Config{LoadedPath: configPath, AdminToken: "config-token"}
		if err := cfg.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := stateMgr.Set("admin", map[string]any{
			"password_hash":        "legacy-hash-2",
			"must_change_password": true,
			"token":                "legacy-state-token",
		}); err != nil {
			t.Fatalf("Set admin: %v", err)
		}

		migrateLegacyAuthConfig(cfg, stateMgr, true)

		if cfg.AdminToken != "config-token" {
			t.Fatalf("expected existing config token to win, got %q", cfg.AdminToken)
		}
		if cfg.AdminPasswordHash != "legacy-hash-2" || !cfg.AdminMustChangePassword {
			t.Fatalf("expected admin credentials to migrate, got hash=%q mustChange=%v", cfg.AdminPasswordHash, cfg.AdminMustChangePassword)
		}
		if got := existingConfigAdminToken(configPath); got != "config-token" {
			t.Fatalf("expected persisted config token to remain unchanged, got %q", got)
		}
	})
}

func resetLegacyAuthState(t *testing.T, stateMgr *persistence.StateManager) {
	t.Helper()
	for _, key := range []string{"admin", "admin_sessions", "devices", "users"} {
		if err := stateMgr.Delete(key); err != nil {
			t.Fatalf("Delete %s: %v", key, err)
		}
	}
}

func assertStateKeyMissing(t *testing.T, stateMgr *persistence.StateManager, key string) {
	t.Helper()
	var payload map[string]any
	found, err := stateMgr.Get(key, &payload)
	if err != nil {
		t.Fatalf("Get %s: %v", key, err)
	}
	if found {
		t.Fatalf("expected %s to be removed from legacy state, got %#v", key, payload)
	}
}

func closeStateManagerDB(t *testing.T, stateMgr *persistence.StateManager) {
	t.Helper()
	if stateMgr == nil {
		return
	}
	field := reflect.ValueOf(stateMgr).Elem().FieldByName("db")
	if !field.IsValid() || field.IsNil() {
		return
	}
	db := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(*sql.DB)
	_ = db.Close()
}
