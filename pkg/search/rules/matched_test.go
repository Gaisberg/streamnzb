package rules_test

import (
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/rules"
)

func boolPtr(v bool) *bool { return &v }

// The use case rule references exist for: tier rules define which groups are
// trusted, and one reject rule reuses them rather than copying their
// conditions.
func TestReferenceReusesTierRules(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "UHD BluRay T1", When: `group == "FraMeSToR"`, Points: 3000},
		config.RuleConfig{Name: "UHD BluRay T2", When: `group == "TayTo"`, Points: 2000},
		config.RuleConfig{
			Name: "Untrusted 4K encode",
			When: `resolution == "2160p" and "bluray" in traits` +
				` and not (matched("UHD BluRay T1") or matched("UHD BluRay T2"))` +
				` and exists(resolution == "2160p" and "remux" in traits)`,
			Action: config.RuleActionReject,
		},
	)

	remux := "Movie 2020 2160p BluRay REMUX HEVC-FraMeSToR"
	untrusted := "Movie 2020 2160p BluRay x265-NOBODY"
	trusted := "Movie 2020 2160p BluRay x265-TayTo"

	outs := evalSet(set, envsFor(remux, untrusted, trusted), "movie")
	if len(outs[1].Rejections) == 0 {
		t.Error("the untrusted encode survived although a remux exists")
	}
	if len(outs[2].Rejections) != 0 {
		t.Errorf("a T2 release was rejected: %v", outs[2].Rejections)
	}
	if len(outs[0].Rejections) != 0 {
		t.Errorf("the remux was rejected: %v", outs[0].Rejections)
	}
}

// A reference is resolved at compile time, so it works inside a result-set
// call too: "is there a release the good-group rule would pay out on".
func TestReferenceInsideResultSetCall(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Good group", When: `group == "FraMeSToR"`, Points: 1000},
		config.RuleConfig{
			Name:   "No good group anywhere",
			When:   `none(matched("Good group"))`,
			Points: 500,
		},
	)

	without := evalSet(set, envsFor("Movie 2020 1080p WEB-DL-NOBODY"), "movie")
	if without[0].Points != 500 {
		t.Errorf("Points = %d, want 500 with no good group in the set", without[0].Points)
	}

	with := evalSet(set, envsFor("Movie 2020 1080p WEB-DL-NOBODY", "Movie 2020 2160p REMUX-FraMeSToR"), "movie")
	if with[0].Points != 0 {
		t.Errorf("Points = %d, want 0 once a good group is in the set", with[0].Points)
	}
}

// Order does not matter: a reference is inlined, so a rule may name one
// written below it.
func TestReferenceForward(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Uses later", When: `matched("Later")`, Points: 7},
		config.RuleConfig{Name: "Later", When: `resolution == "2160p"`, Points: 1},
	)
	if got := set.Evaluate(envFor("Movie 2020 2160p WEB-DL-GRP", nil), "movie"); got.Points != 8 {
		t.Errorf("Points = %d, want 8", got.Points)
	}
}

// A reference chains: what it pulls in may itself reference something else.
func TestReferenceChains(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Base", When: `group == "GRP"`, Points: 1},
		config.RuleConfig{Name: "Middle", When: `matched("Base") and resolution == "2160p"`, Points: 2},
		config.RuleConfig{Name: "Top", When: `matched("Middle") and "webdl" in traits`, Points: 4},
	)
	if got := set.Evaluate(envFor("Movie 2020 2160p WEB-DL-GRP", nil), "movie"); got.Points != 7 {
		t.Errorf("Points = %d, want 7", got.Points)
	}
	if got := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie"); got.Points != 1 {
		t.Errorf("Points = %d, want 1", got.Points)
	}
}

// A rule that is switched off classifies nothing, so a reference to it is
// never true — and its condition is never looked at, which is what keeps a
// broken rule that is turned off from blocking a save.
func TestReferenceToDisabledRule(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Off", When: `nonsense +`, Points: 1, Enabled: boolPtr(false)},
		config.RuleConfig{Name: "Uses it", When: `matched("Off") or resolution == "2160p"`, Points: 5},
	)
	if got := set.Evaluate(envFor("Movie 2020 2160p WEB-DL-GRP", nil), "movie"); got.Points != 5 {
		t.Errorf("Points = %d, want 5", got.Points)
	}
	if got := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie"); got.Points != 0 {
		t.Errorf("Points = %d, want 0 — a disabled rule never matches", got.Points)
	}
}

// A reference carries the referenced rule's scope with it. Without that, a
// rule applying to everything would read a movie-only tier list as holding for
// series as well.
func TestReferenceCarriesScope(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Movie tier", Scope: "movie", When: `group == "GRP"`, Points: 1},
		config.RuleConfig{Name: "Everywhere", When: `matched("Movie tier")`, Points: 10},
	)
	env := envFor("Show 2020 S01E01 1080p WEB-DL-GRP", nil)
	if got := set.Evaluate(env, "movie"); got.Points != 11 {
		t.Errorf("movie Points = %d, want 11", got.Points)
	}
	env.Kind = "series"
	if got := set.Evaluate(env, "series"); got.Points != 0 {
		t.Errorf("series Points = %d, want 0 — the referenced rule is movie-only", got.Points)
	}
}

