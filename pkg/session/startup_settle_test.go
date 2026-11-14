package session

import (
	"fmt"
	"sync"
	"testing"

	"streamnzb/pkg/release"
)

// slotWithCopies builds a session behind slot whose release has copies copies.
func slotWithCopies(t *testing.T, m *Manager, slot string, copies int) *Session {
	t.Helper()
	link := "https://" + slot + ".invalid/nzb"
	sess, _, err := m.CreateDeferredSessionWithFetcherOutcome(slot, link, &release.Release{Title: "Movie-GRP", Link: link},
		nil, nil, "movie", "tt1", "Movie", "s", nil, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	m.NoteSlotCopies(slot, copies)
	return sess
}

func startupHolds(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playbackStarting
}

func isClosed(s *Session) bool {
	select {
	case <-s.Done():
		return true
	default:
		return false
	}
}

// Each request's startup is its own: one request validating the session must
// not clear the hold another request still has on it.
func TestValidationLeavesOtherRequestsStartupHolds(t *testing.T) {
	m := newAccountingManager(t)
	const slot = "stream:s:movie:tt1:0"
	sess := slotWithCopies(t, m, slot, 3)
	first, second := m.BeginSessionStartup(sess), m.BeginSessionStartup(sess)

	m.MarkPlaybackValidated(sess)
	if got := startupHolds(sess); got != 2 {
		t.Fatalf("validation left %d startup holds, want both requests' 2", got)
	}
	first.Release()
	if got := m.SettleFailedStartup(sess, 3, nil); got != StartupHeld {
		t.Fatalf("the second request still starts on the session, got %d", got)
	}
	second.Release()
	if got := m.SettleFailedStartup(sess, 3, nil); got != StartupCopyRetired {
		t.Fatalf("with every hold released the failure should retire the copy, got %d", got)
	}
}

// Every path that may end a startup can release it; only the first release
// counts, so a second one cannot take away another request's hold.
func TestStartupHoldReleasesOnlyOnce(t *testing.T) {
	m := newAccountingManager(t)
	sess := slotWithCopies(t, m, "stream:s:movie:tt1:0", 3)
	mine, theirs := m.BeginSessionStartup(sess), m.BeginSessionStartup(sess)

	mine.Release()
	mine.Release()
	if got := startupHolds(sess); got != 1 {
		t.Fatalf("a double release left %d holds, want the other request's 1", got)
	}
	theirs.Release()
	if got := startupHolds(sess); got != 0 {
		t.Fatalf("after every release: %d holds, want 0", got)
	}
	var none *StartupHold
	none.Release() // a startup that never registered has nothing to release
}

// A request serving the session, or starting on it, holds it: another
// request's failed startup changes nothing — not the slot, the cursor or the
// session — whether or not a copy is left to move to.
func TestFailedStartupLeavesAHeldSession(t *testing.T) {
	for _, hold := range []string{"serving", "starting"} {
		for _, attempts := range []int{3, 1} {
			t.Run(fmt.Sprintf("%s/attempts=%d", hold, attempts), func(t *testing.T) {
				m := newAccountingManager(t)
				const slot = "stream:s:movie:tt1:0"
				sess := slotWithCopies(t, m, slot, 3)
				if hold == "serving" {
					m.StartPlayback(sess, "1.1.1.1")
				} else {
					t.Cleanup(m.BeginSessionStartup(sess).Release)
				}

				reported := false
				if got := m.SettleFailedStartup(sess, attempts, func(bool) { reported = true }); got != StartupHeld {
					t.Fatalf("got %d, want StartupHeld", got)
				}
				if reported || isClosed(sess) || m.PeekSession(slot) != sess ||
					m.SlotCopyIndex(slot) != 0 || m.GetSlotFailedDuringPlayback(slot) {
					t.Fatal("a failure on a held session must change nothing")
				}
			})
		}
	}
}

// Nobody holds the session: the failure retires the copy while one is left,
// and fails the slot otherwise; the verdict is reported with the session out
// of the slot but not yet closed.
func TestFailedStartupRetiresOrFailsAnUnheldSession(t *testing.T) {
	m := newAccountingManager(t)
	const slot = "stream:s:movie:tt1:0"
	sess := slotWithCopies(t, m, slot, 2)
	// Played and released: having played once does not hold it.
	m.StartPlayback(sess, "1.1.1.1")
	m.MarkPlaybackValidated(sess)
	m.EndPlayback(sess, "1.1.1.1")

	var reports []bool
	report := func(s *Session) func(bool) {
		return func(nextCopy bool) {
			if isClosed(s) || m.PeekSession(slot) == s {
				t.Fatal("report must run with the session out of the slot and still open")
			}
			reports = append(reports, nextCopy)
		}
	}
	if got := m.SettleFailedStartup(sess, 2, report(sess)); got != StartupCopyRetired {
		t.Fatalf("got %d, want StartupCopyRetired", got)
	}
	if !isClosed(sess) || m.SlotCopyIndex(slot) != 1 || m.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("the copy should be retired, closed, and the cursor on the next copy")
	}
	if got := m.SettleFailedStartup(sess, 2, report(sess)); got != StartupAlreadyRetired {
		t.Fatalf("a duplicate failure: got %d, want StartupAlreadyRetired", got)
	}

	last := slotWithCopies(t, m, slot, 2)
	m.ExhaustSlotCopies(slot)
	if got := m.SettleFailedStartup(last, 2, report(last)); got != StartupSlotFailed {
		t.Fatalf("no copy left: got %d, want StartupSlotFailed", got)
	}
	if !isClosed(last) || m.PeekSession(slot) != nil || !m.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("with no copy left the slot should be failed and the session gone")
	}
	if len(reports) != 2 || !reports[0] || reports[1] {
		t.Fatalf("reports %v, want [copy-only, release]", reports)
	}
}

