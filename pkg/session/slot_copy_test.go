package session

import (
	"testing"
	"time"

	"streamnzb/pkg/release"
)

func TestRetireSlotCopyActsOnceAndOnlyOnTheCurrentSession(t *testing.T) {
	m := NewManager(nil, time.Minute)
	t.Cleanup(m.Shutdown)
	const slot = "stream:s:movie:tt1:0"
	create := func(link string) *Session {
		t.Helper()
		sess, _, err := m.CreateDeferredSessionWithFetcherOutcome(slot, link, &release.Release{Title: "Movie-GRP", Link: link},
			nil, nil, "movie", "tt1", "Movie", "s", nil, nil)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		return sess
	}
	m.NoteSlotCopies(slot, 3)
	first := create("https://a.invalid/0")

	// A background caller leaves a session alone while a play is starting on it.
	startup := m.BeginSessionStartup(first)
	if got := m.RetireSlotCopy(first, 3, true, nil); got != SlotCopyBusy {
		t.Fatalf("busy session: got %d, want SlotCopyBusy", got)
	}
	if m.PeekSession(slot) != first || m.SlotCopyIndex(slot) != 0 {
		t.Fatal("a busy session must keep its slot and cursor")
	}
	startup.Release()

	reports := 0
	got := m.RetireSlotCopy(first, 3, false, func() {
		reports++
		if first.Release() == nil {
			t.Fatal("report ran after the session was closed; the verdict would have no release")
		}
	})
	if got != SlotCopyRetired || reports != 1 {
		t.Fatalf("first report: got %d reports=%d, want SlotCopyRetired 1", got, reports)
	}
	if m.PeekSession(slot) != nil || m.SlotCopyIndex(slot) != 1 {
		t.Fatal("expected the failed copy's session out of the slot and the cursor on copy 1")
	}

	if got := m.RetireSlotCopy(first, 3, false, func() { reports++ }); got != SlotCopyAlreadyRetired || reports != 1 {
		t.Fatalf("duplicate report: got %d reports=%d, want SlotCopyAlreadyRetired 1", got, reports)
	}
	if m.SlotCopyIndex(slot) != 1 {
		t.Fatal("a duplicate report moved the cursor")
	}

	// A session that leaves the slot without a copy verdict is gone, not retired.
	evicted := create("https://b.invalid/1")
	m.DeleteSession(slot)
	if got := m.RetireSlotCopy(evicted, 3, false, func() { reports++ }); got != SlotCopyGone || reports != 1 {
		t.Fatalf("evicted session: got %d reports=%d, want SlotCopyGone 1", got, reports)
	}
	if m.SlotCopyIndex(slot) != 1 {
		t.Fatal("a report about an evicted session moved the cursor")
	}

	last := create("https://b.invalid/1")
	if got := m.RetireSlotCopy(last, 2, false, func() { reports++ }); got != SlotCopyLast || reports != 1 {
		t.Fatalf("last copy: got %d reports=%d, want SlotCopyLast 1", got, reports)
	}
	if m.PeekSession(slot) != last {
		t.Fatal("with no copy left the slot must be left to the slot-level failure path, session intact")
	}
}

// A play request registers its startup on the session object it holds, and
// learns in the same step whether the slot still holds that session — so a
// background retirement either sees the startup or has already happened.
func TestBeginSessionStartupIsBoundToTheSessionItHolds(t *testing.T) {
	m := NewManager(nil, time.Minute)
	t.Cleanup(m.Shutdown)
	const slot = "stream:s:movie:tt1:0"
	create := func(link string) *Session {
		t.Helper()
		sess, _, err := m.CreateDeferredSessionWithFetcherOutcome(slot, link, &release.Release{Title: "Movie-GRP", Link: link},
			nil, nil, "movie", "tt1", "Movie", "s", nil, nil)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		return sess
	}
	// Three copies, so the replacement below still has one to move to and
	// RetireSlotCopy reaches its idle check instead of answering SlotCopyLast.
	m.NoteSlotCopies(slot, 3)
	held := create("https://a.invalid/0")

	startup := m.BeginSessionStartup(held)
	if startup == nil {
		t.Fatal("the slot holds this session; the startup should register")
	}
	if got := m.RetireSlotCopy(held, 3, true, nil); got != SlotCopyBusy {
		t.Fatalf("a registered startup must make an idle-only retirement back off, got %d", got)
	}
	startup.Release()

	if got := m.RetireSlotCopy(held, 3, true, nil); got != SlotCopyRetired {
		t.Fatalf("an idle session should be retired, got %d", got)
	}
	if m.BeginSessionStartup(held) != nil {
		t.Fatal("a retired session must not register a startup")
	}
	rebuilt := create("https://b.invalid/1")
	if m.BeginSessionStartup(held) != nil {
		t.Fatal("a startup on the retired session must not land on the session that replaced it")
	}
	if got := m.RetireSlotCopy(rebuilt, 3, true, nil); got != SlotCopyRetired {
		t.Fatalf("the replacement should still be idle (no stray startup counted on it), got %d", got)
	}
}

