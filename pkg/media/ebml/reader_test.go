package ebml

import (
	"bytes"
	"io"
	"testing"
)

// fakeStream is a playback stream that knows which of its bytes were made up,
// standing in for a loader-backed one.
type fakeStream struct {
	data    []byte
	pos     int64
	holes   []Range
	readAts int
}

func (s *fakeStream) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *fakeStream) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	case io.SeekEnd:
		s.pos = int64(len(s.data)) + offset
	}
	return s.pos, nil
}

func (s *fakeStream) Close() error { return nil }

func (s *fakeStream) ReadAt(p []byte, off int64) (int, error) {
	s.readAts++
	return readerAt(s.data).ReadAt(p, off)
}

func (s *fakeStream) ZeroFilledRanges() []Range { return s.holes }

// serve reads count bytes from offset through a hole-filling reader, the way a
// range request does.
func serve(t *testing.T, data []byte, holes []Range, cache *PatchCache, offset int64, count int) []byte {
	t.Helper()
	src := &fakeStream{data: append([]byte(nil), data...), holes: holes}
	r := NewHoleFillReader(src, src, int64(len(data)), cache, "fixture.mkv")
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	out := make([]byte, count)
	n, err := io.ReadFull(r, out)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("read: %v", err)
	}
	return out[:n]
}

func TestOverlappingRangeRequestsServeTheSameBytes(t *testing.T) {
	f := buildFixture(t, fixtureOpts{clusters: 4})
	start := f.blockIn(1, 1)
	hole := Range{Start: start.payloadStart + 4, End: f.blockIn(2, 1).payloadStart + 20}
	holed := punch(f.data, hole)
	holes := []Range{hole}
	cache := NewPatchCache()

	// One request streams into the hole from the cluster before it; the other
	// starts inside the hole itself, with no structure ahead of it to parse.
	// Sharing the session's patch cache is what makes them agree.
	windowA := Range{Start: f.clusters[1].Start, End: hole.End + 500}
	windowB := Range{Start: hole.Start + 100, End: f.clusters[3].End}
	a := serve(t, holed, holes, cache, windowA.Start, int(windowA.Len()))
	b := serve(t, holed, holes, cache, windowB.Start, int(windowB.Len()))

	overlap := Range{Start: windowB.Start, End: windowA.End}
	inA := a[overlap.Start-windowA.Start : overlap.End-windowA.Start]
	inB := b[:overlap.Len()]
	if !bytes.Equal(inA, inB) {
		t.Fatal("two range requests over the same hole served different bytes")
	}

	// And the served stream is the repaired one, not the raw zeros.
	whole := serve(t, holed, holes, cache, 0, len(holed))
	if err := validateStrict(whole); err != nil {
		t.Fatalf("served stream should parse: %v", err)
	}
	if len(whole) != len(holed) {
		t.Fatalf("served %d bytes of a %d byte stream", len(whole), len(holed))
	}
}

func TestReaderIsInertWithoutHoles(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	src := &fakeStream{data: append([]byte(nil), f.data...)}
	r := NewHoleFillReader(src, src, int64(len(f.data)), NewPatchCache(), "fixture.mkv")

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(out, f.data) {
		t.Fatal("an intact stream should come back byte for byte")
	}
	// Not one structural read: a healthy release must not pay for the repair,
	// not even to find out whether it is Matroska.
	if src.readAts != 0 {
		t.Fatalf("read the container %d times with no holes to repair", src.readAts)
	}
}

func TestReaderPassesThroughANonMatroskaStream(t *testing.T) {
	data := bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}, 512)
	hole := Range{Start: 1000, End: 2000}
	src := &fakeStream{data: append([]byte(nil), data...), holes: []Range{hole}}
	r := NewHoleFillReader(src, src, int64(len(data)), NewPatchCache(), "movie.mp4")

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatal("an mp4 stream must be served untouched")
	}
}

func TestPatchCacheRecomputesWhenAHoleGrows(t *testing.T) {
	f := buildFixture(t, fixtureOpts{clusters: 4})
	b := f.blockIn(1, 1)
	cache := NewPatchCache()

	small := Range{Start: b.payloadStart + 4, End: b.end + 10}
	first := cache.entryFor(small)
	if second := cache.entryFor(small); second != first {
		t.Fatal("the same hole should reuse its analysis")
	}

	// The run picked up another missing segment: the earlier repair was
	// computed against a shorter hole and must not be reused.
	grown := Range{Start: small.Start, End: f.blockIn(2, 1).payloadStart}
	if cache.entryFor(grown) == first {
		t.Fatal("a grown hole must be analyzed again")
	}
	if again := cache.entryFor(grown); again != cache.entryFor(grown) {
		t.Fatal("the grown hole should now be cached")
	}
}

// The repair writes a Void header at the start of the element whose own header
// the hole ate — a few bytes before the hole. A range request that stops in
// there must carry those bytes, or one response ends with half a Void header
// and the next starts with the other half.
func TestAVoidHeaderIsNeverSplitAcrossResponses(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	second, fourth := f.blockIn(1, 1), f.blockIn(1, 3)
	hole := Range{Start: second.start + 1, End: fourth.start + 3}
	holed := punch(f.data, hole)
	holes := []Range{hole}

	// The split lands one byte into the void header the repair writes at the
	// damaged element's start. Each half is served by its own request, with its
	// own cache, so nothing but the analysis being deterministic makes them fit.
	split := second.start + 1
	head := serve(t, holed, holes, NewPatchCache(), f.clusters[1].Start, int(split-f.clusters[1].Start))
	tail := serve(t, holed, holes, NewPatchCache(), split, 256)

	whole := serve(t, holed, holes, NewPatchCache(), f.clusters[1].Start, len(head)+len(tail))
	if !bytes.Equal(append(append([]byte(nil), head...), tail...), whole) {
		t.Fatal("two requests split inside a void header did not reassemble into the repaired stream")
	}
	if head[second.start-f.clusters[1].Start] != IDVoid {
		t.Fatalf("the request that stopped inside the void header served it unrepaired (%#02x)",
			head[second.start-f.clusters[1].Start])
	}
}
