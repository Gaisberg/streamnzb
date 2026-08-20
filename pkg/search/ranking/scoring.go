package ranking

import (
	"math"
	"time"

	jhinparser "github.com/dreulavelle/jhin/parser"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

// applyScoring adds the profile's NZB attribute points to each result's rank
// for this content kind. It runs over rejected results too, so a release that
// a limit turned away still reports the score it would have had.
//
// The points land in the same rank the title earned rather than in a parallel
// field: everything downstream — the sort here, the candidate score the
// playlist builds, the history — already reads that one number, and a second
// score would have to be threaded through all of it to mean anything.
func (p *Profile) applyScoring(kind string, results []Result) {
	if p == nil {
		return
	}
	scoring := config.ResolveScoring(p.scoring, kind)
	if !scoring.Enabled() {
		return
	}
	episodic := kind == KindSeries || kind == KindAnimeShow
	for i := range results {
		r := &results[i]
		if r.Candidate.Release == nil {
			continue
		}
		r.Torrent.Rank += attributeScore(scoring, episodic, r.Candidate.Release, r.Torrent.Data)
	}
}

// attributeScore is what one release earns from its NZB attributes: each
// configured attribute contributes its weight scaled by a 0-1 factor. An
// attribute the release does not carry contributes nothing, so a missing date
// or grab count never costs a release points it might have earned.
func attributeScore(scoring config.ScoringConfig, episodic bool, rel *release.Release, parsed *jhinparser.Result) int {
	total := 0.0
	if scoring.SizeTargetGB > 0 && scoring.SizeWeight != 0 {
		if size, ok := parser.EffectiveEpisodeSize(rel.Size, episodic, parsed); ok {
			total += float64(scoring.SizeWeight) * sizeFactor(size, scoring.SizeTargetGB)
		}
	}
	if scoring.AgeFreshDays > 0 && scoring.AgeWeight != 0 {
		if at, ok := rel.PublishedAt(); ok {
			total += float64(scoring.AgeWeight) * ageFactor(time.Since(at), scoring.AgeFreshDays)
		}
	}
	if scoring.GrabsTarget > 0 && scoring.GrabsWeight != 0 && rel.Grabs > 0 {
		total += float64(scoring.GrabsWeight) * grabsFactor(rel.Grabs, scoring.GrabsTarget)
	}
	return int(math.Round(total))
}

// sizeFactor peaks at the target and falls off linearly to zero at nothing and
// at twice the target, so "around 8 GB" is expressible without also having to
// say how far from 8 is still acceptable — the limits say that.
func sizeFactor(size int64, targetGB float64) float64 {
	target := gbToBytes(targetGB)
	if target <= 0 || size <= 0 {
		return 0
	}
	distance := math.Abs(float64(size-target)) / float64(target)
	return clampFactor(1 - distance)
}

// ageFactor is 1 for a release posted this instant and 0 once it reaches the
// configured freshness horizon. A release with a future date (clock skew on the
// indexer) is treated as brand new rather than scoring above the full weight.
func ageFactor(age time.Duration, freshDays int) float64 {
	horizon := time.Duration(freshDays) * 24 * time.Hour
	if horizon <= 0 {
		return 0
	}
	return clampFactor(1 - age.Seconds()/horizon.Seconds())
}

// grabsFactor is logarithmic: grab counts span orders of magnitude, so a linear
// scale would make everything below the target indistinguishable from zero.
func grabsFactor(grabs, target int) float64 {
	if grabs <= 0 || target <= 0 {
		return 0
	}
	return clampFactor(math.Log1p(float64(grabs)) / math.Log1p(float64(target)))
}

func clampFactor(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
