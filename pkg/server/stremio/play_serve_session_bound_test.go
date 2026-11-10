package stremio

import (
	"testing"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/session"
)

// A serve keeps its accounting on the session it was handed even when the slot
// has meanwhile been rebuilt under the same id (a copy retry, an eviction):
// its start, validation, bytes and end all land on that session, and the
// replacement's own playback is left exactly as it was.
func TestServeAccountsOnlyToItsOwnSessionAfterSlotReplacement(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{off: true, unlinked: true})
	manager := h.server.sessionManager
	served := h.sess

	manager.DeleteSession(served.ID)
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<b>", Bytes: 10}}}}}
	replacement, err := manager.CreateSession(served.ID, data, nil, &session.AvailReportMeta{})
	if err != nil {
		t.Fatal(err)
	}
	manager.StartPlayback(replacement, "10.0.0.2")

	h.serveRange(t, "bytes=0-", 0, 0, nil)

	if got := replacement.CurrentActivePlays(); got != 1 {
		t.Fatalf("replacement plays=%d after the old serve, want its own 1", got)
	}
	if replacement.IsPlaybackValidated() {
		t.Fatal("the old serve validated the replacement")
	}
	if got := replacement.BytesRead(); got != 0 {
		t.Fatalf("replacement bytes=%d, want none of the old serve's", got)
	}
	if got := served.CurrentActivePlays(); got != 0 {
		t.Fatalf("served session plays=%d after its serve ended, want 0", got)
	}
	if served.BytesRead() == 0 {
		t.Fatal("the serve's bytes were not counted on its own session")
	}
}
