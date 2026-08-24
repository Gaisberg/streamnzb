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

func rulesProfile(t *testing.T, rules ...config.RuleConfig) *ranking.Profile {
	t.Helper()
	p, err := ranking.Compile(config.FilterProfileConfig{Name: "Rules", Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A rule that rejects removes the release and says which rule did it, in the
// same list every other rejection reason lands in.
func TestRuleRejectionJoinsTheRejectionList(t *testing.T) {
	p := rulesProfile(t, config.RuleConfig{
		Name:   "DV without HDR fallback",
		When:   "dolbyVision and not hdrFallback",
		Action: config.RuleActionReject,
	})

	kept, reasons := applyOne(p, ranking.KindMovie, &release.Release{
		Title: "Movie 2020 2160p BluRay REMUX DV HEVC-GRP",
	})
	if kept {
		t.Fatal("a DV-only release was kept")
	}
	found := false
	for _, r := range reasons {
		if strings.HasPrefix(r, "rule: ") && strings.Contains(r, "DV without HDR fallback") {
			found = true
		}
	}
	if !found {
		t.Errorf("rejection reasons %v do not name the rule", reasons)
	}
}

// Rule points land in the same score everything else uses, so they order
// results and count towards the floor like any other points.
func TestRulePointsReachTheScore(t *testing.T) {
	plain := rulesProfile(t)
	scored := rulesProfile(t, config.RuleConfig{
		Name:   "IMAX",
		When:   `releaseName matches "(?i)\\bIMAX\\b"`,
		Points: 1234,
	})

	rel := &release.Release{Title: "Movie 2020 IMAX 1080p WEB-DL H264-GRP"}
	if got := rankOf(t, scored, ranking.KindMovie, rel) - rankOf(t, plain, ranking.KindMovie, rel); got != 1234 {
		t.Errorf("rule earned %d points, want 1234", got)
	}
}

// A rule reading the probe judges library releases and leaves everything else
// alone, rather than rejecting every release that has never been opened.
func TestProbeRuleOnlyJudgesProbedReleases(t *testing.T) {
	p := rulesProfile(t, config.RuleConfig{
		Name:   "No SD files",
		When:   "probed.height < 1080",
		Action: config.RuleActionReject,
	})

	fresh := triage.Candidate{Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}}
	probedSD := triage.Candidate{
		Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP", IsLibrary: true},
		Verdict: triage.Verdict{Probed: &release.MediaCaps{Height: 480}},
	}
	probedHD := triage.Candidate{
		Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP", IsLibrary: true},
		Verdict: triage.Verdict{Probed: &release.MediaCaps{Height: 1080}},
	}

	kept, rejected := p.ApplyWithRejected(
		ranking.Request{Kind: ranking.KindMovie},
		[]triage.Candidate{fresh, probedSD, probedHD},
		rank.RankOptions{},
	)
	if len(kept) != 2 {
		t.Fatalf("kept %d releases, want 2 (the unprobed one and the probed 1080p one)", len(kept))
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected %d releases, want 1", len(rejected))
	}
	if !rejected[0].Candidate.Release.IsLibraryResult() {
		t.Error("the rejected release was not the probed one")
	}
}

// Compiling a profile is what validates its rules, so a broken condition
// rejects the config save with the rule named.
func TestProfileCompileRejectsBrokenRules(t *testing.T) {
	_, err := ranking.Compile(config.FilterProfileConfig{
		Name:  "Broken",
		Rules: []config.RuleConfig{{Name: "Nonsense", When: "seeders > 10"}},
	})
	if err == nil {
		t.Fatal("Compile succeeded with an invalid rule")
	}
	if !strings.Contains(err.Error(), "Nonsense") {
		t.Errorf("error %q does not name the rule", err)
	}
}

// The scoped half: a rule limited to one kind does not touch the others.
func TestRuleScopeAppliesPerKind(t *testing.T) {
	p := rulesProfile(t, config.RuleConfig{
		Name:   "Anime bonus",
		Scope:  ranking.KindAnimeShow,
		When:   "true",
		Points: 777,
	})
	plain := rulesProfile(t)

	rel := &release.Release{Title: "Show S01E01 1080p WEB-DL H264-GRP"}
	base := rankOf(t, plain, ranking.KindAnimeShow, rel)
	if got := rankOf(t, p, ranking.KindAnimeShow, rel) - base; got != 777 {
		t.Errorf("scoped kind earned %d, want 777", got)
	}
	if got := rankOf(t, p, ranking.KindSeries, rel) - rankOf(t, plain, ranking.KindSeries, rel); got != 0 {
		t.Errorf("another kind earned %d, want 0", got)
	}
}

// The bench evaluates rules too, and reports the ones a bare title cannot
// answer instead of letting them look broken.
func TestExplainReportsRulesAndSkips(t *testing.T) {
	p := rulesProfile(t,
		config.RuleConfig{Name: "IMAX", When: `releaseName matches "(?i)\\bIMAX\\b"`, Points: 1000},
		config.RuleConfig{Name: "Measured 10-bit", When: "probed.bitDepth >= 10", Points: 400},
	)

	out := p.Explain([]string{"Movie 2020 IMAX 1080p WEB-DL H264-GRP"}, ranking.Request{Kind: ranking.KindMovie}, rank.RankOptions{})
	if len(out) != 1 {
		t.Fatalf("Explain returned %d results, want 1", len(out))
	}
	ex := out[0]
	if len(ex.Matched) != 1 || ex.Matched[0].Name != "IMAX" {
		t.Errorf("Matched = %+v, want one IMAX entry", ex.Matched)
	}
	if len(ex.SkippedRules) != 1 || !strings.Contains(ex.SkippedRules[0], "Measured 10-bit") {
		t.Errorf("SkippedRules = %v, want the probe rule reported", ex.SkippedRules)
	}
	found := false
	for _, c := range ex.Contributions {
		if c.Source == "rule:IMAX" && c.Rank == 1000 {
			found = true
		}
	}
	if !found {
		t.Errorf("contributions %+v do not include the rule", ex.Contributions)
	}
}

// "At most three 4K releases" is the one thing a per-release condition cannot
// say on its own, so it is an action: the best N matching survive and the tail
// is dropped.
func TestLimitRuleCapsMatchingReleases(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Capped",
		Preset: config.PresetUHD,
		Rules: []config.RuleConfig{
			{Name: "Max two 4K", When: `resolution == "2160p"`, Action: config.RuleActionLimit, Count: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cands := []triage.Candidate{
		{Release: &release.Release{Title: "Movie 2020 2160p BluRay REMUX HEVC-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 2160p WEB-DL H265-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 2160p WEBRip x265-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 1080p BluRay REMUX-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 1080p WEB-DL H264-GRP"}},
	}
	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, cands, rank.RankOptions{})

	fourK := 0
	for _, r := range kept {
		if strings.Contains(r.Candidate.Release.Title, "2160p") {
			fourK++
		}
	}
	if fourK != 2 {
		t.Errorf("kept %d 4K releases, want 2", fourK)
	}
	// Nothing below the cap is touched: the 1080p releases all survive.
	if len(kept) != 4 {
		t.Errorf("kept %d releases in total, want 4", len(kept))
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected %d, want 1", len(rejected))
	}
	if !strings.Contains(strings.Join(rejected[0].Torrent.Rejections, " "), "over the limit of 2") {
		t.Errorf("rejection %v does not say it was over the limit", rejected[0].Torrent.Rejections)
	}
}

// The survivors are the best ones, not the first ones the indexer happened to
// return, so a cap never costs you the release you wanted.
func TestLimitRuleKeepsTheBest(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Best one",
		Preset: config.PresetUHD,
		Rules: []config.RuleConfig{
			{Name: "One 4K only", When: `resolution == "2160p"`, Action: config.RuleActionLimit, Count: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The remux is last in the input and scores highest.
	cands := []triage.Candidate{
		{Release: &release.Release{Title: "Movie 2020 2160p WEBRip x265-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 2160p WEB-DL H265-GRP"}},
		{Release: &release.Release{Title: "Movie 2020 2160p BluRay REMUX HEVC-GRP"}},
	}
	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, cands, rank.RankOptions{})
	if len(kept) != 1 {
		t.Fatalf("kept %d, want 1", len(kept))
	}
	if !strings.Contains(kept[0].Candidate.Release.Title, "REMUX") {
		t.Errorf("kept %q, want the remux — a cap should keep the best, not the first", kept[0].Candidate.Release.Title)
	}
}

// A limit rule with no count is a rule that would silently drop everything, so
// it fails the profile instead.
func TestLimitRuleNeedsACount(t *testing.T) {
	_, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Bad cap",
		Preset: config.PresetUHD,
		Rules:  []config.RuleConfig{{Name: "Nothing", When: "true", Action: config.RuleActionLimit}},
	})
	if err == nil {
		t.Fatal("Compile accepted a limit rule with no count")
	}
	if !strings.Contains(err.Error(), "Nothing") {
		t.Errorf("error %q does not name the rule", err)
	}
}

// The preview runs the same set pipeline a real search does, so a cap shows up
// there rather than looking like a rule that never fires.
func TestExplainReflectsLimitRules(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Preview cap",
		Preset: config.PresetUHD,
		Rules: []config.RuleConfig{
			{Name: "One 4K", When: `resolution == "2160p"`, Action: config.RuleActionLimit, Count: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := p.Explain([]string{
		"Movie 2020 2160p BluRay REMUX HEVC-GRP",
		"Movie 2020 2160p WEB-DL H265-GRP",
		"Movie 2020 1080p WEB-DL H264-GRP",
	}, ranking.Request{Kind: ranking.KindMovie}, rank.RankOptions{})

	if len(out) != 3 {
		t.Fatalf("Explain returned %d results, want 3", len(out))
	}
	offered, capped := 0, 0
	for _, ex := range out {
		if ex.Fetch {
			offered++
			continue
		}
		if strings.Contains(strings.Join(ex.Rejections, " "), "over the limit of 1") {
			capped++
		}
	}
	if offered != 2 {
		t.Errorf("%d offered, want 2 (one 4K plus the 1080p)", offered)
	}
	if capped != 1 {
		t.Errorf("%d capped, want 1", capped)
	}
}

// The preview can be told what to pretend about a release, so a rule about
// size, grabs or a probed file is testable instead of merely reported as
// unjudgeable.
func TestExplainJudgesAgainstASample(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Sampled",
		Preset: config.PresetUHD,
		Rules: []config.RuleConfig{
			{Name: "Unpopular", When: "grabs < 5", Action: config.RuleActionReject},
			{Name: "Measured 10-bit", When: "probed.bitDepth >= 10", Points: 400},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	title := "Movie 2020 2160p WEB-DL HEVC-GRP"

	// With nothing supplied both rules are unjudgeable and neither fires.
	bare := p.Explain([]string{title}, ranking.Request{Kind: ranking.KindMovie}, rank.RankOptions{})
	if len(bare) != 1 {
		t.Fatalf("got %d results, want 1", len(bare))
	}
	if !bare[0].Fetch {
		t.Errorf("a bare title was rejected: %v", bare[0].Rejections)
	}
	if len(bare[0].SkippedRules) != 2 {
		t.Errorf("SkippedRules = %v, want both reported", bare[0].SkippedRules)
	}

	// Supplying a grab count makes the grabs rule answerable, and it rejects.
	sampled := p.Explain([]string{title}, ranking.Request{
		Kind:   ranking.KindMovie,
		Sample: &ranking.Sample{IndexerData: true, SizeBytes: 20e9, Grabs: 2},
	}, rank.RankOptions{})
	if sampled[0].Fetch {
		t.Error("a release with 2 grabs was not rejected once grabs were supplied")
	}

	// A grab count above the bar leaves it alone, and a probed file answers
	// the other rule.
	good := p.Explain([]string{title}, ranking.Request{
		Kind: ranking.KindMovie,
		Sample: &ranking.Sample{
			IndexerData: true, SizeBytes: 20e9, Grabs: 500,
			Probed: &release.MediaCaps{Height: 2160, BitDepth: 10},
		},
	}, rank.RankOptions{})
	if !good[0].Fetch {
		t.Errorf("a healthy sampled release was rejected: %v", good[0].Rejections)
	}
	if len(good[0].SkippedRules) != 0 {
		t.Errorf("SkippedRules = %v, want none once everything is supplied", good[0].SkippedRules)
	}
	var paid bool
	for _, m := range good[0].Matched {
		if m.Name == "Measured 10-bit" {
			paid = true
		}
	}
	if !paid {
		t.Errorf("the probe rule did not pay out: %+v", good[0].Matched)
	}
}

// A simulated release with nothing grabbed yet still has a known grab count of
// zero — the flag says so, rather than the zero being read as "unknown".
func TestSampleVouchesForZeroes(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Zeroes",
		Preset: config.PresetUHD,
		Rules:  []config.RuleConfig{{Name: "Unpopular", When: "grabs < 5", Action: config.RuleActionReject}},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := p.Explain([]string{"Movie 2020 2160p WEB-DL HEVC-GRP"}, ranking.Request{
		Kind:   ranking.KindMovie,
		Sample: &ranking.Sample{IndexerData: true},
	}, rank.RankOptions{})
	if out[0].Fetch {
		t.Error("a release vouched for with no grabs was not rejected")
	}
}

// A result-set condition lets a rule reject conditionally on what else is in
// the set: the WEB-DL goes only when a remux stands ready to replace it.
func TestAggregateRejectsOnlyWithFallback(t *testing.T) {
	p := rulesProfile(t, config.RuleConfig{
		Name:   "WEB-DL when a remux exists",
		When:   `quality == "WEB-DL" and exists("remux" in traits)`,
		Action: config.RuleActionReject,
	})

	webdl := func() []triage.Candidate {
		return candidateWith(&release.Release{Title: "Movie 2020 2160p WEB-DL HEVC-GRP", Size: 8e9})
	}
	both := append(webdl(), candidateWith(&release.Release{Title: "Movie 2020 2160p BluRay REMUX HEVC-GRP", Size: 40e9})...)

	kept, rejected := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, both, rank.RankOptions{})
	if len(kept) != 1 || !strings.Contains(kept[0].Candidate.Release.Title, "REMUX") {
		t.Fatalf("kept %d, want just the remux", len(kept))
	}
	if len(rejected) != 1 || len(rejected[0].Torrent.Rejections) == 0 {
		t.Fatalf("rejected = %+v, want the WEB-DL with a reason", rejected)
	}

	kept, _ = p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, webdl(), rank.RankOptions{})
	if len(kept) != 1 {
		t.Fatal("the only release was rejected although nothing better exists")
	}
}

// Releases something earlier already rejected are not in the set the
// aggregates count: a remux the size limits threw out is no fallback.
func TestAggregatesIgnoreAlreadyRejectedReleases(t *testing.T) {
	p, err := ranking.Compile(config.FilterProfileConfig{
		Name:   "Sized",
		Limits: map[string]*config.LimitsConfig{config.LimitKindDefault: {MaxSizeGB: 30}},
		Rules: []config.RuleConfig{{
			Name:   "WEB-DL when a remux exists",
			When:   `quality == "WEB-DL" and exists("remux" in traits)`,
			Action: config.RuleActionReject,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	candidates := candidateWith(&release.Release{Title: "Movie 2020 2160p WEB-DL HEVC-GRP", Size: 8e9})
	candidates = append(candidates, candidateWith(&release.Release{Title: "Movie 2020 2160p BluRay REMUX HEVC-GRP", Size: 60e9})...)

	kept, _ := p.ApplyWithRejected(ranking.Request{Kind: ranking.KindMovie}, candidates, rank.RankOptions{})
	if len(kept) != 1 || !strings.Contains(kept[0].Candidate.Release.Title, "WEB-DL") {
		t.Fatalf("kept %v, want the WEB-DL: its only fallback fell to the size limit", kept)
	}
}

// The preview runs the same aggregate pass as a live search, so a result-set
// condition fires there too, and one that cannot be judged reports why.
func TestExplainReportsAggregates(t *testing.T) {
	p := rulesProfile(t,
		config.RuleConfig{Name: "Remux available", When: `exists("remux" in traits)`, Points: 77},
		config.RuleConfig{Name: "Probed 4K nearby", When: `exists(probed.height >= 2000)`, Points: 5},
	)

	out := p.Explain(
		[]string{"Movie 2020 1080p WEB-DL H264-GRP", "Movie 2020 2160p BluRay REMUX HEVC-GRP"},
		ranking.Request{Kind: ranking.KindMovie},
		rank.RankOptions{},
	)
	paid := false
	for _, m := range out[0].Matched {
		if m.Name == "Remux available" {
			paid = true
		}
	}
	if !paid {
		t.Errorf("the aggregate rule did not pay out in the preview: %+v", out[0].Matched)
	}
	skipped := strings.Join(out[0].SkippedRules, "; ")
	if !strings.Contains(skipped, "Probed 4K nearby") || !strings.Contains(skipped, "probed") {
		t.Errorf("SkippedRules = %v, want the probe-tier aggregate reported", out[0].SkippedRules)
	}
}
