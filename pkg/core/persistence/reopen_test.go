package persistence

import (
	"os"
	"strings"
	"testing"
)

func storeProbeItem(t *testing.T, ls *LibraryStore, contentID string) {
	t.Helper()
	if err := ls.StoreItem(&LibraryItem{
		ContentType: "movie", ContentID: contentID, ReleaseTitle: "probe-" + contentID,
		DetailsURL: "https://x/" + contentID, NZBData: []byte("<nzb/>"),
	}); err != nil {
		t.Fatalf("store %s: %v", contentID, err)
	}
}

// TestReopenSwitchesDatabaseUnderStores is the load-bearing test for the
// database hot reload: the stores hold their own connRefs, so this proves a
// Reopen repoints them too rather than leaving them on the old pool.
func TestReopenSwitchesDatabaseUnderStores(t *testing.T) {
	first := suiteDB(t)
	second := suiteDB(t)

	mgr := first.open(t)
	t.Cleanup(func() { _ = mgr.Close() })

	storeProbeItem(t, mgr.LibraryStore(), "tt-before")
	if err := mgr.Set("probe_key", map[string]string{"where": "first"}); err != nil {
		t.Fatalf("seed kv: %v", err)
	}

	closeOld, err := mgr.Reopen(second.settings, second.dataDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(closeOld)

	// The second database is a separate file/schema and nothing is imported
	// between two databases of the same kind, so it starts empty. A store still
	// pointed at the old pool would keep returning the seeded row.
	items, err := mgr.LibraryStore().GetCandidates("movie", "tt-before", 0, 0)
	if err != nil {
		t.Fatalf("GetCandidates after reopen: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("library store still reads the old database: got %d rows", len(items))
	}

	var probe map[string]string
	found, err := mgr.Get("probe_key", &probe)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if found {
		t.Fatalf("kv still reads the old database: %#v", probe)
	}

	// Writes must land in the new database and be readable back through the
	// same stores.
	storeProbeItem(t, mgr.LibraryStore(), "tt-after")
	items, err = mgr.LibraryStore().GetCandidates("movie", "tt-after", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("write after reopen not visible: n=%d err=%v", len(items), err)
	}
	if err := mgr.Set("probe_key", map[string]string{"where": "second"}); err != nil {
		t.Fatalf("write kv after reopen: %v", err)
	}
	if found, err := mgr.Get("probe_key", &probe); err != nil || !found {
		t.Fatalf("kv write after reopen not visible: found=%v err=%v", found, err)
	}
	if probe["where"] != "second" {
		t.Fatalf("kv read the wrong database: %#v", probe)
	}
}

// TestReopenFromSQLiteToPostgresCarriesData exercises the switch users actually
// make: running on SQLite, then pointing at Postgres and expecting the library
// and history to still be there afterwards. Needs a real server, so it skips
// unless one is configured.
func TestReopenFromSQLiteToPostgresCarriesData(t *testing.T) {
	base := strings.TrimSpace(os.Getenv(testPostgresDSNEnv))
	if base == "" {
		t.Skipf("%s not set", testPostgresDSNEnv)
	}
	target := Settings{
		Backend:     BackendPostgres,
		DSN:         newPostgresSchema(t, base),
		MigrateData: true,
	}

	dataDir := t.TempDir()
	mgr, err := newManager(Settings{Backend: BackendSQLite}, dataDir)
	if err != nil {
		t.Fatalf("open sqlite manager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	storeProbeItem(t, mgr.LibraryStore(), "tt-carried")
	mgr.RecordAttempt(RecordAttemptParams{
		ContentType: "movie", ContentID: "tt-carried", ReleaseTitle: "probe-tt-carried", Success: true,
	})
	if err := mgr.Set("probe_key", map[string]string{"where": "sqlite"}); err != nil {
		t.Fatalf("seed kv: %v", err)
	}

	closeOld, err := mgr.Reopen(target, dataDir)
	if err != nil {
		t.Fatalf("reopen onto postgres: %v", err)
	}
	t.Cleanup(closeOld)

	if got := mgr.Backend(); got != BackendPostgres {
		t.Fatalf("Backend() = %q, want %q", got, BackendPostgres)
	}
	items, err := mgr.LibraryStore().GetCandidates("movie", "tt-carried", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("library did not carry across: n=%d err=%v", len(items), err)
	}
	attempts, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("history did not carry across: n=%d err=%v", len(attempts), err)
	}
	var probe map[string]string
	found, err := mgr.Get("probe_key", &probe)
	if err != nil || !found || probe["where"] != "sqlite" {
		t.Fatalf("kv did not carry across: found=%v probe=%#v err=%v", found, probe, err)
	}

	// Return leg: history written on Postgres must sync back into the same
	// SQLite file, alongside what was already there and without duplicating it.
	mgr.RecordAttempt(RecordAttemptParams{
		ContentType: "movie", ContentID: "tt-pg", ReleaseTitle: "recorded-on-postgres", Success: true,
	})
	closeBack, err := mgr.Reopen(Settings{Backend: BackendSQLite, MigrateData: true}, dataDir)
	if err != nil {
		t.Fatalf("reopen back onto sqlite: %v", err)
	}
	t.Cleanup(closeBack)

	if got := mgr.Backend(); got != BackendSQLite {
		t.Fatalf("Backend() = %q, want %q", got, BackendSQLite)
	}
	attempts, err = mgr.ListAttempts(ListAttemptsOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListAttempts after round trip: %v", err)
	}
	titles := map[string]int{}
	for _, a := range attempts {
		titles[a.ReleaseTitle]++
	}
	if titles["probe-tt-carried"] != 1 || titles["recorded-on-postgres"] != 1 || len(attempts) != 2 {
		t.Fatalf("cross-backend round trip lost or duplicated history: %#v", titles)
	}
}

// TestMigrationRoundTripSyncsNewHistory is the case the sync watermark exists
// for: switch away, accumulate history on the other backend, switch back. The
// original database must end up with both its own history and everything added
// while it was idle — and no duplicates.
func TestMigrationRoundTripSyncsNewHistory(t *testing.T) {
	home := suiteDB(t)
	away := suiteDB(t)
	migrating := func(s Settings) Settings { s.MigrateData = true; return s }

	mgr := home.open(t)
	t.Cleanup(func() { _ = mgr.Close() })

	mgr.RecordAttempt(RecordAttemptParams{
		ContentType: "movie", ContentID: "tt-home", ReleaseTitle: "recorded-at-home", Success: true,
	})
	storeProbeItem(t, mgr.LibraryStore(), "tt-home")

	// Away we go; the full history comes along.
	closeHome, err := mgr.Reopen(migrating(away.settings), away.dataDir)
	if err != nil {
		t.Fatalf("reopen onto away: %v", err)
	}
	t.Cleanup(closeHome)
	if attempts, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 100}); err != nil || len(attempts) != 1 {
		t.Fatalf("history did not travel: n=%d err=%v", len(attempts), err)
	}

	// New history, recorded only on the away database.
	mgr.RecordAttempt(RecordAttemptParams{
		ContentType: "movie", ContentID: "tt-away", ReleaseTitle: "recorded-while-away", Success: true,
	})

	closeAway, err := mgr.Reopen(migrating(home.settings), home.dataDir)
	if err != nil {
		t.Fatalf("reopen back home: %v", err)
	}
	t.Cleanup(closeAway)

	attempts, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListAttempts after round trip: %v", err)
	}
	titles := map[string]int{}
	for _, a := range attempts {
		titles[a.ReleaseTitle]++
	}
	if titles["recorded-at-home"] != 1 {
		t.Errorf("original history should survive exactly once, got %d", titles["recorded-at-home"])
	}
	if titles["recorded-while-away"] != 1 {
		t.Errorf("history added while away should sync back exactly once, got %d", titles["recorded-while-away"])
	}
	if len(attempts) != 2 {
		t.Fatalf("expected exactly 2 attempts after the round trip, got %d: %#v", len(attempts), titles)
	}
}

