package stremio

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/mdblist"
	"streamnzb/pkg/services/metadata/scrobble"
	"streamnzb/pkg/session"
)

// sentScrobble is one request the fake MDBList received.
type sentScrobble struct {
	verb     string
	progress float64
	body     map[string]any
}

type scrobbleHarness struct {
	server *Server
	stream *auth.Stream
	sess   *session.Session

	mu     sync.Mutex
	sent   []sentScrobble
	status int
	delay  time.Duration
	// stopLag holds each stop back before it lands, as a slower request would.
	stopLag time.Duration
}

type harnessOpts struct {
	off, unlinked bool
	noRuntime     bool
	// runtime overrides the episode's 24 minutes.
	runtime float64
}

// newScrobbleHarness is a stream with MDBList scrobbling on and an account
// linked, whose real client talks to a local fake, and a live media session
// for Hikaru no Go's first episode (kitsu:114, which anime-lists resolves to
// TVDB 72905) that the transport has served.
func newScrobbleHarness(t *testing.T, opts harnessOpts) *scrobbleHarness {
	t.Helper()
	h := &scrobbleHarness{status: http.StatusCreated}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		lag := h.stopLag
		h.mu.Unlock()
		if r.URL.Path == "/scrobble/stop" {
			time.Sleep(lag)
		}
		h.mu.Lock()
		status, delay := h.status, h.delay
		progress, _ := body["progress"].(float64)
		h.sent = append(h.sent, sentScrobble{verb: r.URL.Path, progress: progress, body: body})
		h.mu.Unlock()
		time.Sleep(delay)
		w.WriteHeader(status)
	}))
	t.Cleanup(fake.Close)
	t.Setenv("STREAMNZB_MDBLIST_BASE_URL", fake.URL)

	dir := t.TempDir()
	if !opts.unlinked {
		// The registry's own state key, written the way a completed device
		// flow leaves it.
		scrobble.NewStore[map[string]any](dir, "mdblist_tokens").Set("Test", map[string]any{
			"access_token": "token", "refresh_token": "refresh",
			"created_at":  time.Now().Format(time.RFC3339),
			"fingerprint": scrobble.CredentialFingerprint("client-id"),
		})
	}

	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 10}}}}}
	meta := &session.AvailReportMeta{KitsuID: "114", ImdbID: "tt0426711", TmdbID: "30982", TvdbID: "72905", Season: 1, Episode: 1}
	sess, err := manager.CreateSession("stream:Test:anime:kitsu:114:1:0", data, nil, meta)
	if err != nil {
		t.Fatal(err)
	}
	sess.ContentType, sess.ContentID = "anime", "kitsu:114:1"
	if !opts.noRuntime {
		sess.SetMediaCapabilities(&session.MediaCapabilities{DurationSeconds: cmp.Or(opts.runtime, 1440)})
	}

	h.server = &Server{sessionManager: manager, mdblistClients: mdblist.NewRegistry("client-id", dir)}
	h.server.availReporter = &availnzb.Reporter{MinBytesToReportGood: 1 << 20, MinDurationToReportGood: 300 * time.Millisecond, Disabled: true}
	// Sends log after they land; a later test re-initialising the logger
	// must not race them.
	t.Cleanup(h.server.scrobbleSends.Wait)
	// Nor may a playback's idle timer fire after the test.
	t.Cleanup(h.server.Shutdown)
	h.stream = &auth.Stream{Username: "Test", MDBListScrobble: !opts.off}
	if opts.unlinked {
		// Linked accounts live in a process-wide state store, so an unlinked
		// stream needs a name no other test linked.
		h.stream.Username = "Unlinked"
	}
	h.sess = sess
	h.server.clientPlayback.noteServed(h.stream, sess)
	return h
}

// report sends one client report; playback is "device|play session". It
// comes from the loopback address the harness's serves do.
func (h *scrobbleHarness) report(playback string, event ClientPlaybackEvent, position float64) {
	device, playSession, _ := strings.Cut(playback, "|")
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: "kitsu:114:1", Device: device, Address: "127.0.0.1", PlaySession: playSession, Event: event, PositionSeconds: position})
}

