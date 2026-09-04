package loader

import (
	"context"
	"io"
	"testing"

	"streamnzb/pkg/media/ebml"
)

// The container repair needs the holes as bytes of the file, not as segment
// indices: a run of missing articles is one span of a Matroska stream.
func TestZeroFilledRangesReportsHolesAsByteRanges(t *testing.T) {
	const segments, segmentSize = 12, 1024
	fetcher := newDamagedSegmentFetcher(segmentSize, 3, 6, 7)
	f := NewFile(context.Background(), damagedNZBFile(segments, segmentSize), nil, fetcher)

	if ranges := f.ZeroFilledRanges(); ranges != nil {
		t.Fatalf("nothing has been read yet, so there are no holes to report: %+v", ranges)
	}

	stream, err := f.OpenStreamCtx(playbackCtx())
	if err != nil {
		t.Fatalf("OpenStreamCtx: %v", err)
	}
	defer stream.Close()
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Segments 6 and 7 are consecutive and arrive as one range.
	want := []ebml.Range{
		{Start: 3 * segmentSize, End: 4 * segmentSize},
		{Start: 6 * segmentSize, End: 8 * segmentSize},
	}
	got := f.ZeroFilledRanges()
	if len(got) != len(want) {
		t.Fatalf("zero-filled ranges = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("zero-filled ranges = %+v, want %+v", got, want)
		}
	}
}

// A reader over the whole file shares its coordinates, and can read around a
// hole without moving the position it is serving from.
func TestSegmentReaderIsAHoleSource(t *testing.T) {
	const segments, segmentSize = 8, 1024
	fetcher := newDamagedSegmentFetcher(segmentSize, 5)
	f := NewFile(context.Background(), damagedNZBFile(segments, segmentSize), nil, fetcher)

	stream, err := f.OpenPlaybackStreamCtx(playbackCtx())
	if err != nil {
		t.Fatalf("OpenPlaybackStreamCtx: %v", err)
	}
	defer stream.Close()

	source, ok := stream.(ebml.HoleSource)
	if !ok {
		t.Fatal("a playback stream must be able to report its holes")
	}
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("read: %v", err)
	}
	got := source.ZeroFilledRanges()
	if len(got) != 1 || got[0] != (ebml.Range{Start: 5 * segmentSize, End: 6 * segmentSize}) {
		t.Fatalf("zero-filled ranges = %+v", got)
	}

	// Reading elsewhere must not disturb where the stream is.
	pos, err := stream.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek: %v", err)
	}
	buf := make([]byte, 16)
	if _, err := source.ReadAt(buf, 2*segmentSize); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if want := segmentPayload(2, 16); string(buf) != string(want) {
		t.Fatalf("ReadAt returned the wrong segment's bytes")
	}
	if after, _ := stream.Seek(0, io.SeekCurrent); after != pos {
		t.Fatalf("ReadAt moved the serve position from %d to %d", pos, after)
	}
}

// The repair reads around a hole to find out what the container looks like
// there. Those reads can reach articles the player never asked for, so a miss
// found by one must not spend the budget that measures what playback served —
// the same rule read-ahead has always followed. The read fails instead, and the
// repair falls back to serving the original bytes.
func TestSpeculativeReadsDoNotCountAsHoles(t *testing.T) {
	const segments, segmentSize = 12, 1024
	fetcher := newDamagedSegmentFetcher(segmentSize, 4, 5, 6, 7, 8)
	f := NewFile(context.Background(), damagedNZBFile(segments, segmentSize), nil, fetcher)
	if err := f.EnsureSegmentMapCtx(playbackCtx()); err != nil {
		t.Fatalf("EnsureSegmentMapCtx: %v", err)
	}

	// Read normally, this run of five would fail the file outright.
	buf := make([]byte, 5*segmentSize)
	if _, err := f.ReadAtCtx(WithSpeculativeRead(playbackCtx(), true), buf, 4*segmentSize); err == nil {
		t.Fatal("a speculative read of a missing article should fail rather than pad it")
	}
	if holes := f.ZeroFilledSegments(); holes != 0 {
		t.Fatalf("speculative reads recorded %d holes", holes)
	}
	if f.IsFailed() {
		t.Fatal("speculative reads must not fail the file")
	}

	// A hole playback already knows about reads back as the zeros it serves, so
	// the repair can still read across the damage it was called to look at.
	if _, err := f.ReadAtCtx(playbackCtx(), buf[:segmentSize], 4*segmentSize); err != nil {
		t.Fatalf("real read: %v", err)
	}
	if _, err := f.ReadAtCtx(WithSpeculativeRead(playbackCtx(), true), buf[:segmentSize], 4*segmentSize); err != nil {
		t.Fatalf("speculative read over a known hole: %v", err)
	}
	if holes := f.ZeroFilledSegments(); holes != 1 {
		t.Fatalf("the known hole should be counted exactly once, got %d", holes)
	}
}
