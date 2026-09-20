package stremio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamnzb/pkg/session"
)

type testReadResult struct {
	data []byte
	err  error
}

type testReadSeekCloser struct {
	reads []testReadResult
	idx   int
}

func (t *testReadSeekCloser) Read(p []byte) (int, error) {
	if t.idx >= len(t.reads) {
		return 0, io.EOF
	}
	res := t.reads[t.idx]
	t.idx++
	n := copy(p, res.data)
	return n, res.err
}

func (t *testReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	return 0, nil
}

func (t *testReadSeekCloser) Close() error {
	return nil
}

type bytesReadSeekCloser struct {
	*bytes.Reader
}

func (b *bytesReadSeekCloser) Close() error {
	return nil
}

func TestBufferedResponseWriterSnapshotTracksStatusBytesAndHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	bw := newBufferedResponseWriter(recorder, 8)
	bw.Header().Set("Content-Range", "bytes 5-7/8")
	bw.Header().Set("Content-Length", "3")
	bw.Header().Set("Content-Type", "video/x-matroska")
	bw.Header().Set("Accept-Ranges", "bytes")

	bw.WriteHeader(http.StatusPartialContent)
	if _, err := bw.Write([]byte("abc")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	bw.Flush()

	snap := bw.Snapshot()
	if snap.StatusCode != http.StatusPartialContent {
		t.Fatalf("expected status %d, got %d", http.StatusPartialContent, snap.StatusCode)
	}
	if !snap.WroteHeader {
		t.Fatal("expected wroteHeader to be true")
	}
	if snap.ContentRange != "bytes 5-7/8" {
		t.Fatalf("expected content range %q, got %q", "bytes 5-7/8", snap.ContentRange)
	}
	if snap.ContentLength != "3" {
		t.Fatalf("expected content length %q, got %q", "3", snap.ContentLength)
	}
	if snap.ContentType != "video/x-matroska" {
		t.Fatalf("expected content type %q, got %q", "video/x-matroska", snap.ContentType)
	}
	if snap.AcceptRanges != "bytes" {
		t.Fatalf("expected accept ranges %q, got %q", "bytes", snap.AcceptRanges)
	}
	if snap.BytesWritten != 3 {
		t.Fatalf("expected 3 bytes written, got %d", snap.BytesWritten)
	}
	if snap.WriteCalls != 1 {
		t.Fatalf("expected 1 write call, got %d", snap.WriteCalls)
	}
	if snap.FlushCalls != 1 {
		t.Fatalf("expected 1 flush call, got %d", snap.FlushCalls)
	}
	if snap.FlushError != "" {
		t.Fatalf("expected empty flush error, got %q", snap.FlushError)
	}
}

func TestMediaContentTypeReturnsExpectedTypes(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "mkv", file: "movie.mkv", want: "video/x-matroska"},
		{name: "avi", file: "movie.avi", want: "video/x-msvideo"},
		{name: "mp4", file: "movie.mp4", want: "video/mp4"},
		{name: "m4v", file: "movie.m4v", want: "video/mp4"},
		{name: "unknown", file: "movie.ts", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaContentType(tc.file); got != tc.want {
				t.Fatalf("mediaContentType(%q) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}
}

func TestApplyMediaResponseHeadersSetsSharedMediaHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()

	applyMediaResponseHeaders(recorder, `folder/Test.Movie.2026.mkv`)

	if got := recorder.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Fatalf("Content-Type = %q, want %q", got, "video/x-matroska")
	}
	if got := recorder.Header().Get("Content-Disposition"); got != `inline; filename="Test.Movie.2026.mkv"` {
		t.Fatalf("Content-Disposition = %q, want %q", got, `inline; filename="Test.Movie.2026.mkv"`)
	}
	if got := recorder.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want %q", got, "bytes")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
}

