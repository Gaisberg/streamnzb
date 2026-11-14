package stremio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
)

const (
	loaderSegSize  = 64 << 10
	loaderSegCount = 12
	loaderBadSeg   = 5
	loaderBadAt    = int64(loaderBadSeg * loaderSegSize)
)

// corruptSegmentFetcher serves a release whose article loaderBadSeg decodes as
// corrupt on every provider. That article's fetches wait for gate, so a test
// decides when the fatal read happens.
type corruptSegmentFetcher struct {
	gate chan struct{}
}

func (f *corruptSegmentFetcher) FetchSegment(ctx context.Context, seg *nzb.Segment, _ []string) (pool.SegmentData, error) {
	if seg.Number-1 == loaderBadSeg {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return pool.SegmentData{}, ctx.Err()
		}
		return pool.SegmentData{}, fmt.Errorf("fetch segment %s: %w", seg.ID, decode.ErrCorrupt)
	}
	body := make([]byte, loaderSegSize)
	for i := range body {
		body[i] = byte(seg.Number)
	}
	return pool.SegmentData{Body: body, ProviderHost: "news.example"}, nil
}

// readProbe sits between the loader stream and the serve. It reports when the
// serve asks for the bad article's first byte (every byte before it has been
// read), and records how the read that ended the stream returned.
type readProbe struct {
	io.ReadSeekCloser
	mu        sync.Mutex
	pos       int64
	once      sync.Once
	atBad     chan struct{}
	errN      int
	err       error
	lastGoodN int
}

func (p *readProbe) Seek(off int64, whence int) (int64, error) {
	n, err := p.ReadSeekCloser.Seek(off, whence)
	p.mu.Lock()
	p.pos = n
	p.mu.Unlock()
	return n, err
}

func (p *readProbe) Read(b []byte) (int, error) {
	p.mu.Lock()
	if p.pos == loaderBadAt {
		p.once.Do(func() { close(p.atBad) })
	}
	p.mu.Unlock()
	n, err := p.ReadSeekCloser.Read(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pos += int64(n)
	if err != nil && p.err == nil {
		p.err, p.errN = err, n
	} else if err == nil && n > 0 {
		p.lastGoodN = n
	}
	return n, err
}

func (p *readProbe) result() (lastGoodN, errN int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastGoodN, p.errN, p.err
}

// serveLoaderCopy serves sess from a real loader stream over HTTP, the way a
// play request reads a release: playback read-ahead and pipelined segment
// delivery included. It returns the probe and a channel that closes when the
// serve has finished.
func serveLoaderCopy(t *testing.T, f copyRetireFixture, sess *session.Session, fetcher *corruptSegmentFetcher) (*readProbe, <-chan struct{}) {
	t.Helper()
	sizes := make([]nzb.Segment, loaderSegCount)
	for i := range sizes {
		sizes[i] = nzb.Segment{Number: i + 1, ID: fmt.Sprintf("<seg-%d>", i), Bytes: loaderSegSize}
	}
	// Not bound to the session: a read already in the loader when its copy is
	// retired is the straggler these tests are about.
	file := loader.NewFile(context.Background(), &nzb.File{Subject: "video.mkv", Groups: []string{"alt.test"}, Segments: sizes}, nil, fetcher)
	stream, err := file.OpenPlaybackStreamCtx(loader.WithSkipGapProbing(context.Background(), true))
	if err != nil {
		t.Fatalf("OpenPlaybackStreamCtx: %v", err)
	}
	probe := &readProbe{ReadSeekCloser: stream, atBad: make(chan struct{})}
	size := int64(loaderSegCount * loaderSegSize)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		ctx, cancel := context.WithCancel(r.Context())
		f.server.servePlaybackStream(w, r, f.stream, &resolvedPlayback{
			session: sess, sessionID: sess.ID, stream: probe,
			name: "video.mkv", size: size, ctx: ctx, cancel: cancel,
		}, time.Now())
	}))
	t.Cleanup(srv.Close)
	// Runs before srv.Close, which would wait for a serve a failed assertion left parked.
	t.Cleanup(func() {
		select {
		case <-fetcher.gate:
		default:
			close(fetcher.gate)
		}
		<-done
	})
	go func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header.Set("Range", "bytes=0-")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	return probe, done
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// The loader now returns the bytes it has before fetching the next article,
// so a broken article arrives as a read of zero bytes after a short read
// rather than together with the bytes before it. That fatal read must still
// retire the copy, leave the next copy to the reconnect, and count only the
// bytes actually served, on the copy that served them.
func TestShortReadThenFatalReadRetiresTheServedCopy(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	t.Cleanup(f.server.Shutdown)
	t.Cleanup(f.server.scrobbleSends.Wait)
	a := f.resolve(t, "NZBGeek")
	a.Once(onceFallbackPrefetched)

	fetcher := &corruptSegmentFetcher{gate: make(chan struct{})}
	close(fetcher.gate)
	probe, done := serveLoaderCopy(t, f, a, fetcher)
	waitClosed(t, done, "the serve to end on the corrupt article")

	lastGoodN, errN, err := probe.result()
	if !decode.IsCorrupt(err) || !isFatalStreamErr(err) {
		t.Fatalf("stream ended with %v, want a fatal corruption error", err)
	}
	if errN != 0 || lastGoodN == 0 {
		t.Fatalf("fatal read returned %d bytes after a %d-byte read; want a short read and then an empty fatal read", errN, lastGoodN)
	}
	if f.manager.PeekSession(f.slot) == a {
		t.Fatal("the fatal read did not retire the served copy")
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("with a copy left, the fatal read failed the whole slot")
	}
	if got := a.BytesRead(); got != loaderBadAt {
		t.Fatalf("served copy bytes=%d, want the %d bytes before the corrupt article", got, loaderBadAt)
	}
	if got := a.CurrentActivePlays(); got != 0 {
		t.Fatalf("served copy plays=%d after its serve ended, want 0", got)
	}

	b := f.resolve(t, "DrunkenSlug")
	if got := f.manager.SlotCopyIndex(f.slot); got != 1 {
		t.Fatalf("reconnect landed on copy %d, want the next copy", got)
	}
	if got := b.BytesRead(); got != 0 {
		t.Fatalf("next copy bytes=%d before serving anything", got)
	}
}