// TestMigrationIsSourceAuthoritative covers the keyed half: the database being
// left is the live one, so its version of a row must win over whatever stale
// copy the destination still holds.
func TestMigrationIsSourceAuthoritative(t *testing.T) {
	home := suiteDB(t)
	away := suiteDB(t)
	migrating := func(s Settings) Settings { s.MigrateData = true; return s }

	mgr := home.open(t)
	t.Cleanup(func() { _ = mgr.Close() })
	storeProbeItem(t, mgr.LibraryStore(), "tt-status")

	closeHome, err := mgr.Reopen(migrating(away.settings), away.dataDir)
	if err != nil {
		t.Fatalf("reopen onto away: %v", err)
	}
	t.Cleanup(closeHome)

	// Change the row while away; home still holds the old version.
	items, err := mgr.LibraryStore().GetCandidates("movie", "tt-status", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("item did not travel: n=%d err=%v", len(items), err)
	}
	mgr.LibraryStore().MarkStatus(items[0].ID, LibraryStatusGood, "verified while away")

	closeAway, err := mgr.Reopen(migrating(home.settings), home.dataDir)
	if err != nil {
		t.Fatalf("reopen back home: %v", err)
	}
	t.Cleanup(closeAway)

	items, err = mgr.LibraryStore().GetCandidates("movie", "tt-status", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("item missing after round trip: n=%d err=%v", len(items), err)
	}
	if items[0].Status != LibraryStatusGood {
		t.Fatalf("stale destination row won: status = %q, want %q", items[0].Status, LibraryStatusGood)
	}
}

// TestReopenFailureKeepsCurrentDatabase covers the degraded path the API layer
// relies on: a bad connection string must leave the manager working.
func TestReopenFailureKeepsCurrentDatabase(t *testing.T) {
	db := suiteDB(t)
	mgr := db.open(t)
	t.Cleanup(func() { _ = mgr.Close() })

	storeProbeItem(t, mgr.LibraryStore(), "tt-keep")

	if _, err := mgr.Reopen(Settings{Backend: BackendPostgres, DSN: ""}, db.dataDir); err == nil {
		t.Fatal("expected reopen onto an empty postgres DSN to fail")
	}

	items, err := mgr.LibraryStore().GetCandidates("movie", "tt-keep", 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("database unusable after a failed reopen: n=%d err=%v", len(items), err)
	}
}