// settle waits for the background sends to land and returns what reached
// the fake, failing unless exactly want did.
func (h *scrobbleHarness) settle(t *testing.T, want int) []sentScrobble {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.sent)
		h.mu.Unlock()
		if n >= want || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // anything extra would have landed by now
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sent) != want {
		t.Fatalf("sent %d scrobbles, want %d: %+v", len(h.sent), want, h.sent)
	}
	return append([]sentScrobble(nil), h.sent...)
}

func wantScrobble(t *testing.T, got sentScrobble, verb string, progress float64) {
	t.Helper()
	if got.verb != "/scrobble/"+verb || got.progress != progress {
		t.Fatalf("got %s at %v, want /scrobble/%s at %v", got.verb, got.progress, verb, progress)
	}
}

func countVerbs(sent []sentScrobble) (starts, stops int) {
	for _, s := range sent {
		switch s.verb {
		case "/scrobble/start":
			starts++
		case "/scrobble/stop":
			stops++
		}
	}
	return starts, stops
}

func TestClientPlaybackStartAndStop(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	// Resumed at 119s of a 24-minute episode, stopped at 187s.
	h.report("dev|ps", ClientPlaybackStart, 119)
	sent := h.settle(t, 1)
	wantScrobble(t, sent[0], "start", 8.26)
	show, _ := sent[0].body["show"].(map[string]any)
	ids, _ := show["ids"].(map[string]any)
	if ids["tvdb"] != float64(72905) || ids["imdb"] != "tt0426711" || show["season"] != float64(1) || show["episode"] != float64(1) {
		t.Fatalf("anime episode addressed as %v", sent[0].body)
	}

	h.report("dev|ps", ClientPlaybackProgress, 127)
	h.report("dev|ps", ClientPlaybackProgress, 167)
	h.report("dev|ps", ClientPlaybackStop, 187)
	h.report("dev|ps", ClientPlaybackStop, 187) // a client repeating itself
	sent = h.settle(t, 2)
	wantScrobble(t, sent[1], "stop", 12.99)
}

func TestClientPlaybackWatchedThrough(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("a", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("a", ClientPlaybackStop, 1300)
	// Past MDBList's 80%, which is what marks it watched there.
	wantScrobble(t, h.settle(t, 2)[1], "stop", 90.28)
}

// Pausing reports the same position over and over; nothing goes out until
// the stop, which is where the client says it is.
func TestClientPlaybackPauseAndResume(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("a", ClientPlaybackStart, 0)
	h.settle(t, 1)
	for range 3 {
		h.report("a", ClientPlaybackProgress, 720) // paused
	}
	h.report("a", ClientPlaybackProgress, 730) // resumed
	h.report("a", ClientPlaybackStop, 900)
	wantScrobble(t, h.settle(t, 2)[1], "stop", 62.5)
}

// A client that never said it started still reports: its first progress
// opens the playback, and a stop alone is still a stop.
func TestClientPlaybackWithoutStart(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("a", ClientPlaybackProgress, 144)
	wantScrobble(t, h.settle(t, 1)[0], "start", 10)
	h.report("a", ClientPlaybackStop, 288)
	wantScrobble(t, h.settle(t, 2)[1], "stop", 20)

	h2 := newScrobbleHarness(t, harnessOpts{})
	h2.report("b", ClientPlaybackStop, 720)
	wantScrobble(t, h2.settle(t, 1)[0], "stop", 50)
}

// Without a runtime a position cannot be placed, and the stop is not sent:
// the session's served estimate spans every playback it has served, so it
// could carry an earlier one's progress into this one.
func TestClientPlaybackStopWithoutRuntime(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{noRuntime: true})
	h.sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: 950, TotalSize: 1000, Elapsed: time.Hour})
	h.report("a", ClientPlaybackStop, 120)
	h.settle(t, 0)
}

// A session the title was served from that has since gone is no address: the
// start waits for a live one rather than being spent on the dead one.
func TestClientPlaybackStartWaitsForALiveSession(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.server.clientPlayback.noteServed(h.stream, &session.Session{ID: "evicted", ContentID: "kitsu:114:1"})
	h.report("a|ps", ClientPlaybackStart, 0)
	h.settle(t, 0)
	h.server.clientPlayback.noteServed(h.stream, h.sess)
	h.report("a|ps", ClientPlaybackProgress, 144)
	wantScrobble(t, h.settle(t, 1)[0], "start", 10)
}

