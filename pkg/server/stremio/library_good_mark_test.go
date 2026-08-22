package stremio

import (
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/session"
)

// TestCommitGoodMarksLibraryItemGoodForDirectPlay reproduces the reported bug:
// a pending library item played via Direct Play reached the good threshold but
// stayed pending. Direct-play sessions carry no Release (only ContentTitle), so
// the Release-gated library save silently skipped the status update; the
// title-based good-mark must flip the stored row.
func TestCommitGoodMarksLibraryItemGoodForDirectPlay(t *testing.T) {
	logger.Init("ERROR")
	tempDir, err := os.MkdirTemp("", "goodmark_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	mgr, err := persistence.GetManager(tempDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	defer mgr.Close()

	const title = "Avengers.Endgame.2019.NORDiC.1080p.BluRay.x265-TWA"
	if err := mgr.LibraryStore().StoreItem(&persistence.LibraryItem{
		ContentType: "movie", ContentID: "tt4154796", ReleaseTitle: title,
		NZBData: []byte("<nzb/>"),
	}); err != nil {
		t.Fatalf("store pending item: %v", err)
	}

	server := &Server{attemptRecorder: mgr}
	// Direct-play session shape: no Release, title only in ContentTitle.
	sess := &session.Session{ID: "direct-abc123", ContentTitle: title}
	sess.AddBytesRead(65 << 20)

	if committed := server.commitGoodAttemptIfQualified(sess, sess.ID, time.Now().Add(-21*time.Second)); !committed {
		t.Fatal("expected sustained direct play to commit success")
	}

	items, _, err := mgr.LibraryStore().GetFilteredItems("", "all", false, "all", 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: err=%v n=%d", err, len(items))
	}
	if items[0].Status != persistence.LibraryStatusGood {
		t.Fatalf("library item status = %q, want %q", items[0].Status, persistence.LibraryStatusGood)
	}
	if items[0].LastVerifiedAt.IsZero() {
		t.Fatal("good mark must refresh last_verified_at")
	}
}
