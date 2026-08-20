package speedtest

import "math"

// Suggesting a pipeline depth.
//
// A worker holds one connection and fetches articles strictly back to back, so
// the single-connection step measures exactly the thing pipelining removes: the
// dead time between one article ending and the next one starting. Two numbers
// from that step are enough.
//
//	mean article time = WindowMS / Segments   (the whole per-article cost)
//	dead time         = TTFBMedian            (issue BODY → first byte back)
//
// Pipelining overlaps the dead time with the previous article's transfer, so
// the ceiling is the transfer alone and the speedup is
//
//	mean / (mean - dead)
//
// One pipeline slot is needed per unit of speedup — a link that can go twice as
// fast needs two requests outstanding to get there — so the suggested depth is
// that ratio rounded up.
//
// TTFB rather than the DATE ping on purpose: the ping is network round trip
// only, while the gap pipelining actually hides is the round trip *plus*
// whatever the provider spends locating the article, which is what TTFB
// measures on the real BODY path.
//
// This is the same model that set DefaultPipelineDepth, checked against the
// throttled benchmarks in pkg/usenet/nntp: fed the T1 those runs measured, it
// predicts the depth that actually saturated each link, including the 100 Mbit
// 80 ms case that needs 3 where every other case needs 2.

// minPipelineGain is the speedup below which the suggestion is "off" rather
// than a depth. Depth is not free — it is also how many articles are already
// committed to a connection when the viewer seeks — so a gain this small is not
// worth paying for.
const minPipelineGain = 1.05

// maxSuggestedPipelineDepth mirrors pool.MaxPipelineDepth. It is repeated
// rather than imported because this package deliberately does not depend on
// pkg/usenet/pool; the two are kept in step by TestSuggestedDepthMatchesPoolCap.
const maxSuggestedPipelineDepth = 8

// suggestPipelineDepth returns the depth the single-connection step implies and
// the speedup it predicts, or (0, 0) when the step cannot support a suggestion.
// A zero depth means "no opinion" and must not be shown as a recommendation.
func suggestPipelineDepth(steps []StepResult) (int, float64) {
	step, ok := singleConnectionStep(steps)
	if !ok {
		return 0, 0
	}

	meanMS := float64(step.WindowMS) / float64(step.Segments)
	deadMS := step.TTFBMedian
	// A dead time at or past the whole article's cost is not a measurement, it
	// is noise: the article cannot have arrived before it was requested.
	if deadMS <= 0 || deadMS >= meanMS {
		return 0, 0
	}

	gain := meanMS / (meanMS - deadMS)
	if gain < minPipelineGain {
		// Off. Distinguishable from "no opinion" by the gain travelling with it.
		return 1, gain
	}

	depth := int(math.Ceil(gain))
	if depth < 2 {
		depth = 2
	}
	if depth > maxSuggestedPipelineDepth {
		depth = maxSuggestedPipelineDepth
	}
	return depth, gain
}

// singleConnectionStep finds the one-connection step and reports whether it can
// be trusted. A truncated step's window was closed by the byte budget rather
// than the clock, which makes its rate noisy — and this suggestion divides by a
// difference of two measurements, so noise there is amplified, not averaged out.
func singleConnectionStep(steps []StepResult) (StepResult, bool) {
	for _, step := range steps {
		if step.Connections != 1 {
			continue
		}
		if step.Truncated || step.Segments < 2 || step.WindowMS <= 0 {
			return StepResult{}, false
		}
		return step, true
	}
	return StepResult{}, false
}
