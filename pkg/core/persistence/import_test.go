package persistence

import (
	"testing"
	"time"
)

// seedSQLiteSource builds a populated streamnzb.db and returns the directory
// holding it, standing in for an existing installation about to move backends.
func seedSQLiteSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src, err := newManager(Settings{Backend: BackendSQLite}, dir)
	if err != nil {
		t.Fatalf("open source manager: %v", err)
	}

	if err := src.Set("provider_usage", map[string]int{"example": 7}); err != nil {
		t.Fatalf("seed kv: %v", err)
	}
	if err := src.LibraryStore().StoreItem(&LibraryItem{
		ContentType: "movie", ContentID: "tt0111161", ReleaseTitle: "Imported.Release",
		DetailsURL: "https://x/nzb/import", IndexerName: "example", SizeBytes: 4096,
		NZBData: []byte("<nzb>payload</nzb>"), Status: LibraryStatusGood,
		VideoCodec: "hevc", Height: 2160, MediaFileName: "movie.mkv", BlueprintJSON: `{"files":1}`,
	}); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	src.BadReleaseStore().MarkBad("https://x/nzb/bad", "Bad.Release", "article hole", time.Hour)
	src.RecordAttempt(RecordAttemptParams{
		ContentType: "movie", ContentID: "tt0111161", ReleaseTitle: "Imported.Release",
		ServedFile: "movie.mkv", Success: true, TTFFMS: 1234,
	})
	if err := src.RecordMetricsSnapshot(
		[]ProviderMetric{{CollectedAt: time.Now(), ProviderName: "provider-a", DownloadedMB: 12.5}},
		[]IndexerMetric{{CollectedAt: time.Now(), IndexerName: "indexer-a", SearchesCount: 3}},
	); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	return dir
}

func TestMigrateDataCarriesEveryTable(t *testing.T) {
	sourceDir := seedSQLiteSource(t)
	target := openTestManager(t)

	if err := importFromSQLite(target.wdb, sourceDir); err != nil {
		t.Fatalf("import: %v", err)
	}

	var usage map[string]int
	found, err := target.Get("provider_usage", &usage)
	if err != nil || !found {
		t.Fatalf("kv not imported: found=%v err=%v", found, err)
	}
	if usage["example"] != 7 {
		t.Fatalf("kv value = %#v", usage)
	}

	items, err := target.LibraryStore().GetCandidates("movie", "tt0111161", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("library not imported: n=%d err=%v", len(items), err)
	}
	if got := items[0]; got.ReleaseTitle != "Imported.Release" || got.Status != LibraryStatusGood {
		t.Fatalf("library row not faithful: %+v", got)
	}
	// The blob and its blueprint must survive the round trip, not just the row.
	if string(items[0].NZBData) != "<nzb>payload</nzb>" {
		t.Fatalf("nzb payload = %q", items[0].NZBData)
	}
	if items[0].VideoCodec != "hevc" || items[0].Height != 2160 {
		t.Fatalf("blueprint columns not imported: %+v", items[0])
	}

	if bad := target.BadReleaseStore().BadSet([]string{"https://x/nzb/bad"}); len(bad) != 1 {
		t.Fatalf("bad release not imported: %#v", bad)
	}

	attempts, err := target.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts not imported: n=%d err=%v", len(attempts), err)
	}
	if attempts[0].TTFFMS != 1234 || !attempts[0].Success {
		t.Fatalf("attempt row not faithful: %+v", attempts[0])
	}

	providers, err := target.GetProviderMetricsSummary(nil, nil)
	if err != nil || len(providers) != 1 {
		t.Fatalf("provider metrics not imported: n=%d err=%v", len(providers), err)
	}
	indexers, err := target.GetIndexerMetricsSummary(nil, nil)
	if err != nil || len(indexers) != 1 {
		t.Fatalf("indexer metrics not imported: n=%d err=%v", len(indexers), err)
	}
}

// TestMigrateDataIsIdempotent guards the property that makes the import
// safe to leave enabled: a second run must not duplicate history.
func TestMigrateDataIsIdempotent(t *testing.T) {
	sourceDir := seedSQLiteSource(t)
	target := openTestManager(t)

	if err := importFromSQLite(target.wdb, sourceDir); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// Clearing the completion marker forces the second run to actually walk the
	// tables, so this exercises the per-row conflict handling and the
	// non-empty-table guard rather than the cheap short-circuit.
	if err := deleteKV(target.wdb, importCompletedKey); err != nil {
		t.Fatalf("clear import marker: %v", err)
	}
	if err := importFromSQLite(target.wdb, sourceDir); err != nil {
		t.Fatalf("second import: %v", err)
	}

	attempts, err := target.ListAttempts(ListAttemptsOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("expected 1 attempt after two imports, got %d", len(attempts))
	}
	items, err := target.LibraryStore().GetCandidates("movie", "tt0111161", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected 1 library item after two imports, got %d (err=%v)", len(items), err)
	}
	providers, err := target.GetProviderMetricsSummary(nil, nil)
	if err != nil || len(providers) != 1 {
		t.Fatalf("expected 1 provider series after two imports, got %d (err=%v)", len(providers), err)
	}
}

// TestMigrateDataWithoutSourceIsNoOp covers a fresh install pointed
// straight at Postgres: there is no streamnzb.db to carry across.
func TestMigrateDataWithoutSourceIsNoOp(t *testing.T) {
	target := openTestManager(t)
	if err := importFromSQLite(target.wdb, t.TempDir()); err != nil {
		t.Fatalf("import with no source: %v", err)
	}
	attempts, err := target.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 0 {
		t.Fatalf("expected an empty database, got %d attempts", len(attempts))
	}
}