// A reference to a probe rule inherits its tier, so the referring rule is
// skipped on a release that was never opened rather than judged against zeros.
func TestReferenceInheritsTier(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Small picture", When: `probed.height < 1080`, Points: 1},
		config.RuleConfig{Name: "Reject small", When: `matched("Small picture")`, Action: config.RuleActionReject},
	)
	got := set.Evaluate(envFor("Movie 2020 1080p WEB-DL-GRP", nil), "movie")
	if len(got.Rejections) != 0 {
		t.Errorf("an unprobed release was rejected: %v", got.Rejections)
	}
	if len(got.Skipped) != 2 {
		t.Errorf("Skipped = %v, want both rules skipped as probe-dependent", got.Skipped)
	}
}

// A limit rule may group by a reference: the bucket is whether the other rule
// classifies the release.
func TestReferenceInGroupBy(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Trusted", When: `group == "GRP"`, Points: 1},
		config.RuleConfig{
			Name: "Cap", When: `resolution == "2160p"`,
			Action: config.RuleActionLimit, Count: 1, GroupBy: `matched("Trusted")`,
		},
	)
	trusted := set.Evaluate(envFor("Movie 2020 2160p WEB-DL-GRP", nil), "movie")
	other := set.Evaluate(envFor("Movie 2020 2160p WEB-DL-OTHER", nil), "movie")
	if len(trusted.Limits) != 1 || len(other.Limits) != 1 {
		t.Fatalf("limits = %v / %v, want one each", trusted.Limits, other.Limits)
	}
	if trusted.Limits[0].Group == other.Limits[0].Group {
		t.Errorf("both releases landed in bucket %q", trusted.Limits[0].Group)
	}
}

func TestReferenceErrors(t *testing.T) {
	tests := []struct {
		name  string
		cfgs  []config.RuleConfig
		wants string
	}{
		{
			name:  "unknown rule",
			cfgs:  []config.RuleConfig{{Name: "Uses it", When: `matched("Nothing")`, Points: 1}},
			wants: `no rule is named "Nothing"`,
		},
		{
			name: "ambiguous name",
			cfgs: []config.RuleConfig{
				{Name: "Tier", When: `group == "A"`, Points: 1},
				{Name: "tier", When: `group == "B"`, Points: 1},
				{Name: "Uses it", When: `matched("Tier")`, Points: 1},
			},
			wants: `more than one rule is named "Tier"`,
		},
		{
			name:  "references itself",
			cfgs:  []config.RuleConfig{{Name: "Loop", When: `matched("Loop")`, Points: 1}},
			wants: "circle: Loop -> Loop",
		},
		{
			name: "references in a circle",
			cfgs: []config.RuleConfig{
				{Name: "A", When: `matched("B")`, Points: 1},
				{Name: "B", When: `matched("A")`, Points: 1},
			},
			wants: "circle: A -> B -> A",
		},
		{
			name: "name is not a literal",
			cfgs: []config.RuleConfig{
				{Name: "Tier", When: `group == "A"`, Points: 1},
				{Name: "Uses it", When: `matched(group)`, Points: 1},
			},
			wants: "rule name in quotes",
		},
		{
			name:  "no argument",
			cfgs:  []config.RuleConfig{{Name: "Uses it", When: `matched()`, Points: 1}},
			wants: "exactly one rule name",
		},
		{
			name: "referenced rule has no condition",
			cfgs: []config.RuleConfig{
				{Name: "Uses it", When: `matched("Empty")`, Points: 1},
				{Name: "Empty", When: "  ", Points: 1},
			},
			wants: `rule "Empty" has no condition`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rules.Compile(tt.cfgs)
			if err == nil {
				t.Fatal("Compile succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.wants) {
				t.Errorf("error %q does not mention %q", err, tt.wants)
			}
		})
	}
}

// Two rules referencing the same one share its expansion, and the aggregate
// inside it is still lifted once.
func TestReferencesShareOneAggregate(t *testing.T) {
	set := compile(t,
		config.RuleConfig{Name: "Has remux", When: `exists("remux" in traits)`, Points: 1},
		config.RuleConfig{Name: "A", When: `matched("Has remux") and resolution == "2160p"`, Points: 2},
		config.RuleConfig{Name: "B", When: `matched("Has remux") and resolution == "1080p"`, Points: 4},
	)
	outs := evalSet(set, envsFor("Movie 2020 2160p BluRay REMUX-GRP", "Movie 2020 1080p WEB-DL-GRP"), "movie")
	if outs[0].Points != 3 {
		t.Errorf("Points = %d, want 3", outs[0].Points)
	}
	if outs[1].Points != 5 {
		t.Errorf("Points = %d, want 5", outs[1].Points)
	}
}
