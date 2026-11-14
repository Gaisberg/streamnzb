package session

import (
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
)

// newSlotSession puts a fresh deferred session behind id, the way a play list
// slot is built.
func newSlotSession(t *testing.T, m *Manager, id, url string) *Session {
	t.Helper()
	sess, err := m.CreateDeferredSession(id, url, nil, nil, nil, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// replaceSlot takes the slot's session out (closing it, as a copy retry or an
// eviction does) and builds a new one behind the same id.
func replaceSlot(t *testing.T, m *Manager, id string) *Session {
	t.Helper()
	m.DeleteSession(id)
	return newSlotSession(t, m, id, "https://indexer/next.nzb")
}

func newAccountingManager(t *testing.T) *Manager {
	t.Helper()
	logger.Init("ERROR")
	m := NewManager(nil, time.Minute)
	t.Cleanup(m.Shutdown)
	return m
}

func playsAndEnded(s *Session) (int32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ActivePlays, !s.PlaybackEndedAt.IsZero()
}

// A serve that started on a session the slot has since replaced ends on that
// session, not on the replacement playing under the same id.
func TestStaleEndPlaybackLeavesTheReplacementPlaying(t *testing.T) {
	m := newAccountingManager(t)
	a := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	m.StartPlayback(a, "1.1.1.1")
	b := replaceSlot(t, m, "x")
	m.StartPlayback(b, "2.2.2.2")

	m.EndPlayback(a, "1.1.1.1")

	if plays, ended := playsAndEnded(b); plays != 1 || ended {
		t.Fatalf("replacement: plays=%d ended=%v, want 1 and still playing", plays, ended)
	}
	if plays, _ := playsAndEnded(a); plays != 0 {
		t.Fatalf("old session: plays=%d, want its own start ended", plays)
	}
}

// A validation that finishes after its session was replaced must not let the
// replacement skip its own ffprobe validation.
func TestLateValidationStaysOnItsOwnSession(t *testing.T) {
	m := newAccountingManager(t)
	a := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	b := replaceSlot(t, m, "x")

	m.MarkPlaybackValidated(a)

	if b.IsPlaybackValidated() {
		t.Fatal("the replacement was validated by the old session's probe")
	}
	if !a.IsPlaybackValidated() {
		t.Fatal("the validated session itself was not marked")
	}
}

// A request that reaches StartPlayback after its session was replaced counts
// its play on the session it holds, closed or not.
func TestLateStartPlaybackStaysOnItsOwnSession(t *testing.T) {
	m := newAccountingManager(t)
	a := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	b := replaceSlot(t, m, "x") // closes a, which drops its clients map

	m.StartPlayback(a, "1.1.1.1")

	b.mu.Lock()
	plays, clients := b.ActivePlays, len(b.clients)
	b.mu.Unlock()
	if plays != 0 || clients != 0 {
		t.Fatalf("replacement: plays=%d clients=%d, want untouched", plays, clients)
	}
	if got := a.CurrentActivePlays(); got != 1 {
		t.Fatalf("old session: plays=%d, want 1", got)
	}
	m.KeepAlive(a, "1.1.1.1")
	m.EndPlayback(a, "1.1.1.1")
	if got := a.CurrentActivePlays(); got != 0 {
		t.Fatalf("old session after its end: plays=%d, want 0", got)
	}
}

// The consequence that matters on main: a stale end zeroed the replacement's
// count, and once its client entries went stale (a pause) cleanup evicted a
// session that still had a serve open.
func TestStaleEndCannotGetAPlayingReplacementEvicted(t *testing.T) {
	m := newAccountingManager(t)
	a := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	m.StartPlayback(a, "1.1.1.1")
	b := replaceSlot(t, m, "x")
	m.StartPlayback(b, "2.2.2.2")
	m.EndPlayback(a, "1.1.1.1")

	// Time passes with b paused: client entries go stale, and any recorded
	// playback end falls outside the post-playback TTL.
	b.mu.Lock()
	for ip := range b.clients {
		b.clients[ip] = time.Now().Add(-2 * clientStaleTTL)
	}
	if !b.PlaybackEndedAt.IsZero() {
		b.PlaybackEndedAt = b.PlaybackEndedAt.Add(-5 * time.Hour)
	}
	b.mu.Unlock()
	m.cleanup()

	if m.PeekSession("x") != b {
		t.Fatal("cleanup evicted the replacement while its own serve is open")
	}
}

// Stale and live requests interleave freely; whatever the order, the old
// session's requests never reach the replacement and both counts balance.
func TestConcurrentStaleAndLiveAccountingStaySeparate(t *testing.T) {
	m := newAccountingManager(t)
	a := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	b := replaceSlot(t, m, "x")
	m.StartPlayback(b, "2.2.2.2") // the replacement's long-running serve

	const n = 50
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-gate
			m.MarkPlaybackValidated(a)
			m.StartPlayback(a, "1.1.1.1")
			m.KeepAlive(a, "1.1.1.1")
			a.AddBytesRead(1)
			m.EndPlayback(a, "1.1.1.1")
		}()
		go func() {
			defer wg.Done()
			<-gate
			m.StartPlayback(b, "3.3.3.3")
			m.KeepAlive(b, "3.3.3.3")
			m.EndPlayback(b, "3.3.3.3")
		}()
	}
	close(gate)
	wg.Wait()

	if plays, ended := playsAndEnded(b); plays != 1 || ended {
		t.Fatalf("replacement: plays=%d ended=%v, want its long serve still counted", plays, ended)
	}
	if b.IsPlaybackValidated() || b.BytesRead() != 0 {
		t.Fatalf("replacement: validated=%v bytes=%d, want neither from the old session", b.IsPlaybackValidated(), b.BytesRead())
	}
	if plays, _ := playsAndEnded(a); plays != 0 {
		t.Fatalf("old session: plays=%d, want balanced", plays)
	}
	if m.PeekSession("x") != b {
		t.Fatal("the replacement left the slot")
	}
}

