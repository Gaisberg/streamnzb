package stremio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/session"
)

// gatedFile holds every read until release is closed, closing reading on the
// first: a serve reads only after it has started its playback.
type gatedFile struct {
	*diagFile
	once             sync.Once
	reading, release chan struct{}
}

func (f *gatedFile) Read(p []byte) (int, error) {
	f.once.Do(func() { close(f.reading) })
	<-f.release
	return f.diagFile.Read(p)
}

// A serve keeps its accounting on the session it was handed even when the slot
// has meanwhile been rebuilt under the same id (a copy retry, an eviction):
// its start, validation, bytes and end all land on that session, and the
// replacement's own playback is left exactly as it was.
func TestServeAccountsOnlyToItsOwnSessionAfterSlotReplacement(t *testing.T) {
	h := newScrobbleHarness(t, harnessOpts{off: true, unlinked: true})
	manager := h.server.sessionManager
	served := h.sess

	manager.DeleteSession(served.ID)
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<b>", Bytes: 10}}}}}
	replacement, err := manager.CreateSession(served.ID, data, nil, &session.AvailReportMeta{})
	if err != nil {
		t.Fatal(err)
	}
	manager.StartPlayback(replacement, "10.0.0.2")

	file := &gatedFile{diagFile: &diagFile{size: diagFileSize}, reading: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(file.release) }) }
	serveDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serveDone)
		ctx, cancel := context.WithCancel(r.Context())
		h.server.servePlaybackStream(w, r, h.stream, &resolvedPlayback{
			session: served, sessionID: served.ID, stream: file,
			name: "video.mkv", size: diagFileSize, ctx: ctx, cancel: cancel,
		}, time.Now())
	}))
	t.Cleanup(srv.Close)
	// Registered after srv.Close so it runs first: Close waits for the serve,
	// which a failed assertion would otherwise leave held.
	t.Cleanup(func() {
		release()
		<-serveDone
	})
	go func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header.Set("Range", "bytes=0-")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	select {
	case <-file.reading:
	case <-serveDone:
		t.Fatal("the serve ended without reading")
	}
	// Mid-serve, so a start and end that both landed on the replacement
	// (2 then back to 1) cannot pass for correct accounting.
	if got := served.CurrentActivePlays(); got != 1 {
		t.Fatalf("served session plays=%d while its serve is open, want 1", got)
	}
	if got := replacement.CurrentActivePlays(); got != 1 {
		t.Fatalf("replacement plays=%d while the old serve is open, want its own 1", got)
	}
	release()
	<-serveDone

	if got := replacement.CurrentActivePlays(); got != 1 {
		t.Fatalf("replacement plays=%d after the old serve, want its own 1", got)
	}
	if replacement.IsPlaybackValidated() {
		t.Fatal("the old serve validated the replacement")
	}
	if got := replacement.BytesRead(); got != 0 {
		t.Fatalf("replacement bytes=%d, want none of the old serve's", got)
	}
	if got := served.CurrentActivePlays(); got != 0 {
		t.Fatalf("served session plays=%d after its serve ended, want 0", got)
	}
	if served.BytesRead() == 0 {
		t.Fatal("the serve's bytes were not counted on its own session")
	}
}
