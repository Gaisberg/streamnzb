package ranking_test

import (
	"strings"
	"testing"

	"github.com/dreulavelle/jhin/rank"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
)

// pruneCandidates builds one candidate per title, in the order given.
func pruneCandidates(titles ...string) []triage.Candidate {
	cands := make([]triage.Candidate, len(titles))
	for i, title := range titles {
		cands[i] = triage.Candidate{Release: &release.Release{Title: title}}
	}
	return cands
}

// weakRule pushes anything named BADGRP far below the prune threshold the
// tests use, while staying well clear of the default score floor — a release
// the floor rejects never reaches the prune pass.
var weakRule = config.RuleConfig{
	Name: "Weak", When: `releaseName matches "(?i)BADGRP"`, Points: -100000,
}

// The feature request's use case end to end: very weak results are removed
// only when enough materially stronger alternatives remain.
func TestPruneDropsWeakOnlyWithEnoughSurvivors(t *testing.T) {
	rules := []config.RuleConfig{
		weakRule,
		{
			Name:   "Weak with fallbacks",
			When:   "finalScore < -10000 and count(finalScore >= -10000) >= 3",
			Action: config.RuleActionPrune,
		},
	}
	p := rulesProfile(t, rules...)

	good := []string{
		"Movie 2020 1080p BluRay REMUX-AAA",
		"Movie 2020 1080p WEB-DL H264-BBB",
		"Movie 2020 1080p WEBRip x264-CCC",
	}
	bad := []string{
		"Movie 2020 1080p WEB-DL H264-BADGRP1",
		"Movie 2020 1080p WEB-DL H264-BADGRP2",
	}

	// Three strong survivors: the weak pair is pruned, and says so in the
	// same rejection list every other stage writes into.
	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
		pruneCandidates(append(good, bad...)...), rank.RankOptions{})
	if len(kept) != 3 || len(rejected) != 2 {
		t.Fatalf("kept %d and rejected %d, want 3 and 2", len(kept), len(rejected))
	}
	for _, r := range rejected {
		joined := strings.Join(r.Torrent.Rejections, " ")
		if !strings.Contains(joined, "rule: Weak with fallbacks") {
			t.Errorf("rejection %v does not name the prune rule", r.Torrent.Rejections)
		}
		if verdict := strings.Join(r.Candidate.Verdict.Rejections, " "); !strings.Contains(verdict, "Weak with fallbacks") {
			t.Errorf("verdict rejections %v were not refreshed", r.Candidate.Verdict.Rejections)
		}
	}

	// Too few strong survivors: the weak releases stay as fallbacks.
	kept, rejected = p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
		pruneCandidates(good[0], bad[0], bad[1]), rank.RankOptions{})
	if len(kept) != 3 || len(rejected) != 0 {
		t.Errorf("with one strong survivor, kept %d and rejected %d, want 3 and 0",
			len(kept), len(rejected))
	}
}

// finalRank is the 1-based position in the sorted survivors, so a rank
// condition cuts the tail wherever the profile draws the line.
func TestPruneByFinalRankKeepsTheTop(t *testing.T) {
	p := rulesProfile(t,
		config.RuleConfig{Name: "A", When: `group == "AAA"`, Points: 3000},
		config.RuleConfig{Name: "B", When: `group == "BBB"`, Points: 2000},
		config.RuleConfig{Name: "Tail", When: "finalRank > 2", Action: config.RuleActionPrune},
	)

	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
		pruneCandidates(
			"Movie 2020 1080p WEB-DL H264-CCC",
			"Movie 2020 1080p WEB-DL H264-BBB",
			"Movie 2020 1080p WEB-DL H264-AAA",
		), rank.RankOptions{})
	if len(kept) != 2 || len(rejected) != 1 {
		t.Fatalf("kept %d and rejected %d, want 2 and 1", len(kept), len(rejected))
	}
	if !strings.HasSuffix(kept[0].Candidate.Release.Title, "AAA") ||
		!strings.HasSuffix(kept[1].Candidate.Release.Title, "BBB") {
		t.Errorf("kept %q and %q, want the two boosted releases in score order",
			kept[0].Candidate.Release.Title, kept[1].Candidate.Release.Title)
	}
	if !strings.HasSuffix(rejected[0].Candidate.Release.Title, "CCC") {
		t.Errorf("pruned %q, want the unboosted release", rejected[0].Candidate.Release.Title)
	}
}

// Pruning runs before the caps, so a pruned release does not consume a limit
// slot: the cap's survivors are drawn from what pruning left.
func TestPruneFreesLimitSlots(t *testing.T) {
	p := rulesProfile(t,
		config.RuleConfig{Name: "A", When: `group == "AAA"`, Points: 3000},
		config.RuleConfig{Name: "B", When: `group == "BBB"`, Points: 2000},
		config.RuleConfig{Name: "Top two", When: "true", Action: config.RuleActionLimit, Count: 2},
		config.RuleConfig{Name: "Second out", When: "finalRank == 2", Action: config.RuleActionPrune},
	)

	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
		pruneCandidates(
			"Movie 2020 1080p WEB-DL H264-AAA",
			"Movie 2020 1080p WEB-DL H264-BBB",
			"Movie 2020 1080p WEB-DL H264-CCC",
		), rank.RankOptions{})
	if len(kept) != 2 {
		t.Fatalf("kept %d, want 2 — the pruned release should not use a cap slot", len(kept))
	}
	if !strings.HasSuffix(kept[0].Candidate.Release.Title, "AAA") ||
		!strings.HasSuffix(kept[1].Candidate.Release.Title, "CCC") {
		t.Errorf("kept %q and %q, want AAA and CCC — BBB was pruned, its slot passes on",
			kept[0].Candidate.Release.Title, kept[1].Candidate.Release.Title)
	}
}

