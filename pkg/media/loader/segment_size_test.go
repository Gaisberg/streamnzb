package loader

import (
	"context"
	"testing"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/unpack"
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

func TestShouldProbeMiddleSegmentRespectsFastMode(t *testing.T) {
	segments := []*Segment{
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
		{Segment: nzbSegment(10)},
	}
	if !shouldProbeMiddleSegment(context.Background(), segments) {
		t.Fatal("expected middle probe when fast mode disabled")
	}
	fast := unpack.WithArchiveFastFailoverMode(context.Background(), true)
	if shouldProbeMiddleSegment(fast, segments) {
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
