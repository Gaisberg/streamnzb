package speedtest

import (
	"testing"
	"time"

	"streamnzb/pkg/media/nzb"
)

func TestRampStepsLadderEndsAtConfiguredConnections(t *testing.T) {
	cases := []struct {
		conns int
		quick bool
		want  []int
	}{
		{conns: 20, want: []int{1, 2, 4, 8, 20}},
		{conns: 8, want: []int{1, 2, 4, 8}},
		{conns: 3, want: []int{1, 2, 3}},
		{conns: 1, want: []int{1}},
		{conns: 0, want: []int{1}},
		{conns: 50, quick: true, want: []int{50}},
	}
	for _, tc := range cases {
		got := rampSteps(tc.conns, tc.quick)
		if len(got) != len(tc.want) {
			t.Fatalf("rampSteps(%d, %v) = %v, want %v", tc.conns, tc.quick, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("rampSteps(%d, %v) = %v, want %v", tc.conns, tc.quick, got, tc.want)
			}
		}
		if len(got) > maxRampSteps {
			t.Fatalf("rampSteps(%d) returned %d steps, over the %d cap", tc.conns, len(got), maxRampSteps)
		}
	}
}

func TestPeakAndKneePicksSmallestStepNearPeak(t *testing.T) {
	steps := []StepResult{
		{Connections: 1, Mbps: 40, WindowMS: 4500},
		{Connections: 2, Mbps: 78, WindowMS: 4500},
		{Connections: 4, Mbps: 140, WindowMS: 4500},
		{Connections: 8, Mbps: 142, WindowMS: 4500},
		{Connections: 20, Mbps: 141, WindowMS: 4500},
	}
	peak, knee, plateau := peakAndKnee(steps)
	if peak != 142 {
		t.Fatalf("peak = %v, want 142", peak)
	}
	if knee != 4 {
		t.Fatalf("knee = %d, want 4 (first step within %v of peak)", knee, kneeFraction)
	}
	if !plateau {
		t.Fatal("expected a plateau: throughput stopped rising before the last step")
	}
}

func TestPeakAndKneeReportsNoPlateauWhenStillClimbing(t *testing.T) {
	// Every step is faster than the last, so the ramp ran out of connections
	// before the provider ran out of speed. The suggestion is a floor.
	steps := []StepResult{
		{Connections: 1, Mbps: 46, WindowMS: 4500},
		{Connections: 2, Mbps: 124, WindowMS: 4500},
		{Connections: 4, Mbps: 220, WindowMS: 4500},
		{Connections: 8, Mbps: 429, WindowMS: 4500},
		{Connections: 100, Mbps: 911, WindowMS: 2900, Truncated: true},
	}
	peak, knee, plateau := peakAndKnee(steps)
	if peak != 911 {
		t.Fatalf("peak = %v, want 911 (a shortened but steady window still counts)", peak)
	}
	if knee != 100 {
		t.Fatalf("knee = %d, want 100", knee)
	}
	if plateau {
		t.Fatal("expected no plateau: the top step was still the fastest")
	}
}

func TestPeakAndKneeIgnoresTruncatedSteps(t *testing.T) {
	// The last step ran out of byte budget after half a second. Its number must
	// not drag the peak down or move the recommendation.
	steps := []StepResult{
		{Connections: 1, Mbps: 87, WindowMS: 4500},
		{Connections: 2, Mbps: 152, WindowMS: 4500},
		{Connections: 4, Mbps: 271, WindowMS: 4500},
		{Connections: 8, Mbps: 483, WindowMS: 4500},
		{Connections: 50, Mbps: 386, WindowMS: 400, Truncated: true},
	}
	peak, knee, plateau := peakAndKnee(steps)
	if peak != 483 {
		t.Fatalf("peak = %v, want 483 (truncated step excluded)", peak)
	}
	if knee != 8 {
		t.Fatalf("knee = %d, want 8", knee)
	}
	// Dropping the bad step also drops the only evidence about what happens
	// above 8 connections, so this is a floor, not a plateau.
	if plateau {
		t.Fatal("expected no plateau: the trustworthy part of the ramp was still climbing")
	}
}