// The preview reports the prune pass like any other: its result-set
// conditions join the aggregate trace, and a pruned release's explanation
// carries the rejection.
func TestExplainCoversThePrunePass(t *testing.T) {
	p := rulesProfile(t,
		weakRule,
		config.RuleConfig{
			Name:   "Weak with fallbacks",
			When:   "finalScore < -10000 and count(finalScore >= -10000) >= 1",
			Action: config.RuleActionPrune,
		},
	)

	explanations, aggregates := p.Explain([]string{
		"Movie 2020 1080p WEB-DL H264-AAA",
		"Movie 2020 1080p WEB-DL H264-BADGRP1",
	}, ranking.Request{Kind: ranking.KindMovie}, rank.RankOptions{})

	foundAgg := false
	for _, agg := range aggregates {
		if strings.Contains(agg.Source, "finalScore") {
			foundAgg = true
		}
	}
	if !foundAgg {
		t.Errorf("aggregate trace %+v does not cover the prune pass", aggregates)
	}

	foundRejection := false
	for _, ex := range explanations {
		if strings.Contains(strings.Join(ex.Rejections, " "), "Weak with fallbacks") {
			foundRejection = true
		}
	}
	if !foundRejection {
		t.Error("no explanation carries the prune rule's rejection")
	}
}

// The candidate-relative form end to end: a release is pruned only when
// enough alternatives beat it by a margin, with no absolute threshold in the
// rule. The groups are scored 5000 apart so the gap, not the band, decides.
func TestPruneDropsWhatEnoughAlternativesBeatByAMargin(t *testing.T) {
	p := rulesProfile(t,
		config.RuleConfig{Name: "A", When: `group == "AAA"`, Points: 20000},
		config.RuleConfig{Name: "B", When: `group == "BBB"`, Points: 15000},
		config.RuleConfig{Name: "C", When: `group == "CCC"`, Points: 10000},
		config.RuleConfig{Name: "D", When: `group == "DDD"`, Points: 5000},
		config.RuleConfig{
			Name:   "Weak tail",
			When:   "count(finalScore >= current.finalScore + 5000) >= 3",
			Action: config.RuleActionPrune,
		},
	)
	titles := []string{
		"Movie 2020 1080p WEB-DL H264-AAA",
		"Movie 2020 1080p WEB-DL H264-BBB",
		"Movie 2020 1080p WEB-DL H264-CCC",
		"Movie 2020 1080p WEB-DL H264-DDD",
	}

	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
		pruneCandidates(titles...), rank.RankOptions{})
	if len(kept) != 3 || len(rejected) != 1 {
		t.Fatalf("kept %d and rejected %d, want 3 and 1", len(kept), len(rejected))
	}
	if !strings.HasSuffix(rejected[0].Candidate.Release.Title, "DDD") {
		t.Errorf("pruned %q, want the release three alternatives beat by 5000",
			rejected[0].Candidate.Release.Title)
	}

	// The same rule over a sparser set keeps the weak release: the margin is
	// there, the alternatives are not.
	kept, rejected = p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie},
		pruneCandidates(titles[0], titles[3]), rank.RankOptions{})
	if len(kept) != 2 || len(rejected) != 0 {
		t.Errorf("with one better alternative, kept %d and rejected %d, want 2 and 0",
			len(kept), len(rejected))
	}
}

// The preview shows its work for a relative question too: one report per
// release, naming whose perspective produced the count, alongside the single
// shared report an absolute question still gets.
func TestExplainReportsRelativeAggregatesPerRelease(t *testing.T) {
	p := rulesProfile(t,
		config.RuleConfig{Name: "A", When: `group == "AAA"`, Points: 20000},
		config.RuleConfig{Name: "B", When: `group == "BBB"`, Points: 10000},
		config.RuleConfig{
			Name:   "Weak tail",
			When:   "count(finalScore >= current.finalScore + 5000) >= 1 and count(finalScore >= 20000) >= 1",
			Action: config.RuleActionPrune,
		},
	)

	_, aggregates := p.Explain([]string{
		"Movie 2020 1080p WEB-DL H264-AAA",
		"Movie 2020 1080p WEB-DL H264-BBB",
	}, ranking.Request{Kind: ranking.KindMovie}, rank.RankOptions{})

	perRelease := map[string]int{}
	shared := 0
	for _, agg := range aggregates {
		if agg.Release == "" {
			shared++
			continue
		}
		perRelease[agg.Release] = agg.Count
	}
	if shared != 1 {
		t.Errorf("got %d shared reports, want 1 — the absolute question is counted once", shared)
	}
	if len(perRelease) != 2 {
		t.Fatalf("got %d per-release reports, want one for each release: %+v", len(perRelease), aggregates)
	}
	if got := perRelease["Movie 2020 1080p WEB-DL H264-AAA"]; got != 0 {
		t.Errorf("the best release counts %d alternatives 5000 above it, want 0", got)
	}
	if got := perRelease["Movie 2020 1080p WEB-DL H264-BBB"]; got != 1 {
		t.Errorf("the weaker release counts %d alternatives 5000 above it, want 1", got)
	}
}
