package stremio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/session"
)

// F1: a duplicate request is still preparing a copy when another request
// retires that copy. Retirement closes the session, which cancels the
// duplicate's prepare — but its client is still connected, so it must carry
// on with what the slot holds now instead of returning an empty response. A
// client that really left still stops at once, with no fallback work.

// gatedNZBIndexer serves the fixture's NZB downloads: copy 0's download holds
// until its context ends (so a request sits mid-prepare until the session
// closes), and copy 1's holds until release1 is closed, then fails.
type gatedNZBIndexer struct {
	entered0, entered1, release1 chan struct{}
	once0, once1                 sync.Once
	mu                           sync.Mutex
	calls                        map[int]int
}

func newGatedNZBIndexer() *gatedNZBIndexer {
	return &gatedNZBIndexer{entered0: make(chan struct{}), entered1: make(chan struct{}), release1: make(chan struct{}), calls: map[int]int{}}
}

func (g *gatedNZBIndexer) DownloadNZB(ctx context.Context, url string) ([]byte, error) {
	copyIndex := 0
	if strings.HasPrefix(url, "https://1.invalid") {
		copyIndex = 1
	}
	g.mu.Lock()
	g.calls[copyIndex]++
	g.mu.Unlock()
	if copyIndex == 0 {
		g.once0.Do(func() { close(g.entered0) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	g.once1.Do(func() { close(g.entered1) })
	select {
	case <-g.release1:
		return nil, errors.New("nzb unavailable")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *gatedNZBIndexer) callsFor(copyIndex int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[copyIndex]
}

func (*gatedNZBIndexer) Search(context.Context, indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return &indexer.SearchResponse{}, nil
}
func (*gatedNZBIndexer) Name() string               { return "Gated" }
func (*gatedNZBIndexer) GetUsage() indexer.Usage    { return indexer.Usage{} }
func (*gatedNZBIndexer) Ping(context.Context) error { return nil }

// newRetiredPrepareFixture is a two-copy slot with copy walking on, a real
// playback service, the gated indexer and History recording.
func newRetiredPrepareFixture(t *testing.T) (copyRetireFixture, *gatedNZBIndexer, *persistence.StateManager) {
	t.Helper()
	f, mgr := newHistoryFixture(t, 2)
	idx := newGatedNZBIndexer()
	f.server.indexer = idx
	f = f.withFailingPrepare(t)
	// Long enough that a held download ends only by cancellation.
	f.server.playback.StartupTimeout = func() time.Duration { return time.Minute }
	return f, idx, mgr
}

// play runs one play request for the slot on its own goroutine; done closes
// when the handler returns, and only then may rec be read.
func (f copyRetireFixture) play(ctx context.Context) (rec *httptest.ResponseRecorder, done chan struct{}) {
	rec = httptest.NewRecorder()
	done = make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, "/play/"+f.slot, nil).WithContext(ctx)
	go func() {
		defer close(done)
		if resolved, ok := f.server.resolvePlaybackSlot(rec, req, f.stream, f.slot); ok {
			resolved.startup.Release()
			resolved.cancel()
			resolved.stream.Close()
		}
	}()
	return rec, done
}

// waitFor waits for ch, failing the test if the request ends (done) first or
// nothing happens within a bound.
func waitFor(t *testing.T, ch, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-done:
		select {
		case <-ch:
		default:
			t.Fatalf("the request ended before %s", what)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// requireNoHold fails if a startup hold on sess leaked: only a held session
// is refused by SettleFailedStartup. It settles sess, so it comes last.
func requireNoHold(t *testing.T, f copyRetireFixture, sess *session.Session) {
	t.Helper()
	if got := f.manager.SettleFailedStartup(sess, 1, nil); got == session.StartupHeld {
		t.Fatal("a startup hold on the session was never released")
	}
}

var errFatalRead = fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills)

// 1, 2, 8, 9, 10. B is preparing copy 0 when A's fatal read retires it. B's
// client is still there: B carries on with copy 1, and when that fails too —
// the last copy — B gets the terminal answer, not an empty response. History
// has one row per copy, and copy 0's verdict is A's alone.
func TestRetiredCopyDuringPrepareCarriesOnWithTheNextCopy(t *testing.T) {
	f, idx, mgr := newRetiredPrepareFixture(t)
	copy0 := f.resolve(t, "NZBGeek")
	rec, done := f.play(context.Background())
	waitFor(t, idx.entered0, done, "B started preparing copy 0")

	if !f.server.failServedCopy(f.stream, copy0, errFatalRead) {
		t.Fatal("A's fatal read should retire copy 0")
	}
	waitFor(t, idx.entered1, done, "B carried on with copy 1")
	if cur := f.manager.PeekSession(f.slot); cur == nil || cur.Release().Indexer != "DrunkenSlug" {
		t.Fatal("B should be preparing the rebuilt copy 1")
	}
	close(idx.release1)
	waitDone(t, done)

	if !strings.Contains(rec.Header().Get("Location"), "/error/") {
		t.Fatal("B's connected client got an empty response")
	}
	if idx.callsFor(0) != 1 {
		t.Fatalf("copy 0 was downloaded %d times; the condemned copy must not be retried", idx.callsFor(0))
	}
	rows := requireRows(t, mgr, f.slot, "NZBGeek failed", "DrunkenSlug failed")
	if !strings.Contains(rows[0].FailureReason, unpack.ErrTooManyZeroFills.Error()) {
		t.Fatalf("copy 0's row = %q, want A's verdict only", rows[0].FailureReason)
	}
}

// 3, 10. As above, but the next copy cannot be built: B still answers.
func TestRetiredCopyDuringPrepareWithNoUsableCopyAnswers(t *testing.T) {
	f, idx, _ := newRetiredPrepareFixture(t)
	cached, _ := f.server.playlistCache.Load(f.key.CacheKey())
	cached.(*playlistCacheEntry).result.Candidates[0].Release.Variants[0].Link = ""
	copy0 := f.resolve(t, "NZBGeek")
	rec, done := f.play(context.Background())
	waitFor(t, idx.entered0, done, "B started preparing copy 0")

	f.server.failServedCopy(f.stream, copy0, errFatalRead)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("B never answered")
	}
	if !strings.Contains(rec.Header().Get("Location"), "/error/") {
		t.Fatal("B's connected client got an empty response")
	}
	if idx.callsFor(1) != 0 {
		t.Fatal("an unbuildable copy was downloaded")
	}
	// The slot is left to the next request, which finds the next copy cannot
	// be built and gives the slot up on its own evidence.
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("B gave the slot up on another request's behalf")
	}
	if _, _, err := f.server.recoverPlaySessionAfterEviction(context.Background(), f.slot, f.stream); err == nil {
		t.Fatal("the next request should not recover a slot whose next copy cannot be built")
	}
	if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("the next request should give the slot up")
	}
}

// A session closed on purpose — the dashboard's Close — while a request is
// preparing it still aborts that request: no rebuild, no fallback.
func TestDashboardCloseDuringPrepareStillAborts(t *testing.T) {
	f, idx, _ := newRetiredPrepareFixture(t)
	f.resolve(t, "NZBGeek")
	_, done := f.play(context.Background())
	waitFor(t, idx.entered0, done, "B started preparing copy 0")

	f.manager.DeleteSession(f.slot)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("closing the session did not end B's prepare")
	}
	if f.manager.PeekSession(f.slot) != nil || idx.callsFor(1) != 0 || f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a deliberate close was undone by a rebuild or fallback")
	}
}

