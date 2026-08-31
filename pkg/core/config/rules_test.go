package config

import (
	"strings"
	"testing"

	"github.com/dreulavelle/jhin/rank"
)

// Weighted patterns move onto named rules so they can be renamed, edited and
// reported. The jhin side is cleared in the same step, which is what keeps a
// pattern from being scored twice and what makes the migration idempotent.
func TestMigratePatternRanks(t *testing.T) {
	fp := FilterProfileConfig{
		Name: "Legacy",
		Ranking: &rank.Profile{
			PatternRanks: []rank.PatternRank{
				{Pattern: `(?i)\bIMAX\b`, Rank: 1000},
				{Pattern: `Dual.?Audio`, Rank: 500},
			},
		},
	}

	if !fp.migratePatternRanks() {
		t.Fatal("migratePatternRanks reported no change")
	}
	if len(fp.Ranking.PatternRanks) != 0 {
		t.Errorf("jhin still holds %d patterns; they would be scored twice", len(fp.Ranking.PatternRanks))
	}
	if len(fp.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(fp.Rules))
	}
	if fp.Rules[0].Name != "IMAX" {
		t.Errorf("first rule named %q, want %q", fp.Rules[0].Name, "IMAX")
	}
	if fp.Rules[0].Points != 1000 {
		t.Errorf("first rule worth %d, want 1000", fp.Rules[0].Points)
	}
	if fp.Rules[0].EffectiveAction() != RuleActionScore {
		t.Errorf("migrated rule action = %q, want score", fp.Rules[0].EffectiveAction())
	}
	if !strings.Contains(fp.Rules[0].When, "releaseName matches") {
		t.Errorf("migrated condition %q does not match against the release name", fp.Rules[0].When)
	}

	// Running again moves nothing, because there is nothing left to move.
	if fp.migratePatternRanks() {
		t.Error("migratePatternRanks reported a change on a profile it had already migrated")
	}
	if len(fp.Rules) != 2 {
		t.Errorf("second pass left %d rules, want 2", len(fp.Rules))
	}
}

// Two patterns that reduce to the same label still produce rules a user can
// tell apart.
func TestMigratePatternRanksDisambiguatesNames(t *testing.T) {
	fp := FilterProfileConfig{
		Name: "Dupes",
		Ranking: &rank.Profile{PatternRanks: []rank.PatternRank{
			{Pattern: `(?i)\bIMAX\b`, Rank: 1000},
			{Pattern: `/IMAX/`, Rank: 500},
		}},
	}
	fp.migratePatternRanks()

	if len(fp.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(fp.Rules))
	}
	if fp.Rules[0].Name == fp.Rules[1].Name {
		t.Errorf("both rules named %q", fp.Rules[0].Name)
	}
}

// Case sensitivity survives the move: a slash-wrapped pattern stays
// case-sensitive, everything else keeps the case-insensitive default the
// Rules tab has always applied.
func TestMatchesReleaseNameCaseHandling(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{`IMAX`, `releaseName matches "(?i)IMAX"`},
		{`(?i)IMAX`, `releaseName matches "(?i)IMAX"`},
		{`/IMAX/`, `releaseName matches "IMAX"`},
	}
	for _, tt := range tests {
		if got := MatchesReleaseName(tt.pattern); got != tt.want {
			t.Errorf("MatchesReleaseName(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}

func TestRuleDefaults(t *testing.T) {
	var r RuleConfig
	if !r.IsEnabled() {
		t.Error("a rule with no enabled flag should be enabled")
	}
	if r.EffectiveAction() != RuleActionScore {
		t.Errorf("default action = %q, want score", r.EffectiveAction())
	}
	if r.EffectiveScope() != RuleScopeAll {
		t.Errorf("default scope = %q, want all", r.EffectiveScope())
	}
}

// A profile carried over from the pre-jhin schema gets no score floor. The
// floor now judges the finished score, so inheriting jhin's default would give
// a migrated profile a threshold its owner never set.
func TestSynthesizeLeavesNoScoreFloor(t *testing.T) {
	spec := Synthesize(FilterProfileConfig{Name: "Legacy", AllowedResolutions: []string{"1080p"}})
	if spec.Options.MinRank != noScoreFloor {
		t.Errorf("MinRank = %d, want the no-floor value %d", spec.Options.MinRank, noScoreFloor)
	}
}
