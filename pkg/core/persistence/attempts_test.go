package persistence

import (
	"os"
	"testing"
	"time"
)

func newTestStateManager(t *testing.T) *StateManager {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "persistence-attempts-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		globalManager = nil
		_ = os.RemoveAll(tempDir)
	})
	globalManager = nil
	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}
	return mgr
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
	mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-final", ReleaseTitle: "Preloaded release"})

	mgr.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.RecordAttempt(RecordAttemptParams{SlotPath: "slot-final", ReleaseTitle: "Preloaded release", Success: true})
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
	mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-pack", ReleaseTitle: "Season pack"})

	wantServedFile := "Altered.Carbon.S02E03.1080p.mkv"
	mgr.RecordAttempt(RecordAttemptParams{
		SlotPath:     "slot-pack",
		ReleaseTitle: "Season pack",
		ServedFile:   wantServedFile,
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
	if list[0].Preload {
		t.Fatal("expected preload row to be resolved")
	}
}

func TestUpdatePendingAttemptKeepsPreloadAndAddsReason(t *testing.T) {
	mgr := newTestStateManager(t)
	mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-pending", ReleaseTitle: "Early stop"})

	mgr.UpdatePendingAttempt(RecordAttemptParams{
		SlotPath:      "slot-pending",
		ProviderName:  "news.example.net",
		ServedFile:    "Example.mkv",
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
