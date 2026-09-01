package rules_test

import (
	"fmt"
	"testing"
)

// A relative result-set question is counted once per release rather than once
// per set, so its cost grows with the square of the set. This is what says
// whether that is affordable on the sets a real search produces: the prune
// pass runs on every survivor, and a broad query can leave hundreds.
func BenchmarkPruneAggregates(b *testing.B) {
	for _, n := range []int{50, 200, 500, 1000} {
		envs := pruneEnvs(scoreBand(n)...)

		b.Run(fmt.Sprintf("shared/%d", n), func(b *testing.B) {
			_, post, err := compileSet("finalScore < 0 and count(finalScore >= 0) >= 6")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				post.ComputeAggregates(envs, "movie")
			}
		})

		b.Run(fmt.Sprintf("relative/%d", n), func(b *testing.B) {
			_, post, err := compileSet("count(finalScore >= current.finalScore + 5000) >= 6")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				post.ComputeRelativeAggregates(envs, "movie")
			}
		})
	}
}

// scoreBand spreads n scores over the range a resolution-scored profile
// produces, with the ties such a profile produces too — every release of one
// resolution and quality scores the same before the rules run.
func scoreBand(n int) []int {
	scores := make([]int, n)
	for i := range scores {
		scores[i] = 20000 - (i/4)*250
	}
	return scores
}
