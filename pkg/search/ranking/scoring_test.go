package ranking_test

import (
	"testing"
	"time"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
)

// rankOf runs one release through the profile and returns the score it ended
// with, kept or not.
func rankOf(t *testing.T, p *ranking.Profile, kind string, rel *release.Release) int {
	t.Helper()
	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: kind}, candidateWith(rel), rank.RankOptions{})
	if len(kept) == 1 {
		return kept[0].Torrent.Rank
	}
	if len(rejected) == 1 {
		return rejected[0].Torrent.Rank
	}
	t.Fatalf("release produced neither a kept nor a rejected result")
	return 0
}

const scoringTitle = "Movie 2020 1080p BluRay-GRP"

func TestScoringSizeTargetPeaksAtTarget(t *testing.T) {
	gb := int64(1e9)
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	scored := limitsProfile(t, config.FilterProfileConfig{
		Name:    "scored",
		Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: {SizeTargetGB: 8, SizeWeight: 300}},
	})

	base := rankOf(t, plain, ranking.KindMovie, &release.Release{Title: scoringTitle, Size: 8 * gb})
	atTarget := rankOf(t, scored, ranking.KindMovie, &release.Release{Title: scoringTitle, Size: 8 * gb})
	if got := atTarget - base; got != 300 {
		t.Fatalf("release at the target earned %d, want the full weight 300", got)
	}

	half := rankOf(t, scored, ranking.KindMovie, &release.Release{Title: scoringTitle, Size: 4 * gb})
	if got := half - base; got != 150 {
		t.Fatalf("release at half the target earned %d, want 150", got)
	}

	// Zero at twice the target and beyond, never negative.
	for _, size := range []int64{16 * gb, 40 * gb} {
		if got := rankOf(t, scored, ranking.KindMovie, &release.Release{Title: scoringTitle, Size: size}) - base; got != 0 {
			t.Fatalf("release of %d bytes earned %d, want nothing", size, got)
		}
	}

	// A release that reports no size is not judged on size at all.
	if got := rankOf(t, scored, ranking.KindMovie, &release.Release{Title: scoringTitle}) - base; got != 0 {
		t.Fatalf("sizeless release earned %d, want nothing", got)
	}
}

func TestScoringAgeFavoursFreshReleases(t *testing.T) {
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	scored := limitsProfile(t, config.FilterProfileConfig{
		Name:    "scored",
		Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: {AgeFreshDays: 30, AgeWeight: 200}},
	})
	base := rankOf(t, plain, ranking.KindMovie, &release.Release{Title: scoringTitle})

	at := func(age time.Duration) int {
		rel := &release.Release{Title: scoringTitle, PubDate: time.Now().Add(-age).Format(time.RFC1123Z)}
		return rankOf(t, scored, ranking.KindMovie, rel) - base
	}

	if got := at(0); got < 195 || got > 200 {
		t.Fatalf("a release posted now earned %d, want about the full weight 200", got)
	}
	if got := at(15 * 24 * time.Hour); got < 95 || got > 105 {
		t.Fatalf("a release halfway to the horizon earned %d, want about 100", got)
	}
	if got := at(60 * 24 * time.Hour); got != 0 {
		t.Fatalf("a release past the horizon earned %d, want nothing", got)
	}
	// No date means no judgement, not a penalty.
	if got := rankOf(t, scored, ranking.KindMovie, &release.Release{Title: scoringTitle}) - base; got != 0 {
		t.Fatalf("undated release earned %d, want nothing", got)
	}
}

func TestScoringGrabsAreLogarithmic(t *testing.T) {
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	scored := limitsProfile(t, config.FilterProfileConfig{
		Name:    "scored",
		Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: {GrabsTarget: 100, GrabsWeight: 150}},
	})
	base := rankOf(t, plain, ranking.KindMovie, &release.Release{Title: scoringTitle})
	at := func(grabs int) int {
		return rankOf(t, scored, ranking.KindMovie, &release.Release{Title: scoringTitle, Grabs: grabs}) - base
	}

	if got := at(100); got != 150 {
		t.Fatalf("a release at the grab target earned %d, want the full weight 150", got)
	}
	if got := at(500); got != 150 {
		t.Fatalf("a release above the grab target earned %d, want the weight capped at 150", got)
	}
	if got := at(0); got != 0 {
		t.Fatalf("a release reporting no grabs earned %d, want nothing", got)
	}
	// Logarithmic, so ten grabs is well past a tenth of the target's points.
	ten := at(10)
	if ten <= 15 || ten >= 150 {
		t.Fatalf("ten grabs earned %d, want a logarithmic share of 150", ten)
	}
	if at(1) >= ten {
		t.Fatalf("grab scoring must increase with grabs")
	}
}

