package persistence

import (
	"testing"
)

// TestInitSchemaIsIdempotentAndComplete runs initSchema twice against a fresh
// database and asserts every column the code reads actually exists. Running it
// twice also proves the ALTER TABLE migrations tolerate already-present
// columns, which is the property the duplicate-column handling provides.
func TestInitSchemaIsIdempotentAndComplete(t *testing.T) {
	mgr := openTestManager(t)

	// newManager already ran it once; a second pass must be a no-op.
	if err := initSchema(mgr.wdb); err != nil {
		t.Fatalf("initSchema second pass: %v", err)
	}

	want := map[string][]string{
		"library_blueprints": {"media_caps_json", "video_codec", "height", "bit_depth", "hdr", "dolby_vision", "audio_codec"},
		"library_nzbs":       {"imdb_id", "tmdb_id", "tvdb_id", "kitsu_id", "status", "status_reason", "last_verified_at"},
		"provider_metrics":   {"article_available_count", "article_missing_count"},
		"indexer_metrics": {"unique_hits_count", "grab_success_count", "grab_failure_count",
			"unique_success_count", "avg_grab_ms"},
		"nzb_attempts": {"preload", "served_file", "match_type", "indexer_name", "stream_name",
			"provider_name", "avail_status", "avail_reason", "ttff_ms"},
	}
	for table, columns := range want {
		have, err := tableColumns(mgr.db, table)
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
