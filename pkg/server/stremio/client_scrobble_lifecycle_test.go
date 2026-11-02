package stremio

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/session"
)

// reportFrom sends one client report from a given device and address.
func (h *scrobbleHarness) reportFrom(device, address, playSession string, event ClientPlaybackEvent, position float64) {
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: "kitsu:114:1", Device: device, Address: address, PlaySession: playSession, Event: event, PositionSeconds: position})
}

// addEpisode opens and serves a live media session for another episode of the
// harness's series, returning its content id.
func (h *scrobbleHarness) addEpisode(t *testing.T, episode int) string {
	t.Helper()
	contentID := fmt.Sprintf("kitsu:114:%d", episode)
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<b>", Bytes: 10}}}}}
	meta := &session.AvailReportMeta{KitsuID: "114", ImdbID: "tt0426711", TmdbID: "30982", TvdbID: "72905", Season: 1, Episode: episode}
	sess, err := h.server.sessionManager.CreateSession("stream:Test:anime:"+contentID+":0", data, nil, meta)
	if err != nil {
		t.Fatal(err)
	}
	sess.ContentType, sess.ContentID = "anime", contentID
	sess.SetMediaCapabilities(&session.MediaCapabilities{DurationSeconds: 1440})
	h.server.clientPlayback.noteServed(h.stream, sess)
	return contentID
}

func jellyfinServeFromURL(device string) *PlayServeOptions {
	return &PlayServeOptions{ClientReportsPlayback: true, ClientDevice: device, ClientDeviceFromURL: true}
}

// The fallback for a client that does not report sends its start and its
// stop from timers of their own. When the serve qualifies as it ends, both
// fall due together; the stop must still land after the start, however slow
// the start's request.
func TestFallbackStartLandsBeforeItsStop(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.clientPlayback.timings.reportGrace = 100 * time.Millisecond
	h.mu.Lock()
	h.startLag = 300 * time.Millisecond
	h.mu.Unlock()
	h.sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: 500, TotalSize: 1000, Elapsed: time.Hour})
	rs := &reportingServe{device: "silent", address: "127.0.0.1", began: time.Now()}
	h.server.clientPlayback.register(h.stream, h.sess.ContentID, rs)
	h.server.scrobbleStart(h.stream, h.sess, rs)
	h.server.scrobbleStopIfIdle(h.stream, h.sess, rs)
	time.Sleep(h.server.clientPlayback.timings.reportGrace)
	sent := h.settle(t, 2)
	wantScrobble(t, sent[0], "start", 50)
	wantScrobble(t, sent[1], "stop", 50)
}

// Separate reports are ordered too: a resume reported while the stop before
// it is still on its way to a slow target must not overtake that stop, or
// the target shows the title stopped while it plays.
func TestResumeDoesNotOvertakeASlowStop(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.mu.Lock()
	h.stopLag = 300 * time.Millisecond
	h.mu.Unlock()
	h.report("dev|ps", ClientPlaybackStop, 432)
	h.report("dev|ps", ClientPlaybackStart, 432) // resumed at once
	sent := h.settle(t, 3)
	wantScrobble(t, sent[1], "stop", 30)
	wantScrobble(t, sent[2], "start", 30)
}

// Ordering is per stream and title: a slow stop for one title holds back
// nothing of another's.
func TestSlowSendHoldsBackNoOtherTitle(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	other := h.addEpisode(t, 2)
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.mu.Lock()
	h.stopLag = 500 * time.Millisecond
	h.mu.Unlock()
	h.report("dev|ps", ClientPlaybackStop, 432)
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: other, Device: "tv", Address: "127.0.0.1", PlaySession: "p2", Event: ClientPlaybackStart})
	sent := h.settle(t, 3)
	wantScrobble(t, sent[1], "start", 0)
	wantScrobble(t, sent[2], "stop", 30)
}

// A target answering with an error does not stall what follows it.
func TestFailingTargetDoesNotStallLaterSends(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.mu.Lock()
	h.status = http.StatusInternalServerError
	h.mu.Unlock()
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.report("dev|ps", ClientPlaybackStop, 432)
	h.report("dev|ps", ClientPlaybackStart, 432)
	sent := h.settle(t, 3)
	wantScrobble(t, sent[0], "start", 0)
	wantScrobble(t, sent[1], "stop", 30)
	wantScrobble(t, sent[2], "start", 30)
}

