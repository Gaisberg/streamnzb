package loader

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/pool"
)

type boundarySegmentFetcher struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *boundarySegmentFetcher) FetchSegment(ctx context.Context, segment *nzb.Segment, _ []string) (pool.SegmentData, error) {
	if segment.Number == 2 {
		f.once.Do(func() { close(f.started) })
		select {
		case <-f.release:
		case <-ctx.Done():
			return pool.SegmentData{}, ctx.Err()
		}
	}
	return pool.SegmentData{Body: bytesForSegment(segment.Number, segment.Bytes), Size: segment.Bytes}, nil
}

func TestSegmentReaderReturnsAvailableBytesBeforeNextArticle(t *testing.T) {
	const segmentSize = 768 * 1024
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetcher := &boundarySegmentFetcher{started: make(chan struct{}), release: make(chan struct{})}
	f := NewFile(ctx, testNZBFile("boundary.mkv", segmentSize, segmentSize), nil, fetcher)
	r := NewSegmentReader(ctx, f, segmentSize-4096)
	defer r.Close()
	r.currentSegIdx, r.currentData = 0, bytesForSegment(1, segmentSize)
	buf := make([]byte, 32*1024)
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Read(buf)
		done <- result{n, err}
	}()
	select {
	case got := <-done:
		if got.n != 4096 || got.err != nil || !bytes.Equal(buf[:got.n], bytes.Repeat([]byte{'a'}, 4096)) {
			t.Fatalf("first Read = %d, %v; want cached 4096-byte tail", got.n, got.err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("available bytes blocked on the next article")
	}
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("read-ahead did not start the next article")
	}
	close(fetcher.release)
	if n, err := io.ReadFull(r, buf); n != len(buf) || err != nil || !bytes.Equal(buf, bytes.Repeat([]byte{'b'}, len(buf))) {
		t.Fatalf("continued read = %d, %v; want next article bytes", n, err)
	}
	if _, err := r.Seek(segmentSize-4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	want := []byte("aaaabbbb")
	if n, err := io.ReadFull(r, buf[:len(want)]); n != len(want) || err != nil || !bytes.Equal(buf[:n], want) {
		t.Fatalf("cross-boundary seek read = %q, %v", buf[:n], err)
	}
	if _, err := r.Seek(-4, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if n, err := io.ReadFull(r, buf[:8]); n != 4 || err != io.ErrUnexpectedEOF {
		t.Fatalf("tail ReadFull = %d, %v; want 4, unexpected EOF", n, err)
	}
}

type latencySegmentFetcher struct{ delay time.Duration }

func (f latencySegmentFetcher) FetchSegment(ctx context.Context, segment *nzb.Segment, _ []string) (pool.SegmentData, error) {
	select {
	case <-time.After(f.delay):
		return pool.SegmentData{Body: bytesForSegment(segment.Number, segment.Bytes), Size: segment.Bytes}, nil
	case <-ctx.Done():
		return pool.SegmentData{}, ctx.Err()
	}
}

// A seek/range response starts 4 KiB before a cached article boundary, with
// the next 768 KiB article requiring a 5 ms fetch. Measure time until bytes can be
// handed to the HTTP writer, rather than time to fill its entire 32 KiB buffer.
func BenchmarkSegmentReaderBoundaryFirstRead(b *testing.B) {
	const segmentSize = 768 * 1024
	data := bytesForSegment(1, segmentSize)
	buf := make([]byte, 32*1024)
	for b.Loop() {
		f := NewFile(context.Background(), testNZBFile("latency.mkv", segmentSize, segmentSize), nil, latencySegmentFetcher{5 * time.Millisecond})
		r := NewSegmentReader(context.Background(), f, segmentSize-4096)
		r.readAheadSize = 0
		r.currentSegIdx, r.currentData = 0, data
		if n, err := r.Read(buf); n == 0 || err != nil {
			b.Fatalf("Read = %d, %v", n, err)
		}
		_ = r.Close()
	}
}
