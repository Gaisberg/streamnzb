package stremio

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"streamnzb/pkg/services/metadata/scrobble"
)

// sentSoFar is what reached the fake, without waiting for more.
func (h *scrobbleHarness) sentSoFar() []sentScrobble {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sentScrobble(nil), h.sent...)
}

// A graceful shutdown sends what was already decided: a final stop queued
// behind a slow start still lands before Shutdown returns.
func TestGracefulShutdownSendsAcceptedStops(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.server.stopCh = make(chan struct{})
	h.mu.Lock()
	h.delay = 100 * time.Millisecond
	h.mu.Unlock()
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	h.server.Shutdown()
	sent := h.sentSoFar()
	if len(sent) != 2 {
		t.Fatalf("sent %+v before Shutdown returned, want the start and the final stop", sent)
	}
	wantScrobble(t, sent[1], "stop", 95)
}

// A serve whose connection is cut by the shutdown, to a client that never
// reported, has its fallback decided at once rather than dropped with its
// grace timer.
func TestShutdownSendsPendingFallbacks(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.stopCh = make(chan struct{})
	h.server.clientPlayback.timings.reportGrace = time.Hour
	h.serveRange(t, "bytes=0-", 60e6, 0, jellyfinServe("silent"))
	h.server.Shutdown()
	if starts, stops := countVerbs(h.sentSoFar()); starts != 1 || stops != 1 {
		t.Fatalf("sent %d starts and %d stops at shutdown, want the fallback's one of each", starts, stops)
	}
}

// The drain is bounded: a target that does not answer holds Shutdown for the
// drain and no longer, and nothing decided after it goes out.
func TestShutdownDrainIsBounded(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.server.stopCh = make(chan struct{})
	h.server.clientPlayback.timings.shutdownDrain = 200 * time.Millisecond
	h.mu.Lock()
	h.delay = 2 * time.Second
	h.mu.Unlock()
	h.report("dev|ps", ClientPlaybackStart, 0)
	began := time.Now()
	h.server.Shutdown()
	if waited := time.Since(began); waited > time.Second {
		t.Fatalf("Shutdown waited %v on a target that does not answer", waited)
	}
	item, _ := h.server.scrobbleItemForSession(h.sess)
	targets := scrobbleTargets(h.server.runtime(), h.stream)
	h.server.sendScrobble(targets, clientPlaybackKey(h.stream, h.sess.ContentID), h.sess.ContentID, scrobble.VerbStop, item, 95)
	h.settle(t, 1)
}

// hangingTarget is a target that answers nothing until released or until its
// request is cancelled.
type hangingTarget struct{ release chan struct{} }

func (hangingTarget) Name() string                { return "Simkl" }
func (hangingTarget) Supports(scrobble.Item) bool { return true }
func (h hangingTarget) Scrobble(ctx context.Context, _ string, _ scrobble.Item, _ float64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.release:
		return nil
	}
}

// Each target keeps its own order: Simkl hanging on a start holds back none
// of MDBList's sends, and MDBList's final stop lands at once.
func TestSlowTargetHoldsBackNoOtherTarget(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	hang := hangingTarget{release: make(chan struct{})}
	t.Cleanup(func() { close(hang.release) })
	targets := []scrobble.Target{hang, h.server.mdblistClients.For("Test")}
	item, _ := h.server.scrobbleItemForSession(h.sess)
	lane := clientPlaybackKey(h.stream, h.sess.ContentID)
	began := time.Now()
	h.server.sendScrobble(targets, lane, h.sess.ContentID, scrobble.VerbStart, item, 0)
	h.server.sendScrobble(targets, lane, h.sess.ContentID, scrobble.VerbStop, item, 95)
	sent := h.settle(t, 2)
	wantScrobble(t, sent[0], "start", 0)
	wantScrobble(t, sent[1], "stop", 95)
	if waited := time.Since(began); waited > 2*time.Second {
		t.Fatalf("MDBList's stop waited %v behind Simkl", waited)
	}
}

// A previous playback's stop, reported late, after a new serve began, is no
// proof that the new serve's player reports: a Jellyfin app that handed the
// replay to an external player keeps that serve's fallback, whether or not
// its reports named a play session.
func TestLateStopOfAnEarlierPlaybackKeepsTheFallback(t *testing.T) {
	for _, c := range []struct {
		name, playSession string
		opts              *PlayServeOptions
	}{
		{"play session, header device", "A", jellyfinServe("dev")},
		{"no play session, header device", "", jellyfinServe("dev")},
		{"play session, URL device", "A", jellyfinServeFromURL("dev")},
		{"no play session, URL device", "", jellyfinServeFromURL("dev")},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newScrobbleHarness(t, harnessOpts{runtime: 10})
			h.server.clientPlayback.timings.reportGrace = 200 * time.Millisecond
			h.reportFrom("dev", "127.0.0.1", c.playSession, ClientPlaybackStart, 0)
			h.reportFrom("dev", "127.0.0.1", c.playSession, ClientPlaybackProgress, 3)
			h.settle(t, 1)
			time.AfterFunc(100*time.Millisecond, func() {
				h.reportFrom("dev", "127.0.0.1", c.playSession, ClientPlaybackStop, 6)
			})
			h.serveRange(t, "bytes=0-", 60e6, 0, c.opts)
			time.Sleep(h.server.clientPlayback.timings.reportGrace)
			sent := h.settle(t, 4)
			if starts, stops := countVerbs(sent[1:]); starts != 1 || stops != 2 {
				t.Fatalf("after the earlier playback's late stop, sent %+v; want its stop and the replay's fallback start and stop", sent)
			}
		})
	}
}

