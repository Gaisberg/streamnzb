package loader

import (
	"context"
	"testing"

	"streamnzb/pkg/media/nzb"
)

func TestScaleDecodedSizeUsesPerSegmentNZBBytes(t *testing.T) {
	got := scaleDecodedSize(15, 10, 8)
	if got != 12 {
		t.Fatalf("scaleDecodedSize(15,10,8) = %d, want 12", got)
	}
}

func TestSegmentProbeIndicesDistinctNZBBytesAndLast(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(15)},
		{Segment: nzbSegment(10)},
	}
	got := segmentProbeIndices(segments, nil, false, false)
	want := []int{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("probe indices = %v, want %v", got, want)
	}
	for i, idx := range want {
		if got[i] != idx {
			t.Fatalf("probe indices = %v, want %v", got, want)
		}
	}
}

// TestSegmentProbePlanSeparatesFinalSegmentClass reproduces the real-world
// failure: a 157-segment final RAR volume whose full segments encode to
// ~739,315 bytes (716,800 decoded) while the final segment encodes to 686,112
// (668,107 decoded) — only 7.2% apart. The old 10% clustering merged both into
// one class, probed ONLY the last segment, painted 668,107 across the whole
// volume, and truncated the virtual file by 7.6MB right where the Matroska
// cues live — tail reads returned 0 bytes and playback never started.
func TestSegmentProbePlanSeparatesFinalSegmentClass(t *testing.T) {
	const (
		fullEncoded  = 739315
		fullDecoded  = 716800
		lastEncoded  = 686112
		lastDecoded  = 668107
		segmentCount = 157
	)
	segments := make([]*Segment, segmentCount)
	for i := 0; i < segmentCount-1; i++ {
		// Vary encoded size slightly (±700) to mimic the 137 unique sizes seen
		// in the real NZB; all belong to the same decoded class.
		segments[i] = &Segment{Segment: nzbSegment(int64(fullEncoded + (i%7)*100 - 300))}
	}
	segments[segmentCount-1] = &Segment{Segment: nzbSegment(lastEncoded)}

	// Fast-mode plan (skipGapProbing=true) must probe a full-class segment, not
	// just the physically-last one.
	indices := segmentProbeIndices(segments, nil, false, true)
	hasNonLast := false
	for _, idx := range indices {
		if idx != segmentCount-1 {
			hasNonLast = true
			break
		}
	}
	if !hasNonLast {
		t.Fatalf("probe plan %v must include a full-size-class segment, not only the last", indices)
	}

	// With both classes probed, the reconstructed map must hit the true total.
	probed := map[int]int64{}
	for _, idx := range indices {
		if idx == segmentCount-1 {
			probed[idx] = lastDecoded
		} else {
			probed[idx] = fullDecoded
		}
	}
	sizes := buildSegmentDecodedSizesFromProbes(segments, probed, nil, true)
	var total int64
	for _, sz := range sizes {
		total += sz
	}
	want := int64((segmentCount-1)*fullDecoded + lastDecoded)
	if total != want {
		t.Fatalf("reconstructed volume size = %d, want %d (delta %d)", total, want, total-want)
	}
}

// TestLastSegmentNeverRepresentsFullClass reproduces the REMUX flicker bug:
// a 23,538-segment file whose LAST segment (734,057 encoded / 711,755 decoded)
// sits WITHIN cluster tolerance of the full segments (~739,600 / 716,800). The
// plan collapsed to probing only the last segment and painted 711,755 across
// every segment (-5,045 bytes each) — offsets drifted ~1MB per 140MB, the
// Matroska demuxer desynced, and the client reopened the stream 3-4x/second.
func TestLastSegmentNeverRepresentsFullClass(t *testing.T) {
	const (
		fullDecoded  = 716800
		lastEncoded  = 734057
		lastDecoded  = 711755
		segmentCount = 500
	)
	segments := make([]*Segment, segmentCount)
	for i := 0; i < segmentCount-1; i++ {
		// Full segments: encoded ~739,600 ± small content-dependent variation —
		// all within 3% of the last segment's encoded size.
		segments[i] = &Segment{Segment: nzbSegment(int64(739600 + (i%9)*700 - 2800))}
	}
	segments[segmentCount-1] = &Segment{Segment: nzbSegment(lastEncoded)}

	// The fast-mode plan must include a non-last probe even though clustering
	// merges everything into one class.
	indices := segmentProbeIndices(segments, nil, false, true)
	hasNonLast := false
	for _, idx := range indices {
		if idx != segmentCount-1 {
			hasNonLast = true
			break
		}
	}
	if !hasNonLast {
		t.Fatalf("probe plan %v collapsed to the last segment only", indices)
	}

	// With both probes, every full segment must get the full-class size — the
	// last segment's decoded size must never leak onto other segments.
	probed := map[int]int64{}
	for _, idx := range indices {
		if idx == segmentCount-1 {
			probed[idx] = lastDecoded
		} else {
			probed[idx] = fullDecoded
		}
	}
	sizes := buildSegmentDecodedSizesFromProbes(segments, probed, nil, true)
	for i := 0; i < segmentCount-1; i++ {
		if sizes[i] != fullDecoded {
			t.Fatalf("segment %d size = %d, want %d (last segment's size leaked into the full class)", i, sizes[i], fullDecoded)
		}
	}
	if sizes[segmentCount-1] != lastDecoded {
		t.Fatalf("last segment size = %d, want %d", sizes[segmentCount-1], lastDecoded)
	}
}