// A device opening a new play session has left its last one. One that never
// stopped — an app killed, then the title reopened — is reported stopped at
// the position it last confirmed, as the new one starts, and sends nothing
// more after: not even when it would have gone quiet.
func TestNewPlaySessionReplacesTheDevicesLast(t *testing.T) {
	idle := clientPlaybackIdleTTL
	clientPlaybackIdleTTL = 400 * time.Millisecond
	t.Cleanup(func() { clientPlaybackIdleTTL = idle })

	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps-1", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("dev|ps-1", ClientPlaybackProgress, 576)
	h.report("dev|ps-2", ClientPlaybackProgress, 600) // no start for the new one
	// The two sends race each other, so they are matched without order.
	sent := h.settle(t, 3)[1:]
	if !slices.ContainsFunc(sent, func(s sentScrobble) bool { return s.verb == "/scrobble/stop" && s.progress == 40 }) ||
		!slices.ContainsFunc(sent, func(s sentScrobble) bool { return s.verb == "/scrobble/start" && s.progress == 41.67 }) {
		t.Fatalf("replacement sent %+v, want ps-1 stopped at 40 and ps-2 started at 41.67", sent)
	}
	h.report("dev|ps-2", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 95)
	time.Sleep(600 * time.Millisecond) // past ps-1's idle window
	h.settle(t, 4)

	// A stopped playback being replaced has nothing left to report.
	h.report("dev|ps-3", ClientPlaybackStart, 0)
	wantScrobble(t, h.settle(t, 5)[4], "start", 0)
}

func TestClientPlaybackNeedsALinkedEnabledAccount(t *testing.T) {
	for name, opts := range map[string]harnessOpts{"off": {off: true}, "unlinked": {unlinked: true}} {
		h := newScrobbleHarness(t, opts)
		h.report("a", ClientPlaybackStart, 10)
		h.report("a", ClientPlaybackStop, 700)
		h.settle(t, 0)
		if h.server.clientPlayback.reportedBy(h.stream, "kitsu:114:1", &reportingServe{device: "a"}) {
			t.Fatalf("%s: a stream that reports nowhere stood its serves' stop down", name)
		}
	}
}

// Two devices playing the same title are two playbacks, each reported at its
// own position.
func TestConcurrentClientPlaybacksStayApart(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("tv|1", ClientPlaybackStart, 0)
	h.report("tablet|2", ClientPlaybackStart, 0)
	h.settle(t, 2)
	h.report("tablet|2", ClientPlaybackProgress, 864)
	h.report("tv|1", ClientPlaybackStop, 432)
	wantScrobble(t, h.settle(t, 3)[2], "stop", 30)
	h.report("tablet|2", ClientPlaybackStop, 1008)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 70)
}

// A later playback is its own: it starts and stops for itself whatever an
// earlier one on the same session reported, including a serve that proved
// good in between.
func TestConsecutiveClientPlaybacks(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps-1", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("dev|ps-1", ClientPlaybackStop, 720)
	h.settle(t, 2)

	h.serveRange(t, "bytes=0-", 50e6, 0, jellyfinServe("dev"))
	h.report("dev|ps-2", ClientPlaybackStart, 0)
	wantScrobble(t, h.settle(t, 3)[2], "start", 0)
	h.report("dev|ps-2", ClientPlaybackStop, 288)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 20)

	// The same play session starting over is a new playback too.
	h.report("dev|ps-2", ClientPlaybackStart, 0)
	wantScrobble(t, h.settle(t, 5)[4], "start", 0)
}

// A playback that reports nothing for the idle window — paused with a
// client that stays quiet, or an app killed — goes out at the last position
// the client confirmed, never further; reporting again starts it anew.
func TestClientPlaybackGoneQuietReportsItsLastConfirmedPosition(t *testing.T) {
	idle := clientPlaybackIdleTTL
	clientPlaybackIdleTTL = 500 * time.Millisecond
	t.Cleanup(func() { clientPlaybackIdleTTL = idle })

	h := newScrobbleHarness(t, harnessOpts{})
	h.report("a", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("a", ClientPlaybackProgress, 720) // paused here
	wantScrobble(t, h.settle(t, 2)[1], "stop", 50)
	h.report("a", ClientPlaybackProgress, 725) // resumed
	wantScrobble(t, h.settle(t, 3)[2], "start", 50.35)
	h.report("a", ClientPlaybackStop, 725)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 50.35)
}

