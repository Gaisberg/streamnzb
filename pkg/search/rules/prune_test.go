package rules_test

import (
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/rules"
)

// compileStaged compiles a profile's rules into its two passes, failing the
// test on any error.
func compileStaged(t *testing.T, cfgs ...config.RuleConfig) (pre, post *rules.Set) {
	t.Helper()
	pre, post, err := rules.CompileStaged(cfgs)
	if err != nil {
		t.Fatalf("CompileStaged: %v", err)
	}
	return pre, post
}

// compileSet compiles one prune rule, for a benchmark that cannot fail a
// testing.T. The scoring pass comes back empty; the prune pass is the subject.
func compileSet(when string) (pre, post *rules.Set, err error) {
	return rules.CompileStaged([]config.RuleConfig{{
		Name: "Bench", When: when, Action: config.RuleActionPrune,
	}})
}

// pruneEnvs builds environments the way the prune pass does: parsed titles
// with the finished score and 1-based rank filled in, in the order given —
// which is the sorted order, so ranks count up from the first title.
func pruneEnvs(scores ...int) []rules.Env {
	envs := make([]rules.Env, len(scores))
	for i, score := range scores {
		envs[i] = envFor("Movie 2020 1080p WEB-DL H264-GRP", nil)
		envs[i].FinalScore = score
		envs[i].FinalRank = i + 1
	}
	return envs
}

// The scoring pass must not read the score it is still building, so the
// checker rejects finalScore there — the exact condition the feature request
// reported as rejected, now rejected on purpose and pointed at the action
// that can answer it.
func TestScoringRulesCannotReadFinalScore(t *testing.T) {
	_, _, err := rules.CompileStaged([]config.RuleConfig{{
		Name:   "Too eager",
		When:   "finalScore < 0",
		Action: config.RuleActionReject,
	}})
	if err == nil {
		t.Fatal("a scoring-pass rule reading finalScore compiled")
	}
	if !strings.Contains(err.Error(), "finalScore") {
		t.Errorf("error %q does not name the attribute", err)
	}
	if !strings.Contains(err.Error(), "prune") {
		t.Errorf("error %q does not point at the prune action", err)
	}
}

// CompileStaged routes each rule to the pass its action belongs to.
func TestCompileStagedSplitsByAction(t *testing.T) {
	pre, post := compileStaged(t,
		config.RuleConfig{Name: "IMAX", When: `releaseName matches "(?i)IMAX"`, Points: 100},
		config.RuleConfig{Name: "Weak tail", When: "finalScore < 0", Action: config.RuleActionPrune},
	)
	if pre.Len() != 1 {
		t.Errorf("scoring pass holds %d rules, want 1", pre.Len())
	}
	if post.Len() != 1 {
		t.Errorf("prune pass holds %d rules, want 1", post.Len())
	}
}

// A prune rule judges the finished verdict: score and rank, both read-only.
func TestPruneRuleReadsFinalScoreAndRank(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Deep weak tail",
		When:   "finalRank > 2 and finalScore < 0",
		Action: config.RuleActionPrune,
	})

	envs := pruneEnvs(900, 500, -1000)
	for i, want := range []bool{false, false, true} {
		out := post.Evaluate(envs[i], "movie")
		if got := len(out.Rejections) > 0; got != want {
			t.Errorf("rank %d (score %d): pruned = %v, want %v",
				i+1, envs[i].FinalScore, got, want)
		}
	}
}

// The adaptive filter from the feature request: drop the very weak only while
// enough materially stronger alternatives remain. count() runs over the
// surviving set's final scores, once, before any prune rule fires.
func TestPruneAggregatesCountFinalScores(t *testing.T) {
	rule := config.RuleConfig{
		Name:   "Weak with fallbacks",
		When:   "finalScore < -500 and count(finalScore >= -500) >= 3",
		Action: config.RuleActionPrune,
	}

	// Three strong survivors: the weak pair goes.
	_, post := compileStaged(t, rule)
	outs := evalSet(post, pruneEnvs(900, 700, 50, -1000, -1000), "movie")
	for i, want := range []bool{false, false, false, true, true} {
		if got := len(outs[i].Rejections) > 0; got != want {
			t.Errorf("with fallbacks, rank %d: pruned = %v, want %v", i+1, got, want)
		}
	}

	// Too few strong survivors: the weak ones stay as fallbacks.
	_, post = compileStaged(t, rule)
	for i, out := range evalSet(post, pruneEnvs(900, -1000, -1000), "movie") {
		if len(out.Rejections) > 0 {
			t.Errorf("without fallbacks, rank %d was pruned", i+1)
		}
	}
}

