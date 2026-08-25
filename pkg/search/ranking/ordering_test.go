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
// order to set MinRank directly.
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

// Score is the whole of the order. A rule that pays enough puts its releases
// first, whatever resolution they are and whatever resolution the rest are:
// resolution used to sort ahead of the score as a hard tier, so a rule worth
// 80000 could not lift a 1080p release past a 4K one worth 900.
func TestScoreOrdersAcrossResolutions(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "language",
		Preset: config.PresetUHD,
		Rules: []config.RuleConfig{{
			Name:   "Prefer Finnish",
			When:   `"fi" in languages`,
			Action: config.RuleActionScore,
			Points: 80000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The 2160p remux is the highest-scoring release here on its own merits,
	// which is what makes the rule's points worth testing.
	cands := []triage.Candidate{
		{Release: &release.Release{Title: "Movie 2020 2160p BluRay REMUX HEVC-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 1080p FINNISH WEB-DL H264-GRP"}},
	}
	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, cands, rank.RankOptions{})
	if len(kept) != 2 {
		t.Fatalf("kept %d releases, want 2", len(kept))
	}
	if got := kept[0].Torrent.Resolution(); got != rank.Res1080p {
		t.Fatalf("first result is %s, want the 1080p release the rule paid for", got)
	}
	if kept[0].Torrent.Rank <= kept[1].Torrent.Rank {
		t.Fatal("test is not exercising the order: the rule did not outscore the 2160p remux")
	}
}

// Without a rule to say otherwise, the better resolution still leads: the
// tiers are scored, so dropping the resolution bracket did not turn the
// default order into a lottery.
func TestBetterResolutionStillLeadsByScore(t *testing.T) {
	p := orderingProfile(t, "default", config.PresetSpec(config.PresetUHD), nil)

	cands := []triage.Candidate{
		{Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 2160p WEB-DL H265-GRP"}},
	}
	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, cands, rank.RankOptions{})
	if len(kept) != 2 {
		t.Fatalf("kept %d releases, want 2", len(kept))
	}
	if got := kept[0].Torrent.Resolution(); got != rank.Res2160p {
		t.Fatalf("first result is %s, want 2160p", got)
	}
}

// A resolution is worth one tier step, and the step is wide enough that nothing
// the baseline scores — a remux is 1500 — crosses it. That is what keeps "4K
// first" true by default now that resolution competes for the order in points
// rather than outranking them.
func TestResolutionIsWorthOneTierStep(t *testing.T) {
	p := orderingProfile(t, "tiers", config.PresetSpec(config.PresetUHD), nil)

	rankOfTitle := func(title string) int {
		kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
			[]triage.Candidate{{Release: &release.Release{Title: title}}}, rank.RankOptions{})
		if len(kept) != 1 {
			t.Fatalf("%q was not kept", title)
		}
		return kept[0].Torrent.Rank
	}

	uhd := rankOfTitle("Movie 2020 2160p WEB-DL H264-GRP")
	hd := rankOfTitle("Movie 2020 1080p WEB-DL H264-GRP")
	sd := rankOfTitle("Movie 2020 720p WEB-DL H264-GRP")

	if got := hd - sd; got != ranking.ResolutionTierPoints {
		t.Errorf("1080p is worth %d over 720p, want %d", got, ranking.ResolutionTierPoints)
	}
	if got := uhd - hd; got != 2*ranking.ResolutionTierPoints {
		t.Errorf("2160p is worth %d over 1080p, want %d", got, 2*ranking.ResolutionTierPoints)
	}

	// A remux is the best thing the baseline can say about a release, and it
	// still does not buy a tier.
	if remux := rankOfTitle("Movie 2020 1080p BluRay REMUX AVC-GRP"); remux >= uhd {
		t.Errorf("a 1080p remux scored %d against a 2160p WEB-DL's %d — the tier step is too narrow to hold", remux, uhd)
	}
}

// An unreadable resolution ranks with 720p rather than under it: keeping such
// a release and then burying it would undo the decision to keep it.
func TestUnknownResolutionRanksWithTheBottomTier(t *testing.T) {
	p := orderingProfile(t, "unknown", config.PresetSpec(config.PresetUHD), nil)

	cands := []triage.Candidate{
		{Release: &release.Release{Title: "Movie 2020 720p WEB-DL H264-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 WEB-DL H264-GRP"}},
	}
	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, cands, rank.RankOptions{})
	if len(kept) != 2 {
		t.Fatalf("kept %d releases, want 2", len(kept))
	}
	if kept[0].Torrent.Rank != kept[1].Torrent.Rank {
		t.Errorf("unknown scored %d against 720p's %d", kept[1].Torrent.Rank, kept[0].Torrent.Rank)
	}
}

// The score floor judges the score a release actually ends up with. The
// library bonus, the NZB attribute points and the rules all pay out after jhin
// has read the title, so a floor applied to the title's score alone rejects
// releases whose real score clears it.
func TestMinRankSeesLibraryBonus(t *testing.T) {
	spec := rank.Default()
	// Well above what a plain 1080p WEB-DL earns from its title and its
	// resolution, but below what it earns once the library bonus lands.
	spec.Options.MinRank = ranking.ResolutionTierPoints + 5000
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
	spec.Options.MinRank = ranking.ResolutionTierPoints + 5000
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