// A target failing or hanging must never hold up the client's request.
func TestClientPlaybackDoesNotWaitOnTheTarget(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.mu.Lock()
	h.status, h.delay = http.StatusInternalServerError, time.Second
	h.mu.Unlock()
	began := time.Now()
	h.report("a", ClientPlaybackStart, 0)
	h.report("b", ClientPlaybackStop, 300)
	if took := time.Since(began); took > 200*time.Millisecond {
		t.Fatalf("reports took %v waiting on the target", took)
	}
	h.settle(t, 2)
}

// diagFile is a file of zeros delivered at a fixed rate.
type diagFile struct {
	mu        sync.Mutex
	size, pos int64
	rate      float64 // bytes per second, 0 unthrottled
	closed    bool
}

func (f *diagFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, errors.New("closed")
	}
	if f.pos >= f.size {
		return 0, io.EOF
	}
	n := min(int64(len(p)), f.size-f.pos)
	if f.rate > 0 {
		time.Sleep(time.Duration(float64(n) / f.rate * float64(time.Second)))
	}
	clear(p[:n])
	f.pos += n
	return int(n), nil
}

func (f *diagFile) Seek(off int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch whence {
	case io.SeekStart:
		f.pos = off
	case io.SeekCurrent:
		f.pos += off
	case io.SeekEnd:
		f.pos = f.size + off
	}
	return f.pos, nil
}

func (f *diagFile) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

const diagFileSize = int64(64 << 20)

// jellyfinServe serves the way the Jellyfin layer does, to the client with
// the given DeviceId; "" leaves the serve to be matched by its address.
func jellyfinServe(device string) *PlayServeOptions {
	return &PlayServeOptions{ClientReportsPlayback: true, ClientDevice: device}
}

// serveRange runs one real HTTP request through servePlaybackStream, the
// client draining as fast as it is fed; cancelAfter > 0 abandons it early.
// opts, nil for a Stremio serve, are the serve's options.
func (h *scrobbleHarness) serveRange(t *testing.T, rangeHeader string, rate float64, cancelAfter time.Duration, opts *PlayServeOptions) {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if opts != nil {
			r = r.WithContext(context.WithValue(r.Context(), playServeOptionsKey, *opts))
		}
		ctx, cancel := context.WithCancel(r.Context())
		h.server.servePlaybackStream(w, r, h.stream, &resolvedPlayback{
			session: h.sess, sessionID: h.sess.ID, stream: &diagFile{size: diagFileSize, rate: rate},
			name: "video.mkv", size: diagFileSize, ctx: ctx, cancel: cancel,
		}, time.Now())
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cancelAfter > 0 {
		time.AfterFunc(cancelAfter, cancel)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", rangeHeader)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	<-done
}

// The VidHub pattern behind this: a cancelled first request, tail probes,
// then the whole rest of the file delivered in a burst while the client
// reports from its own buffer, stopping long after the transfer ended. The
// client's positions are reported, and the transport adds nothing of its own.
func TestBufferedPlaybackIsScrobbledFromClientPositions(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 300 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{})
	h.serveRange(t, "bytes=0-", 20e6, 30*time.Millisecond, jellyfinServe("vidhub"))
	h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize-64<<10), 0, 0, jellyfinServe("vidhub"))
	served := make(chan struct{})
	go func() {
		defer close(served)
		h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize*119/1440), 100e6, 0, jellyfinServe("vidhub"))
	}()
	time.Sleep(100 * time.Millisecond)
	h.report("vidhub|ps", ClientPlaybackStart, 119)
	<-served
	h.report("vidhub|ps", ClientPlaybackProgress, 167)
	h.report("vidhub|ps", ClientPlaybackStop, 187)
	sent := h.settle(t, 2)
	wantScrobble(t, sent[0], "start", 8.26)
	wantScrobble(t, sent[1], "stop", 12.99)
	// The serve outlived the good threshold, so a Stremio serve would have
	// sent a start of its own.
	if !h.sess.OnceDone(onceSuccessRecorded) {
		t.Fatal("the serve never committed, so this proved nothing about the transport start")
	}
}

