package ebml

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"
	"testing/iotest"
)

// fakeStream is a playback stream that knows which of its bytes were made up,
// standing in for a loader-backed one.
type fakeStream struct {
	data    []byte
	pos     int64
	holes   []Range
	readAts int
	seeks   int
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
	s.seeks++
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

// lateStream is a stream that learns about a hole the way the loader does: when
// a read first delivers a byte from inside it, and never before. Like the
// loader it reads segment by segment, so no read crosses the start of a hole —
// the read before the hole ends exactly where the hole begins.
type lateStream struct {
	*fakeStream
	furthest int64

	// failAt, when set, makes the first read that starts there fail with errBoom
	// — a transient error — before it delivers anything.
	failAt int64
	failed bool
}

func (s *lateStream) Read(p []byte) (int, error) {
	if s.failAt > 0 && !s.failed && s.pos == s.failAt {
		s.failed = true
		return 0, errBoom
	}
	for _, h := range s.holes {
		if s.pos < h.Start && s.pos+int64(len(p)) > h.Start {
			p = p[:h.Start-s.pos]
		}
	}
	n, err := s.fakeStream.Read(p)
	s.furthest = max(s.furthest, s.pos)
	return n, err
}

func (s *lateStream) ZeroFilledRanges() []Range {
	var known []Range
	for _, h := range s.holes {
		if s.furthest > h.Start {
			known = append(known, h)
		}
	}
	return known
}

func readInChunks(t *testing.T, r io.Reader, chunk int) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, chunk)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
}

// The repair writes a Void header up to one element header before the hole, and
// the hole is only discovered by reading into it. A stream's first read must
// serve those bytes as repaired as every later read does, whatever the chunking.
func TestFirstReadServesTheRepairBeforeAHoleIsKnown(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	second, fourth := f.blockIn(1, 1), f.blockIn(1, 3)
	hole := Range{Start: second.start + 1, End: fourth.start + 3}
	holed := punch(f.data, hole)

	want := serve(t, holed, []Range{hole}, NewPatchCache(), 0, len(holed))
	if want[second.start] != IDVoid {
		t.Fatalf("the repair should open a void at %d, got %#02x", second.start, want[second.start])
	}

	for _, chunk := range []int{1, 5, 12, 13, 64, 4096} {
		src := &lateStream{fakeStream: &fakeStream{data: append([]byte(nil), holed...), holes: []Range{hole}}}
		r := NewHoleFillReader(src, src, int64(len(holed)), NewPatchCache(), "fixture.mkv")
		for pass := 1; pass <= 2; pass++ {
			if _, err := r.Seek(0, io.SeekStart); err != nil {
				t.Fatalf("seek: %v", err)
			}
			if got := readInChunks(t, r, chunk); !bytes.Equal(got, want) {
				at := 0
				for at < len(got) && at < len(want) && got[at] == want[at] {
					at++
				}
				t.Fatalf("chunk %d, pass %d: served bytes differ from the repaired stream at offset %d (%#02x, want %#02x)",
					chunk, pass, at, got[at], want[at])
			}
		}
	}
}

// Holding a tail back must not lose, repeat or reorder a byte, whatever the
// read size, and the tail must be flushed at the end of the stream.
func TestReaderServesEveryByteOnceWhatEverTheChunking(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	for _, chunk := range []int{1, 3, 12, 13, 100, len(f.data) + 7} {
		src := &fakeStream{data: append([]byte(nil), f.data...)}
		r := NewHoleFillReader(src, src, int64(len(f.data)), NewPatchCache(), "fixture.mkv")
		if got := readInChunks(t, r, chunk); !bytes.Equal(got, f.data) {
			t.Fatalf("chunk %d: served %d bytes of %d, or the wrong ones", chunk, len(got), len(f.data))
		}
	}

	// The inner stream is ahead of what was served by the held tail, so a
	// relative seek has to count from the served position.
	src := &fakeStream{data: append([]byte(nil), f.data...)}
	r := NewHoleFillReader(src, src, int64(len(f.data)), NewPatchCache(), "fixture.mkv")
	head := make([]byte, 50)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read: %v", err)
	}
	if pos, err := r.Seek(10, io.SeekCurrent); err != nil || pos != 60 {
		t.Fatalf("Seek(10, SeekCurrent) = %d, %v; want 60", pos, err)
	}
	next := make([]byte, 20)
	if _, err := io.ReadFull(r, next); err != nil || !bytes.Equal(next, f.data[60:80]) {
		t.Fatalf("after a relative seek the stream served the wrong bytes (%v)", err)
	}
}

var errBoom = errors.New("boom")