func TestStreamMonitorSnapshotTracksEOFWithoutErrorString(t *testing.T) {
	monitor := &StreamMonitor{
		ReadSeekCloser: &bytesReadSeekCloser{Reader: bytes.NewReader([]byte("abc"))},
		manager:        &session.Manager{},
		lastUpdate:     time.Now(),
	}

	buf := make([]byte, 4)
	if n, err := monitor.Read(buf); err != nil || n != 3 {
		t.Fatalf("first Read = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := monitor.Read(buf); !errors.Is(err, io.EOF) || n != 0 {
		t.Fatalf("second Read = (%d, %v), want (0, EOF)", n, err)
	}

	snap := monitor.Snapshot()
	if snap.BytesRead != 3 {
		t.Fatalf("expected 3 bytes read, got %d", snap.BytesRead)
	}
	if snap.ReadCalls != 2 {
		t.Fatalf("expected 2 read calls, got %d", snap.ReadCalls)
	}
	if !snap.SawEOF {
		t.Fatal("expected EOF to be recorded")
	}
	if snap.LastReadError != "" {
		t.Fatalf("expected empty last read error, got %q", snap.LastReadError)
	}
}

func TestStreamMonitorSnapshotTracksReadErrorAndReadErrorOnce(t *testing.T) {
	boom := errors.New("boom")
	callbackCalls := 0
	monitor := &StreamMonitor{
		ReadSeekCloser: &testReadSeekCloser{reads: []testReadResult{{data: []byte("ab"), err: boom}, {err: boom}}},
		manager:        &session.Manager{},
		lastUpdate:     time.Now(),
		onReadError: func(_ string, err error) {
			callbackCalls++
			if !errors.Is(err, boom) {
				t.Fatalf("callback error = %v, want boom", err)
			}
		},
	}

	buf := make([]byte, 4)
	if n, err := monitor.Read(buf); !errors.Is(err, boom) || n != 2 {
		t.Fatalf("first Read = (%d, %v), want (2, boom)", n, err)
	}
	if _, err := monitor.Read(buf); !errors.Is(err, boom) {
		t.Fatalf("second Read error = %v, want boom", err)
	}

	snap := monitor.Snapshot()
	if snap.BytesRead != 2 {
		t.Fatalf("expected 2 bytes read, got %d", snap.BytesRead)
	}
	if snap.ReadCalls != 2 {
		t.Fatalf("expected 2 read calls, got %d", snap.ReadCalls)
	}
	if snap.SawEOF {
		t.Fatal("did not expect EOF to be recorded")
	}
	if snap.LastReadError != boom.Error() {
		t.Fatalf("expected last read error %q, got %q", boom.Error(), snap.LastReadError)
	}
	if callbackCalls != 1 {
		t.Fatalf("expected onReadError to be called once, got %d", callbackCalls)
	}
}

func TestClassifyProbeLikeServeDetectsTailEOFProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/play/test", nil)

	probeLike, reason := classifyProbeLikeServe(
		req,
		10<<30,
		"bytes=10737418239-",
		bufferedResponseSnapshot{},
		streamMonitorSnapshot{SawEOF: true},
		"",
	)

	if !probeLike {
		t.Fatal("expected tail EOF request to be classified as probe-like")
	}
	if reason != "tail_eof_probe" {
		t.Fatalf("expected tail_eof_probe reason, got %q", reason)
	}
}

func TestClassifyProbeLikeServeDetectsEmptyCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/play/test", nil).WithContext(ctx)
	cancel()

	probeLike, reason := classifyProbeLikeServe(
		req,
		10<<30,
		"bytes=0-",
		bufferedResponseSnapshot{},
		streamMonitorSnapshot{},
		"playback canceled",
	)

	if !probeLike {
		t.Fatal("expected empty canceled request to be classified as probe-like")
	}
	if reason != "empty_canceled_request" {
		t.Fatalf("expected empty_canceled_request reason, got %q", reason)
	}
}

func TestClassifyProbeLikeServeDoesNotClassifyActualPlayback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/play/test", nil)

	probeLike, reason := classifyProbeLikeServe(
		req,
		10<<30,
		"bytes=0-",
		bufferedResponseSnapshot{BytesWritten: 1024},
		streamMonitorSnapshot{BytesRead: 1024},
		"",
	)

	if probeLike {
		t.Fatalf("expected request with media bytes to be treated as playback, got reason %q", reason)
	}
}

func TestClassifyProbeLikeServeDetectsSmallTailEOFProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/play/test", nil)

	probeLike, reason := classifyProbeLikeServe(
		req,
		16994390140,
		"bytes=16994239897-",
		bufferedResponseSnapshot{BytesWritten: 140886},
		streamMonitorSnapshot{BytesRead: 140886, SawEOF: true},
		"",
	)

	if !probeLike {
		t.Fatal("expected small near-EOF request that hits EOF to be classified as probe-like")
	}
	if reason != "tail_small_eof_probe" {
		t.Fatalf("expected tail_small_eof_probe reason, got %q", reason)
	}
}

// http.ServeContent copies an exact byte count, so the read finishing a
// tail-to-EOF range need not return io.EOF — a probe the classifier used to
// let through as playback, and credit as watched to the end (#341).
func TestClassifyProbeLikeServeDetectsSmallTailReadWithoutEOF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/play/test", nil)

	probeLike, reason := classifyProbeLikeServe(
		req,
		12554040792,
		"bytes=12553854793-",
		bufferedResponseSnapshot{BytesWritten: 185999},
		streamMonitorSnapshot{BytesRead: 185999, SawEOF: false},
		"",
	)

	if !probeLike {
		t.Fatal("expected small near-EOF request to be classified as probe-like without an observed EOF")
	}
	if reason != "tail_small_eof_probe" {
		t.Fatalf("expected tail_small_eof_probe reason, got %q", reason)
	}
}

// A near-EOF range is only a probe while it stays small: a player that reopens
// close to the end and keeps reading past the probe window is playing.
func TestClassifyProbeLikeServeDoesNotClassifyLargeTailRead(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/play/test", nil)

	probeLike, reason := classifyProbeLikeServe(
		req,
		12554040792,
		"bytes=12553119192-",
		bufferedResponseSnapshot{BytesWritten: 900 << 10},
		streamMonitorSnapshot{BytesRead: 900 << 10},
		"",
	)

	if probeLike {
		t.Fatalf("expected a large near-EOF read to remain playback, got reason %q", reason)
	}
}