// 4, 7. A client that leaves mid-prepare ends the request: no fallback, no
// verdict, the slot and its session untouched, the startup hold released.
func TestClientLeavingDuringPrepareTriggersNoFallback(t *testing.T) {
	f, idx, mgr := newRetiredPrepareFixture(t)
	copy0 := f.resolve(t, "NZBGeek")
	ctx, cancel := context.WithCancel(context.Background())
	_, done := f.play(ctx)
	waitFor(t, idx.entered0, done, "B started preparing copy 0")
	cancel()
	waitDone(t, done)

	if idx.callsFor(1) != 0 || f.manager.SlotCopyIndex(f.slot) != 0 || f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a client disconnect triggered fallback work")
	}
	if f.manager.PeekSession(f.slot) != copy0 || isClosedSession(copy0) {
		t.Fatal("a client disconnect disturbed the slot's session")
	}
	if copy0.OnceDone(onceFailureRecorded) {
		t.Fatal("a client disconnect recorded a verdict")
	}
	requireRows(t, mgr, f.slot, "NZBGeek pending")
	requireNoHold(t, f, copy0)
}

// 5. Once B is preparing the replacement, a late report about copy 0 — a
// straggling read on the retired session — does not touch the replacement.
func TestStaleRetirementLeavesTheReplacementPreparing(t *testing.T) {
	f, idx, _ := newRetiredPrepareFixture(t)
	copy0 := f.resolve(t, "NZBGeek")
	_, done := f.play(context.Background())
	waitFor(t, idx.entered0, done, "B started preparing copy 0")
	f.server.failServedCopy(f.stream, copy0, errFatalRead)
	waitFor(t, idx.entered1, done, "B carried on with copy 1")
	replacement := f.manager.PeekSession(f.slot)

	f.server.failServedCopy(f.stream, copy0, errFatalRead)

	if f.manager.PeekSession(f.slot) != replacement || isClosedSession(replacement) {
		t.Fatal("a stale report about copy 0 disturbed the replacement")
	}
	select {
	case <-done:
		t.Fatal("a stale report about copy 0 ended B's prepare of the replacement")
	default:
	}
	close(idx.release1)
	waitDone(t, done)
}

