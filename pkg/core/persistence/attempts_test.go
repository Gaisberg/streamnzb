package persistence

import (
	"testing"
	"time"
)

func newTestStateManager(t *testing.T) *StateManager {
	t.Helper()
	return openTestManager(t)
}

func TestRecordPreloadAttemptUsesSharedWriteLock(t *testing.T) {
	mgr := newTestStateManager(t)
	mgr.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-lock", ReleaseTitle: "Locked release"})
	}()

	select {
	case <-done:
		t.Fatal("RecordPreloadAttempt should wait for the shared write lock")
	case <-time.After(50 * time.Millisecond):
	}

	mgr.mu.Unlock()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("RecordPreloadAttempt did not complete after releasing the shared write lock")
	}

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 || !list[0].Preload {
		t.Fatalf("expected one preload attempt after lock release, got %+v", list)
	}
}

func TestRecordAttemptUsesSharedWriteLock(t *testing.T) {
	mgr := newTestStateManager(t)
	id := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-final", ReleaseTitle: "Preloaded release"})

	mgr.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.RecordAttempt(RecordAttemptParams{ID: id.ID, Gen: id.Gen, SlotPath: "slot-final", ReleaseTitle: "Preloaded release", Success: true})
	}()

	select {
	case <-done:
		t.Fatal("RecordAttempt should wait for the shared write lock")
	case <-time.After(50 * time.Millisecond):
	}

	mgr.mu.Unlock()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("RecordAttempt did not complete after releasing the shared write lock")
	}

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one resolved attempt, got %d", len(list))
	}
	if list[0].Preload {
		t.Fatal("expected preload row to be resolved")
	}
	if !list[0].Success {
		t.Fatal("expected resolved attempt to be marked successful")
	}
}

func TestRecordAttemptPersistsServedFile(t *testing.T) {
	mgr := newTestStateManager(t)
	id := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-pack", ReleaseTitle: "Season pack", MatchType: "season_pack"})

	wantServedFile := "Altered.Carbon.S02E03.1080p.mkv"
	mgr.RecordAttempt(RecordAttemptParams{
		ID:           id.ID,
		Gen:          id.Gen,
		SlotPath:     "slot-pack",
		ReleaseTitle: "Season pack",
		ServedFile:   wantServedFile,
		MatchType:    "season_pack",
		Success:      true,
	})

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one resolved attempt, got %d", len(list))
	}
	if got := list[0].ServedFile; got != wantServedFile {
		t.Fatalf("ServedFile = %q, want %q", got, wantServedFile)
	}
	if got := list[0].MatchType; got != "season_pack" {
		t.Fatalf("MatchType = %q, want %q", got, "season_pack")
	}
	if list[0].Preload {
		t.Fatal("expected preload row to be resolved")
	}
}

func TestUpdatePendingAttemptKeepsPreloadAndAddsReason(t *testing.T) {
	mgr := newTestStateManager(t)
	id := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-pending", ReleaseTitle: "Early stop"})

	mgr.UpdatePendingAttempt(RecordAttemptParams{
		ID:            id.ID,
		Gen:           id.Gen,
		SlotPath:      "slot-pending",
		ProviderName:  "news.example.net",
		ServedFile:    "Example.mkv",
		MatchType:     "multi_episode",
		FailureReason: "Playback ended too early to classify this release as good.",
		AvailStatus:   "skipped",
		AvailReason:   "Playback ended before the good threshold was reached.",
	})

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one pending attempt, got %d", len(list))
	}
	if !list[0].Preload {
		t.Fatal("expected preload row to remain pending")
	}
	if got := list[0].ProviderName; got != "news.example.net" {
		t.Fatalf("ProviderName = %q, want %q", got, "news.example.net")
	}
	if got := list[0].ServedFile; got != "Example.mkv" {
		t.Fatalf("ServedFile = %q, want %q", got, "Example.mkv")
	}
	if got := list[0].MatchType; got != "multi_episode" {
		t.Fatalf("MatchType = %q, want %q", got, "multi_episode")
	}
	if got := list[0].FailureReason; got != "Playback ended too early to classify this release as good." {
		t.Fatalf("FailureReason = %q", got)
	}
	if got := list[0].AvailStatus; got != "skipped" {
		t.Fatalf("AvailStatus = %q, want %q", got, "skipped")
	}
	if got := list[0].AvailReason; got != "Playback ended before the good threshold was reached." {
		t.Fatalf("AvailReason = %q", got)
	}
}

