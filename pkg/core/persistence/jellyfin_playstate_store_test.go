package persistence

import (
	"testing"
	"time"
)

func TestJellyfinPlaystateUpsertAndGet(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if err := store.Upsert(JellyfinPlaystate{
		StreamName: "alice", ItemID: "item-1", ContentType: "movie", ContentID: "tt0133093",
		PositionTicks: 1_000, RuntimeTicks: 100_000, PlayCount: 1,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.Upsert(JellyfinPlaystate{
		StreamName: "alice", ItemID: "item-1", ContentType: "movie", ContentID: "tt0133093",
		PositionTicks: 5_000, RuntimeTicks: 100_000, PlayCount: 1,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, ok := store.Get("alice", "item-1")
	if !ok {
		t.Fatal("expected row")
	}
	if got.PositionTicks != 5_000 || got.ContentID != "tt0133093" || got.Played {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.LastPlayedAt.IsZero() || time.Since(got.LastPlayedAt) > time.Minute {
		t.Fatalf("last played not stamped: %v", got.LastPlayedAt)
	}
	if _, ok := store.Get("bob", "item-1"); ok {
		t.Fatal("rows are per stream")
	}
}

func TestJellyfinPlaystateResumeAndPlayed(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	base := time.Now().Add(-time.Hour)
	for i, item := range []string{"a", "b", "c"} {
		if err := store.Upsert(JellyfinPlaystate{
			StreamName: "alice", ItemID: item, ContentType: "movie", ContentID: "tt" + item,
			PositionTicks: 100, LastPlayedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("upsert %s: %v", item, err)
		}
	}
	// A finished item never shows in resume.
	if err := store.SetPlayed("alice", "b", "movie", "ttb", true); err != nil {
		t.Fatalf("set played: %v", err)
	}
	// A row without progress is not resumable either.
	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "d", ContentType: "movie", ContentID: "ttd"}); err != nil {
		t.Fatalf("upsert d: %v", err)
	}

	resume := store.ListResume("alice", 10)
	if len(resume) != 2 || resume[0].ItemID != "c" || resume[1].ItemID != "a" {
		t.Fatalf("unexpected resume rows: %+v", resume)
	}

	played, _ := store.Get("alice", "b")
	if !played.Played || played.PositionTicks != 0 || played.PlayCount != 1 {
		t.Fatalf("unexpected played row: %+v", played)
	}
	if err := store.SetPlayed("alice", "b", "movie", "ttb", false); err != nil {
		t.Fatalf("set unplayed: %v", err)
	}
	unplayed, _ := store.Get("alice", "b")
	if unplayed.Played || unplayed.PlayCount != 1 {
		t.Fatalf("unplayed must keep the play count: %+v", unplayed)
	}
	if n := len(store.ListForStream("alice")); n != 4 {
		t.Fatalf("expected 4 rows for stream, got %d", n)
	}
}

func TestJellyfinPlaystateRenameAndDelete(t *testing.T) {
	m := openTestManager(t)
	store := m.JellyfinPlaystateStore()

	if err := store.Upsert(JellyfinPlaystate{StreamName: "alice", ItemID: "a", ContentType: "movie", ContentID: "tta", PositionTicks: 5}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := m.RenameStreamReferences("alice", "alicia"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := store.Get("alice", "a"); ok {
		t.Fatal("old name still has rows")
	}
	if _, ok := store.Get("alicia", "a"); !ok {
		t.Fatal("new name has no rows")
	}
	if err := store.DeleteForStream("alicia"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows := store.ListForStream("alicia"); len(rows) != 0 {
		t.Fatalf("expected no rows after delete, got %+v", rows)
	}
}