// holedHeaderFixture is a Matroska file whose hole eats a block's header, so the
// repair rewrites a byte *before* the hole — the byte the holdback exists for.
// want is the stream every read must serve.
func holedHeaderFixture(t *testing.T) (holed []byte, hole Range, want []byte) {
	t.Helper()
	f := buildFixture(t, fixtureOpts{})
	second, fourth := f.blockIn(1, 1), f.blockIn(1, 3)
	hole = Range{Start: second.start + 1, End: fourth.start + 3}
	holed = punch(f.data, hole)
	want = serve(t, holed, []Range{hole}, NewPatchCache(), 0, len(holed))
	if want[second.start] != IDVoid {
		t.Fatalf("the repair should open a void at %d, got %#02x", second.start, want[second.start])
	}
	return holed, hole, want
}

func newLateReader(holed []byte, hole Range) (*HoleFillReader, *lateStream) {
	src := &lateStream{fakeStream: &fakeStream{data: append([]byte(nil), holed...), holes: []Range{hole}}}
	return NewHoleFillReader(src, src, int64(len(holed)), NewPatchCache(), "fixture.mkv"), src
}

// A read that fails must not ship the bytes the repair still owes. A player that
// got an error resumes from the byte after the last one it received; if the held
// tail had been flushed unrepaired along with the error, the resumed stream
// would be the unrepaired tail followed by repaired bytes — invalid EBML that no
// single read ever produced.
func TestAnErrorDoesNotFlushBytesTheRepairStillOwes(t *testing.T) {
	holed, hole, want := holedHeaderFixture(t)

	r, src := newLateReader(holed, hole)
	src.failAt = hole.Start

	var got []byte
	buf := make([]byte, 7)
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			if err != errBoom {
				t.Fatalf("read: %v", err)
			}
			break
		}
	}
	if int64(len(got)) >= hole.Start {
		t.Fatalf("served %d bytes up to the failure at %d; the held tail should not have been released", len(got), hole.Start)
	}
	if !bytes.Equal(got, want[:len(got)]) {
		t.Fatal("bytes served before the failure are not the repaired stream's")
	}

	// The same reader, retried — and a fresh request resuming where the client
	// stopped, which is what a player does — both continue the repaired stream.
	resumed := append(append([]byte(nil), got...), readInChunks(t, r, 7)...)
	if !bytes.Equal(resumed, want) {
		t.Fatal("retrying the failed read did not continue the repaired stream")
	}
	fresh, _ := newLateReader(holed, hole)
	if _, err := fresh.Seek(int64(len(got)), io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if after := append(append([]byte(nil), got...), readInChunks(t, fresh, 7)...); !bytes.Equal(after, want) {
		t.Fatal("a request resuming after the failure did not continue the repaired stream")
	}
}

// Seeks count from what was served, not from where the inner stream got to with
// the held tail, and a bare position query must disturb neither.
func TestSeekingCountsFromTheServedPosition(t *testing.T) {
	holed, hole, want := holedHeaderFixture(t)
	r, src := newLateReader(holed, hole)

	if _, err := io.ReadFull(r, make([]byte, 100)); err != nil {
		t.Fatalf("read: %v", err)
	}
	seeks := src.seeks
	if pos, err := r.Seek(0, io.SeekCurrent); err != nil || pos != 100 {
		t.Fatalf("Seek(0, SeekCurrent) = %d, %v; want 100", pos, err)
	}
	if src.seeks != seeks {
		t.Fatal("a position query repositioned the inner stream, throwing away its read-ahead")
	}
	next := make([]byte, 50)
	if _, err := io.ReadFull(r, next); err != nil || !bytes.Equal(next, want[100:150]) {
		t.Fatalf("a position query changed what came next (%v)", err)
	}

	if pos, err := r.Seek(-30, io.SeekCurrent); err != nil || pos != 120 {
		t.Fatalf("Seek(-30, SeekCurrent) = %d, %v; want 120", pos, err)
	}
	if _, err := io.ReadFull(r, next[:20]); err != nil || !bytes.Equal(next[:20], want[120:140]) {
		t.Fatalf("after seeking back the stream served the wrong bytes (%v)", err)
	}

	size := int64(len(want))
	if pos, err := r.Seek(-20, io.SeekEnd); err != nil || pos != size-20 {
		t.Fatalf("Seek(-20, SeekEnd) = %d, %v; want %d", pos, err, size-20)
	}
	if tail := readInChunks(t, r, 3); !bytes.Equal(tail, want[size-20:]) {
		t.Fatalf("the last 20 bytes were served as % x, want % x", tail, want[size-20:])
	}
	if pos, err := r.Seek(0, io.SeekCurrent); err != nil || pos != size {
		t.Fatalf("after reading to the end Seek(0, SeekCurrent) = %d, %v; want %d", pos, err, size)
	}
}

