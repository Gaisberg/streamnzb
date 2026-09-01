package ffprobe

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// probeStreamServer serves one io.ReadSeeker over a loopback HTTP listener so
// ffprobe can probe it as a seekable input. A pipe forces ffprobe to read
// strictly forward, which makes moov-at-end MP4/MOV unprobeable and pays for
// the whole -probesize window even when the header is tiny; over ranges it
// fetches exactly the boxes it needs.
//
// The listener binds 127.0.0.1 on an ephemeral port and answers only a
// random-token path, so nothing else on the host can fetch the stream during
// the probe window. Handlers serialize on one mutex — the seeker is shared
// state — and Close waits for the in-flight handler the same way the pipe
// path's cmd.Run waited for its stdin copy.
type probeStreamServer struct {
	rs   io.ReadSeeker
	ln   net.Listener
	srv  *http.Server
	path string

	mu sync.Mutex // serializes handlers on rs; held for the whole ServeContent

	errMu   sync.Mutex
	lastErr error
}

func newProbeStreamServer(rs io.ReadSeeker) (*probeStreamServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		_ = ln.Close()
		return nil, err
	}
	s := &probeStreamServer{
		rs:   rs,
		ln:   ln,
		path: "/" + hex.EncodeToString(token[:]),
	}
	s.srv = &http.Server{Handler: http.HandlerFunc(s.handle)}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *probeStreamServer) URL() string {
	return fmt.Sprintf("http://%s%s", s.ln.Addr().String(), s.path)
}

// probeWriteStallBudget is how long one Write to ffprobe may sit on a full
// socket before the handler gives up on that connection. ffmpeg's http seek
// opens the NEW connection before closing the old one; a handler blocked
// writing to the abandoned connection while holding the seeker would deadlock
// the seek until the probe's exec timeout killed everything (observed as
// validate_ms pinned at exactly the timeout). The deadline breaks the cycle:
// the stalled write fails, the handler releases the seeker, the seek proceeds.
var probeWriteStallBudget = 3 * time.Second

// probeOpenRangeChunk caps how much of the file an open-ended range request
// ("bytes=X-") is answered with. ffprobe rarely reads more than this before
// seeking away, and every unread byte is a byte the handler can end up
// stalled on; a short valid 206 makes ffmpeg simply issue the next range.
const probeOpenRangeChunk = int64(4 << 20)

var openEndedRangeRE = regexp.MustCompile(`^bytes=(\d+)-$`)

func (s *probeStreamServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != s.path {
		http.NotFound(w, r)
		return
	}
	if m := openEndedRangeRE.FindStringSubmatch(r.Header.Get("Range")); m != nil {
		if start, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+probeOpenRangeChunk-1))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Explicit type skips ServeContent's sniffing read; ffprobe identifies the
	// container from content, not the header.
	w.Header().Set("Content-Type", "application/octet-stream")
	dw := &stallGuardedWriter{w: w, rc: http.NewResponseController(w)}
	http.ServeContent(dw, r, "", time.Time{}, &errRecordingSeeker{rs: s.rs, record: s.recordErr})
}

// stallGuardedWriter arms a fresh write deadline before every Write, so a
// connection whose client stopped reading fails within probeWriteStallBudget
// instead of holding the shared seeker forever.
type stallGuardedWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (g *stallGuardedWriter) Header() http.Header { return g.w.Header() }

func (g *stallGuardedWriter) WriteHeader(code int) { g.w.WriteHeader(code) }

func (g *stallGuardedWriter) Write(p []byte) (int, error) {
	_ = g.rc.SetWriteDeadline(time.Now().Add(probeWriteStallBudget))
	return g.w.Write(p)
}

// recordErr keeps the underlying stream's failure so it survives ffprobe's
// opaque non-zero exit, exactly like recordingReader does on the pipe path.
func (s *probeStreamServer) recordErr(err error) {
	if err == nil || err == io.EOF {
		return
	}
	s.errMu.Lock()
	if s.lastErr == nil {
		s.lastErr = err
	}
	s.errMu.Unlock()
}

func (s *probeStreamServer) LastErr() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

// Close stops the listener and its connections, then waits out any handler
// still holding the seeker so the caller can rewind the stream without racing.
func (s *probeStreamServer) Close() error {
	err := s.srv.Close()
	s.mu.Lock()
	//lint:ignore SA2001 the empty critical section is the wait itself
	s.mu.Unlock()
	return err
}

type errRecordingSeeker struct {
	rs     io.ReadSeeker
	record func(error)
}

func (e *errRecordingSeeker) Read(p []byte) (int, error) {
	n, err := e.rs.Read(p)
	e.record(err)
	return n, err
}

func (e *errRecordingSeeker) Seek(offset int64, whence int) (int64, error) {
	n, err := e.rs.Seek(offset, whence)
	e.record(err)
	return n, err
}
