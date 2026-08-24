package rules_test

import (
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/rules"
	"streamnzb/pkg/search/triage"
)

// evalSet runs a set the way the ranking pipeline does: aggregates first,
// once, then every release.
func evalSet(set *rules.Set, envs []rules.Env, kind string) []rules.Outcome {
	state := set.ComputeAggregates(envs)
	outs := make([]rules.Outcome, len(envs))
	for i := range envs {
		state.Inject(&envs[i])
		outs[i] = set.Evaluate(envs[i], kind)
	}
	return outs
}

func envsFor(titles ...string) []rules.Env {
	envs := make([]rules.Env, len(titles))
	for i, title := range titles {
		envs[i] = envFor(title, nil)
	}
	return envs
}

// The use case the feature exists for: reject the questionable 4K release only
// when the set holds a better one to fall back on.
func TestRejectOnlyWhenBetterExists(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "questionable 4K",
		When:   `resolution == "2160p" and upscaled and exists(resolution == "2160p" and "remux" in traits)`,
		Action: config.RuleActionReject,
	})

	upscaled := "Movie 2020 2160p UPSCALED WEB-DL-GRP"

	with := evalSet(set, envsFor(upscaled, "Movie 2020 2160p BluRay REMUX-GRP"), "movie")
	if len(with[0].Rejections) == 0 {
		t.Error("the upscale was kept although a remux exists")
	}
	if len(with[1].Rejections) != 0 {
		t.Errorf("the remux was rejected: %v", with[1].Rejections)
	}

	without := evalSet(set, envsFor(upscaled, "Movie 2020 1080p WEB-DL-GRP"), "movie")
	if len(without[0].Rejections) != 0 {
		t.Errorf("the only 4K release was rejected: %v", without[0].Rejections)
	}
}

func TestCountAndNoneOverTheSet(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "thin 4K", When: `count(resolution == "2160p") < 3`, Points: 10},
		config.RuleConfig{Name: "no webdl", When: `none(quality == "WEB-DL")`, Points: 100},
	)

	envs := envsFor(
		"Movie 2020 2160p BluRay-GRP",
		"Movie 2020 2160p BluRay REMUX-GRP",
		"Movie 2020 1080p BluRay-GRP",
	)
	outs := evalSet(set, envs, "movie")
	for i, out := range outs {
		// Two 4K releases (< 3) and no WEB-DL: both rules pay out everywhere.
		if out.Points != 110 {
			t.Errorf("release %d scored %d, want 110 (%v)", i, out.Points, out.Skipped)
		}
	}

	envs = envsFor(
		"Movie 2020 2160p WEB-DL-GRP",
		"Movie 2020 2160p BluRay-GRP",
		"Movie 2020 2160p BluRay REMUX-GRP",
	)
	if outs := evalSet(set, envs, "movie"); outs[0].Points != 0 {
		t.Errorf("three 4K releases and a WEB-DL still scored %d, want 0", outs[0].Points)
	}
}

// any() is exists() under another name, matching the issue's vocabulary.
func TestAnyAliasesExists(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "remux available", When: `any("remux" in traits)`, Points: 50,
	})
	outs := evalSet(set, envsFor("Movie 2020 1080p WEB-DL-GRP", "Movie 2020 2160p BluRay REMUX-GRP"), "movie")
	if outs[0].Points != 50 {
		t.Errorf("Points = %d, want 50", outs[0].Points)
	}
}