func TestResolvePendingAttemptFinalizesWithoutInsertingFallbackRow(t *testing.T) {
	mgr := newTestStateManager(t)
	id := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-pending-finalize", ReleaseTitle: "Short play"})

	mgr.ResolvePendingAttempt(RecordAttemptParams{
		ID:            id.ID,
		Gen:           id.Gen,
		SlotPath:      "slot-pending-finalize",
		FailureReason: "Playback probe ended before the good threshold was reached.",
		AvailStatus:   "skipped",
		AvailReason:   "Playback ended before the good threshold was reached.",
	})

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one finalized attempt, got %d", len(list))
	}
	if list[0].Preload {
		t.Fatal("expected preload row to be resolved")
	}
	if list[0].Success {
		t.Fatal("expected resolved attempt to remain unsuccessful")
	}
	if got := list[0].FailureReason; got != "Playback probe ended before the good threshold was reached." {
		t.Fatalf("FailureReason = %q", got)
	}

	mgr.ResolvePendingAttempt(RecordAttemptParams{
		ID:            id.ID,
		Gen:           id.Gen,
		SlotPath:      "slot-pending-finalize",
		FailureReason: "should not create another row",
	})

	list, err = mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts after second finalize: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected second finalize to remain a no-op, got %d rows", len(list))
	}
	if got := list[0].FailureReason; got != "Playback probe ended before the good threshold was reached." {
		t.Fatalf("expected second finalize to preserve failure reason, got %q", got)
	}
}

