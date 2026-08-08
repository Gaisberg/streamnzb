package persistence

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestInitSchemaIsIdempotentAndComplete runs initSchema twice against a fresh
// database and asserts every column the code reads actually exists. Running it
// twice also proves the ALTER TABLE migrations tolerate already-present
// columns, which is the property the "duplicate column" check provides.
func TestInitSchemaIsIdempotentAndComplete(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(dir, "test.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 2; i++ {
		if err := initSchema(db); err != nil {
			t.Fatalf("initSchema pass %d: %v", i+1, err)
		}
	}

	want := map[string][]string{
		"library_blueprints": {"media_caps_json", "video_codec", "height", "bit_depth", "hdr", "dolby_vision", "audio_codec"},
		"library_nzbs":       {"imdb_id", "tmdb_id", "tvdb_id", "kitsu_id", "status", "status_reason", "last_verified_at"},
		"provider_metrics":   {"article_available_count", "article_missing_count"},
		"indexer_metrics":    {"unique_hits_count"},
		"nzb_attempts": {"preload", "served_file", "match_type", "indexer_name", "stream_name",
			"provider_name", "avail_status", "avail_reason", "ttff_ms"},
	}
	for table, columns := range want {
		have, err := tableColumns(db, table)
		if err != nil {
			t.Fatalf("tableColumns(%s): %v", table, err)
		}
		for _, c := range columns {
			if _, ok := have[c]; !ok {
				t.Errorf("%s is missing column %s", table, c)
			}
		}
	}
}