// With no copy left to move to — always, with copy walking off — the failure
// is about the release, and the caller's slot-level verdict must run even
// when a play holds the session. Answering SlotCopyBusy there made the
// preload record a copy-only verdict on a release that had no other copy.
func TestNoCopyLeftIsReportedEvenForABusySession(t *testing.T) {
	m := NewManager(nil, time.Minute)
	t.Cleanup(m.Shutdown)
	const slot = "stream:s:movie:tt1:0"
	sess, _, err := m.CreateDeferredSessionWithFetcherOutcome(slot, "https://a.invalid/0", &release.Release{Title: "M"},
		nil, nil, "movie", "tt1", "M", "s", nil, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	m.NoteSlotCopies(slot, 3)

	for _, tc := range []struct {
		name string
		prep func()
	}{
		{"idle", func() {}},
		{"starting", func() { m.BeginSessionStartup(sess) }},
		{"validated", func() { m.MarkPlaybackValidated(sess) }},
	} {
		tc.prep()
		if got := m.RetireSlotCopy(sess, 1, true, nil); got != SlotCopyLast {
			t.Fatalf("%s, copy walking off: got %d, want SlotCopyLast", tc.name, got)
		}
		if m.PeekSession(slot) != sess || m.SlotCopyIndex(slot) != 0 {
			t.Fatalf("%s: SlotCopyLast must leave the session and cursor alone", tc.name)
		}
	}
}

// A session that has played is not idle for a background verdict even after
// every hold is gone: a preload probe has no business closing it.
func TestValidatedSessionIsNotIdleForABackgroundVerdict(t *testing.T) {
	m := NewManager(nil, time.Minute)
	t.Cleanup(m.Shutdown)
	const slot = "stream:s:movie:tt1:0"
	sess, _, err := m.CreateDeferredSessionWithFetcherOutcome(slot, "https://a.invalid/0", &release.Release{Title: "M"},
		nil, nil, "movie", "tt1", "M", "s", nil, nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	m.NoteSlotCopies(slot, 2)

	startup := m.BeginSessionStartup(sess)
	if startup == nil {
		t.Fatal("startup should register")
	}
	m.MarkPlaybackValidated(sess)
	startup.Release()
	if got := m.RetireSlotCopy(sess, 2, true, nil); got != SlotCopyBusy {
		t.Fatalf("a validated session must not be closed by a background verdict, got %d", got)
	}
	// A request's own failure report is not a background verdict.
	if got := m.RetireSlotCopy(sess, 2, false, nil); got != SlotCopyRetired {
		t.Fatalf("a failure report about the validated session should still retire it, got %d", got)
	}
}

// A failure on a session that left its slot fails the slot only while nothing
// replaced it; a failure on the slot's current session always does.
func TestFailSlotUnlessReplacedSparesAReplacement(t *testing.T) {
	m := NewManager(nil, time.Minute)
	t.Cleanup(m.Shutdown)
	const slot = "stream:s:movie:tt1:0"
	create := func() *Session {
		t.Helper()
		sess, _, err := m.CreateDeferredSessionWithFetcherOutcome(slot, "https://a.invalid/0", &release.Release{Title: "Movie-GRP", Link: "https://a.invalid/0"},
			nil, nil, "movie", "tt1", "Movie", "s", nil, nil)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		return sess
	}

	old := create()
	m.DeleteSession(slot)
	replacement := create()
	if m.FailSlotUnlessReplaced(old) || m.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("a failure on a replaced session failed the replacement's slot")
	}
	if m.PeekSession(slot) != replacement {
		t.Fatal("the replacement must keep its slot")
	}

	if !m.FailSlotUnlessReplaced(replacement) || !m.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("a failure on the slot's current session should fail the slot")
	}

	m2 := NewManager(nil, time.Minute)
	t.Cleanup(m2.Shutdown)
	m = m2
	gone := create()
	m.DeleteSession(slot)
	if !m.FailSlotUnlessReplaced(gone) || !m.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("a failure on a session whose slot is empty should fail the slot")
	}
	if m.FailSlotUnlessReplaced(nil) {
		t.Fatal("a nil session has no slot to fail")
	}
}