// 6, 7. The client leaves while the replacement is being prepared: the
// request ends, the slot stays on the replacement, unfailed, cursor on copy 1,
// and the hold on the replacement is released.
func TestClientLeavingWhileReplacementPreparesLeavesTheSlotSane(t *testing.T) {
	f, idx, _ := newRetiredPrepareFixture(t)
	copy0 := f.resolve(t, "NZBGeek")
	ctx, cancel := context.WithCancel(context.Background())
	_, done := f.play(ctx)
	waitFor(t, idx.entered0, done, "B started preparing copy 0")
	f.server.failServedCopy(f.stream, copy0, errFatalRead)
	waitFor(t, idx.entered1, done, "B carried on with copy 1")
	replacement := f.manager.PeekSession(f.slot)
	cancel()
	waitDone(t, done)

	if f.manager.PeekSession(f.slot) != replacement || isClosedSession(replacement) {
		t.Fatal("the client leaving disturbed the replacement")
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) || f.manager.SlotCopyIndex(f.slot) != 1 {
		t.Fatal("the client leaving changed the slot's state")
	}
	requireNoHold(t, f, replacement)
}

// waitDone waits for a request to end, failing rather than hanging the
// package if it never does.
func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never ended")
	}
}

// serveResolved runs the serve phase for sess, as servePlaySlot does after a
// successful resolve: the request holds a startup on sess, and its playback
// context ends with the client or the session. Its first read — the range
// prime, before any header — blocks until file.release is closed.
func (f copyRetireFixture) serveResolved(t *testing.T, ctx context.Context, sess *session.Session, file io.ReadSeekCloser, size int64) (rec *httptest.ResponseRecorder, done chan struct{}) {
	t.Helper()
	sess.Once(onceFallbackPrefetched) // no prefetch outliving the test
	startup := f.manager.BeginSessionStartup(sess)
	if startup == nil {
		t.Fatal("startup should register")
	}
	rec = httptest.NewRecorder()
	done = make(chan struct{})
	req := httptest.NewRequest(http.MethodGet, "/play/"+f.slot, nil).WithContext(ctx)
	req.Header.Set("Range", "bytes=0-")
	go func() {
		defer close(done)
		pctx, cancel := f.server.playbackContext(req, sess)
		defer cancel()
		defer startup.Release()
		f.server.servePlaybackStream(rec, req, f.stream, &resolvedPlayback{
			session: sess, sessionID: sess.ID, stream: file,
			name: "video.mkv", size: size, ctx: pctx, cancel: cancel, startup: startup,
		}, time.Now())
	}()
	return rec, done
}

func newGatedFile() *gatedFile {
	return &gatedFile{diagFile: &diagFile{size: 1 << 20}, reading: make(chan struct{}), release: make(chan struct{})}
}

