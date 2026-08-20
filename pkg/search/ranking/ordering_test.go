package ranking_test

import (
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
)

// orderingProfile builds a profile from a jhin spec, which these tests need in
// order to set ResolutionOrder and MinRank directly.
func orderingProfile(t *testing.T, name string, spec rank.Profile, mutate func(*config.FilterProfileConfig)) *ranking.Profile {
	t.Helper()
	fp := config.FilterProfileConfig{Name: name, Ranking: &spec}
	if mutate != nil {
		mutate(&fp)
	}
	p, err := ranking.Compile(fp)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A profile that puts 1080p first returns 1080p first, even when a 2160p
// release outscores it. The precedence is the whole point of the setting:
// "prefer 1080p without banning 4K" is not expressible any other way.
func TestResolutionPrecedenceSurvivesScoring(t *testing.T) {
	spec := rank.Default()
	spec.ResolutionOrder = []rank.Resolution{rank.Res1080p, rank.Res2160p}
	p := orderingProfile(t, "precedence", spec, nil)

	// The remux scores far above the WEB-DL, so a score-only sort would put
	// the 2160p release first.
	cands := []triage.Candidate{
		{Release: &release.Release{Title: "Movie 2020 2160p BluRay REMUX HEVC-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}},
	}
	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, cands, rank.RankOptions{})
	if len(kept) != 2 {
		t.Fatalf("kept %d releases, want 2", len(kept))
	}
	if got := kept[0].Torrent.Resolution(); got != rank.Res1080p {
		t.Fatalf("first result is %s, want 1080p — resolution precedence was discarded", got)
	}
	if kept[1].Torrent.Rank <= kept[0].Torrent.Rank {
		t.Fatal("test is not exercising precedence: the 2160p release did not outscore the 1080p one")
	}
}

// The score floor judges the score a release actually ends up with. The
// library bonus, the NZB attribute points and the rules all pay out after jhin
// has read the title, so a floor applied to the title's score alone rejects
// releases whose real score clears it.
func TestMinRankSeesLibraryBonus(t *testing.T) {
	spec := rank.Default()
	// Well above what a plain WEB-DL earns from its title, but below what it
	// earns once the library bonus lands.
	spec.Options.MinRank = 5000
	p := orderingProfile(t, "floor", spec, func(fp *config.FilterProfileConfig) {
		bonus := 100000
		fp.LibraryScoreBonus = &bonus
	})

	fresh := &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}
	if kept, _ := applyOne(p, ranking.KindMovie, fresh); kept {
		t.Fatal("a fresh release below the floor was kept")
	}

	cached := &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP", IsLibrary: true}
	kept, reasons := applyOne(p, ranking.KindMovie, cached)
	if !kept {
		t.Fatalf("library release rejected by the floor before its bonus counted: %v", reasons)
	}
}

// NZB attribute points count towards the floor for the same reason.
func TestMinRankSeesAttributeScoring(t *testing.T) {
	spec := rank.Default()
	spec.Options.MinRank = 5000
	p := orderingProfile(t, "floor-nzb", spec, func(fp *config.FilterProfileConfig) {
		fp.Scoring = map[string]*config.ScoringConfig{
			config.LimitKindDefault: {GrabsTarget: 100, GrabsWeight: 100000},
		}
	})

	popular := &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP", Size: 8e9, Grabs: 100}
	kept, reasons := applyOne(p, ranking.KindMovie, popular)
	if !kept {
		t.Fatalf("release rejected by the floor before its NZB points counted: %v", reasons)
	}
}

// A release the floor does reject says so in terms the user set.
func TestMinRankRejectionNamesTheFloor(t *testing.T) {
	spec := rank.Default()
	spec.Options.MinRank = 500000
	p := orderingProfile(t, "floor-msg", spec, nil)

	kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"})
	if kept {
		t.Fatal("release above an unreachable floor was kept")
	}
	found := false
	for _, r := range reasons {
		if len(r) > 5 && r[:5] == "score" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rejection reasons %v do not mention the score floor", reasons)
	}
}