// A late failure from a session the slot has replaced is about nothing the
// slot holds now: the replacement keeps the slot, open, and the slot stays
// unfailed.
func TestFailedStartupSparesAReplacement(t *testing.T) {
	m := newAccountingManager(t)
	const slot = "stream:s:movie:tt1:0"
	stale := slotWithCopies(t, m, slot, 1)
	m.DeleteSession(slot)
	replacement := slotWithCopies(t, m, slot, 1)

	if got := m.SettleFailedStartup(stale, 1, func(bool) { t.Fatal("nothing to report") }); got != StartupGone {
		t.Fatalf("got %d, want StartupGone", got)
	}
	if m.PeekSession(slot) != replacement || isClosed(replacement) || m.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("a stale failure deleted or failed the replacement")
	}
}

// A startup registering while another request's failure is being settled
// lands on one side of it: it registers and the failure backs off, or the
// failure took the session out of the slot first and the startup is refused.
// Never both, never neither.
func TestFailedStartupRacingAStartupIsAtomic(t *testing.T) {
	m := newAccountingManager(t)
	for i := range 200 {
		slot := fmt.Sprintf("stream:s:movie:tt1:%d", i)
		sess := slotWithCopies(t, m, slot, 3)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var hold *StartupHold
		var settled StartupFailure
		wg.Add(2)
		go func() { defer wg.Done(); <-start; hold = m.BeginSessionStartup(sess) }()
		go func() { defer wg.Done(); <-start; settled = m.SettleFailedStartup(sess, 3, nil) }()
		close(start)
		wg.Wait()

		if (hold != nil) != (settled == StartupHeld) {
			t.Fatalf("round %d: startup registered=%v, failure settled as %d", i, hold != nil, settled)
		}
		if hold != nil && (isClosed(sess) || m.PeekSession(slot) != sess) {
			t.Fatalf("round %d: the session a startup holds was closed", i)
		}
		hold.Release()
	}
}
