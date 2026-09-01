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
