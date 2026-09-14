package stremio

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStreamMonitorNilStreamSeekReturnsErrorInsteadOfPanicking guards against
// the v6.0.0 regression where a StreamMonitor built around a nil
// io.ReadSeekCloser panicked as soon as net/http.ServeContent called Seek on
// it (calling a method on a nil interface has no method table to dispatch
// through, so it panics with "invalid memory address or nil pointer
// dereference" at the call site) — crashing the connection instead of
// answering the request with an error.
func TestStreamMonitorNilStreamSeekReturnsErrorInsteadOfPanicking(t *testing.T) {
	monitor := &StreamMonitor{sessionID: "nil-stream-session"}

	n, err := monitor.Seek(0, 0)

	if n != 0 {
		t.Fatalf("expected offset 0 from a nil stream, got %d", n)
	}
	if !errors.Is(err, errNoUnderlyingStream) {
		t.Fatalf("expected errNoUnderlyingStream, got %v", err)
	}
}

func TestStreamMonitorNilStreamReadReturnsErrorInsteadOfPanicking(t *testing.T) {
	monitor := &StreamMonitor{sessionID: "nil-stream-session"}

	buf := make([]byte, 16)
	n, err := monitor.Read(buf)

	if n != 0 {
		t.Fatalf("expected 0 bytes read from a nil stream, got %d", n)
	}
	if !errors.Is(err, errNoUnderlyingStream) {
		t.Fatalf("expected errNoUnderlyingStream, got %v", err)
	}
}

// TestStreamMonitorNilStreamServeContentReturns500NotPanic exercises the
// exact call path that crashed in the field: net/http.ServeContent, which
// always Seeks the body to determine range/content-length before reading it.
// Before the fix this took the whole connection down; after it, the client
// gets a plain 500 and the server keeps serving other requests.
func TestStreamMonitorNilStreamServeContentReturns500NotPanic(t *testing.T) {
	monitor := &StreamMonitor{sessionID: "nil-stream-session"}

	req := httptest.NewRequest(http.MethodGet, "/play/test", nil)
	rec := httptest.NewRecorder()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ServeContent panicked instead of returning an error: %v", r)
		}
	}()

	http.ServeContent(rec, req, "movie.mkv", time.Time{}, monitor)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from a nil underlying stream, got %d", rec.Code)
	}
}