// Whatever the reads and seeks, every byte served is the repaired stream's byte
// at the position the caller believes it is at — which is also what makes a
// range request and a sequential read agree, since a range is a seek and a read.
func TestAnyReadOrSeekServesTheRepairedStreamAtTheServedPosition(t *testing.T) {
	holed, hole, want := holedHeaderFixture(t)
	size := int64(len(want))
	rng := rand.New(rand.NewSource(411))

	for run := 0; run < 200; run++ {
		r, _ := newLateReader(holed, hole)
		var pos int64
		for op := 0; op < 60; op++ {
			switch rng.Intn(4) {
			case 0: // absolute, biased towards the hole where the repair lives
				target := hole.Start - 20 + rng.Int63n(50)
				if got, err := r.Seek(target, io.SeekStart); err != nil || got != target {
					t.Fatalf("Seek(%d, SeekStart) = %d, %v", target, got, err)
				}
				pos = target
			case 1: // relative
				target := min(max(pos+rng.Int63n(80)-40, 0), size)
				if got, err := r.Seek(target-pos, io.SeekCurrent); err != nil || got != target {
					t.Fatalf("Seek(%d, SeekCurrent) from %d = %d, %v; want %d", target-pos, pos, got, err, target)
				}
				pos = target
			case 2: // end-relative
				back := rng.Int63n(60)
				if got, err := r.Seek(-back, io.SeekEnd); err != nil || got != size-back {
					t.Fatalf("Seek(%d, SeekEnd) = %d, %v", -back, got, err)
				}
				pos = size - back
			default: // read
				buf := make([]byte, 1+rng.Intn(40))
				n, err := r.Read(buf)
				if n > 0 && !bytes.Equal(buf[:n], want[pos:pos+int64(n)]) {
					t.Fatalf("run %d: read %d bytes at %d served % x, want % x", run, n, pos, buf[:n], want[pos:pos+int64(n)])
				}
				pos += int64(n)
				if err != nil && err != io.EOF {
					t.Fatalf("read: %v", err)
				}
				if err == io.EOF && pos != size {
					t.Fatalf("run %d: EOF at %d of %d", run, pos, size)
				}
			}
		}
	}
}

// A byte's value does not depend on how the request that carried it was cut: for
// every window around the hole, a cold range read equals the same slice of the
// repaired stream.
func TestEveryRangeAroundAHoleMatchesTheRepairedStream(t *testing.T) {
	holed, hole, want := holedHeaderFixture(t)
	size := int64(len(want))
	for start := hole.Start - 40; start < hole.End+40; start++ {
		for _, count := range []int64{1, 2, 7, 12, 13, 25, 100} {
			r, _ := newLateReader(holed, hole)
			if _, err := r.Seek(start, io.SeekStart); err != nil {
				t.Fatalf("seek: %v", err)
			}
			got := make([]byte, count)
			n, err := io.ReadFull(r, got)
			if err != nil && err != io.ErrUnexpectedEOF {
				t.Fatalf("read %d+%d: %v", start, count, err)
			}
			if end := min(start+count, size); !bytes.Equal(got[:n], want[start:end]) {
				t.Fatalf("range %d+%d differs from the repaired stream", start, count)
			}
		}
	}
}

// What holding the tail back costs an intact stream: one extra copy per read.
func BenchmarkHoleFillReaderIntact(b *testing.B) {
	data := make([]byte, 64<<20)
	src := &fakeStream{data: data}
	r := NewHoleFillReader(src, src, int64(len(data)), NewPatchCache(), "fixture.mkv")
	buf := make([]byte, 32<<10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Seek(0, io.SeekStart)
		for {
			if _, err := r.Read(buf); err != nil {
				break
			}
		}
	}
}

func BenchmarkRawStreamIntact(b *testing.B) {
	data := make([]byte, 64<<20)
	src := &fakeStream{data: data}
	buf := make([]byte, 32<<10)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		src.Seek(0, io.SeekStart)
		for {
			if _, err := src.Read(buf); err != nil {
				break
			}
		}
	}
}

// The holdback has to cover how far before a hole the repair can write: the
// whole of the header the hole started inside. Put the hole at every offset of
// the Cluster's header (the longest in the fixture) and of each block's, and the
// first read of the stream has to equal what every later read serves.
func TestHoldbackCoversEveryOffsetInsideAnElementHeader(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	headers := []int64{f.clusters[1].Start}
	for bi := 0; bi < 4; bi++ {
		headers = append(headers, f.blockIn(1, bi).start)
	}
	for _, start := range headers {
		payload, _ := ReadElementHeader(f.data[start:])
		for off := int64(1); off < int64(payload); off++ {
			hole := Range{Start: start + off, End: start + off + 64}
			holed := punch(f.data, hole)
			want := serve(t, holed, []Range{hole}, NewPatchCache(), 0, len(holed))
			for _, chunk := range []int{1, 9, 4096} {
				r, _ := newLateReader(holed, hole)
				if got := readInChunks(t, r, chunk); !bytes.Equal(got, want) {
					t.Fatalf("header at %d, hole %d bytes into it, chunk %d: first read differs from the repaired stream", start, off, chunk)
				}
			}
		}
	}
}