// Copy A has served a short read and is waiting on the next article when
// another request retires it and the slot is rebuilt on copy B. When A's
// read finally fails, that error is about A alone.
func TestStaleFatalReadAfterShortReadLeavesTheReplacementAlone(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	t.Cleanup(f.server.Shutdown)
	t.Cleanup(f.server.scrobbleSends.Wait)
	a := f.resolve(t, "NZBGeek")
	a.Once(onceFallbackPrefetched)

	fetcher := &corruptSegmentFetcher{gate: make(chan struct{})}
	probe, done := serveLoaderCopy(t, f, a, fetcher)
	waitClosed(t, probe.atBad, "copy A to serve the bytes before the corrupt article")

	// Another request on copy A reports it broken and moves the slot on.
	if !f.server.failServedCopy(f.stream, a, fmt.Errorf("read: %w", decode.ErrCorrupt)) {
		t.Fatal("the other request's fatal read was not recorded")
	}
	b := f.resolve(t, "DrunkenSlug")
	f.manager.MarkPlaybackValidated(b)
	f.manager.StartPlayback(b, "10.0.0.2")
	const bBytes = 7
	b.AddBytesRead(bBytes)

	close(fetcher.gate)
	waitClosed(t, done, "copy A's serve to end")

	if _, _, err := probe.result(); !isFatalStreamErr(err) {
		t.Fatalf("copy A ended with %v, want its fatal read", err)
	}
	if f.manager.PeekSession(f.slot) != b || f.manager.SlotCopyIndex(f.slot) != 1 {
		t.Fatal("copy A's stale read moved the slot off copy B")
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("copy A's stale read failed the slot copy B plays in")
	}
	select {
	case <-b.Done():
		t.Fatal("copy A's stale read closed copy B")
	default:
	}
	if got := b.BytesRead(); got != bBytes {
		t.Fatalf("copy B bytes=%d, want its own %d", got, bBytes)
	}
	if got := b.CurrentActivePlays(); got != 1 || !b.IsPlaybackValidated() {
		t.Fatalf("copy B plays=%d validated=%v, want its own play and validation", got, b.IsPlaybackValidated())
	}
	if got := a.BytesRead(); got != loaderBadAt {
		t.Fatalf("copy A bytes=%d, want the %d bytes it served", got, loaderBadAt)
	}
}

// A provider pool shut down under a running stream (a reload that drops or
// fails to revalidate the provider) answers ErrPoolClosed. Through the real
// pool and loader, that reads as "no provider could be asked", never as a
// missing or corrupt article, so it must not condemn the copy.
func TestClosedProviderPoolIsNotABadCopyVerdict(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	served := f.resolve(t, "NZBGeek")

	clientPool := nntp.NewClientPool("127.0.0.1", 1, false, "user", "pass", 1)
	clientPool.Shutdown()
	if _, err := clientPool.Get(context.Background()); !errors.Is(err, nntp.ErrPoolClosed) {
		t.Fatalf("closed client pool returned %v, want ErrPoolClosed", err)
	}
	usenet, err := pool.NewPool(&pool.Config{Providers: []pool.ProviderConfig{{ID: "closed", ClientPool: clientPool}}})
	if err != nil {
		t.Fatal(err)
	}
	segs := make([]nzb.Segment, 4)
	for i := range segs {
		segs[i] = nzb.Segment{Number: i + 1, ID: fmt.Sprintf("<closed-%d@test>", i), Bytes: 1024}
	}
	file := loader.NewFile(context.Background(), &nzb.File{Subject: "video.mkv", Groups: []string{"alt.test"}, Segments: segs}, nil, usenet)

	// The first article, which is fatal when missing, and one past it, where a
	// miss would be a zero-fill hole.
	for _, index := range []int{0, 2} {
		_, readErr := file.DownloadSegment(context.Background(), index)
		if readErr == nil {
			t.Fatalf("segment %d downloaded from a closed pool", index)
		}
		if isFatalStreamErr(readErr) {
			t.Fatalf("segment %d: closed pool read as a fatal stream error: %v", index, readErr)
		}
		if f.server.failServedCopy(f.stream, served, readErr) {
			t.Fatalf("segment %d: closed pool recorded a copy failure: %v", index, readErr)
		}
	}
	if file.ZeroFilledSegments() != 0 {
		t.Fatal("a closed pool was taken as evidence of a missing article")
	}
	if f.manager.PeekSession(f.slot) != served || f.manager.SlotCopyIndex(f.slot) != 0 {
		t.Fatal("a closed pool moved the slot off its copy")
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a closed pool failed the slot")
	}
}
