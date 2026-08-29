package stremio

import (
	"net/http/httptest"
	"testing"
	"time"

	"streamnzb/pkg/session"
)

func newPastEOFTestServer(t *testing.T) (*Server, *resolvedPlayback) {
	t.Helper()
	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{sessionManager: manager}
	// Not a slot path on purpose: the threshold request tries to derive the
	// next slot, and a bare test server has no playlist to answer from — an
	// unparsable id keeps that path on its no-next-slot branch.
	const sessionID = "test-slot:1:6:0"
	resolved := &resolvedPlayback{
		session:   &session.Session{ID: sessionID},
		sessionID: sessionID,
		size:      4_657_079_476,
	}
	return server, resolved
}

// The loop this exists for: a virtual file smaller than its container header
// declares sends the player to a tail offset past the served size, ServeContent
// answers 416 without reading a byte, and nothing ever marked the slot failed.
// Three strikes must fail the slot so the next reconnect lands on the next
// candidate.
func TestRepeatedPastEOFRangesFailTheSlotOver(t *testing.T) {
	server, resolved := newPastEOFTestServer(t)
	closes := 0
	closeStream := func(string) { closes++ }

	for i := 1; i <= maxPastEOFRangesBeforeFailover; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/play/"+resolved.sessionID, nil)
		answered := server.failSlotOnRepeatedPastEOFRanges(w, r, nil, resolved, "bytes=4665631465-", closeStream)

		if i < maxPastEOFRangesBeforeFailover {
			if answered {
				t.Fatalf("request %d answered early; the threshold is %d", i, maxPastEOFRangesBeforeFailover)
			}
			if server.sessionManager.GetSlotFailedDuringPlayback(resolved.sessionID) {
				t.Fatalf("slot marked failed after %d requests; the threshold is %d", i, maxPastEOFRangesBeforeFailover)
			}
			continue
		}
		if !answered {
			t.Fatal("the threshold request must be answered instead of serving another 416")
		}
	}
	if !server.sessionManager.GetSlotFailedDuringPlayback(resolved.sessionID) {
		t.Fatal("the slot must be marked failed so resolvePlaybackSlot steps over it")
	}
	if closes != 1 {
		t.Fatalf("closeStream called %d times, want once on the threshold request", closes)
	}
}

func TestPastEOFRangesNeverFailACommittedSlot(t *testing.T) {
	server, resolved := newPastEOFTestServer(t)
	if !resolved.session.Once(onceSuccessRecorded) {
		t.Fatal("failed to mark the session committed")
	}

	for i := 0; i < maxPastEOFRangesBeforeFailover*2; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/play/"+resolved.sessionID, nil)
		if server.failSlotOnRepeatedPastEOFRanges(w, r, nil, resolved, "bytes=4665631465-", func(string) {}) {
			t.Fatal("a committed slot must never be answered by the past-EOF backstop")
		}
	}
	if server.sessionManager.GetSlotFailedDuringPlayback(resolved.sessionID) {
		t.Fatal("a committed slot must never be marked failed by stale-metadata requests")
	}
}

func TestOnlyExplicitPastEOFStartsCount(t *testing.T) {
	server, resolved := newPastEOFTestServer(t)

	for name, rangeHeader := range map[string]string{
		"in range":    "bytes=1024-",
		"last byte":   "bytes=4657079475-",
		"no range":    "",
		"suffix":      "bytes=-500",
		"multi range": "bytes=0-99,200-299",
	} {
		for i := 0; i < maxPastEOFRangesBeforeFailover*2; i++ {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/play/"+resolved.sessionID, nil)
			if server.failSlotOnRepeatedPastEOFRanges(w, r, nil, resolved, rangeHeader, func(string) {}) {
				t.Fatalf("%s: range %q was answered by the past-EOF backstop", name, rangeHeader)
			}
		}
	}
	if server.sessionManager.GetSlotFailedDuringPlayback(resolved.sessionID) {
		t.Fatal("servable ranges must not accumulate toward the past-EOF threshold")
	}
}

func TestSessionCountEventTalliesPerKey(t *testing.T) {
	sess := &session.Session{}
	for want := 1; want <= 3; want++ {
		if got := sess.CountEvent(countPastEOFRanges); got != want {
			t.Fatalf("CountEvent = %d, want %d", got, want)
		}
	}
	if got := sess.CountEvent(session.OnceKey("other")); got != 1 {
		t.Fatalf("a second key started at %d, want its own count of 1", got)
	}
	var nilSess *session.Session
	if got := nilSess.CountEvent(countPastEOFRanges); got != 0 {
		t.Fatalf("nil session CountEvent = %d, want 0", got)
	}
}