// What io.Reader and io.Seeker promise any reader, checked by the standard
// library's conformance test over a stream that holds a tail back: the content
// read whole, in odd chunk sizes and after seeks, is the repaired stream.
func TestReaderConformsToIOReaderAndSeeker(t *testing.T) {
	holed, hole, want := holedHeaderFixture(t)
	r, _ := newLateReader(holed, hole)
	if err := iotest.TestReader(r, want); err != nil {
		t.Fatalf("repaired stream: %v", err)
	}

	f := buildFixture(t, fixtureOpts{})
	src := &fakeStream{data: append([]byte(nil), f.data...)}
	if err := iotest.TestReader(NewHoleFillReader(src, src, int64(len(f.data)), NewPatchCache(), "fixture.mkv"), f.data); err != nil {
		t.Fatalf("intact stream: %v", err)
	}
}

// eofWithData reports io.EOF in the same call that returns the last bytes,
// which io.Reader allows and the held tail has to flush on.
type eofWithData struct{ *lateStream }

func (s eofWithData) Read(p []byte) (int, error) {
	n, err := s.lateStream.Read(p)
	if err == nil && s.pos >= int64(len(s.data)) {
		err = io.EOF
	}
	return n, err
}

// errWithData fails in the same call that returns the bytes before the failure:
// a partial read followed by an error, once, at offset at.
type errWithData struct {
	*lateStream
	at   int64
	done bool
}

func (s *errWithData) Read(p []byte) (int, error) {
	if !s.done && s.pos < s.at && s.pos+int64(len(p)) > s.at {
		p = p[:s.at-s.pos]
	}
	n, err := s.lateStream.Read(p)
	if err == nil && !s.done && s.pos == s.at {
		s.done = true
		return n, errBoom
	}
	return n, err
}

func TestReaderHandlesTheReturnShapesAnInnerReaderMayUse(t *testing.T) {
	holed, hole, want := holedHeaderFixture(t)
	newInner := func() *lateStream {
		return &lateStream{fakeStream: &fakeStream{data: append([]byte(nil), holed...), holes: []Range{hole}}}
	}

	t.Run("data and EOF together", func(t *testing.T) {
		src := newInner()
		r := NewHoleFillReader(eofWithData{src}, src, int64(len(holed)), NewPatchCache(), "fixture.mkv")
		for _, chunk := range []int{1, 5, 13, 4096} {
			r.Seek(0, io.SeekStart)
			if got := readInChunks(t, r, chunk); !bytes.Equal(got, want) {
				t.Fatalf("chunk %d: served %d bytes of %d, or the wrong ones", chunk, len(got), len(want))
			}
		}
	})

	// A read that returns bytes and an error together, away from the hole and on
	// the byte before it: what was served is a prefix of the repaired stream, and
	// nothing is lost or repeated when the reader is retried.
	for name, at := range map[string]int64{"mid-stream": 500, "at the hole": hole.Start} {
		t.Run("partial read then error, "+name, func(t *testing.T) {
			src := newInner()
			inner := &errWithData{lateStream: src, at: at}
			r := NewHoleFillReader(inner, src, int64(len(holed)), NewPatchCache(), "fixture.mkv")

			var got []byte
			buf := make([]byte, 9)
			for {
				n, err := r.Read(buf)
				got = append(got, buf[:n]...)
				if err == errBoom {
					break
				}
				if err != nil {
					t.Fatalf("read: %v", err)
				}
			}
			if !bytes.Equal(got, want[:len(got)]) {
				t.Fatal("bytes served before the error are not the repaired stream's")
			}
			if all := append(got, readInChunks(t, r, 9)...); !bytes.Equal(all, want) {
				t.Fatalf("after the error the stream served %d bytes, want the %d of the repaired stream", len(all), len(want))
			}
		})
	}

	t.Run("empty read", func(t *testing.T) {
		src := &fakeStream{data: want}
		r := NewHoleFillReader(src, src, int64(len(want)), NewPatchCache(), "fixture.mkv")
		if n, err := r.Read(nil); n != 0 || err != nil || src.pos != 0 {
			t.Fatalf("Read(nil) = %d, %v with the inner stream at %d; want 0, nil and no read", n, err, src.pos)
		}
	})
}
