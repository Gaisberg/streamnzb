package ffprobe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestProbeStreamServerServesRanges(t *testing.T) {
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i)
	}
	srv, err := newProbeStreamServer(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Full body.
	resp, err := http.Get(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(body, payload) {
		t.Fatalf("full read returned %d bytes, want %d", len(body), len(payload))
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatal("server does not advertise range support; ffprobe would not seek")
	}

	// Tail range — the moov-at-end access pattern.
	req, _ := http.NewRequest(http.MethodGet, srv.URL(), nil)
	req.Header.Set("Range", "bytes=4000-4095")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range request answered %d, want 206", resp.StatusCode)
	}
	if !bytes.Equal(body, payload[4000:]) {
		t.Fatal("range request returned wrong bytes")
	}

	// The random token path is required.
	resp, err = http.Get(fmt.Sprintf("http://%s/other", srv.ln.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path answered %d, want 404", resp.StatusCode)
	}
}

type failingSeeker struct {
	*bytes.Reader
	err error
}

func (f *failingSeeker) Read(p []byte) (int, error) { return 0, f.err }

func TestProbeStreamServerRecordsStreamError(t *testing.T) {
	cause := errors.New("segment unavailable: 430")
	srv, err := newProbeStreamServer(&failingSeeker{Reader: bytes.NewReader(make([]byte, 128)), err: cause})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	resp, err := http.Get(srv.URL())
	if err == nil {
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if got := srv.LastErr(); !errors.Is(got, cause) {
		t.Fatalf("LastErr = %v, want the stream's own failure", got)
	}
}

// zeroSeeker is a large all-zero file without the allocation.
type zeroSeeker struct{ size, off int64 }

func (z *zeroSeeker) Read(p []byte) (int, error) {
	if z.off >= z.size {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > z.size-z.off {
		n = z.size - z.off
	}
	for i := int64(0); i < n; i++ {
		p[i] = 0
	}
	z.off += n
	return int(n), nil
}

func (z *zeroSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		z.off = offset
	case io.SeekCurrent:
		z.off += offset
	case io.SeekEnd:
		z.off = z.size + offset
	}
	return z.off, nil
}

// ffmpeg's http seek opens the new connection BEFORE closing the old one. A
// handler blocked writing to the abandoned connection must not hold the seeker
// past the stall budget, or the seek deadlocks until ffprobe's exec timeout —
// the validate_ms=15002 regression this reproduces.
func TestProbeStreamServerSurvivesAbandonedConnection(t *testing.T) {
	oldBudget := probeWriteStallBudget
	probeWriteStallBudget = 300 * time.Millisecond
	defer func() { probeWriteStallBudget = oldBudget }()

	srv, err := newProbeStreamServer(&zeroSeeker{size: 512 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	// Connection 1: full GET, read almost nothing, then stop reading entirely
	// (socket buffers fill, the handler's Write blocks) — but keep it open.
	resp1, err := http.Get(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	if _, err := io.ReadFull(resp1.Body, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}

	// Connection 2: the seek. It must be answered despite connection 1.
	req, _ := http.NewRequest(http.MethodGet, srv.URL(), nil)
	req.Header.Set("Range", "bytes=536870000-")
	client := &http.Client{Timeout: 5 * time.Second}
	start := time.Now()
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("seek connection starved by the abandoned one: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusPartialContent {
		t.Fatalf("seek answered %d, want 206", resp2.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("seek returned no bytes")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("seek took %v; the stall budget did not free the seeker", took)
	}
}

// Open-ended ranges are answered in bounded chunks so an abandoning client
// leaves less stalled work behind; the 206 must still be correct.
func TestProbeStreamServerBoundsOpenEndedRanges(t *testing.T) {
	srv, err := newProbeStreamServer(&zeroSeeker{size: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL(), nil)
	req.Header.Set("Range", "bytes=1000-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", resp.StatusCode)
	}
	if int64(len(body)) != probeOpenRangeChunk {
		t.Fatalf("open-ended range served %d bytes, want the %d chunk", len(body), probeOpenRangeChunk)
	}
	if cr := resp.Header.Get("Content-Range"); cr != fmt.Sprintf("bytes 1000-%d/%d", 1000+probeOpenRangeChunk-1, int64(64<<20)) {
		t.Fatalf("Content-Range = %q", cr)
	}
}
