package stremio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/session"
)

type closeCountingStream struct{ closes int }

func (c *closeCountingStream) Read([]byte) (int, error)       { return 0, io.EOF }
func (c *closeCountingStream) Seek(int64, int) (int64, error) { return 0, nil }
func (c *closeCountingStream) Close() error                   { c.closes++; return nil }

// newLiveSession builds a session through the manager, which is the only thing
// that gives it a context to cancel.
func newLiveSession(t *testing.T, manager *session.Manager, id string) *session.Session {
	t.Helper()
	data := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 10}}}}}
	sess, err := manager.CreateSession(id, data, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession(%s) returned error: %v", id, err)
	}
	return sess
}

func TestPlaybackContextEndsWithTheRequest(t *testing.T) {
	logger.Init("ERROR")
	s := &Server{}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/play/slot", nil).WithContext(reqCtx)

	ctx, cancel := s.playbackContext(r, &session.Session{ID: "slot"})
	defer cancel()

	if ctx.Err() != nil {
		t.Fatal("the playback context should start live")
	}
	cancelReq()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the playback context outlived the request")
	}
}

func TestPlaybackContextEndsWhenTheSessionCloses(t *testing.T) {
	logger.Init("ERROR")
	manager := session.NewManager(nil, time.Minute)
	defer manager.Shutdown()
	s := &Server{}

	sess := newLiveSession(t, manager, "slot")
	r := httptest.NewRequest(http.MethodGet, "/play/slot", nil)

	ctx, cancel := s.playbackContext(r, sess)
	defer cancel()

	// Closing a session from the dashboard has to abort playback and stop
	// downloading, rather than wait for the client to notice.
	sess.Close()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("closing the session did not cancel playback")
	}
}

func TestRedirectToResolvedSlotSendsTheClientOnAndReleasesTheStream(t *testing.T) {
	logger.Init("ERROR")
	s := &Server{baseURL: "http://addon.test:7000"}

	stream := &closeCountingStream{}
	_, cancel := context.WithCancel(context.Background())
	resolved := &resolvedPlayback{
		sessionID: "stream/movie/tt42/3",
		stream:    stream,
		cancel:    cancel,
	}

	r := httptest.NewRequest(http.MethodGet, "/play/stream/movie/tt42/0?next=1", nil)
	w := httptest.NewRecorder()

	s.redirectToResolvedSlot(w, r, &auth.Stream{Token: "tok"}, resolved, "stream/movie/tt42/0")

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTemporaryRedirect)
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "/play/stream/movie/tt42/3") {
		t.Fatalf("Location %q does not point at the slot that opened", location)
	}
	// The query has to survive: /next/ progression rides on it.
	if !strings.Contains(location, "next=1") {
		t.Fatalf("Location %q dropped the query string", location)
	}
	if w.Header().Get("Cache-Control") == "" {
		t.Fatal("a failover redirect must not be cacheable")
	}
	// The stream opened for the other slot is never served, so it must not be
	// left holding its provider connections.
	if stream.closes != 1 {
		t.Fatalf("stream closed %d times, want exactly 1", stream.closes)
	}
}

func TestPrimeRangeOrFailoverSkipsHEAD(t *testing.T) {
	logger.Init("ERROR")
	s := &Server{}

	// HEAD carries no body, so there is no range to promise and no read to
	// spend on proving it.
	r := httptest.NewRequest(http.MethodHead, "/play/slot", nil)
	w := httptest.NewRecorder()
	resolved := &resolvedPlayback{sessionID: "slot", stream: &closeCountingStream{}, size: 1024}

	onReadError := func(string, error) { t.Fatal("HEAD must not report a read error") }
	closeStream := func(string) { t.Fatal("HEAD must not close the stream") }

	if !s.primeRangeOrFailover(w, r, &auth.Stream{}, resolved, "bytes=0-", onReadError, closeStream) {
		t.Fatal("HEAD should be allowed to proceed")
	}
	if w.Body.Len() != 0 {
		t.Fatal("HEAD priming should not have written a response body")
	}
}

func TestNextFallbackSlotRespectsDisabledFailover(t *testing.T) {
	s := &Server{}
	disabled := false

	// With failover off there is nothing to walk to, and finding that out must
	// not cost a playlist lookup.
	sess, id := s.nextFallbackSlot(context.Background(), &session.Session{ID: "slot"}, &auth.Stream{EnableFailover: &disabled})
	if sess != nil || id != "" {
		t.Fatalf("expected no fallback, got %v / %q", sess, id)
	}
}

func TestRecordFailedSlotReleasesTheSessionAndMarksTheSlot(t *testing.T) {
	logger.Init("ERROR")
	manager := session.NewManager(nil, time.Minute)
	defer manager.Shutdown()
	s := &Server{sessionManager: manager}

	sess := newLiveSession(t, manager, "slot-broken")
	s.recordFailedSlot(sess, sess.ID, errors.New("archive is corrupt"))

	if !manager.GetSlotFailedDuringPlayback(sess.ID) {
		t.Fatal("a failed slot must be marked so the walk does not come back to it")
	}
	// The session goes with it; a retry re-resolves the slot from scratch.
	if _, err := manager.GetSession(sess.ID); err == nil {
		t.Fatal("the failed session was left in the manager")
	}
}

// A quota error proves nothing about the release, but the slot is still marked
// so the player moves on now instead of retrying a slot that just refused to
// open. What keeps that from being a verdict is elsewhere: the mark expires with
// the session TTL, and conclusiveBadRelease excludes quota errors from the
// durable bad-release verdict. Item 14 settled this; the guard that claimed
// otherwise is gone.
func TestRecordFailedSlotMarksThrottledSlotsToo(t *testing.T) {
	logger.Init("ERROR")
	manager := session.NewManager(nil, time.Minute)
	defer manager.Shutdown()
	s := &Server{sessionManager: manager}

	sess := newLiveSession(t, manager, "slot-throttled")
	s.recordFailedSlot(sess, sess.ID, errors.New("indexer api limit reached"))

	if !manager.GetSlotFailedDuringPlayback(sess.ID) {
		t.Fatal("a failed slot must be marked so the walk does not retry it immediately")
	}
}

func TestThrottledIndexerEarnsNoDurableVerdict(t *testing.T) {
	// The half that actually protects the release: a quota error must never be
	// recorded as a conclusive bad release.
	if conclusiveBadRelease(errors.New("indexer api limit reached")) {
		t.Fatal("a quota error was treated as proof the release is bad")
	}
	if !conclusiveBadRelease(errors.New("archive is corrupt")) {
		t.Fatal("a real failure stopped counting as a verdict")
	}
}
