package config

import (
	"strings"
	"testing"

	"github.com/dreulavelle/jhin/rank"
)

func ruleNamed(rules []RuleConfig, name string) (RuleConfig, bool) {
	for _, r := range rules {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return RuleConfig{}, false
}

func hasCondition(rules []RuleConfig, substr string) bool {
	for _, r := range rules {
		if strings.Contains(r.When, substr) {
			return true
		}
	}
	return false
}

// A profile that only ever used the recommended values migrates to a bare
// preset. Producing rules for settings the user never touched would bury the
// ones they did.
func TestMigrateUntouchedProfileProducesNoRules(t *testing.T) {
	spec := PresetSpec(PresetUHD)
	fp := FilterProfileConfig{Name: "Stock", Ranking: &spec}

	if !fp.MigrateToPreset() {
		t.Fatal("MigrateToPreset reported no change")
	}
	if fp.Preset != PresetUHD {
		t.Errorf("preset = %q, want %q", fp.Preset, PresetUHD)
	}
	if len(fp.Rules) != 0 {
		t.Errorf("got %d rules for an untouched profile: %+v", len(fp.Rules), fp.Rules)
	}
	if fp.Ranking != nil {
		t.Error("the spec should be cleared once it is expressed as a preset")
	}
}

// The preset is taken from the highest tier the profile offered, which is the
// decision the resolution toggles were really encoding.
func TestMigratePicksPresetFromCeiling(t *testing.T) {
	tests := []struct {
		name        string
		resolutions map[rank.Resolution]bool
		want        string
	}{
		{"4K enabled", map[rank.Resolution]bool{rank.Res2160p: true, rank.Res1080p: true}, PresetUHD},
		{"1080p ceiling", map[rank.Resolution]bool{rank.Res1080p: true, rank.Res720p: true}, Preset1080p},
		{"720p only", map[rank.Resolution]bool{rank.Res720p: true}, Preset720p},
		{"1440p counts as 4K", map[rank.Resolution]bool{rank.Res1440p: true}, PresetUHD},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := rank.Default()
			spec.Resolutions = tt.resolutions
			fp := FilterProfileConfig{Name: tt.name, Ranking: &spec}
			fp.MigrateToPreset()
			if fp.Preset != tt.want {
				t.Errorf("preset = %q, want %q", fp.Preset, tt.want)
			}
		})
	}
}

// Everything a user tuned becomes a rule they can still see. This is the whole
// bargain of removing the controls: the settings do not disappear, they become
// nameable and combinable.
func TestMigrateCarriesTuningIntoRules(t *testing.T) {
	spec := PresetSpec(PresetUHD)
	spec.Require = []string{`IMAX`}
	spec.Exclude = []string{`HDCAM`}
	spec.Preferred = []string{`Dual.?Audio`}
	spec.Languages.Exclude = []string{"ru"}
	spec.Languages.Required = []string{"en", "ja"}
	// Blocking a trait the baseline allows, and re-scoring one it scores.
	spec.Attributes[rank.AttrWebRip] = rank.Policy{Fetch: false, Rank: -1000}
	spec.Attributes[rank.AttrRemux] = rank.Policy{Fetch: true, Rank: 9000}

	fp := FilterProfileConfig{
		Name:    "Tuned",
		Ranking: &spec,
		Limits: map[string]*LimitsConfig{
			LimitKindDefault: {MaxSizeGB: 30, MinGrabs: 5},
			"series":         {MaxSizeGB: 5},
		},
	}
	fp.MigrateToPreset()

	if !hasCondition(fp.Rules, `not (releaseName matches "(?i)IMAX")`) {
		t.Errorf("must-match pattern did not become a rejection: %+v", fp.Rules)
	}
	if !hasCondition(fp.Rules, `releaseName matches "(?i)HDCAM"`) {
		t.Error("never-match pattern did not become a rejection")
	}
	if r, ok := ruleNamed(fp.Rules, "Dual Audio"); !ok || r.Points != 10000 {
		t.Errorf("preferred pattern did not become a score rule at the bonus: %+v", r)
	}
	if r, ok := ruleNamed(fp.Rules, "Block Webrip"); !ok || r.EffectiveAction() != RuleActionReject {
		t.Errorf("blocked trait did not become a rejection: %+v", r)
	}
	if !hasCondition(fp.Rules, `"webrip" in traits`) {
		t.Error("blocked trait rule does not read the trait list")
	}
	// Only the difference from the baseline carries, whatever the baseline is.
	wantDelta := 9000 - PresetSpec(PresetUHD).Attributes[rank.AttrRemux].Rank
	if r, ok := ruleNamed(fp.Rules, "Remux"); !ok || r.Points != wantDelta {
		t.Errorf("re-scored trait = %+v, want the %d difference from the baseline", r, wantDelta)
	}
	if !hasCondition(fp.Rules, `"ru" in languages`) {
		t.Error("excluded language did not become a rejection")
	}
	if !hasCondition(fp.Rules, `"en" in languages or "ja" in languages`) {
		t.Errorf("required languages did not become one rejection: %+v", fp.Rules)
	}

	// Size bounds judge the per-episode share, exactly as the limits did.
	tooLarge, ok := ruleNamed(fp.Rules, "Too large")
	if !ok {
		t.Fatal("max size limit did not become a rule")
	}
	if !strings.Contains(tooLarge.When, "sizePerEpisodeGB > 30") {
		t.Errorf("size rule = %q, want the per-episode share", tooLarge.When)
	}
	if tooLarge.EffectiveScope() != RuleScopeAll {
		t.Errorf("default limit scope = %q, want all", tooLarge.EffectiveScope())
	}
	// The per-kind override becomes a scoped rule rather than being lost.
	var scoped bool
	for _, r := range fp.Rules {
		if r.EffectiveScope() == "series" && strings.Contains(r.When, "sizePerEpisodeGB > 5") {
			scoped = true
		}
	}
	if !scoped {
		t.Errorf("per-kind size limit did not become a scoped rule: %+v", fp.Rules)
	}

	if fp.Limits != nil || fp.Scoring != nil {
		t.Error("the per-kind maps should be cleared once they are rules")
	}
}

// Running the migration twice must not duplicate anything.
func TestMigrateIsIdempotent(t *testing.T) {
	spec := PresetSpec(Preset1080p)
	spec.Exclude = []string{"HDCAM"}
	fp := FilterProfileConfig{Name: "Twice", Ranking: &spec}

	fp.MigrateToPreset()
	first := len(fp.Rules)
	if fp.MigrateToPreset() {
		t.Error("second pass reported a change on an already-migrated profile")
	}
	if len(fp.Rules) != first {
		t.Errorf("rules grew from %d to %d on a second pass", first, len(fp.Rules))
	}
}

// English was the old baseline's own default rather than a choice anyone made,
// so it is dropped rather than migrated into a rule that would make it
// permanent. Any other language was deliberate and is carried.
func TestMigrateDropsEnglishAndKeepsOtherPreferredLanguages(t *testing.T) {
	spec := PresetSpec(PresetUHD)
	spec.Languages.Preferred = []string{"en", "ja"}
	fp := FilterProfileConfig{Name: "Langs", Ranking: &spec}
	fp.MigrateToPreset()

	if _, ok := ruleNamed(fp.Rules, "Prefer en"); ok {
		t.Error("English got a rule; the baseline default is meant to be dropped")
	}
	if _, ok := ruleNamed(fp.Rules, "Prefer ja"); !ok {
		t.Errorf("Japanese did not get a rule: %+v", fp.Rules)
	}
}
