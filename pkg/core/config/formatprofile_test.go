package config

import "testing"

func TestFormatProfileMigrationDedupesAndBinds(t *testing.T) {
	cfg := loadFromJSON(t, `{
		"config_version": 2,
		"streams": {
			"living-room": {"username": "living-room", "token": "t1",
				"result_name_template": "{{.Resolution}}", "result_description_template": "{{.Size}}"},
			"bedroom": {"username": "bedroom", "token": "t2",
				"result_name_template": "{{.Resolution}}", "result_description_template": "{{.Size}}"},
			"kids": {"username": "kids", "token": "t3",
				"result_name_template": "{{.Title}}"},
			"plain": {"username": "plain", "token": "t4"}
		}
	}`)

	if len(cfg.FormatProfiles) != 2 {
		t.Fatalf("FormatProfiles len = %d, want 2 (identical templates collapse)", len(cfg.FormatProfiles))
	}
	sharedName := cfg.Streams["living-room"].FormatProfileName
	if sharedName == "" || sharedName != cfg.Streams["bedroom"].FormatProfileName {
		t.Errorf("identical templates should share one profile, got %q / %q",
			cfg.Streams["living-room"].FormatProfileName, cfg.Streams["bedroom"].FormatProfileName)
	}
	kidsName := cfg.Streams["kids"].FormatProfileName
	if kidsName == "" || kidsName == sharedName {
		t.Errorf("distinct templates should get their own profile, got %q", kidsName)
	}
	if cfg.Streams["plain"].FormatProfileName != "" {
		t.Error("stream without templates must stay unbound")
	}
	for name, entry := range cfg.Streams {
		if entry.ResultNameTemplate != "" || entry.ResultDescriptionTemplate != "" {
			t.Errorf("stream %q keeps inline templates after migration", name)
		}
	}
	if fp := cfg.FormatProfileByName(sharedName); fp == nil || fp.ResultNameTemplate != "{{.Resolution}}" {
		t.Errorf("shared profile lost its templates: %+v", fp)
	}

	// Idempotency: a second load must not duplicate profiles or rebind.
	cfg2, err := LoadWithPath(cfg.LoadedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.FormatProfiles) != 2 {
		t.Errorf("second load FormatProfiles len = %d, want 2", len(cfg2.FormatProfiles))
	}
}

func TestFormatProfileEmptyListNotReseeded(t *testing.T) {
	cfg := loadFromJSON(t, `{
		"config_version": 2,
		"format_profiles": [],
		"streams": {"tv": {"username": "tv", "token": "tok", "result_name_template": "{{.Title}}"}}
	}`)
	if cfg.FormatProfiles == nil || len(cfg.FormatProfiles) != 0 {
		t.Errorf("deleted-all-profiles state must stay empty, got %+v", cfg.FormatProfiles)
	}
	// Migration never ran, so the inline template stays (legacy fallback).
	if cfg.Streams["tv"].ResultNameTemplate != "{{.Title}}" {
		t.Error("inline template must survive when migration is not run")
	}
}
