package persistence

import (
	"database/sql"
	"os"
	"testing"
)

// TestRealDBMigration runs initSchema against a COPY of a real production
// database (path from STREAMNZB_TEST_DB) to prove the migrations apply cleanly
// to an existing schema, not just a freshly created one. Skipped when unset.
func TestRealDBMigration(t *testing.T) {
	path := os.Getenv("STREAMNZB_TEST_DB")
	if path == "" {
		t.Skip("STREAMNZB_TEST_DB not set")
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema on real DB: %v", err)
	}
	for table, cols := range map[string][]string{
		"library_nzbs": {"imdb_id", "tmdb_id", "tvdb_id", "kitsu_id", "status", "status_reason", "last_verified_at"},
		"nzb_attempts": {"preload", "ttff_ms", "avail_status", "avail_reason", "match_type"},
		"library_blueprints": {"media_caps_json", "video_codec", "height", "bit_depth",
			"hdr", "dolby_vision", "audio_codec"},
	} {
		have, err := tableColumns(db, table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for _, c := range cols {
			if _, ok := have[c]; !ok {
				t.Errorf("%s missing %s", table, c)
			}
		}
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM nzb_attempts").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	t.Logf("real DB migrated OK; nzb_attempts rows preserved: %d", n)
}