func TestPeakAndKneeFindsPlateauWhenTopStepIsSlower(t *testing.T) {
	steps := []StepResult{
		{Connections: 1, Mbps: 87, WindowMS: 4500},
		{Connections: 8, Mbps: 483, WindowMS: 4500},
		{Connections: 50, Mbps: 386, WindowMS: 4500},
	}
	peak, knee, plateau := peakAndKnee(steps)
	if peak != 483 || knee != 8 {
		t.Fatalf("peakAndKnee = (%v, %d), want (483, 8)", peak, knee)
	}
	if !plateau {
		t.Fatal("expected a plateau: 50 connections measured slower than 8")
	}
}

func TestPeakAndKneeFallsBackWhenEveryStepTruncated(t *testing.T) {
	steps := []StepResult{
		{Connections: 1, Mbps: 40, WindowMS: 300, Truncated: true},
		{Connections: 2, Mbps: 90, WindowMS: 300, Truncated: true},
	}
	peak, knee, plateau := peakAndKnee(steps)
	if peak != 90 || knee != 2 || plateau {
		t.Fatalf("peakAndKnee = (%v, %d, %v), want (90, 2, false)", peak, knee, plateau)
	}
}

func TestResolutionVerdictsReportCheapestSustainingStep(t *testing.T) {
	steps := []StepResult{
		{Connections: 1, Mbps: 8, WindowMS: 4500},
		{Connections: 2, Mbps: 20, WindowMS: 4500},
		{Connections: 4, Mbps: 45, WindowMS: 4500},
		{Connections: 8, Mbps: 90, WindowMS: 4500},
	}
	verdicts := resolutionVerdicts(steps, 90)
	if len(verdicts) != 3 {
		t.Fatalf("got %d verdicts, want 3", len(verdicts))
	}

	// 720p needs 10 * 1.25 = 12.5 Mbps -> the 2-connection step is the first
	// to clear it; 1 connection at 8 Mbps does not.
	if verdicts[0].Label != "720p" || verdicts[0].Connections != 2 || !verdicts[0].Achieved {
		t.Fatalf("720p verdict = %+v, want 2 connections achieved", verdicts[0])
	}
	// 1080p needs 31.25 -> 4 connections.
	if verdicts[1].Connections != 4 {
		t.Fatalf("1080p verdict = %+v, want 4 connections", verdicts[1])
	}
	// 4K needs 75 -> 8 connections, and 90 Mbps peak covers exactly one stream.
	if verdicts[2].Connections != 8 || verdicts[2].Streams != 1 {
		t.Fatalf("4K verdict = %+v, want 8 connections and 1 stream", verdicts[2])
	}
}

func TestResolutionVerdictsMarkUnreachableTiers(t *testing.T) {
	steps := []StepResult{{Connections: 1, Mbps: 14, WindowMS: 4500}}
	verdicts := resolutionVerdicts(steps, 14)
	if !verdicts[0].Achieved || verdicts[0].Connections != 1 {
		t.Fatalf("720p verdict = %+v, want achieved on 1 connection", verdicts[0])
	}
	if verdicts[1].Achieved || verdicts[1].Connections != 0 {
		t.Fatalf("1080p verdict = %+v, want unreachable", verdicts[1])
	}
	if verdicts[2].Achieved {
		t.Fatalf("4K verdict = %+v, want unreachable", verdicts[2])
	}
}

func TestResolutionVerdictsSkipUntrustworthySteps(t *testing.T) {
	// The 4-connection step is the only one fast enough for 1080p, but its
	// window was too short to promise sustained playback.
	steps := []StepResult{
		{Connections: 1, Mbps: 15, WindowMS: 4500},
		{Connections: 4, Mbps: 200, WindowMS: 300, Truncated: true},
	}
	verdicts := resolutionVerdicts(steps, 15)
	if verdicts[1].Connections != 0 {
		t.Fatalf("1080p verdict = %+v, want no connection count from a short window", verdicts[1])
	}
}

func TestBudgetReachedHonoursStepShareAndCeiling(t *testing.T) {
	state := &runState{maxBytes: 1000}
	state.bytes.Store(100)
	if state.budgetReached() {
		t.Fatal("no step limit set and under the ceiling: fetching should continue")
	}

	state.stepLimit.Store(200)
	if state.budgetReached() {
		t.Fatal("under the step share: fetching should continue")
	}
	state.bytes.Store(200)
	if !state.budgetReached() {
		t.Fatal("at the step share: fetching should stop")
	}

	// A later step gets a fresh share, but the run ceiling still wins.
	state.stepLimit.Store(2000)
	if state.budgetReached() {
		t.Fatal("new step share should re-open fetching")
	}
	state.bytes.Store(1000)
	if !state.budgetReached() {
		t.Fatal("at the run ceiling: fetching should stop regardless of the step share")
	}
}