// fallbackSent runs one serve to a client and reports whether the transport
// fell back to scrobbling it: a start and a stop of its own on top of the
// before sends already made.
func (h *scrobbleHarness) fallbackSent(t *testing.T, before int, opts *PlayServeOptions) bool {
	t.Helper()
	h.serveRange(t, "bytes=0-", 60e6, 0, opts)
	time.Sleep(h.server.clientPlayback.timings.reportGrace)
	h.mu.Lock()
	n := len(h.sent)
	h.mu.Unlock()
	if n == before {
		h.settle(t, before)
		return false
	}
	starts, stops := countVerbs(h.settle(t, before+2)[before:])
	return starts == 1 && stops == 1
}

// Behind a reverse proxy every client has the proxy's address. A device that
// names itself in its reports is not the serve that named no device, so its
// reports leave that serve's fallback alone. A client that names no device
// either way is still matched by address.
func TestDevicelessServeBehindAProxy(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.clientPlayback.timings.reportGrace = 200 * time.Millisecond
	h.reportFrom("phone", "127.0.0.1", "ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	if !h.fallbackSent(t, 1, jellyfinServe("")) {
		t.Fatal("a device reporting from the proxy's address silenced a serve that named no device")
	}

	h.reportFrom("", "127.0.0.1", "anon", ClientPlaybackStart, 0)
	h.settle(t, 4)
	if h.fallbackSent(t, 4, jellyfinServe("")) {
		t.Fatal("a client naming no device, reporting from the serve's address, did not count")
	}
}

// The DeviceId in a stream URL names the device the URL was handed to, not
// necessarily the one fetching it: a cached or shared URL plays elsewhere.
// That device's reports count for the serve only from the serve's own
// address; a DeviceId the request sent itself counts from anywhere.
func TestStreamURLDeviceCountsOnlyFromItsAddress(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.clientPlayback.timings.reportGrace = 200 * time.Millisecond
	h.reportFrom("ipad", "10.0.0.5", "ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	if !h.fallbackSent(t, 1, jellyfinServeFromURL("ipad")) {
		t.Fatal("reports from the URL's device elsewhere silenced the device fetching it")
	}
	if h.fallbackSent(t, 3, jellyfinServe("ipad")) {
		t.Fatal("the device the request named itself was not matched to its reports")
	}

	h.reportFrom("tv", "127.0.0.1", "ps", ClientPlaybackStart, 0)
	h.settle(t, 4)
	if h.fallbackSent(t, 4, jellyfinServeFromURL("tv")) {
		t.Fatal("the URL's device reporting from the serve's address was not matched")
	}
}

// A report from before a serve began speaks for that serve only when it is
// the start of the playback the serve is about: the previous playback's stop
// is not. So a replay within a minute in a player that does not report still
// gets its fallback, whatever play session the earlier playback named.
func TestEarlierPlaybackDoesNotSilenceAReplay(t *testing.T) {
	for _, c := range []struct {
		name, device, playSession string
		opts                      *PlayServeOptions
	}{
		{"no device", "", "A", jellyfinServe("")},
		{"no device or play session", "", "", jellyfinServe("")},
		{"same device", "dev", "A", jellyfinServe("dev")},
		{"device from the URL", "dev", "", jellyfinServeFromURL("dev")},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newScrobbleHarness(t, harnessOpts{runtime: 10})
			h.server.clientPlayback.timings.reportGrace = 200 * time.Millisecond
			h.reportFrom(c.device, "127.0.0.1", c.playSession, ClientPlaybackStart, 0)
			h.reportFrom(c.device, "127.0.0.1", c.playSession, ClientPlaybackStop, 6)
			h.settle(t, 2)
			if !h.fallbackSent(t, 2, c.opts) {
				t.Fatal("the earlier playback's reports silenced the replay")
			}
		})
	}
}

