package ranking_test

import (
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
)

// The legacy "must contain" rules map onto jhin's Require, which gates
// without exempting a release from the profile's other rules.
func TestSynthesizeRequireRules(t *testing.T) {
	yes := true
	p := config.Synthesize(config.FilterProfileConfig{
		Name:               "Legacy",
		AllowedResolutions: []string{"1080p", "720p"},
		BlockedQualities:   []string{"CAM"},
		RequireHDR:         &yes,
		RequiredKeywords:   []string{"Obsession"},
	})
	r, err := rank.New(p)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		title string
		want  bool
	}{
		{"Obsession 2026 1080p BluRay HDR", true},
		{"Obsession 2026 1080p BluRay", false},          // HDR required
		{"Something Else 2026 1080p BluRay HDR", false}, // keyword required
		{"Obsession 2026 1080p CAM HDR", false},         // require must not bypass the CAM block
		{"Obsession 2026 2160p BluRay HDR", false},      // require must not bypass the resolution gate
	}
	for _, tt := range tests {
		got := r.Rank(tt.title)
		if got.Fetch != tt.want {
			t.Errorf("%-42s Fetch=%v want %v (rejections %v)", tt.title, got.Fetch, tt.want, got.Rejections)
		}
	}
}

// Weighted patterns are what let one preference outrank another; a flat
// preference bonus applies once no matter how many things match.
func TestWeightedPatternsRankPreferencesAgainstEachOther(t *testing.T) {
	p := rank.Default()
	p.Options.RemoveTrash = false
	p.PatternRanks = []rank.PatternRank{
		{Pattern: `\bDual[. _-]?Audio\b`, Rank: 5000},
		{Pattern: `\bIMAX\b`, Rank: 2000},
	}
	r, err := rank.New(p)
	if err != nil {
		t.Fatal(err)
	}

	base := r.Rank("Movie 2020 1080p WEB-DL-GRP")
	dual := r.Rank("Movie 2020 1080p WEB-DL Dual.Audio-GRP")
	imax := r.Rank("Movie 2020 1080p WEB-DL IMAX-GRP")
	both := r.Rank("Movie 2020 1080p WEB-DL IMAX Dual.Audio-GRP")

	// Deltas are compared against each other rather than to the pattern score,
	// because the added words change what else parses out of the title: "Dual
	// Audio" also marks the release dubbed, and IMAX is an edition.
	if dual.Rank <= base.Rank {
		t.Errorf("dual audio should raise the score: %d vs %d", dual.Rank, base.Rank)
	}
	if imax.Rank <= base.Rank {
		t.Errorf("IMAX should raise the score: %d vs %d", imax.Rank, base.Rank)
	}
	if dual.Rank <= imax.Rank {
		t.Errorf("the heavier preference should win: dual=%d imax=%d", dual.Rank, imax.Rank)
	}
	// They stack, where a flat preference bonus would apply once however many
	// matched. Compared against each alone rather than as an exact sum, since
	// adding words to a title also changes what else parses out of it.
	if both.Rank <= dual.Rank || both.Rank <= imax.Rank {
		t.Errorf("weighted patterns should stack: both=%d dual=%d imax=%d", both.Rank, dual.Rank, imax.Rank)
	}
}
