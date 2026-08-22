package api

import (
	"testing"
	"time"

	"streamnzb/pkg/session"
)

// startedServer builds a Server with the same background loops the constructors
// start, without the rest of the wiring. collectStats needs a session manager;
// its zero value answers with an empty list, which is all this needs.
func startedServer() *Server {
	s := &Server{
		sessionMgr: &session.Manager{},
		clients:    make(map[*Client]bool),
		logCh:      make(chan string, 100),
		stopCh:     make(chan struct{}),
	}
	s.bgDone.Add(2)
	go s.broadcastLogs()
	go s.collectStatsLoop()
	return s
}

// The stats loop persists provider and indexer metrics every thirty seconds and
// used to have no stop path at all, so it outlived gracefulShutdown — which
// closes the database last, specifically so nothing is still writing when it
// does. Shutdown has to mean "the loops have finished", not "the loops have
// been told to finish", or the sequencing in cmd/streamnzb/shutdown.go buys
// nothing.
//
// A loop that ignored stopCh would leave bgDone.Wait() blocked forever, so
// every call here goes through shutdownWithin: a regression has to fail with a
// message rather than park the suite until the panic timeout. Verified by
// removing the stop path and watching these fail.
func TestShutdownWaitsForTheBackgroundLoops(t *testing.T) {
	s := startedServer()
	waitFor(t, "the stats loop to publish", func() bool { return s.snapshotStats() != nil })
	shutdownWithin(t, s)
}

// The loop must actually stop publishing, not just stop being waited on. The
// ticker is one second, so this watches for longer than that: a loop still
// running would have refilled the snapshot by the time it gives up.
func TestStatsLoopStopsPublishingAfterShutdown(t *testing.T) {
	s := startedServer()
	waitFor(t, "the stats loop to publish", func() bool { return s.snapshotStats() != nil })

	shutdownWithin(t, s)

	s.latestStatsMu.Lock()
	s.latestStats = nil
	s.latestStatsMu.Unlock()

	time.Sleep(1500 * time.Millisecond)
	if s.snapshotStats() != nil {
		t.Fatal("the stats loop published after Shutdown returned")
	}
}

// The log broadcaster is the other loop, and the cheaper one to observe: after
// Shutdown its queue must stop draining.
func TestLogBroadcastStopsAfterShutdown(t *testing.T) {
	s := startedServer()
	shutdownWithin(t, s)

	s.logCh <- "after shutdown"

	time.Sleep(100 * time.Millisecond)
	if len(s.logCh) != 1 {
		t.Fatalf("the log broadcaster drained its queue after Shutdown: len = %d", len(s.logCh))
	}
}

// Shutdown runs on paths that may already have run it, and the package's own
// tests build bare Servers with no loops at all. Neither may panic or block.
func TestShutdownIsSafeWhenRepeatedOrUnstarted(t *testing.T) {
	(&Server{}).Shutdown()

	s := startedServer()
	for range 3 {
		shutdownWithin(t, s)
	}
}

// shutdownWithin calls Shutdown and fails if it does not return, rather than
// letting a stuck bgDone.Wait() hold the test binary open.
func shutdownWithin(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return: a background loop is ignoring stopCh")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
