package session

import (
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
)

func TestShutdownClosesLiveSessions(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
		stopCh:    make(chan struct{}),
	}
	nzbData := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 10}}}}}

	for _, id := range []string{"sess-1", "sess-2"} {
		if _, err := m.CreateSession(id, nzbData, nil, nil); err != nil {
			t.Fatalf("CreateSession(%s) returned error: %v", id, err)
		}
	}

	// A playback stream stands in for the NNTP connections a live session is
	// holding; shutdown has to reach it, not just drop the map.
	stream := &fakePlaybackStream{}
	sess, err := m.GetSession("sess-1")
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	sess.mu.Lock()
	state := sess.ensurePlaybackStateLocked()
	state.stream = stream
	sess.mu.Unlock()

	m.Shutdown()

	if !stream.closed {
		t.Fatal("the playback stream survived shutdown, so its connections were dropped rather than released")
	}

	m.mu.Lock()
	remaining := len(m.sessions)
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d sessions remain after shutdown", remaining)
	}

	select {
	case <-m.stopCh:
	default:
		t.Fatal("the cleanup loop was not signalled to stop")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
		stopCh:    make(chan struct{}),
	}

	// Shutdown can be reached from a signal and from a failed listener at the
	// same time. Closing stopCh twice would panic, so this is not cosmetic.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Shutdown()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Shutdown calls deadlocked")
	}
}