// matched() reaches across the split: a prune rule can reference a scoring
// rule or a define by name, same as any scoring rule can.
func TestPruneRuleReferencesScoringPass(t *testing.T) {
	_, post := compileStaged(t,
		config.RuleConfig{Name: "Upscaled", When: "upscaled", Action: config.RuleActionDefine},
		config.RuleConfig{
			Name:   "Upscaled tail",
			When:   `matched("Upscaled") and finalRank > 1`,
			Action: config.RuleActionPrune,
		},
	)

	env := envFor("Movie 2020 2160p UPSCALED WEB-DL-GRP", nil)
	env.FinalScore, env.FinalRank = 100, 2
	if out := post.Evaluate(env, "movie"); len(out.Rejections) == 0 {
		t.Error("an upscaled release at rank 2 was not pruned")
	}
}

// The two passes report under one namespace, so a name has to be unique
// across both — exactly as it had to be when one engine held every rule.
func TestCompileStagedRejectsDuplicateNamesAcrossPasses(t *testing.T) {
	_, _, err := rules.CompileStaged([]config.RuleConfig{
		{Name: "Tail", When: "upscaled", Points: -100},
		{Name: "Tail", When: "finalRank > 20", Action: config.RuleActionPrune},
	})
	if err == nil {
		t.Fatal("two rules sharing a name across passes compiled")
	}
	if !strings.Contains(err.Error(), "Tail") {
		t.Errorf("error %q does not name the rule", err)
	}
}

// evalRelativeSet runs a prune set whose result-set questions are counted per
// release, the way the prune pass does it.
func evalRelativeSet(set *rules.Set, envs []rules.Env, kind string) []rules.Outcome {
	states := set.ComputeRelativeAggregates(envs, kind)
	outs := make([]rules.Outcome, len(envs))
	for i := range envs {
		states[i].Inject(&envs[i])
		outs[i] = set.Evaluate(envs[i], kind)
	}
	return outs
}

// The candidate-relative filter from issue #248: drop a release only when
// enough alternatives are materially better than it, with no absolute
// threshold to encode which score band "materially better" lives in. Inside
// count(), a bare finalScore is the release being counted and
// current.finalScore the one being judged.
func TestPruneCountsAlternativesBetterThanCurrent(t *testing.T) {
	rule := config.RuleConfig{
		Name:   "Weak tail",
		When:   "count(finalScore >= current.finalScore + 5000) >= 3",
		Action: config.RuleActionPrune,
	}

	_, post := compileStaged(t, rule)
	if !post.HasRelativeAggregates() {
		t.Fatal("a question reading current.finalScore was not recognised as relative")
	}
	// Only the last release has three alternatives 5000 or more above it.
	outs := evalRelativeSet(post, pruneEnvs(20000, 15000, 10000, 5000), "movie")
	for i, want := range []bool{false, false, false, true} {
		if got := len(outs[i].Rejections) > 0; got != want {
			t.Errorf("rank %d: pruned = %v, want %v", i+1, got, want)
		}
	}

	// The same rule over a sparse set keeps the weak release as a fallback:
	// the gap is there, the alternatives are not.
	_, post = compileStaged(t, rule)
	for i, out := range evalRelativeSet(post, pruneEnvs(20000, 5000), "movie") {
		if len(out.Rejections) > 0 {
			t.Errorf("rank %d was pruned with only one better alternative", i+1)
		}
	}
}

// The score band the comparison lives in must not matter: the same rule that
// prunes at 20000/5000 prunes at 500/-14500, because it asks about the gap.
func TestPruneRelativeIsIndependentOfScoreBand(t *testing.T) {
	rule := config.RuleConfig{
		Name:   "Weak tail",
		When:   "count(finalScore >= current.finalScore + 5000) >= 3",
		Action: config.RuleActionPrune,
	}
	for _, band := range []int{0, 20000, -30000} {
		_, post := compileStaged(t, rule)
		outs := evalRelativeSet(post, pruneEnvs(band+15000, band+10000, band+5000, band), "movie")
		for i, want := range []bool{false, false, false, true} {
			if got := len(outs[i].Rejections) > 0; got != want {
				t.Errorf("band %d, rank %d: pruned = %v, want %v", band, i+1, got, want)
			}
		}
	}
}

// current.finalRank is the other half of the pair: how many alternatives sit
// above this one is a question about the set, not about a fixed position.
func TestPruneCountsAlternativesRankedAboveCurrent(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Deep tail",
		When:   "count(finalRank < current.finalRank) >= 3",
		Action: config.RuleActionPrune,
	})

	outs := evalRelativeSet(post, pruneEnvs(900, 800, 700, 600, 500), "movie")
	for i, want := range []bool{false, false, false, true, true} {
		if got := len(outs[i].Rejections) > 0; got != want {
			t.Errorf("rank %d: pruned = %v, want %v", i+1, got, want)
		}
	}
}