// A Jellyfin client that reports nothing is scrobbled from the transport, as
// a Stremio player is — start and stop — once its serve has waited out the
// grace for a report. The stop still credits no more than the serve's own
// time, however fast the client buffered.
func TestJellyfinServeWithoutReportsFallsBackToTheTransport(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 200 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{runtime: 60})
	began := time.Now()
	h.serveRange(t, "bytes=0-", 50e6, 0, jellyfinServe("silent"))
	open := time.Since(began)
	time.Sleep(clientReportGrace) // the stop waits out the grace before deciding
	sent := h.settle(t, 2)
	if starts, stops := countVerbs(sent); starts != 1 || stops != 1 {
		t.Fatalf("fallback: %+v", sent)
	}
	stop := sent[slices.IndexFunc(sent, func(s sentScrobble) bool { return s.verb == "/scrobble/stop" })]
	if limit := open.Seconds() / 60 * 125; stop.progress <= 0 || stop.progress > limit+0.01 {
		t.Fatalf("a fully buffered serve open %v credited %v%%, limit %.2f%%", open, stop.progress, limit)
	}
}

// A probe — a request opened and dropped at once — by a client that never
// reports is no playback: it sends no start, and moves no stop.
func TestJellyfinProbeWithoutReportsSendsNothing(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 100 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{runtime: 60})
	h.serveRange(t, "bytes=0-", 20e6, 30*time.Millisecond, jellyfinServe("silent"))
	time.Sleep(3 * clientReportGrace)
	h.settle(t, 0)
}

// A reporting client may open and drop a request — here at its resume point,
// which alone credits 8% — before it says it is playing. The serve waits for
// that, and sends no stop of its own.
func TestReportJustAfterAnOpenerKeepsTheTransportOut(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 300 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{})
	h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize*119/1440), 50e6, 50*time.Millisecond, jellyfinServe("vidhub"))
	h.report("vidhub|ps", ClientPlaybackStart, 119)
	wantScrobble(t, h.settle(t, 1)[0], "start", 8.26)
	time.Sleep(400 * time.Millisecond) // past the grace
	h.settle(t, 1)
}

// Without client reports the transport scrobbles as it always has.
func TestTransportScrobblesWithoutClientReports(t *testing.T) {
	// A one-minute title, so a serve of a second or more earns a stop; with the
	// episode's 24 minutes it would earn 0.1%, below the step a stop needs.
	h := newScrobbleHarness(t, harnessOpts{runtime: 60})
	began := time.Now()
	h.serveRange(t, "bytes=0-", 50e6, 0, nil)
	open := time.Since(began)
	sent := h.settle(t, 2)
	// The sends race each other, so they are matched without order.
	if starts, stops := countVerbs(sent); starts != 1 || stops != 1 {
		t.Fatalf("transport scrobbles: %+v", sent)
	}
	stop := sent[slices.IndexFunc(sent, func(s sentScrobble) bool { return s.verb == "/scrobble/stop" })]
	if stop.progress <= 0 {
		t.Fatalf("transport stop credited nothing: %+v", stop)
	}
	// The whole file was delivered, yet the stop credits no more than the
	// serve's own time at the title's rate (with its 25% slack).
	if limit := open.Seconds() / 60 * 125; stop.progress > limit+0.01 {
		t.Fatalf("a fully buffered serve open %v credited %v%%, limit %.2f%%", open, stop.progress, limit)
	}
}

