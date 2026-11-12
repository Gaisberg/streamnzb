package stremio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/media/unpack"
)

// holeFile streams holeAt bytes, signals atHole and waits for fail; then it
// streams tail more bytes and fails with a fatal read: a copy that turns out
// to be broken partway in.
type holeFile struct {
	*diagFile
	holeAt, tail int64
	once         sync.Once
	atHole, fail chan struct{}
}

func (f *holeFile) Read(p []byte) (int, error) {
	f.diagFile.mu.Lock()
	pos := f.diagFile.pos
	f.diagFile.mu.Unlock()
	end := f.holeAt
	if pos >= f.holeAt {
		f.once.Do(func() { close(f.atHole) })
		<-f.fail
		end += f.tail
	}
	if pos >= end {
		return 0, fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills)
	}
	return f.diagFile.Read(p[:min(int64(len(p)), end-pos)])
}

// Two range requests serve copy A when it breaks. The first one's fatal read
// retires A and the slot is rebuilt on copy B, which starts playing. The
// second request on A only finishes after that, with its own fatal read. Its
// failure, end of playback and bytes are about A alone: B keeps its slot,
// plays, validation and bytes, and the slot is not failed.
func TestStragglingServeOnARetiredCopyLeavesTheNextCopyAlone(t *testing.T) {
	f := newCopyRetireFixture(t, 2)
	t.Cleanup(f.server.Shutdown)
	t.Cleanup(f.server.scrobbleSends.Wait)
	a := f.resolve(t, "NZBGeek")

	// The second serve keeps reading after B takes the slot, so bytes that
	// followed the slot id would land on B.
	const holeAt, tail = 256 << 10, 64 << 10
	files := map[string]*holeFile{}
	done := map[string]chan struct{}{}
	for name, tail := range map[string]int64{"/1": 0, "/2": tail} {
		files[name] = &holeFile{diagFile: &diagFile{size: diagFileSize}, holeAt: holeAt, tail: tail, atHole: make(chan struct{}), fail: make(chan struct{})}
		done[name] = make(chan struct{})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done[r.URL.Path])
		ctx, cancel := context.WithCancel(r.Context())
		f.server.servePlaybackStream(w, r, f.stream, &resolvedPlayback{
			session: a, sessionID: a.ID, stream: files[r.URL.Path],
			name: "video.mkv", size: diagFileSize, ctx: ctx, cancel: cancel,
		}, time.Now())
	}))
	t.Cleanup(srv.Close)
	// Runs before srv.Close, which waits for serves a failed assertion left parked.
	t.Cleanup(func() {
		for name, file := range files {
			select {
			case <-file.fail:
			default:
				close(file.fail)
			}
			<-done[name]
		}
	})
	for name := range files {
		go func() {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+name, nil)
			req.Header.Set("Range", "bytes=0-")
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	for name, file := range files {
		select {
		case <-file.atHole:
		case <-done[name]:
			t.Fatalf("serve %s ended before reaching the hole", name)
		}
	}
	if got := a.CurrentActivePlays(); got != 2 {
		t.Fatalf("copy A plays=%d with both serves open, want 2", got)
	}

	// The first serve's fatal read retires A.
	close(files["/1"].fail)
	<-done["/1"]
	if f.manager.PeekSession(f.slot) == a {
		t.Fatal("the fatal read did not retire copy A")
	}

	// The reconnect rebuilds the slot on copy B, which starts playing.
	b := f.resolve(t, "DrunkenSlug")
	f.manager.MarkPlaybackValidated(b)
	f.manager.StartPlayback(b, "10.0.0.2")
	const bBytes = 7
	b.AddBytesRead(bBytes)

	// A's second serve finishes now, after B became current.
	close(files["/2"].fail)
	<-done["/2"]

	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("the straggling serve on copy A failed the slot copy B plays in")
	}
	if f.manager.PeekSession(f.slot) != b || f.manager.SlotCopyIndex(f.slot) != 1 {
		t.Fatal("the straggling serve on copy A moved the slot off copy B")
	}
	select {
	case <-b.Done():
		t.Fatal("the straggling serve on copy A closed copy B")
	default:
	}
	if got := b.CurrentActivePlays(); got != 1 {
		t.Fatalf("copy B plays=%d, want its own 1", got)
	}
	if !b.IsPlaybackValidated() {
		t.Fatal("copy B lost its validation")
	}
	if got := b.BytesRead(); got != bBytes {
		t.Fatalf("copy B bytes=%d, want its own %d", got, bBytes)
	}
	if got := a.CurrentActivePlays(); got != 0 {
		t.Fatalf("copy A plays=%d after both serves ended, want 0", got)
	}
	if got := a.BytesRead(); got != 2*holeAt+tail {
		t.Fatalf("copy A bytes=%d, want both serves' %d", got, 2*holeAt+tail)
	}
	if f.manager.FailSlotUnlessReplaced(a) || f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a slot verdict about copy A failed the slot copy B replaced it in")
	}
}