// Outside a result-set question the release being judged is this release, so
// current.finalScore and finalScore are the same number — and a set that only
// says so is not relative, because nothing about it varies per release.
func TestCurrentOutsideAggregateIsThisRelease(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Weak",
		When:   "current.finalScore < 0 and current.finalRank > 1",
		Action: config.RuleActionPrune,
	})
	if post.HasRelativeAggregates() {
		t.Error("a rule with no result-set question was treated as relative")
	}

	envs := pruneEnvs(900, -1000)
	for i, want := range []bool{false, true} {
		if got := len(post.Evaluate(envs[i], "movie").Rejections) > 0; got != want {
			t.Errorf("rank %d: pruned = %v, want %v", i+1, got, want)
		}
	}
}

// A set asking only about the set itself keeps the one shared computation.
func TestAbsoluteAggregatesAreNotRelative(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Weak with fallbacks",
		When:   "finalScore < -500 and count(finalScore >= -500) >= 3",
		Action: config.RuleActionPrune,
	})
	if post.HasRelativeAggregates() {
		t.Error("an absolute result-set question was treated as relative")
	}
}

// current. inside a string is text. Mistaking it for a reference would only
// cost a redundant computation, but the detection is exact and the release
// names people actually search for are not the place to start guessing.
func TestCurrentInsideStringIsNotRelative(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Odd tail",
		When:   `count(releaseName contains "current.finalScore") >= 1`,
		Action: config.RuleActionPrune,
	})
	if post.HasRelativeAggregates() {
		t.Error("current. inside a string literal was read as a reference")
	}
}

// Mixing the two forms in one profile answers both correctly: the shared
// question is still counted over the whole set, the relative one per release.
func TestPruneMixesRelativeAndAbsoluteAggregates(t *testing.T) {
	_, post := compileStaged(t,
		config.RuleConfig{
			Name:   "Weak tail",
			When:   "count(finalScore >= current.finalScore + 5000) >= 2 and count(finalScore >= 10000) >= 2",
			Action: config.RuleActionPrune,
		},
	)

	outs := evalRelativeSet(post, pruneEnvs(20000, 15000, 10000, 5000), "movie")
	for i, want := range []bool{false, false, true, true} {
		if got := len(outs[i].Rejections) > 0; got != want {
			t.Errorf("rank %d: pruned = %v, want %v", i+1, got, want)
		}
	}
}

// The scoring pass has no current.* either, and says why: the fix is a
// different action, not a different spelling.
func TestScoringRulesCannotReadCurrentFinalScore(t *testing.T) {
	_, _, err := rules.CompileStaged([]config.RuleConfig{{
		Name:   "Too eager",
		When:   "current.finalScore < 0",
		Action: config.RuleActionReject,
	}})
	if err == nil {
		t.Fatal("a scoring-pass rule reading current.finalScore compiled")
	}
	if !strings.Contains(err.Error(), "prune") {
		t.Errorf("error %q does not point at the prune action", err)
	}
}

// Releases holding the same score ask the same relative question, and the
// answer is reused rather than recomputed. Ties are what make that worth
// doing — a resolution-scored profile hands out the same points to every
// release of a resolution — so the tie is also where the reuse has to be
// right.
func TestPruneRelativeSharesAnswersAcrossTiedScores(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Weak tail",
		When:   "count(finalScore >= current.finalScore + 5000) >= 2",
		Action: config.RuleActionPrune,
	})

	outs := evalRelativeSet(post, pruneEnvs(10000, 10000, 5000, 5000), "movie")
	for i, want := range []bool{false, false, true, true} {
		if got := len(outs[i].Rejections) > 0; got != want {
			t.Errorf("rank %d: pruned = %v, want %v", i+1, got, want)
		}
	}
}

// A question reading current.finalRank cannot share an answer between two
// releases that merely score the same: their ranks differ, which is the whole
// question. Tied scores are exactly where a reuse keyed on the score would be
// wrong, so that is what this asks.
func TestPruneRelativeByRankSurvivesTiedScores(t *testing.T) {
	_, post := compileStaged(t, config.RuleConfig{
		Name:   "Deep tail",
		When:   "count(finalRank < current.finalRank) >= 2",
		Action: config.RuleActionPrune,
	})

	outs := evalRelativeSet(post, pruneEnvs(500, 500, 500, 500), "movie")
	for i, want := range []bool{false, false, true, true} {
		if got := len(outs[i].Rejections) > 0; got != want {
			t.Errorf("rank %d of four tied scores: pruned = %v, want %v", i+1, got, want)
		}
	}
}