func TestPeakAndKneeAllZero(t *testing.T) {
	peak, knee, plateau := peakAndKnee([]StepResult{{Connections: 1}, {Connections: 2}})
	if peak != 0 || knee != 0 || plateau {
		t.Fatalf("peakAndKnee = (%v, %d, %v), want (0, 0, false)", peak, knee, plateau)
	}
}

func TestCorpusFromNZBDropsPar2AndOrdersLargestFirst(t *testing.T) {
	parsed := &nzb.NZB{Files: []nzb.File{
		{Subject: "Release.part02.rar", Segments: []nzb.Segment{{ID: "small-a", Bytes: 100}}},
		{Subject: "Release.vol000+01.par2", Segments: []nzb.Segment{{ID: "par2", Bytes: 9000}}},
		{Subject: "Release.part01.rar", Segments: []nzb.Segment{{ID: "big-a", Bytes: 500}, {ID: "big-b", Bytes: 500}}},
	}}

	corpus := corpusFromNZB(parsed, "test")
	if corpus == nil {
		t.Fatal("expected a corpus")
	}
	want := []string{"big-a", "big-b", "small-a"}
	if len(corpus.Articles) != len(want) {
		t.Fatalf("got %d articles, want %d", len(corpus.Articles), len(want))
	}
	for i, id := range want {
		if corpus.Articles[i].ID != id {
			t.Fatalf("article %d = %q, want %q", i, corpus.Articles[i].ID, id)
		}
	}
}

func TestCorpusFromNZBEmptyIsNil(t *testing.T) {
	if corpus := corpusFromNZB(&nzb.NZB{}, "test"); corpus != nil {
		t.Fatalf("expected nil corpus, got %+v", corpus)
	}
	onlyPar2 := &nzb.NZB{Files: []nzb.File{
		{Subject: "Release.vol000+01.par2", Segments: []nzb.Segment{{ID: "par2", Bytes: 10}}},
	}}
	if corpus := corpusFromNZB(onlyPar2, "test"); corpus != nil {
		t.Fatalf("expected nil corpus for a par2-only NZB, got %+v", corpus)
	}
}

func TestNextArticleWrapsAndFlagsLooping(t *testing.T) {
	state := &runState{corpus: &Corpus{Articles: []Article{{ID: "a"}, {ID: "b"}}}}
	got := []string{
		state.nextArticle().ID,
		state.nextArticle().ID,
		state.nextArticle().ID,
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "a" {
		t.Fatalf("nextArticle order = %v, want [a b a]", got)
	}
	if !state.looped.Load() {
		t.Fatal("expected the loop flag once the corpus wrapped")
	}
}

func TestStepAccumIgnoresWarmupSamples(t *testing.T) {
	accum := &stepAccum{windowStart: time.Now().Add(time.Hour)}
	accum.addSegment(time.Now(), 50*time.Millisecond)
	var result StepResult
	accum.fill(&result)
	if result.Segments != 0 {
		t.Fatalf("segments = %d, want 0 (sample was inside the warm-up)", result.Segments)
	}

	accum = &stepAccum{windowStart: time.Now().Add(-time.Hour)}
	accum.addSegment(time.Now(), 40*time.Millisecond)
	accum.addSegment(time.Now(), 60*time.Millisecond)
	accum.fill(&result)
	if result.Segments != 2 {
		t.Fatalf("segments = %d, want 2", result.Segments)
	}
	if result.TTFBMedian != 40 {
		t.Fatalf("ttfb median = %v, want 40", result.TTFBMedian)
	}
}

func TestPercentileMS(t *testing.T) {
	samples := []time.Duration{
		100 * time.Millisecond,
		10 * time.Millisecond,
		50 * time.Millisecond,
		20 * time.Millisecond,
	}
	if got := percentileMS(samples, 0.5); got != 20 {
		t.Fatalf("p50 = %v, want 20", got)
	}
	if got := percentileMS(samples, 0.95); got != 100 {
		t.Fatalf("p95 = %v, want 100", got)
	}
	if got := percentileMS(nil, 0.5); got != 0 {
		t.Fatalf("p50 of nothing = %v, want 0", got)
	}
}