func TestNZBBytesNearby(t *testing.T) {
	if !nzbBytesNearby(739315, 738628) {
		t.Error("0.1%% apart must be the same class")
	}
	if nzbBytesNearby(686112, 739315) {
		t.Error("7.2%% apart must be separate classes (final-segment case)")
	}
}

func TestSegmentUnprobedIndices(t *testing.T) {
	got := segmentUnprobedIndices(5, []int{0, 2, 4})
	want := []int{1, 3}
	if len(got) != len(want) {
		t.Fatalf("unprobed = %v, want %v", got, want)
	}
	for i, idx := range want {
		if got[i] != idx {
			t.Fatalf("unprobed = %v, want %v", got, want)
		}
	}
}

func TestSegmentProbeIndicesSkipsKnownEstimatorBytes(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
	}
	known := map[int64]int64{10: 8}
	got := segmentProbeIndices(segments, known, true, false)
	want := []int{1, 2}
	if len(got) != len(want) {
		t.Fatalf("probe indices = %v, want %v", got, want)
	}
	for i, idx := range want {
		if got[i] != idx {
			t.Fatalf("probe indices = %v, want %v", got, want)
		}
	}
}

func TestBuildSegmentDecodedSizesFromProbesVariableNZBBytes(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(15)},
		{Segment: nzbSegment(10)},
	}
	probed := map[int]int64{0: 8, 1: 12, 2: 5}
	sizes := buildSegmentDecodedSizesFromProbes(segments, probed, nil, false)
	want := []int64{8, 12, 5}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("sizes[%d] = %d, want %d (full=%v)", i, sizes[i], w, sizes)
		}
	}
}

func TestApplyUniformMiddleCalibration(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
	}
	sizes := []int64{8, 8, 5}
	applyUniformMiddleCalibration(segments, sizes, 1, 10)
	want := []int64{8, 10, 5}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("sizes[%d] = %d, want %d (full=%v)", i, sizes[i], w, sizes)
		}
	}
}

func TestShouldProbeMiddleSegmentAlwaysSkipsInFastMode(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
	}
	if shouldProbeMiddleSegment(context.Background(), segments) {
		t.Fatal("expected no middle probe in fast failover mode")
	}
}

func TestShouldProbeMiddleSegmentSkipsVariableNZBBytes(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(15)},
		{Segment: nzbSegment(10)},
	}
	if shouldProbeMiddleSegment(context.Background(), segments) {
		t.Fatal("expected no middle probe when NZB bytes already vary per segment")
	}
}

func nzbSegment(bytes int64) nzb.Segment {
	return nzb.Segment{Bytes: bytes, Number: 1}
}

func TestSegmentProbeIndicesGroupsSimilarSizesInSkipGapProbing(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(792710)},
		{Segment: nzbSegment(792818)},
		{Segment: nzbSegment(792682)},
		{Segment: nzbSegment(792562)},
		{Segment: nzbSegment(792707)},
		{Segment: nzbSegment(82924)},
	}
	got := segmentProbeIndices(segments, nil, false, true)
	want := []int{3, 5}
	if len(got) != len(want) {
		t.Fatalf("probe indices = %v, want %v", got, want)
	}
	for i, idx := range want {
		if got[i] != idx {
			t.Fatalf("probe indices = %v, want %v", got, want)
		}
	}
}

func TestBuildSegmentDecodedSizesFromProbesGroupsSimilarSizesInSkipGapProbing(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(792710)},
		{Segment: nzbSegment(792818)},
		{Segment: nzbSegment(792682)},
		{Segment: nzbSegment(82924)},
	}
	probed := map[int]int64{0: 768000, 3: 80000}
	sizes := buildSegmentDecodedSizesFromProbes(segments, probed, nil, true)
	want := []int64{768000, 768000, 768000, 80000}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("sizes[%d] = %d, want %d (full=%v)", i, sizes[i], w, sizes)
		}
	}
}

func TestBuildSegmentDecodedSizesFromProbesPreservesEstimator(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(768000)},
		{Segment: nzbSegment(768000)},
		{Segment: nzbSegment(366792)},
	}
	probed := map[int]int64{2: 350762}
	known := map[int64]int64{768000: 768000}
	sizes := buildSegmentDecodedSizesFromProbes(segments, probed, known, true)
	want := []int64{768000, 768000, 350762}
	for i, w := range want {
		if sizes[i] != w {
			t.Fatalf("sizes[%d] = %d, want %d (full=%v)", i, sizes[i], w, sizes)
		}
	}
}