// A result-set condition reading an optional tier counts only the releases
// that carry it, and is unknowable — skipping its rules — when none does.
func TestAggregateTierFailsOpen(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "no probed 4K",
		When:   `resolution == "2160p" and none(probed.height >= 2000)`,
		Action: config.RuleActionReject,
	})

	// A fresh search: nothing probed. The rule must skip, not reject every 4K
	// release because a count over no data came back zero.
	fresh := envsFor("Movie 2020 2160p WEB-DL-GRP", "Movie 2020 1080p WEB-DL-GRP")
	outs := evalSet(set, fresh, "movie")
	if len(outs[0].Rejections) != 0 {
		t.Errorf("rejected on an unprobed set: %v", outs[0].Rejections)
	}
	if len(outs[0].Skipped) != 1 || !strings.Contains(outs[0].Skipped[0], "probed") {
		t.Errorf("Skipped = %v, want the probe-tier reason", outs[0].Skipped)
	}

	// One probed 1080p release: the aggregate is knowable, finds no probed 4K,
	// and the rule fires. Note the rule itself needs no probe on the release
	// it judges — only the lifted condition reads the tier.
	probed := []rules.Env{
		envFor("Movie 2020 2160p WEB-DL-GRP", nil),
		envFor("Movie 2020 1080p WEB-DL-GRP", func(c *triage.Candidate) {
			c.Verdict.Probed = &release.MediaCaps{VideoCodec: "h264", Height: 1080}
		}),
	}
	outs = evalSet(set, probed, "movie")
	if len(outs[0].Rejections) == 0 {
		t.Error("a 4K release with no probed 4K anywhere was not rejected")
	}

	// A probed 4K release: the aggregate counts it and the rule stands down.
	probed4K := []rules.Env{
		envFor("Movie 2020 2160p WEB-DL-GRP", nil),
		envFor("Movie 2020 2160p BluRay REMUX-GRP", func(c *triage.Candidate) {
			c.Verdict.Probed = &release.MediaCaps{VideoCodec: "hevc", Height: 2160}
		}),
	}
	outs = evalSet(set, probed4K, "movie")
	if len(outs[0].Rejections) != 0 {
		t.Errorf("rejected although a probed 4K release exists: %v", outs[0].Rejections)
	}
}

// Evaluating a set with aggregates without computing them first must skip the
// dependent rules, never guess.
func TestUncomputedAggregatesSkip(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name: "needs the set", When: `exists(library)`, Action: config.RuleActionReject,
	})
	out := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie")
	if len(out.Rejections) != 0 {
		t.Errorf("rejected without computed aggregates: %v", out.Rejections)
	}
	if len(out.Skipped) != 1 {
		t.Errorf("Skipped = %v, want one entry", out.Skipped)
	}
}

// expr's own collection builtins share names with the result-set functions.
// The two-argument forms and count over a literal list keep their meaning.
func TestCollectionBuiltinsUntouched(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "dv tag", When: `any(hdr, # == "DV")`, Points: 5},
		config.RuleConfig{Name: "fixed", When: `count([proper, repack]) >= 1`, Points: 7},
	)
	got := set.Evaluate(envFor("Movie 2020 2160p DV HDR REPACK BluRay-GRP", nil), "movie")
	if got.Points != 12 {
		t.Errorf("Points = %d, want 12 (%v)", got.Points, got.Skipped)
	}
}

func TestAggregateCompileErrors(t *testing.T) {
	tests := []struct {
		name string
		when string
		want string
	}{
		{"nested", `exists(count(library) > 0)`, "cannot nest"},
		{"reserved", `__aggs[0] > 1`, "reserved"},
		{"no condition", `count() > 0`, "exactly one condition"},
		{"two arguments", `exists(library, upscaled)`, "exactly one condition"},
		{"not boolean", `count(sizeGB) > 0`, "in count(sizeGB)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rules.Compile([]config.RuleConfig{{Name: "broken", When: tt.when}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Compile error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// The rewrite must survive conditions whose string literals look like calls or
// carry parentheses.
func TestAggregateRewriteIgnoresStringLiterals(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "regex inside",
		When:   `exists(releaseName matches "(?i)\\bREMUX\\b" and resolution == "2160p")`,
		Points: 9,
	})
	outs := evalSet(set, envsFor("Movie 2020 1080p WEB-DL-GRP", "Movie 2020 2160p BluRay REMUX-GRP"), "movie")
	if outs[0].Points != 9 {
		t.Errorf("Points = %d, want 9 (%v)", outs[0].Points, outs[0].Skipped)
	}
}
