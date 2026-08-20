package speedtest

import (
	"math"
	"testing"

	"streamnzb/pkg/usenet/pool"
)

// step1 builds a one-connection step from a mean per-article time and the dead
// portion of it, which is how the suggestion reads a real run.
func step1(meanMS, ttfbMS float64, segments int) StepResult {
	return StepResult{
		Connections: 1,
		Segments:    segments,
		WindowMS:    int64(meanMS * float64(segments)),
		TTFBMedian:  ttfbMS,
	}
}

// The model is only worth shipping if it reproduces measurements taken
// independently of it. These four links were measured by the throttled
// benchmarks in pkg/usenet/nntp, which set the bandwidth and round trip and
// recorded the resulting single-connection rate; feeding that rate back in has
// to name the depth that actually saturated each one.
func TestSuggestedDepthMatchesThrottledBenchmarks(t *testing.T) {
	const articleMB = 0.768

	cases := []struct {
		name string
		// t1MBps is the measured BenchmarkThrottledBodyOnly rate.
		t1MBps float64
		rttMS  float64
		// want is the smallest depth at which BenchmarkThrottledBodyPipelined
		// reached the link's rate.
		want int
	}{
		{name: "25Mbit/30ms", t1MBps: 2.78, rttMS: 30, want: 2},
		{name: "25Mbit/80ms", t1MBps: 2.35, rttMS: 80, want: 2},
		{name: "100Mbit/30ms", t1MBps: 8.35, rttMS: 30, want: 2},
		{name: "100Mbit/80ms", t1MBps: 5.41, rttMS: 80, want: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A step that ran long enough for the mean to be the article time.
			meanMS := articleMB / tc.t1MBps * 1000
			got, gain := suggestPipelineDepth([]StepResult{step1(meanMS, tc.rttMS, 64)})
			if got != tc.want {
				t.Fatalf("suggested depth = %d (gain %.2f), want %d — the model no longer reproduces the measured saturation depth",
					got, gain, tc.want)
			}
		})
	}
}

func TestSuggestPipelineDepth(t *testing.T) {
	cases := []struct {
		name           string
		steps          []StepResult
		wantDepth      int
		wantGainAtMost float64
		why            string
	}{
		{
			name:      "half the article time is dead",
			steps:     []StepResult{step1(200, 100, 32)},
			wantDepth: 2,
			why:       "hiding half the cost doubles the rate, which takes two requests outstanding",
		},
		{
			name:      "two thirds dead",
			steps:     []StepResult{step1(300, 200, 32)},
			wantDepth: 3,
			why:       "a 3x speedup takes three outstanding",
		},
		{
			name:      "a negligible gap is not worth paying depth for",
			steps:     []StepResult{step1(1000, 20, 32)},
			wantDepth: 1,
			why:       "2% is inside the noise and depth costs bytes on every seek",
		},
		{
			name:      "an extreme gap is capped",
			steps:     []StepResult{step1(100, 99, 32)},
			wantDepth: maxSuggestedPipelineDepth,
			why:       "past the cap, depth only adds articles to throw away on a seek",
		},
		{
			name:      "no single-connection step",
			steps:     []StepResult{{Connections: 2, Segments: 32, WindowMS: 6000, TTFBMedian: 100}},
			wantDepth: 0,
			why:       "a step at two connections already overlaps its own dead time, so it says nothing about depth",
		},
		{
			name:      "truncated step is not measured, it is noise",
			steps:     []StepResult{{Connections: 1, Segments: 32, WindowMS: 6000, TTFBMedian: 100, Truncated: true}},
			wantDepth: 0,
			why:       "this divides by a difference of two measurements, so a noisy window is amplified rather than averaged",
		},
		{
			name:      "too few articles to have a median worth reading",
			steps:     []StepResult{{Connections: 1, Segments: 1, WindowMS: 300, TTFBMedian: 100}},
			wantDepth: 0,
			why:       "one article cannot separate the gap from the transfer",
		},
		{
			name:      "dead time past the whole article is impossible",
			steps:     []StepResult{step1(100, 150, 32)},
			wantDepth: 0,
			why:       "the article cannot arrive before it was asked for; the run is unusable",
		},
		{
			name:      "no steps at all",
			steps:     nil,
			wantDepth: 0,
			why:       "a failed run has no opinion",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gain := suggestPipelineDepth(tc.steps)
			if got != tc.wantDepth {
				t.Fatalf("suggested depth = %d (gain %.2f), want %d — %s", got, gain, tc.wantDepth, tc.why)
			}
			if got == 0 && gain != 0 {
				t.Fatalf("no-opinion result carried a gain of %.2f; the UI would show it as a finding", gain)
			}
		})
	}
}

// The suggestion must never name a depth the provider setting would clamp away,
// or the UI would offer a value that silently becomes a different one.
func TestSuggestedDepthMatchesPoolCap(t *testing.T) {
	if maxSuggestedPipelineDepth != pool.MaxPipelineDepth {
		t.Fatalf("suggestion cap %d and pool cap %d have drifted apart",
			maxSuggestedPipelineDepth, pool.MaxPipelineDepth)
	}
	for dead := 1; dead < 100; dead++ {
		depth, _ := suggestPipelineDepth([]StepResult{step1(100, float64(dead), 32)})
		if depth > pool.MaxPipelineDepth {
			t.Fatalf("dead=%dms suggested depth %d, past the pool cap %d", dead, depth, pool.MaxPipelineDepth)
		}
	}
}

// The gain is what the UI shows next to the depth, so it has to be the speedup
// the depth is chosen to reach and not some other number.
func TestPipelineGainIsTheReportedSpeedup(t *testing.T) {
	depth, gain := suggestPipelineDepth([]StepResult{step1(250, 100, 32)})
	// 250ms per article of which 100 is dead: 250/150 = 1.667.
	if math.Abs(gain-1.6667) > 0.001 {
		t.Fatalf("gain = %.4f, want 1.6667", gain)
	}
	if depth != 2 {
		t.Fatalf("depth = %d, want 2 for a 1.67x speedup", depth)
	}
}
