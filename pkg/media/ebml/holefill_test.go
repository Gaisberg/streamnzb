package ebml

import (
	"bytes"
	"testing"
)

func TestHoleInsideABlockPayloadIsLeftAlone(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	b := f.blockIn(1, 1)
	hole := Range{Start: b.payloadStart + 8, End: b.payloadStart + 120}

	holed := punch(f.data, hole)
	// The container never looks at these bytes: the block's own size says how
	// far to skip. That is why the repair has nothing to do here.
	if err := validateStrict(holed); err != nil {
		t.Fatalf("a hole inside a block payload should still parse: %v", err)
	}

	edits, err := Analyze(readerAt(holed), int64(len(holed)), hole, []Range{hole})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(edits) != 0 {
		t.Fatalf("expected no edits for a hole inside a payload, got %+v", edits)
	}
}

func TestHoleAtABlockPayloadStartGetsATrackNumber(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	b := f.blockIn(1, 2)
	hole := Range{Start: b.payloadStart, End: b.payloadStart + 200}

	holed := punch(f.data, hole)
	if err := validateStrict(holed); err == nil {
		t.Fatal("a zeroed block payload start should fail a strict demuxer")
	}

	fixed := repaired(t, holed, hole)
	if err := validateStrict(fixed); err != nil {
		t.Fatalf("repaired stream should parse: %v", err)
	}
	if fixed[b.payloadStart] != 0x81 {
		t.Fatalf("block payload should open with track 1, got %#02x", fixed[b.payloadStart])
	}
	assertOnlyDiffersAt(t, holed, fixed, b.payloadStart)
}

func TestHoleOutlivingItsBlockIsVoidedToTheClusterEnd(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	first, third := f.blockIn(1, 0), f.blockIn(1, 2)
	// Starts inside one block's payload, ends inside a later block's header:
	// everything between is zeros where element headers should be.
	hole := Range{Start: first.payloadStart + 16, End: third.start + 2}

	holed := punch(f.data, hole)
	if err := validateStrict(holed); err == nil {
		t.Fatal("a hole across block boundaries should fail a strict demuxer")
	}

	fixed := repaired(t, holed, hole)
	if err := validateStrict(fixed); err != nil {
		t.Fatalf("repaired stream should parse: %v", err)
	}

	// The void starts where the block the hole began in ends, and runs to the
	// end of the cluster — the rest of that cluster is the price of the repair.
	cluster := f.clusters[1]
	el := elementAt(t, fixed, first.end)
	if el.ID != IDVoid || el.End != cluster.End {
		t.Fatalf("expected a void at %d spanning to %d, got id %#x ending at %d",
			first.end, cluster.End, el.ID, el.End)
	}
	// Clusters after the damaged one are untouched.
	assertRangeUnchanged(t, holed, fixed, f.clusters[2])
}

func TestHoleAcrossAClusterBoundaryIsVoidedToTheNextCluster(t *testing.T) {
	f := buildFixture(t, fixtureOpts{clusters: 4})
	start := f.blockIn(1, 1)
	// Runs from the middle of cluster 1 into the middle of cluster 2, taking
	// cluster 2's own header with it.
	hole := Range{Start: start.payloadStart + 4, End: f.blockIn(2, 1).payloadStart + 20}

	holed := punch(f.data, hole)
	if err := validateStrict(holed); err == nil {
		t.Fatal("a hole across a cluster boundary should fail a strict demuxer")
	}

	fixed := repaired(t, holed, hole)
	if err := validateStrict(fixed); err != nil {
		t.Fatalf("repaired stream should parse: %v", err)
	}

	inner := elementAt(t, fixed, start.end)
	if inner.ID != IDVoid || inner.End != f.clusters[1].End {
		t.Fatalf("expected the cluster remainder voided, got id %#x ending at %d", inner.ID, inner.End)
	}
	outer := elementAt(t, fixed, f.clusters[1].End)
	if outer.ID != IDVoid || outer.End != f.clusters[3].Start {
		t.Fatalf("expected a segment-level void up to cluster 3 at %d, got id %#x ending at %d",
			f.clusters[3].Start, outer.ID, outer.End)
	}
	assertRangeUnchanged(t, holed, fixed, f.clusters[3])
}