// A replay by a client that does report is its own: its start, reported just
// ahead of the request or during it, keeps the transport out.
func TestReportingReplayKeepsTheTransportOut(t *testing.T) {
	for _, early := range []bool{true, false} {
		t.Run(fmt.Sprintf("start before the request %v", early), func(t *testing.T) {
			h := newScrobbleHarness(t, harnessOpts{runtime: 10})
			h.server.clientPlayback.timings.reportGrace = 200 * time.Millisecond
			h.report("dev|A", ClientPlaybackStart, 0)
			h.report("dev|A", ClientPlaybackStop, 6)
			h.settle(t, 2)
			if early {
				h.report("dev|B", ClientPlaybackStart, 0)
			} else {
				time.AfterFunc(200*time.Millisecond, func() { h.report("dev|B", ClientPlaybackStart, 0) })
			}
			h.serveRange(t, "bytes=0-", 60e6, 0, jellyfinServe("dev"))
			time.Sleep(h.server.clientPlayback.timings.reportGrace)
			if starts, stops := countVerbs(h.settle(t, 3)); starts != 2 || stops != 1 {
				t.Fatalf("got %d starts and %d stops, want the client's alone", starts, stops)
			}
		})
	}
}

// A serve that gives up before it streams — here a range the release cannot
// deliver — is still matched to its client's reports: the fallback deferred
// for it must not send a stop the reporting client never asked for, from
// bytes other serves delivered.
func TestServeEndingEarlyStillSeesItsClientsReports(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.clientPlayback.timings.reportGrace = 200 * time.Millisecond
	h.sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: 500, TotalSize: 1000, Elapsed: time.Hour})
	h.holeFrom = diagFileSize / 2
	h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize*3/4), 0, 0, jellyfinServe("dev"))
	h.report("dev|ps", ClientPlaybackStart, 0)
	time.Sleep(2 * h.server.clientPlayback.timings.reportGrace)
	wantScrobble(t, h.settle(t, 1)[0], "start", 0)
}

// A fallback that fell due survives its media session: what the title is
// scrobbled as is taken while the serve has it, not when the timer fires.
func TestFallbackOutlivesItsMediaSession(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.clientPlayback.timings.reportGrace = 300 * time.Millisecond
	h.serveRange(t, "bytes=0-", 60e6, 0, jellyfinServe("silent"))
	h.server.sessionManager.DeleteSession(h.sess.ID)
	time.Sleep(h.server.clientPlayback.timings.reportGrace)
	sent := h.settle(t, 2)
	if starts, stops := countVerbs(sent); starts != 1 || stops != 1 {
		t.Fatalf("fallback after eviction: %+v", sent)
	}
	for _, s := range sent {
		show, _ := s.body["show"].(map[string]any)
		if ids, _ := show["ids"].(map[string]any); ids["tvdb"] != float64(72905) {
			t.Fatalf("%s addressed as %v", s.verb, s.body)
		}
	}
}

// What a title is scrobbled as is replaced only by something a target can
// place: a session closing, or one opened with no ids, leaves the kept item
// alone, so the final stop still goes out.
func TestKeptItemIsNotReplacedByAnUnusableOne(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.sess.Close() // closing: still found, but its ids are gone
	h.report("dev|ps", ClientPlaybackProgress, 600)

	// A failover onto a session that has no ids.
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<c>", Bytes: 10}}}}}
	bare, err := h.server.sessionManager.CreateSession("stream:Test:anime:kitsu:114:1:1", data, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bare.ContentType, bare.ContentID = "anime", "kitsu:114:1"
	h.server.clientPlayback.noteServed(h.stream, bare)
	h.report("dev|ps", ClientPlaybackProgress, 1200)
	h.server.sessionManager.DeleteSession(bare.ID)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	sent := h.settle(t, 2)
	wantScrobble(t, sent[1], "stop", 95)
	show, _ := sent[1].body["show"].(map[string]any)
	if ids, _ := show["ids"].(map[string]any); ids["tvdb"] != float64(72905) {
		t.Fatalf("stop addressed as %v", sent[1].body)
	}
}

// A live session that a target can place does replace the kept item: ids
// refreshed under the same title are the ones scrobbled.
func TestRefreshedItemReplacesTheKeptOne(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<d>", Bytes: 10}}}}}
	meta := &session.AvailReportMeta{KitsuID: "114", ImdbID: "tt0426711", TmdbID: "30982", TvdbID: "99999", Season: 1, Episode: 1}
	fresh, err := h.server.sessionManager.CreateSession("stream:Test:anime:kitsu:114:1:1", data, nil, meta)
	if err != nil {
		t.Fatal(err)
	}
	fresh.ContentType, fresh.ContentID = "anime", "kitsu:114:1"
	fresh.SetMediaCapabilities(&session.MediaCapabilities{DurationSeconds: 1440})
	h.server.clientPlayback.noteServed(h.stream, fresh)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	sent := h.settle(t, 2)
	show, _ := sent[1].body["show"].(map[string]any)
	if ids, _ := show["ids"].(map[string]any); ids["tvdb"] != float64(99999) {
		t.Fatalf("stop addressed as %v, want the refreshed ids", sent[1].body)
	}
}

// Reports racing the session's teardown never leave the title unplaceable.
func TestReportsRacingTeardownKeepTheItem(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			h.report("dev|ps", ClientPlaybackProgress, float64(i))
		}
	}()
	go func() { defer wg.Done(); h.server.sessionManager.DeleteSession(h.sess.ID) }()
	wg.Wait()
	h.report("dev|ps", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 2)[1], "stop", 95)
}