func TestDeleteAttemptsBeforeRemovesOlderRows(t *testing.T) {
	mgr := newTestStateManager(t)

	nowMs := time.Now().UnixMilli()
	_, err := mgr.db.Exec(`INSERT INTO nzb_attempts (tried_at, stream_name, content_type, content_id, content_title, indexer_name, release_title, release_url, release_size, served_file, success, failure_reason, slot_path, preload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0),
		       (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		nowMs-(100*24*time.Hour).Milliseconds(), "StreamOld", "movie", "tt-old", "Old Movie", "Indexer", "Old Release", "", 0, "", 0, "EOF", "slot-old",
		nowMs-(5*24*time.Hour).Milliseconds(), "StreamNew", "movie", "tt-new", "New Movie", "Indexer", "New Release", "", 0, "", 1, "", "slot-new",
	)
	if err != nil {
		t.Fatalf("insert attempts: %v", err)
	}

	deleted, err := mgr.DeleteAttemptsBefore(time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("DeleteAttemptsBefore: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected one remaining attempt, got %d", len(list))
	}
	if list[0].ContentID != "tt-new" {
		t.Fatalf("remaining attempt content_id = %q, want %q", list[0].ContentID, "tt-new")
	}
}

func TestDeleteAttemptsScopesToStream(t *testing.T) {
	mgr := newTestStateManager(t)

	nowMs := time.Now().UnixMilli()
	// Three rows: one named stream, one other stream, and a legacy row whose
	// stream_name was never written — the UI labels that one "default".
	_, err := mgr.db.Exec(`INSERT INTO nzb_attempts (tried_at, stream_name, content_type, content_id, release_title, success, preload)
		VALUES (?, ?, 'movie', 'tt-a', 'A', 1, 0),
		       (?, ?, 'movie', 'tt-b', 'B', 1, 0),
		       (?, '',  'movie', 'tt-c', 'C', 1, 0)`,
		nowMs, "living-room", nowMs, "phone", nowMs)
	if err != nil {
		t.Fatalf("insert attempts: %v", err)
	}

	deleted, err := mgr.DeleteAttempts("living-room")
	if err != nil {
		t.Fatalf("DeleteAttempts: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	list, _ := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if len(list) != 2 {
		t.Fatalf("expected 2 remaining attempts, got %d", len(list))
	}

	// "default" also covers the row with no stream name at all.
	deleted, err = mgr.DeleteAttempts(DefaultStreamLabel)
	if err != nil {
		t.Fatalf("DeleteAttempts default: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("default scope deleted = %d, want 1", deleted)
	}

	// Empty scope clears whatever is left.
	deleted, err = mgr.DeleteAttempts("")
	if err != nil {
		t.Fatalf("DeleteAttempts all: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("all scope deleted = %d, want 1", deleted)
	}
	if list, _ := mgr.ListAttempts(ListAttemptsOptions{Limit: 10}); len(list) != 0 {
		t.Fatalf("expected empty history, got %d", len(list))
	}
}

// Rows are selected by attempt id, never by slot path: a second attempt behind
// the same slot gets its own row, a write about the first lands on the first,
// and a finalized row keeps its first verdict.
func TestAttemptRowsAreSelectedByIDNotSlot(t *testing.T) {
	mgr := newTestStateManager(t)
	oldID := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-x", ReleaseTitle: "Copy A"})
	newID := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-x", ReleaseTitle: "Copy B"})
	if oldID.ID <= 0 || newID.ID <= 0 || oldID.ID == newID.ID {
		t.Fatalf("expected two distinct rows for two attempts on one slot, got %d and %d", oldID.ID, newID.ID)
	}

	// The replacement succeeds; the old attempt's delayed failure arrives after.
	mgr.RecordAttempt(RecordAttemptParams{ID: newID.ID, Gen: newID.Gen, SlotPath: "slot-x", Success: true})
	mgr.RecordAttempt(RecordAttemptParams{ID: oldID.ID, Gen: oldID.Gen, SlotPath: "slot-x", FailureReason: "old copy failed"})
	// A stale failure or resolution about the replacement after its success,
	// and a pending update on a finalized row: all no-ops. (One verdict per
	// attempt is the caller's rule; a success is also guarded here.)
	mgr.RecordAttempt(RecordAttemptParams{ID: newID.ID, Gen: newID.Gen, SlotPath: "slot-x", FailureReason: "late failure"})
	mgr.ResolvePendingAttempt(RecordAttemptParams{ID: newID.ID, Gen: newID.Gen, SlotPath: "slot-x", FailureReason: "late resolve"})
	mgr.UpdatePendingAttempt(RecordAttemptParams{ID: oldID.ID, Gen: oldID.Gen, SlotPath: "slot-x", FailureReason: "late pending"})

	// A row settled without a verdict (a short play) still takes the attempt's
	// real verdict when it comes.
	shortID := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-short"})
	mgr.ResolvePendingAttempt(RecordAttemptParams{ID: shortID.ID, Gen: shortID.Gen, FailureReason: "Playback probe ended before the good threshold was reached."})
	mgr.RecordAttempt(RecordAttemptParams{ID: shortID.ID, Gen: shortID.Gen, SlotPath: "slot-short", Success: true})

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected exactly the three attempt rows, got %d: %+v", len(list), list)
	}
	byID := map[int64]NZBAttempt{}
	for _, a := range list {
		byID[a.ID] = a
	}
	if a := byID[newID.ID]; a.Preload || !a.Success || a.FailureReason != "" {
		t.Fatalf("replacement row = %+v, want its success to stand", a)
	}
	if a := byID[oldID.ID]; a.Preload || a.Success || a.FailureReason != "old copy failed" {
		t.Fatalf("old row = %+v, want its own failure to stand", a)
	}
	if a := byID[shortID.ID]; a.Preload || !a.Success {
		t.Fatalf("short-play row = %+v, want it to take the later success", a)
	}
}

// A verdict for a row the user cleared mid-play is still recorded (a new row);
// a pending row's existing reason survives its resolution.
func TestAttemptRowClearedOrCarryingAReason(t *testing.T) {
	mgr := newTestStateManager(t)
	cleared := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-cleared"})
	if _, err := mgr.DeleteAttempts(""); err != nil {
		t.Fatalf("DeleteAttempts: %v", err)
	}
	mgr.RecordAttempt(RecordAttemptParams{ID: cleared.ID, Gen: cleared.Gen, SlotPath: "slot-cleared", Success: true})

	early := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-early"})
	mgr.UpdatePendingAttempt(RecordAttemptParams{ID: early.ID, Gen: early.Gen, FailureReason: "Playback ended too early."})
	mgr.ResolvePendingAttempt(RecordAttemptParams{ID: early.ID, Gen: early.Gen, FailureReason: "generic"})

	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	got := map[string]NZBAttempt{}
	for _, a := range list {
		got[a.SlotPath] = a
	}
	if a, ok := got["slot-cleared"]; !ok || a.Preload || !a.Success {
		t.Fatalf("cleared row's verdict = %+v, want a new success row", a)
	}
	if a := got["slot-early"]; a.Preload || a.FailureReason != "Playback ended too early." {
		t.Fatalf("early-stop row = %+v, want its own reason kept", a)
	}
}