func TestHoleEatingAnElementHeaderVoidsFromThatElement(t *testing.T) {
	f := buildFixture(t, fixtureOpts{})
	second, fourth := f.blockIn(1, 1), f.blockIn(1, 3)
	// The hole opens one byte into a block's header, so that block's declared
	// size is read out of zeros and cannot be trusted.
	hole := Range{Start: second.start + 1, End: fourth.start + 3}

	fixed := repaired(t, punch(f.data, hole), hole)
	if err := validateStrict(fixed); err != nil {
		t.Fatalf("repaired stream should parse: %v", err)
	}
	el := elementAt(t, fixed, second.start)
	if el.ID != IDVoid || el.End != f.clusters[1].End {
		t.Fatalf("expected a void from the damaged header at %d, got id %#x ending at %d",
			second.start, el.ID, el.End)
	}
}

func TestHoleInsideABlockGroupPopsOutward(t *testing.T) {
	f := buildFixture(t, fixtureOpts{blockGroups: true})
	b := f.blockIn(1, 1)
	hole := Range{Start: b.payloadStart + 8, End: b.end + 40}

	fixed := repaired(t, punch(f.data, hole), hole)
	if err := validateStrict(fixed); err != nil {
		t.Fatalf("repaired stream should parse: %v", err)
	}
}

// A Cluster's CRC-32 sits at the front of the Cluster, long before the hole
// that would make the repair look at it, so by the time anything knows the
// damage is there those bytes have already been served. The repair does not
// touch it — and a Cluster carrying one still fails its checksum, exactly as
// it did before the repair existed.
func TestTheClusterCRCIsLeftAlone(t *testing.T) {
	f := buildFixture(t, fixtureOpts{withCRC: true})
	first, third := f.blockIn(1, 0), f.blockIn(1, 2)
	hole := Range{Start: first.payloadStart + 16, End: third.start + 2}
	holed := punch(f.data, hole)

	fixed := repaired(t, holed, hole)
	if err := validateStrict(fixed); err != nil {
		t.Fatalf("repaired stream should parse: %v", err)
	}
	crcAt := elementAt(t, f.data, f.clusters[1].Start).PayloadStart
	if fixed[crcAt] != IDCRC32 {
		t.Fatalf("the CRC-32 at %d was rewritten (%#02x); every edit must stay within a header of the hole",
			crcAt, fixed[crcAt])
	}
	for _, e := range analyzeEdits(t, holed, hole) {
		if e.Offset < hole.Start-maxHeaderLen || e.Offset >= hole.End {
			t.Fatalf("edit at %d is outside the hole %d-%d and the header before it", e.Offset, hole.Start, hole.End)
		}
	}
}

// Every edit has to land close enough to the hole that a read window near it
// can be told to apply it; the serve layer relies on exactly this bound.
func TestEveryEditStaysWithinAHeaderOfTheHole(t *testing.T) {
	f := buildFixture(t, fixtureOpts{clusters: 4, withCRC: true})
	cases := map[string]Range{
		"inside a payload":    {Start: f.blockIn(1, 1).payloadStart + 8, End: f.blockIn(1, 1).payloadStart + 100},
		"at a payload start":  {Start: f.blockIn(1, 2).payloadStart, End: f.blockIn(1, 2).payloadStart + 200},
		"eating a header":     {Start: f.blockIn(1, 1).start + 1, End: f.blockIn(1, 3).start + 3},
		"across the boundary": {Start: f.blockIn(1, 1).payloadStart + 4, End: f.blockIn(2, 1).payloadStart + 20},
	}
	for name, hole := range cases {
		t.Run(name, func(t *testing.T) {
			for _, e := range analyzeEdits(t, punch(f.data, hole), hole) {
				if e.Offset < hole.Start-maxHeaderLen || e.Offset >= hole.End {
					t.Fatalf("edit at %d is outside the hole %d-%d and the header before it", e.Offset, hole.Start, hole.End)
				}
			}
		})
	}
}

func analyzeEdits(t *testing.T, data []byte, hole Range) []Edit {
	t.Helper()
	edits, err := Analyze(readerAt(data), int64(len(data)), hole, []Range{hole})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return edits
}