// A Stremio player and a reporting Jellyfin client on the same stream and
// title share a media session; the Jellyfin reports must not silence the
// Stremio playback, even when the Jellyfin serve is the last to end.
func TestJellyfinReportsDoNotSilenceStremio(t *testing.T) {
	// A 10-second title, so a second-long serve earns a stop.
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.report("jf|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.serveRange(t, "bytes=0-", 30e6, 0, jellyfinServe("jf")) }() // ends last
	go func() { defer wg.Done(); h.serveRange(t, "bytes=0-", 60e6, 0, nil) }()
	wg.Wait()
	h.report("jf|ps", ClientPlaybackStop, 6)

	sent := h.settle(t, 4)
	// Stremio: the transport's start and stop. Jellyfin: the client's.
	// The sends race each other, so they are matched without order.
	starts, stops := countVerbs(sent)
	clientStop := slices.ContainsFunc(sent, func(s sentScrobble) bool {
		return s.verb == "/scrobble/stop" && s.progress == 60
	})
	if starts != 2 || stops != 2 || !clientStop {
		t.Fatalf("got %d starts and %d stops: %+v", starts, stops, sent)
	}
}

// A player that buffered the whole title keeps reporting after its media
// session was evicted. What the title is scrobbled as was kept from the live
// session, so the final stop still goes out, at the client's position.
func TestClientPlaybackOutlivesItsMediaSession(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("dev|ps", ClientPlaybackProgress, 144)
	h.server.sessionManager.DeleteSession(h.sess.ID)
	if h.server.sessionManager.PeekSession(h.sess.ID) != nil {
		t.Fatal("the session was not evicted")
	}
	h.report("dev|ps", ClientPlaybackProgress, 1200)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	sent := h.settle(t, 2)
	wantScrobble(t, sent[1], "stop", 95)
	show, _ := sent[1].body["show"].(map[string]any)
	if ids, _ := show["ids"].(map[string]any); ids["tvdb"] != float64(72905) || show["episode"] != float64(1) {
		t.Fatalf("stop addressed as %v", sent[1].body)
	}

	// The kept item is the title's on this stream alone: another stream, or
	// another title on this one, finds nothing to address a stop by.
	noop := func(string, string, *clientAttempt, int) {}
	other := &auth.Stream{Username: "Other", MDBListScrobble: true}
	for _, c := range []struct {
		stream  *auth.Stream
		content string
	}{{other, "kitsu:114:1"}, {h.stream, "kitsu:114:2"}} {
		out := h.server.clientPlayback.apply(c.stream, ClientPlayback{ContentID: c.content, Device: "dev", Event: ClientPlaybackStop, PositionSeconds: 1368}, nil, noop)
		if len(out) != 1 || out[0].item != nil {
			t.Fatalf("%s %s reused another playback's item: %+v", c.stream.Username, c.content, out)
		}
	}
}

// One Jellyfin device reporting a title must not stand down the fallback for
// another device playing it that reports nothing. Each gets its own start
// and stop, and the reporting one no more than its own.
func TestReportingDeviceDoesNotSilenceAnother(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 200 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	// A 10-second title, so a second-long serve earns a stop.
	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.report("vidhub|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.serveRange(t, "bytes=0-", 60e6, 0, jellyfinServe("vidhub")) }()
	go func() { defer wg.Done(); h.serveRange(t, "bytes=0-", 30e6, 0, jellyfinServe("silent")) }() // ends last
	wg.Wait()
	h.report("vidhub|ps", ClientPlaybackStop, 6)
	time.Sleep(clientReportGrace)

	sent := h.settle(t, 4)
	starts, stops := countVerbs(sent)
	clientStop := slices.ContainsFunc(sent, func(s sentScrobble) bool { return s.verb == "/scrobble/stop" && s.progress == 60 })
	if starts != 2 || stops != 2 || !clientStop {
		t.Fatalf("got %d starts and %d stops: %+v", starts, stops, sent)
	}
}

// A serve whose request carried no DeviceId is matched to reports by address:
// a client reporting from elsewhere decides nothing for it.
func TestServeWithoutDeviceIDMatchesReportsByAddress(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 100 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: "kitsu:114:1", Device: "phone", Address: "10.0.0.9", Event: ClientPlaybackStart})
	h.settle(t, 1)
	rs := &reportingServe{address: "127.0.0.1", began: time.Now()}
	if h.server.clientPlayback.reportedBy(h.stream, "kitsu:114:1", rs) {
		t.Fatal("a report from another address stood the serve down")
	}
	h.report("tv|ps", ClientPlaybackStart, 0)
	if !h.server.clientPlayback.reportedBy(h.stream, "kitsu:114:1", rs) {
		t.Fatal("a report from the serve's own address did not count")
	}
}