func TestScoringPerKindOverridesDefault(t *testing.T) {
	gb := int64(1e9)
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	scored := limitsProfile(t, config.FilterProfileConfig{
		Name: "scored",
		Scoring: map[string]*config.ScoringConfig{
			config.LimitKindDefault: {SizeTargetGB: 20, SizeWeight: 300},
			ranking.KindSeries:      {SizeTargetGB: 2},
		},
	})

	movie := &release.Release{Title: scoringTitle, Size: 20 * gb}
	movieBase := rankOf(t, plain, ranking.KindMovie, movie)
	if got := rankOf(t, scored, ranking.KindMovie, movie) - movieBase; got != 300 {
		t.Fatalf("movie at the default target earned %d, want 300", got)
	}

	// The series entry overrides only the target; the weight is inherited.
	episode := &release.Release{Title: "Show S01E01 1080p WEB-GRP", Size: 2 * gb}
	episodeBase := rankOf(t, plain, ranking.KindSeries, episode)
	if got := rankOf(t, scored, ranking.KindSeries, episode) - episodeBase; got != 300 {
		t.Fatalf("episode at the series target earned %d, want the inherited weight 300", got)
	}
}

func TestScoringJudgesPacksPerEpisode(t *testing.T) {
	gb := int64(1e9)
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	scored := limitsProfile(t, config.FilterProfileConfig{
		Name:    "scored",
		Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: {SizeTargetGB: 2, SizeWeight: 300}},
	})

	// Four episodes at 2 GB each hits the per-episode target.
	pack := &release.Release{Title: "Show S01E01-E04 1080p WEB-GRP", Size: 8 * gb}
	packBase := rankOf(t, plain, ranking.KindSeries, pack)
	if got := rankOf(t, scored, ranking.KindSeries, pack) - packBase; got != 300 {
		t.Fatalf("multi-episode release earned %d, want the per-episode target's 300", got)
	}

	// A season pack whose episode count the title does not reveal is not judged.
	season := &release.Release{Title: "Show S01 1080p WEB-GRP", Size: 40 * gb}
	seasonBase := rankOf(t, plain, ranking.KindSeries, season)
	if got := rankOf(t, scored, ranking.KindSeries, season) - seasonBase; got != 0 {
		t.Fatalf("unparseable season pack earned %d, want nothing", got)
	}
}

func TestScoringReordersEligibleReleases(t *testing.T) {
	gb := int64(1e9)
	p := limitsProfile(t, config.FilterProfileConfig{
		Name:    "scored",
		Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: {GrabsTarget: 100, GrabsWeight: 5000}},
	})
	candidates := []triage.Candidate{
		{Release: &release.Release{Title: scoringTitle, Size: 8 * gb, Grabs: 1}},
		{Release: &release.Release{Title: scoringTitle, Size: 8 * gb, Grabs: 400}},
	}
	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, candidates, rank.RankOptions{})
	if len(kept) != 2 {
		t.Fatalf("kept %d releases, want 2", len(kept))
	}
	if kept[0].Candidate.Release.Grabs != 400 {
		t.Fatalf("well-grabbed release sorted second; scoring did not reorder")
	}
}

func TestScoringInertWithoutBothTargetAndWeight(t *testing.T) {
	gb := int64(1e9)
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	rel := &release.Release{Title: scoringTitle, Size: 8 * gb, Grabs: 50, PubDate: time.Now().Format(time.RFC1123Z)}
	base := rankOf(t, plain, ranking.KindMovie, rel)

	for name, scoring := range map[string]*config.ScoringConfig{
		"target without weight": {SizeTargetGB: 8, AgeFreshDays: 30, GrabsTarget: 100},
		"weight without target": {SizeWeight: 300, AgeWeight: 200, GrabsWeight: 150},
	} {
		p := limitsProfile(t, config.FilterProfileConfig{
			Name:    name,
			Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: scoring},
		})
		if got := rankOf(t, p, ranking.KindMovie, rel) - base; got != 0 {
			t.Fatalf("%s changed the score by %d, want nothing", name, got)
		}
	}
}

func TestScoringNegativeWeightInvertsPreference(t *testing.T) {
	plain := limitsProfile(t, config.FilterProfileConfig{Name: "plain"})
	scored := limitsProfile(t, config.FilterProfileConfig{
		Name:    "scored",
		Scoring: map[string]*config.ScoringConfig{config.LimitKindDefault: {GrabsTarget: 100, GrabsWeight: -150}},
	})
	rel := &release.Release{Title: scoringTitle, Grabs: 100}
	base := rankOf(t, plain, ranking.KindMovie, rel)
	if got := rankOf(t, scored, ranking.KindMovie, rel) - base; got != -150 {
		t.Fatalf("negative weight scored %d, want -150", got)
	}
}