func TestRepairRefusesWhatItCannotReason(t *testing.T) {
	t.Run("unknown cluster size", func(t *testing.T) {
		f := buildFixture(t, fixtureOpts{clusters: 2, unknownCluster: true})
		b := f.blockIn(1, 1)
		hole := Range{Start: b.payloadStart + 4, End: b.end + 30}
		if _, err := Analyze(readerAt(punch(f.data, hole)), int64(len(f.data)), hole, []Range{hole}); err == nil {
			t.Fatal("a cluster with an unknown size cannot be repaired and must fall back")
		}
	})

	t.Run("no cluster after the hole", func(t *testing.T) {
		f := buildFixture(t, fixtureOpts{clusters: 2, withCues: true})
		b := f.blockIn(1, 1)
		// Runs from the last cluster into the cues that trail it, so nothing
		// past the hole is a cluster the repair could resume at.
		hole := Range{Start: b.payloadStart + 4, End: int64(len(f.data)) - 4}
		if _, err := Analyze(readerAt(punch(f.data, hole)), int64(len(f.data)), hole, []Range{hole}); err == nil {
			t.Fatal("a hole running to EOF has nothing to resume at and must fall back")
		}
	})

	t.Run("not matroska", func(t *testing.T) {
		data := bytes.Repeat([]byte{'m', 'o', 'o', 'v'}, 4096)
		hole := Range{Start: 1000, End: 2000}
		if _, err := Analyze(readerAt(data), int64(len(data)), hole, []Range{hole}); err == nil {
			t.Fatal("a non-EBML stream must fall back")
		}
		if IsMatroska(readerAt(data)) {
			t.Fatal("IsMatroska should not claim an mp4-ish stream")
		}
	})

	t.Run("hole outside the stream", func(t *testing.T) {
		f := buildFixture(t, fixtureOpts{})
		hole := Range{Start: int64(len(f.data)) - 10, End: int64(len(f.data)) + 100}
		if _, err := Analyze(readerAt(f.data), int64(len(f.data)), hole, []Range{hole}); err == nil {
			t.Fatal("a hole past the end of the stream must be refused")
		}
	})
}

func TestVoidHeaderFillsItsSpaceExactly(t *testing.T) {
	for _, space := range []int64{2, 3, 4, 5, 100, 127, 128, 129, 16382, 16383, 16384, 1 << 20, 1 << 40} {
		header := VoidHeader(space)
		if header == nil {
			t.Fatalf("no void header for %d bytes", space)
		}
		payloadOffset, payloadLen := ReadElementHeader(header)
		if payloadOffset != len(header) {
			t.Fatalf("space %d: header parses %d bytes of a %d byte header", space, payloadOffset, len(header))
		}
		if got := int64(payloadOffset) + payloadLen; got != space {
			t.Fatalf("space %d: void occupies %d bytes", space, got)
		}
		if _, id := ReadID(header); id != IDVoid {
			t.Fatalf("space %d: header is not a void (%#x)", space, id)
		}
	}
	for _, space := range []int64{-1, 0, 1} {
		if VoidHeader(space) != nil {
			t.Fatalf("%d bytes cannot hold a void header", space)
		}
	}
}

func TestMergeRangesCoalescesARun(t *testing.T) {
	got := MergeRanges([]Range{{40, 60}, {10, 20}, {20, 30}, {70, 70}})
	want := []Range{{10, 30}, {40, 60}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

// elementAt parses the element header at off, failing the test when it is not
// readable.
func elementAt(t *testing.T, data []byte, off int64) element {
	t.Helper()
	c := &cursor{src: readerAt(data), size: int64(len(data))}
	el, err := c.element(off)
	if err != nil {
		t.Fatalf("no element at %d: %v", off, err)
	}
	return el
}

// assertOnlyDiffersAt checks that the repair touched nothing but the listed
// offsets — the guarantee that lets it run on every stream by default.
func assertOnlyDiffersAt(t *testing.T, before, after []byte, offsets ...int64) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("length changed: %d -> %d", len(before), len(after))
	}
	allowed := make(map[int64]bool, len(offsets))
	for _, o := range offsets {
		allowed[o] = true
	}
	for i := range before {
		if before[i] != after[i] && !allowed[int64(i)] {
			t.Fatalf("unexpected change at %d: %#02x -> %#02x", i, before[i], after[i])
		}
	}
}

func assertRangeUnchanged(t *testing.T, before, after []byte, r Range) {
	t.Helper()
	if !bytes.Equal(before[r.Start:r.End], after[r.Start:r.End]) {
		t.Fatalf("bytes %d-%d should not have been touched", r.Start, r.End)
	}
}