// A start repeated in the same play session — after a seek, or coming back
// from the background — is the same playback: no stop and new start between.
func TestRepeatedStartIsOnePlayback(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|", ClientPlaybackStart, 0) // the play session named only later
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("dev|ps", ClientPlaybackProgress, 288)
	h.report("dev|ps", ClientPlaybackStart, 576) // seeked
	h.report("dev|ps", ClientPlaybackProgress, 600)
	h.report("dev|ps", ClientPlaybackStart, 600)  // back from the background
	h.report("dev|", ClientPlaybackProgress, 610) // a report naming no play session
	h.settle(t, 1)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 2)[1], "stop", 95)
}

// Clients that send no DeviceId are told apart by app, device and address,
// rather than sharing one empty key and ending each other's playbacks.
func TestClientsWithoutDeviceIDStayApart(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	report := func(client, addr string, event ClientPlaybackEvent, pos float64) {
		h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: "kitsu:114:1", Client: client, Address: addr, Event: event, PositionSeconds: pos})
	}
	report("Infuse|Apple TV", "10.0.0.2", ClientPlaybackStart, 0)
	report("VLC|Laptop", "10.0.0.3", ClientPlaybackStart, 0)
	h.settle(t, 2)
	report("Infuse|Apple TV", "10.0.0.2", ClientPlaybackProgress, 432)
	report("VLC|Laptop", "10.0.0.3", ClientPlaybackProgress, 1008)
	h.settle(t, 2)
	report("Infuse|Apple TV", "10.0.0.2", ClientPlaybackStop, 432)
	report("VLC|Laptop", "10.0.0.3", ClientPlaybackStop, 1008)
	sent := h.settle(t, 4)[2:]
	for _, want := range []float64{30, 70} {
		if !slices.ContainsFunc(sent, func(s sentScrobble) bool { return s.verb == "/scrobble/stop" && s.progress == want }) {
			t.Fatalf("stops %+v, want one at %v", sent, want)
		}
	}
}

// A stop, then playback resumed in the same play session — an app that
// stops when backgrounded — is two watches: the resume starts afresh and its
// own stop goes out. A repeated stop, and progress the stopped playback sent
// before its stop, are neither.
func TestResumeAfterStopIsReported(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("dev|ps", ClientPlaybackProgress, 432)
	h.report("dev|ps", ClientPlaybackStop, 432)
	wantScrobble(t, h.settle(t, 2)[1], "stop", 30)
	h.report("dev|ps", ClientPlaybackStop, 432)     // repeated
	h.report("dev|ps", ClientPlaybackProgress, 430) // late
	h.settle(t, 2)

	h.report("dev|ps", ClientPlaybackStart, 432)
	wantScrobble(t, h.settle(t, 3)[2], "start", 30)
	h.report("dev|ps", ClientPlaybackProgress, 1300)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 95)
}

// A client that resumes with progress alone, no start, resumes too once the
// report is past what could be the stopped playback's own.
func TestResumeByProgressAfterStop(t *testing.T) {
	settle := clientStopSettle
	clientStopSettle = 100 * time.Millisecond
	t.Cleanup(func() { clientStopSettle = settle })

	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackProgress, 432)
	h.report("dev|ps", ClientPlaybackStop, 432)
	h.settle(t, 2)
	time.Sleep(2 * clientStopSettle)
	h.report("dev|ps", ClientPlaybackProgress, 440)
	wantScrobble(t, h.settle(t, 3)[2], "start", 30.56)
	h.report("dev|ps", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 95)
}

// A serve ending and a fallback timer firing on the same session both reach
// for the stop it owes; exactly one gets it.
func TestTransportStopIsSentOnce(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{noRuntime: true})
	h.sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: 950, TotalSize: 1000, Elapsed: time.Hour})
	const rounds = 20
	for range rounds {
		h.sess.SetLastReportedProgress(0)
		h.sess.Once(onceTransportStopDue)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); h.server.transportStopIfIdle(h.stream, h.sess) }()
		}
		wg.Wait()
	}
	if _, stops := countVerbs(h.settle(t, rounds)); stops != rounds {
		t.Fatalf("sent %d stops over %d rounds", stops, rounds)
	}
}

