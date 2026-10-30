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

// report sends one client report; playback is "device|play session".
func (h *scrobbleHarness) report(playback string, event ClientPlaybackEvent, position float64) {
	device, playSession, _ := strings.Cut(playback, "|")
	h.server.ReportClientPlayback(h.stream, ClientPlayback{ContentID: "kitsu:114:1", Device: device, PlaySession: playSession, Event: event, PositionSeconds: position})
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

// A device opening a new play session has left its last one, stopped or
// not: the old one sends nothing more, not even when it would have gone quiet.
func TestNewPlaySessionReplacesTheDevicesLast(t *testing.T) {
	idle := clientPlaybackIdleTTL
	clientPlaybackIdleTTL = 400 * time.Millisecond
	t.Cleanup(func() { clientPlaybackIdleTTL = idle })

	h := newScrobbleHarness(t, harnessOpts{})
	h.report("dev|ps-1", ClientPlaybackStart, 0)
	h.settle(t, 1)
	h.report("dev|ps-1", ClientPlaybackProgress, 576)
	h.report("dev|ps-2", ClientPlaybackProgress, 600) // no start for the new one
	wantScrobble(t, h.settle(t, 2)[1], "start", 41.67)
	h.report("dev|ps-2", ClientPlaybackStop, 1368)
	sent := h.settle(t, 3)
	wantScrobble(t, sent[2], "stop", 95)
	time.Sleep(600 * time.Millisecond) // past ps-1's idle window
	h.settle(t, 3)
}

func TestClientPlaybackNeedsALinkedEnabledAccount(t *testing.T) {
	for name, opts := range map[string]harnessOpts{"off": {off: true}, "unlinked": {unlinked: true}} {
		h := newScrobbleHarness(t, opts)
		h.report("a", ClientPlaybackStart, 10)
		h.report("a", ClientPlaybackStop, 700)
		h.settle(t, 0)
		if h.server.clientPlayback.reportedSince(h.stream, "kitsu:114:1", time.Time{}) {
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

	h.serveRange(t, "bytes=0-", 50e6, 0, true)
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

// serveRange runs one real HTTP request through servePlaybackStream, the
// client draining as fast as it is fed; cancelAfter > 0 abandons it early.
// jellyfin serves it the way the Jellyfin layer does.
func (h *scrobbleHarness) serveRange(t *testing.T, rangeHeader string, rate float64, cancelAfter time.Duration, jellyfin bool) {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		r = r.WithContext(context.WithValue(r.Context(), playServeOptionsKey, PlayServeOptions{ClientReportsPlayback: jellyfin}))
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
	h := newScrobbleHarness(t, harnessOpts{})
	h.serveRange(t, "bytes=0-", 20e6, 30*time.Millisecond, true)
	h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize-64<<10), 0, 0, true)
	served := make(chan struct{})
	go func() {
		defer close(served)
		h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize*119/1440), 100e6, 0, true)
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

// A Jellyfin client that reports nothing is still scrobbled: its serves owe
// the transport's stop. It gets no start, since one from the transport could
// not be told apart from an earlier playback's.
func TestJellyfinServeWithoutReportsFallsBackToTheTransportStop(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 200 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{noRuntime: true})
	h.serveRange(t, "bytes=0-", 50e6, 0, true)
	h.settle(t, 0) // the serve waits out the grace before deciding
	sent := h.settle(t, 1)
	if sent[0].verb != "/scrobble/stop" || sent[0].progress <= 0 {
		t.Fatalf("fallback: %+v", sent)
	}
}

// A reporting client may open and drop a request — here at its resume point,
// which alone credits 8% — before it says it is playing. The serve waits for
// that, and sends no stop of its own.
func TestReportJustAfterAnOpenerKeepsTheTransportOut(t *testing.T) {
	grace := clientReportGrace
	clientReportGrace = 300 * time.Millisecond
	t.Cleanup(func() { clientReportGrace = grace })

	h := newScrobbleHarness(t, harnessOpts{})
	h.serveRange(t, fmt.Sprintf("bytes=%d-", diagFileSize*119/1440), 50e6, 50*time.Millisecond, true)
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
	h.serveRange(t, "bytes=0-", 50e6, 0, false)
	open := time.Since(began)
	sent := h.settle(t, 2)
	if sent[0].verb != "/scrobble/start" || sent[1].verb != "/scrobble/stop" || sent[1].progress <= 0 {
		t.Fatalf("transport scrobbles: %+v", sent)
	}
	// The whole file was delivered, yet the stop credits no more than the
	// serve's own time at the title's rate (with its 25% slack).
	if limit := open.Seconds() / 60 * 125; sent[1].progress > limit+0.01 {
		t.Fatalf("a fully buffered serve open %v credited %v%%, limit %.2f%%", open, sent[1].progress, limit)
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
	go func() { defer wg.Done(); h.serveRange(t, "bytes=0-", 30e6, 0, true) }() // ends last
	go func() { defer wg.Done(); h.serveRange(t, "bytes=0-", 60e6, 0, false) }()
	wg.Wait()
	h.report("jf|ps", ClientPlaybackStop, 6)

	sent := h.settle(t, 4)
	// Stremio: the transport's start and stop. Jellyfin: the client's.
	starts, stops := countVerbs(sent)
	if starts != 2 || stops != 2 || sent[3].progress != 60 {
		t.Fatalf("got %d starts and %d stops: %+v", starts, stops, sent)
	}
}
