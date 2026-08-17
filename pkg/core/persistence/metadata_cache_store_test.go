package persistence

import (
	"fmt"
	"testing"
	"time"
)

func TestMetadataCacheRoundTrip(t *testing.T) {
	m := openTestManager(t)
	store := m.MetadataCacheStore()

	body := []byte(`{"id":603,"title":"The Matrix"}`)
	store.Put("tmdb", "/movie/603?", body, time.Hour)

	got, ok := store.Get("tmdb", "/movie/603?")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if string(got) != string(body) {
		t.Fatalf("body mismatch: got %q", got)
	}

	// Same key under a different source is a different entry.
	if _, ok := store.Get("kitsu", "/movie/603?"); ok {
		t.Fatal("cross-source lookup must miss")
	}
}

func TestMetadataCacheUpsertReplaces(t *testing.T) {
	m := openTestManager(t)
	store := m.MetadataCacheStore()

	store.Put("tmdb", "/movie/603?", []byte(`old`), time.Hour)
	store.Put("tmdb", "/movie/603?", []byte(`new`), time.Hour)

	got, ok := store.Get("tmdb", "/movie/603?")
	if !ok || string(got) != "new" {
		t.Fatalf("expected replaced body, got %q ok=%v", got, ok)
	}
	if n := store.Count(); n != 1 {
		t.Fatalf("expected 1 row after upsert, got %d", n)
	}
}

func TestMetadataCacheExpiredEntriesInvisible(t *testing.T) {
	m := openTestManager(t)
	store := m.MetadataCacheStore()

	// The very first Put triggers the opportunistic sweep (lastSweep starts at
	// zero); warm it so the expired row below is not swept mid-test.
	store.Put("tmdb", "/warm?", []byte(`x`), time.Hour)

	store.Put("tmdb", "/expired?", []byte(`x`), time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if _, ok := store.Get("tmdb", "/expired?"); ok {
		t.Fatal("expired entry must not be returned")
	}
	// The row still exists until a sweep runs.
	if n := store.Count(); n != 2 {
		t.Fatalf("expected 2 rows before purge, got %d", n)
	}
	store.PurgeExpired()
	if n := store.Count(); n != 1 {
		t.Fatalf("expected 1 row after purge, got %d", n)
	}
}

func TestMetadataCacheSurvivesReopen(t *testing.T) {
	db := suiteDB(t)
	m := db.open(t)
	m.MetadataCacheStore().Put("tmdb", "/movie/603?", []byte(`persisted`), time.Hour)
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	m2 := db.open(t)
	t.Cleanup(func() { _ = m2.Close() })
	got, ok := m2.MetadataCacheStore().Get("tmdb", "/movie/603?")
	if !ok || string(got) != "persisted" {
		t.Fatalf("expected entry to survive reopen, got %q ok=%v", got, ok)
	}
}

func TestMetadataCacheEnforceRowCap(t *testing.T) {
	m := openTestManager(t)
	store := m.MetadataCacheStore()

	for i := 0; i < 10; i++ {
		store.Put("tmdb", fmt.Sprintf("/movie/%d?", i), []byte(`b`), time.Hour)
		// fetched_at is UnixMilli; keep insert order distinguishable.
		time.Sleep(2 * time.Millisecond)
	}
	store.EnforceRowCap(4)
	if n := store.Count(); n != 4 {
		t.Fatalf("expected 4 rows after cap, got %d", n)
	}
	// The newest rows survive.
	if _, ok := store.Get("tmdb", "/movie/9?"); !ok {
		t.Fatal("newest row must survive the cap")
	}
	if _, ok := store.Get("tmdb", "/movie/0?"); ok {
		t.Fatal("oldest row must be evicted by the cap")
	}

	// A cap larger than the table is a no-op.
	store.EnforceRowCap(100)
	if n := store.Count(); n != 4 {
		t.Fatalf("expected 4 rows after oversized cap, got %d", n)
	}
}

func TestMetadataCacheNilReceiverSafety(t *testing.T) {
	var store *MetadataCacheStore
	store.Put("tmdb", "/x?", []byte(`b`), time.Hour)
	if _, ok := store.Get("tmdb", "/x?"); ok {
		t.Fatal("nil store must miss")
	}
	store.PurgeExpired()
	store.EnforceRowCap(10)
	if n := store.Count(); n != 0 {
		t.Fatalf("nil store count = %d", n)
	}

	// Nil manager accessor path.
	var m *StateManager
	if m.MetadataCacheStore() != nil {
		t.Fatal("nil manager must return nil store")
	}
}

func TestMetadataCacheRejectsInvalidPuts(t *testing.T) {
	m := openTestManager(t)
	store := m.MetadataCacheStore()

	store.Put("", "/x?", []byte(`b`), time.Hour)
	store.Put("tmdb", "", []byte(`b`), time.Hour)
	store.Put("tmdb", "/x?", nil, time.Hour)
	store.Put("tmdb", "/x?", []byte(`b`), 0)
	if n := store.Count(); n != 0 {
		t.Fatalf("invalid puts must not insert rows, got %d", n)
	}
}