// A report the full table cannot take changes nothing: the title kept for a
// stopped playback, whose media session has gone, still places that
// playback's resume and final stop.
func TestRejectedReportKeepsAnotherTitlesItem(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	c := &h.server.clientPlayback
	c.timings.limit = 2
	c.timings.stopGrace = 50 * time.Millisecond
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.report("dev|ps", ClientPlaybackStop, 720)
	h.settle(t, 2)
	time.Sleep(2 * c.timings.stopGrace) // the stopped playback is released
	h.server.sessionManager.DeleteSession(h.sess.ID)

	second := h.addEpisode(t, 2)
	for _, device := range []string{"tv", "tv3"} {
		h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: second, Device: device, PlaySession: "p", Event: ClientPlaybackStart})
	}
	h.settle(t, 4)
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: "kitsu:114:3", Device: "tv2", PlaySession: "p", Event: ClientPlaybackStart})
	h.settle(t, 4) // full: not tracked
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: second, Device: "tv3", PlaySession: "p", Event: ClientPlaybackStop})
	time.Sleep(2 * c.timings.stopGrace)

	h.report("dev|ps2", ClientPlaybackStart, 720) // resumed from the buffer
	h.report("dev|ps2", ClientPlaybackStop, 1368)
	sent := h.settle(t, 7) // after tv3's stop
	wantScrobble(t, sent[5], "start", 50)
	wantScrobble(t, sent[6], "stop", 95)
}

// A full lane does not take a start for the end of a stop before it: a stop
// at 50% leaves a resume position that later starts, none of them stopped,
// do not replace. What makes room is a start a later send restates.
func TestFullLaneKeepsAPartialStopBeforeUnmatchedStarts(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.mu.Lock()
	h.delay = 50 * time.Millisecond
	h.mu.Unlock()
	h.report("a|pa", ClientPlaybackStart, 0) // in flight
	h.report("a|pa", ClientPlaybackStop, 720)
	for i := range maxQueuedScrobbles {
		h.report(fmt.Sprintf("d%d|p", i), ClientPlaybackStart, 0)
	}
	h.server.scrobbleSends.Wait()
	sent := h.sentSoFar()
	if len(sent) > maxQueuedScrobbles+1 {
		t.Fatalf("all %d sends went out: the lane never filled", len(sent))
	}
	wantScrobble(t, sent[1], "stop", 50)
	wantScrobble(t, sent[len(sent)-1], "start", 0)
}

// Probes are not playback: a HEAD, a range the file cannot satisfy, or a
// player sampling the tail leaves no serve registered and arms no fallback.
// A real serve the client abandons early still arms its own.
func TestProbesArmNoFallback(t *testing.T) {
	for _, c := range []struct {
		name, method, rangeHeader string
		rate                      float64
		cancelAfter               time.Duration
		armed                     int
	}{
		{"HEAD", http.MethodHead, "bytes=0-", 0, 0, 0},
		{"unsatisfiable range", http.MethodGet, fmt.Sprintf("bytes=%d-", 2*diagFileSize), 0, 0, 0},
		{"tail probe", http.MethodGet, fmt.Sprintf("bytes=%d-", diagFileSize-64<<10), 0, 0, 0},
		{"abandoned playback", http.MethodGet, "bytes=0-", 20e6, 50 * time.Millisecond, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newScrobbleHarness(t, harnessOpts{})
			h.server.clientPlayback.timings.reportGrace = time.Hour
			h.serveMethod(t, c.method, c.rangeHeader, c.rate, c.cancelAfter, jellyfinServe("silent"))
			cp := &h.server.clientPlayback
			cp.mu.Lock()
			armed := len(cp.scheduled)
			registered := 0
			if title := cp.titles[clientPlaybackKey(h.stream, h.sess.ContentID)]; title != nil {
				registered = len(title.serves)
			}
			cp.mu.Unlock()
			if armed != c.armed || registered != c.armed {
				t.Fatalf("%d fallbacks armed and %d serves registered, want %d", armed, registered, c.armed)
			}
		})
	}
}