// Reports ask about the media session without touching it: a client
// reporting from its buffer must not keep an idle session from eviction.
func TestClientReportsDoNotKeepTheSessionAlive(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	before := h.sess.LastAccess
	time.Sleep(5 * time.Millisecond)
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.report("dev|ps", ClientPlaybackProgress, 144)
	h.settle(t, 1)
	if !h.sess.LastAccess.Equal(before) {
		t.Fatal("a client report refreshed the session's last access")
	}
}

// Timers that outlive the server send nothing: a playback going quiet, or a
// serve's fallback, after Shutdown.
func TestScrobbleTimersSendNothingAfterShutdown(t *testing.T) {
	idle, grace := clientPlaybackIdleTTL, clientReportGrace
	clientPlaybackIdleTTL, clientReportGrace = time.Second, time.Second
	t.Cleanup(func() { clientPlaybackIdleTTL, clientReportGrace = idle, grace })

	h := newScrobbleHarness(t, harnessOpts{runtime: 10})
	h.server.stopCh = make(chan struct{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.report("dev|ps", ClientPlaybackProgress, 5)
	h.settle(t, 1)
	// A serve to a device that never reports, qualified and ended: both its
	// fallbacks are pending.
	h.sess.NoteServedWindow(session.ServeWindow{StartOffset: 0, MaxOffset: 950, TotalSize: 1000, Elapsed: time.Hour})
	rs := &reportingServe{device: "silent", address: "127.0.0.1", began: time.Now()}
	h.server.scrobbleStart(h.stream, h.sess, rs)
	h.server.scrobbleStopIfIdle(h.stream, h.sess, rs)
	h.server.Shutdown()
	time.Sleep(clientReportGrace + 500*time.Millisecond)
	h.settle(t, 1)
}

// A report the device's previous play session sends late — its stop landing
// after the new session's start — is about a playback already replaced and
// stopped. It must not end the playback running now.
func TestLateReportFromAReplacedPlaySessionIsIgnored(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|A", ClientPlaybackStart, 0)
	h.report("dev|A", ClientPlaybackProgress, 576)
	h.settle(t, 1)
	h.report("dev|B", ClientPlaybackStart, 600) // A stops at 40, B starts
	h.settle(t, 3)
	h.report("dev|A", ClientPlaybackStop, 576)
	h.report("dev|A", ClientPlaybackProgress, 576)
	h.report("dev|B", ClientPlaybackProgress, 700)
	h.settle(t, 3)
	h.report("dev|B", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 4)[3], "stop", 95)
}

// A new play session replacing an unstopped one sends the old one's stop and
// then the new one's start, in that order: a target taking the stop last
// would show the title stopped while it plays.
func TestReplacementStopLandsBeforeTheNewStart(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|A", ClientPlaybackStart, 0)
	h.report("dev|A", ClientPlaybackProgress, 576)
	h.settle(t, 1)
	h.mu.Lock()
	h.stopLag = 200 * time.Millisecond
	h.mu.Unlock()
	h.report("dev|B", ClientPlaybackStart, 600)
	sent := h.settle(t, 3)
	wantScrobble(t, sent[1], "stop", 40)
	wantScrobble(t, sent[2], "start", 41.67)
}

// An idle timer that fired while a report re-armed its playback is stale: it
// must not end the playback the report just kept alive.
func TestStaleIdleTimerDoesNotEndALivePlayback(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps", ClientPlaybackStart, 0)
	h.report("dev|ps", ClientPlaybackProgress, 576)
	h.settle(t, 1)
	key := clientPlaybackKey(h.stream, "kitsu:114:1")
	h.server.clientPlayback.mu.Lock()
	a := h.server.clientPlayback.attempts[key]["id:dev"]
	stale := a.timerGen
	h.server.clientPlayback.mu.Unlock()
	h.report("dev|ps", ClientPlaybackProgress, 600) // re-arms the timer
	if _, ok := h.server.clientPlayback.release(key, "id:dev", a, stale); ok {
		t.Fatal("a stale idle timer ended a playback that had just reported")
	}
	h.report("dev|ps", ClientPlaybackStop, 1368)
	wantScrobble(t, h.settle(t, 2)[1], "stop", 95)
}
