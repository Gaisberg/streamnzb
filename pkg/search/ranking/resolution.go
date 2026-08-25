package ranking

import (
	"github.com/dreulavelle/jhin/rank"
)

// ResolutionTierPoints is what one step up the resolution ladder is worth.
//
// Resolution has to be priced in points because points are the whole of the
// order. jhin scores sources, codecs and traits but pays nothing for
// resolution itself — it treats resolution as a bucket to sort by, which is
// what this used to do too, and what made a rule's points unable to lift a
// release past a higher-resolution one however large they were.
//
// The step is wider than the whole spread of the baseline's other points — a
// remux is 1500, HEVC 700 — so nothing the
// baseline has an opinion about crosses a tier on its own, and the default
// order is still every 4K release, then every 1080p one. It is narrow enough
// that a rule can name a number that means "I want this more than I want the
// pixels": 20000 buys one tier, 80000 buys the ladder.
const ResolutionTierPoints = 20000

// resolutionRungs is the ladder, in tiers above the bottom of what any preset
// offers. The SD tiers sit below 720p rather than at it, so a 480p release
// that slipped past an allow-list still sorts under everything real, and an
// unparsable resolution sits at the bottom: it is not evidence of a bad
// release, which is why it is kept at all, but it is not evidence of a good
// one either.
var resolutionRungs = map[rank.Resolution]int{
	rank.Res2160p: 3,
	rank.Res1440p: 2,
	rank.Res1080p: 1,
	rank.Res720p:  0,
	rank.Res576p:  -1,
	rank.Res480p:  -1,
	rank.Res360p:  -2,
	rank.Res240p:  -2,
	// Unknown ranks with 720p rather than below it: a title nobody could
	// parse is usually an ordinary release with an unusual name, and burying
	// it would undo the decision to keep it.
	rank.ResUnknown: 0,
}

// applyResolutionScore pays each release for the resolution it is. It runs
// before the floor and the sort, like every other source of points, and over
// rejected results too, so a release a limit turned away still reports the
// score it would have had.
func (p *Profile) applyResolutionScore(results []Result) {
	for i := range results {
		results[i].Torrent.Rank += resolutionRungs[results[i].Torrent.Resolution()] * ResolutionTierPoints
	}
}