// Titles nothing is playing or serving are reclaimed to make room, however
// recently they were served; a title still playing is not.
func TestIdleTitlesMakeRoomForNewOnes(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.server.clientPlayback.timings.limit = 2
	h.report("dev|ps", ClientPlaybackStart, 0) // the harness's title, playing
	h.settle(t, 1)
	h.addEpisode(t, 2) // served, then nothing
	third := h.addEpisode(t, 3)
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: third, Device: "tv", Event: ClientPlaybackStart})
	h.settle(t, 2)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 3)[2], "stop", 95)
}

// A title keeps each client's last report only as long as a serve beginning
// now could be about it; reports older than that are dropped as the title is
// next touched, so devices that came and went leave nothing behind.
func TestStaleReportsAreTrimmed(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	for _, device := range []string{"a", "b", "c"} {
		h.report(device+"|ps", ClientPlaybackStart, 0)
	}
	h.settle(t, 3)
	key := clientPlaybackKey(h.stream, "kitsu:114:1")
	c := &h.server.clientPlayback
	c.mu.Lock()
	for k, m := range c.titles[key].reporters {
		m.at = m.at.Add(-2 * clientReportLead)
		c.titles[key].reporters[k] = m
	}
	c.mu.Unlock()
	h.report("d|ps", ClientPlaybackStart, 0)
	h.settle(t, 4)
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.titles[key].reporters); n != 1 {
		t.Fatalf("%d reports kept, want only the fresh one", n)
	}
}

// A Stremio serve is no business of client playback tracking.
func TestStremioServesTrackNothing(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 60})
	h.server.clientPlayback.mu.Lock()
	h.server.clientPlayback.titles = nil
	h.server.clientPlayback.mu.Unlock()
	h.serveRange(t, "bytes=0-", 60e6, 0, nil)
	h.settle(t, 2)
	h.server.clientPlayback.mu.Lock()
	defer h.server.clientPlayback.mu.Unlock()
	if len(h.server.clientPlayback.titles) != 0 {
		t.Fatalf("a Stremio serve left %d title entries", len(h.server.clientPlayback.titles))
	}
}

// Shutdown cancels what is scheduled rather than leaving it to fire: nothing
// waits on a fallback's grace, and a report after it arms nothing.
func TestShutdownCancelsScheduledScrobbles(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.clientPlayback.timings.reportGrace = 5 * time.Second
	h.sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: 500, TotalSize: 1000, Elapsed: time.Hour})
	rs := &reportingServe{device: "silent", address: "127.0.0.1", began: time.Now()}
	h.server.clientPlayback.register(h.stream, h.sess.ContentID, rs)
	h.server.scrobbleStart(h.stream, h.sess, rs)
	h.server.scrobbleStopIfIdle(h.stream, h.sess, rs)
	h.server.Shutdown()

	done := make(chan struct{})
	go func() { h.server.scrobbleSends.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending fallbacks outlived Shutdown")
	}
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.server.clientPlayback.mu.Lock()
	tracked := len(h.server.clientPlayback.attempts)
	h.server.clientPlayback.mu.Unlock()
	if tracked != 0 {
		t.Fatal("a report after Shutdown was tracked")
	}
	h.settle(t, 0)
}
