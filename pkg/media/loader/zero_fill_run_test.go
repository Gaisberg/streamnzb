package loader

import (
	"context"
	"errors"
	"io"
	"testing"

	"streamnzb/pkg/usenet/nntp"
)

// Four consecutive holes still pad; five fail the file instead of serving
// seconds of zeros into the decoder.
func TestMissingRunPastCapFailsInsteadOfPadding(t *testing.T) {
	const segments, segmentSize = 16, 1024

	padded := newDamagedSegmentFetcher(segmentSize, 4, 5, 6, 7)
	f := NewFile(context.Background(), damagedNZBFile(segments, segmentSize), nil, padded)
	stream, err := f.OpenStreamCtx(playbackCtx())
	if err != nil {
		t.Fatalf("OpenStreamCtx: %v", err)
	}
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("a run of %d holes must still pad, got: %v", MaxZeroFillRun, err)
	}
	stream.Close()
	if f.IsFailed() {
		t.Fatal("a run within the cap must not fail the file")
	}
	if holes := f.ZeroFilledSegments(); holes != MaxZeroFillRun {
		t.Fatalf("zero-filled segments = %d, want %d", holes, MaxZeroFillRun)
	}

	fatal := newDamagedSegmentFetcher(segmentSize, 4, 5, 6, 7, 8)
	f = NewFile(context.Background(), damagedNZBFile(segments, segmentSize), nil, fatal)
	stream, err = f.OpenStreamCtx(playbackCtx())
	if err != nil {
		t.Fatalf("OpenStreamCtx: %v", err)
	}
	defer stream.Close()
	_, err = io.ReadAll(stream)
	if !errors.Is(err, ErrTooManyZeroFills) {
		t.Fatalf("a run of %d holes must fail the read with ErrTooManyZeroFills, got: %v", MaxZeroFillRun+1, err)
	}
	// The 430 survives the join, so the serve layer still classifies the
	// release from the cause.
	if !nntp.IsArticleNotFound(err) {
		t.Fatalf("read error lost its missing-article cause: %v", err)
	}
	if !f.IsFailed() {
		t.Fatal("a run past the cap must fail the file")
	}
}

// A seek that lands inside a long run is the case that used to crash players:
// the run is measured from the hole the read hit outward, whichever order the
// holes were discovered in.
func TestMissingRunIsMeasuredAcrossSeeks(t *testing.T) {
	const segments, segmentSize = 16, 1024
	f := NewFile(context.Background(), damagedNZBFile(segments, segmentSize), nil, newDamagedSegmentFetcher(segmentSize, 4, 5, 6, 7, 8))

	// Discover the run from its far end first, then walk back into it.
	for _, idx := range []int{8, 6, 4, 7} {
		if _, err := f.DownloadSegment(context.Background(), idx); err != nil {
			t.Fatalf("segment %d: four holes must pad, got %v", idx, err)
		}
	}
	if _, err := f.DownloadSegment(context.Background(), 5); !errors.Is(err, ErrTooManyZeroFills) {
		t.Fatalf("the fifth hole of the run must fail, got %v", err)
	}
	if !f.IsFailed() {
		t.Fatal("file must report failed once the run passes the cap")
	}
}