// 2-5, 12. The second F1 window: B's prepare succeeded and its serve is on its
// first read, before any header, when A retires the copy. B must not serve the
// retired session as a healthy response: its client, still connected, is sent
// back to the slot, which the reconnect finds rebuilt on the next copy. No
// verdict, playback or History is added for the retired session.
func TestCopyRetiredBeforeHeadersSendsTheClientBackToTheSlot(t *testing.T) {
	f, mgr := newHistoryFixture(t, 2)
	copy0 := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(copy0)
	file := newGatedFile()
	rec, done := f.serveResolved(t, context.Background(), copy0, file, file.size)
	waitFor(t, file.reading, done, "B's first read")

	if !f.server.failServedCopy(f.stream, copy0, errFatalRead) {
		t.Fatal("A's fatal read should retire copy 0")
	}
	close(file.release)
	waitDone(t, done)

	if rec.Code != http.StatusTemporaryRedirect || !strings.HasSuffix(rec.Header().Get("Location"), f.slot) {
		t.Fatalf("B answered %d (Location %q), want a redirect back to its slot", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Body.Len() > 0 && strings.Contains(rec.Header().Get("Content-Type"), "video") {
		t.Fatal("B served the retired copy as media")
	}
	if copy0.OnceDone(onceSuccessRecorded) {
		t.Fatal("the retired copy was committed as good")
	}
	if next, ok := f.server.sessionAfterSlotMoved(context.Background(), f.slot, f.stream); !ok || next.Release().Indexer != "DrunkenSlug" {
		t.Fatal("the reconnect should find the slot on the next copy")
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed")
}

// 7 (serve side). A client that leaves during the first read gets nothing
// more written: no redirect, and the slot is left alone.
func TestClientLeavingBeforeHeadersWritesNothing(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	copy0 := f.resolve(t, "NZBGeek")
	file := newGatedFile()
	ctx, cancel := context.WithCancel(context.Background())
	rec, done := f.serveResolved(t, ctx, copy0, file, file.size)
	waitFor(t, file.reading, done, "B's first read")
	cancel()
	close(file.release)
	waitDone(t, done)

	if rec.Header().Get("Location") != "" || rec.Body.Len() > 0 {
		t.Fatalf("a departed client was answered: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if f.manager.PeekSession(f.slot) != copy0 || f.manager.GetSlotFailedDuringPlayback(f.slot) || f.manager.SlotCopyIndex(f.slot) != 0 {
		t.Fatal("a departed client changed the slot")
	}
}

// 7. Client cancellation wins over recovery: with the client gone, the
// carry-on step does not hand back the replacement, so nothing registers a
// startup hold or a History row on it.
func TestCarryOnStopsForADepartedClient(t *testing.T) {
	f, mgr := newHistoryFixture(t, 2)
	copy0 := f.resolve(t, "NZBGeek")
	f.server.failServedCopy(f.stream, copy0, errFatalRead)
	replacement, ok := f.server.sessionAfterSlotMoved(context.Background(), f.slot, f.stream)
	if !ok {
		t.Fatal("the slot should rebuild on copy 1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/play/"+f.slot, nil).WithContext(ctx)

	if next, _, ok := f.server.carryOnAfterSessionLeft(httptest.NewRecorder(), req, f.server.runtime(), f.stream, copy0, f.slot); ok {
		t.Fatalf("carried on with %p for a client that left", next)
	}
	if f.manager.PeekSession(f.slot) != replacement {
		t.Fatal("the replacement should be left as it was")
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed")
	requireNoHold(t, f, replacement)
}

// 8. A prepare that fails with an ordinary error after the client already
// left is not judged: no retirement, no slot failure, no fallback, no verdict.
func TestPrepareFailureAfterClientLeftIsNotAVerdict(t *testing.T) {
	f := newCopyRetireFixture(t, 2).withFailingPrepare(t)
	copy0 := f.resolve(t, "NZBGeek")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+f.slot, nil).WithContext(ctx)

	if _, ok := f.server.resolvePlaybackSlot(rec, req, f.stream, f.slot); ok {
		t.Fatal("the prepare should have failed")
	}
	if f.manager.PeekSession(f.slot) != copy0 || isClosedSession(copy0) {
		t.Fatal("a departed client's failed prepare retired the copy")
	}
	if f.manager.SlotCopyIndex(f.slot) != 0 || f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a departed client's failed prepare moved or failed the slot")
	}
	if copy0.OnceDone(onceFailureRecorded) {
		t.Fatal("a departed client's failed prepare recorded a verdict")
	}
}

// laterReadGate lets the first read (the range prime) through and holds the
// second — ServeContent's first, after the status line is committed.
type laterReadGate struct {
	*diagFile
	mu               sync.Mutex
	reads            int
	reading, release chan struct{}
}

func (f *laterReadGate) Read(p []byte) (int, error) {
	f.mu.Lock()
	f.reads++
	second := f.reads == 2
	f.mu.Unlock()
	if second {
		close(f.reading)
		<-f.release
	}
	return f.diagFile.Read(p)
}

// 11. A retirement after the headers went out cannot be answered again: the
// response keeps its committed status, with no redirect written over it.
func TestCopyRetiredAfterHeadersWritesNoSecondResponse(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	copy0 := f.resolve(t, "NZBGeek")
	copy0.Once(onceFallbackPrefetched)
	file := &laterReadGate{diagFile: &diagFile{size: 1 << 20}, reading: make(chan struct{}), release: make(chan struct{})}
	startup := f.manager.BeginSessionStartup(copy0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+f.slot, nil)
	req.Header.Set("Range", "bytes=0-")
	done := make(chan struct{})
	go func() {
		defer close(done)
		pctx, cancel := f.server.playbackContext(req, copy0)
		defer cancel()
		defer startup.Release()
		f.server.servePlaybackStream(rec, req, f.stream, &resolvedPlayback{
			session: copy0, sessionID: copy0.ID, stream: file,
			name: "video.mkv", size: file.size, ctx: pctx, cancel: cancel, startup: startup,
		}, time.Now())
	}()
	waitFor(t, file.reading, done, "the serve's first body read")
	f.server.failServedCopy(f.stream, copy0, errFatalRead)
	close(file.release)
	waitDone(t, done)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want the committed 206 to stand", rec.Code)
	}
	if loc := rec.Result().Header.Get("Location"); loc != "" {
		t.Fatalf("a redirect was written after the headers: %q", loc)
	}
}

// As above, but the next copy cannot be built: B is not sent back to a slot
// that would answer its reconnect with a 404. It is walked on like an
// undeliverable range — here, with no fallback left, to the error video.
func TestCopyRetiredBeforeHeadersWithNoUsableCopyWalksOn(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	cached, _ := f.server.playlistCache.Load(f.key.CacheKey())
	cached.(*playlistCacheEntry).result.Candidates[0].Release.Variants[0].Link = ""
	copy0 := f.resolve(t, "NZBGeek")
	file := newGatedFile()
	rec, done := f.serveResolved(t, context.Background(), copy0, file, file.size)
	waitFor(t, file.reading, done, "B's first read")
	f.server.failServedCopy(f.stream, copy0, errFatalRead)
	close(file.release)
	waitDone(t, done)

	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/error/") {
		t.Fatalf("B answered %d (Location %q), want the terminal error video", rec.Code, loc)
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("B gave the slot up on another request's behalf")
	}
}

// fatalAfterGate fails its first read with a fatal stream error once released.
type fatalAfterGate struct {
	*diagFile
	reading, release chan struct{}
	once             sync.Once
}

func (f *fatalAfterGate) Read([]byte) (int, error) {
	f.once.Do(func() { close(f.reading) })
	<-f.release
	return 0, errFatalRead
}

// A fatal first read is a verdict even when the client leaves at the same
// moment: the client-gone exit is only for inconclusive reads.
func TestFatalPrimeReadIsAVerdictEvenAfterTheClientLeft(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	copy0 := f.resolve(t, "NZBGeek")
	file := &fatalAfterGate{diagFile: &diagFile{size: 1 << 20}, reading: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	_, done := f.serveResolved(t, ctx, copy0, file, file.size)
	waitFor(t, file.reading, done, "B's first read")
	cancel()
	close(file.release)
	waitDone(t, done)

	if !copy0.OnceDone(onceFailureRecorded) || f.manager.SlotCopyIndex(f.slot) != 1 {
		t.Fatal("a fatal first read was dropped because the client left")
	}
}

// B's own fatal first read retires copy 0 before any header: B's client, still
// connected, is kept on its release — sent back to the slot, which holds the
// next copy — not walked to a different release.
func TestFatalPrimeThatRetiresTheCopyKeepsTheClientOnItsRelease(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	copy0 := f.resolve(t, "NZBGeek")
	file := &fatalAfterGate{diagFile: &diagFile{size: 1 << 20}, reading: make(chan struct{}), release: make(chan struct{})}
	rec, done := f.serveResolved(t, context.Background(), copy0, file, file.size)
	waitFor(t, file.reading, done, "B's first read")
	close(file.release)
	waitDone(t, done)

	if !copy0.CopyRetired() {
		t.Fatal("the fatal read should retire copy 0")
	}
	if rec.Code != http.StatusTemporaryRedirect || !strings.HasSuffix(rec.Header().Get("Location"), f.slot) {
		t.Fatalf("B answered %d (Location %q), want a redirect back to its slot", rec.Code, rec.Header().Get("Location"))
	}
}