// The ordinary case is unchanged: validate, start, end on the current session.
func TestPlaybackLifecycleOnTheCurrentSession(t *testing.T) {
	m := newAccountingManager(t)
	s := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	startup := m.BeginSessionStartup(s)
	if startup == nil {
		t.Fatal("startup should register on the current session")
	}

	m.MarkPlaybackValidated(s)
	m.StartPlayback(s, "1.1.1.1")
	s.mu.Lock()
	started, starting := !s.PlaybackStartedAt.IsZero(), s.playbackStarting
	s.mu.Unlock()
	// The startup is the request's own until it hands over: validation and
	// the start leave it held, so the session is never unheld in between.
	if !s.IsPlaybackValidated() || !started || starting != 1 || s.CurrentActivePlays() != 1 {
		t.Fatalf("after start: validated=%v started=%v starting=%d plays=%d",
			s.IsPlaybackValidated(), started, starting, s.CurrentActivePlays())
	}
	startup.Release()
	s.mu.Lock()
	starting = s.playbackStarting
	s.mu.Unlock()
	if starting != 0 {
		t.Fatalf("after the hand-over: starting=%d, want 0", starting)
	}

	m.EndPlayback(s, "1.1.1.1")
	if plays, ended := playsAndEnded(s); plays != 0 || !ended {
		t.Fatalf("after end: plays=%d ended=%v, want 0 and ended", plays, ended)
	}
}

// Two serves of one session count separately: the first to finish leaves the
// session playing for the other.
func TestOverlappingServesOfOneSessionCountSeparately(t *testing.T) {
	m := newAccountingManager(t)
	s := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	m.StartPlayback(s, "1.1.1.1")
	m.StartPlayback(s, "1.1.1.1")

	m.EndPlayback(s, "1.1.1.1")
	if plays, ended := playsAndEnded(s); plays != 1 || ended {
		t.Fatalf("one of two ended: plays=%d ended=%v, want still playing", plays, ended)
	}
	m.EndPlayback(s, "1.1.1.1")
	if plays, ended := playsAndEnded(s); plays != 0 || !ended {
		t.Fatalf("both ended: plays=%d ended=%v", plays, ended)
	}
}

// An end with nothing to end (a cancelled startup never started a play) does
// not go negative, and a nil session is a no-op.
func TestEndPlaybackWithoutStartDoesNotGoNegative(t *testing.T) {
	m := newAccountingManager(t)
	s := newSlotSession(t, m, "x", "https://indexer/a.nzb")
	m.EndPlayback(s, "1.1.1.1")
	m.EndPlayback(s, "1.1.1.1")
	if got := s.CurrentActivePlays(); got != 0 {
		t.Fatalf("plays=%d, want 0", got)
	}
	m.StartPlayback(nil, "1.1.1.1")
	m.MarkPlaybackValidated(nil)
	m.KeepAlive(nil, "1.1.1.1")
	m.EndPlayback(nil, "1.1.1.1")
	var nilSess *Session
	nilSess.AddBytesRead(1)
}
