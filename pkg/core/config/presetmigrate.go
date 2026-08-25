package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dreulavelle/jhin/rank"
)

// MigrateToPreset converts a hand-tuned ranking profile into the only two
// things a profile now carries: a preset, and rules.
//
// Nothing a user configured is thrown away. Every knob that used to have its
// own control becomes a rule with a name, which means it stays visible, stays
// editable, and can now be scoped to a content kind or combined with anything
// else. A profile that only ever used the defaults migrates to a bare preset
// and no rules at all.
//
// Reports whether anything changed.
func (fp *FilterProfileConfig) MigrateToPreset() bool {
	if fp == nil || fp.Ranking == nil {
		// Already on a preset, or new. Make sure it has one.
		if fp != nil && fp.Preset == "" {
			fp.Preset = DefaultPreset
			return true
		}
		return false
	}

	spec := *fp.Ranking
	fp.Preset = presetForResolutions(spec.Resolutions)
	baseline := PresetSpec(fp.Preset)

	taken := make(map[string]bool, len(fp.Rules))
	for _, existing := range fp.Rules {
		taken[strings.ToLower(strings.TrimSpace(existing.Name))] = true
	}
	add := func(name, when, action string, points int) {
		fp.Rules = append(fp.Rules, RuleConfig{
			Name:   uniqueRuleName(name, taken),
			When:   when,
			Action: action,
			Points: points,
		})
	}

	// Patterns. Must-match is conjunctive, so each becomes its own rejection
	// of anything the pattern does not appear in.
	for _, pattern := range spec.Require {
		if p := strings.TrimSpace(pattern); p != "" {
			add("Require "+patternRuleName(p), "not ("+MatchesReleaseName(p)+")", RuleActionReject, 0)
		}
	}
	for _, pattern := range spec.Exclude {
		if p := strings.TrimSpace(pattern); p != "" {
			add("Exclude "+patternRuleName(p), MatchesReleaseName(p), RuleActionReject, 0)
		}
	}
	bonus := spec.Options.PreferredBonus
	if bonus == 0 {
		bonus = 10000
	}
	for _, pattern := range spec.Preferred {
		if p := strings.TrimSpace(pattern); p != "" {
			add(patternRuleName(p), MatchesReleaseName(p), RuleActionScore, bonus)
		}
	}

	// Traits the user moved away from the baseline. Only the differences are
	// carried, so a profile that accepted the recommended values gets no rules
	// for them.
	for attr, policy := range spec.Attributes {
		base, hasBase := baseline.Attributes[attr]
		if !hasBase {
			base = rank.DefaultPolicies[attr]
		}
		label := traitLabel(attr)
		if !policy.Fetch && base.Fetch {
			add("Block "+label, fmt.Sprintf("%q in traits", string(attr)), RuleActionReject, 0)
			continue
		}
		if policy.Fetch && policy.Rank != base.Rank {
			delta := policy.Rank - base.Rank
			if delta != 0 {
				add(label, fmt.Sprintf("%q in traits", string(attr)), RuleActionScore, delta)
			}
		}
	}

	// Languages.
	for _, code := range spec.Languages.Exclude {
		if c := strings.TrimSpace(code); c != "" {
			add("No "+c, fmt.Sprintf("%q in languages", c), RuleActionReject, 0)
		}
	}
	if required := trimmed(spec.Languages.Required); len(required) > 0 {
		add("Require language", "not ("+anyLanguage(required)+")", RuleActionReject, 0)
	}
	for _, code := range trimmed(spec.Languages.Preferred) {
		// English is dropped rather than carried into a rule. It was the
		// baseline's own default rather than something anyone chose, and the
		// bonus only ever paid on a release whose title says so — so
		// migrating it would make a default nobody set permanent and visible.
		// Any other language was a deliberate choice and becomes a rule.
		if strings.EqualFold(code, "en") {
			continue
		}
		add("Prefer "+code, fmt.Sprintf("%q in languages", code), RuleActionScore, bonus)
	}

	// NZB bounds. Sizes judge the per-episode share, matching what the limits
	// always did — a rule against the total would reject season packs for
	// being packs.
	for _, kind := range LimitKinds {
		limits := fp.Limits[kind]
		if limits == nil {
			continue
		}
		scope := kind
		if kind == LimitKindDefault {
			scope = RuleScopeAll
		}
		addScoped := func(name, when string) {
			fp.Rules = append(fp.Rules, RuleConfig{
				Name:   uniqueRuleName(name, taken),
				Scope:  scope,
				When:   when,
				Action: RuleActionReject,
			})
		}
		if limits.MaxSizeGB > 0 {
			addScoped("Too large", fmt.Sprintf("sizePerEpisodeGB >= 0 and sizePerEpisodeGB > %s", num(limits.MaxSizeGB)))
		}
		if limits.MinSizeGB > 0 {
			addScoped("Too small", fmt.Sprintf("sizePerEpisodeGB >= 0 and sizePerEpisodeGB < %s", num(limits.MinSizeGB)))
		}
		if limits.MaxAgeDays > 0 {
			addScoped("Too old", fmt.Sprintf("ageDays >= 0 and ageDays > %d", limits.MaxAgeDays))
		}
		if limits.MinGrabs > 0 {
			addScoped("Too few grabs", fmt.Sprintf("grabs > 0 and grabs < %d", limits.MinGrabs))
		}
	}

	// Everything is now expressed as a preset plus rules. The old spec and the
	// per-kind maps are cleared so nothing is applied twice and nothing
	// lingers that no screen can show.
	fp.Ranking = nil
	fp.Limits = nil
	fp.Scoring = nil
	return true
}

// presetForResolutions picks the preset matching the highest tier a tuned
// profile had enabled, which is the decision the tiers were really encoding.
func presetForResolutions(resolutions map[rank.Resolution]bool) string {
	switch {
	case resolutions == nil:
		return DefaultPreset
	case resolutions[rank.Res2160p] || resolutions[rank.Res1440p]:
		return PresetUHD
	case resolutions[rank.Res1080p]:
		return Preset1080p
	case resolutions[rank.Res720p]:
		return Preset720p
	default:
		return DefaultPreset
	}
}

// traitLabel renders an attribute key as something a person would name a rule:
// "dolby_vision" reads as "Dolby vision".
func traitLabel(attr rank.Attr) string {
	words := strings.ReplaceAll(string(attr), "_", " ")
	if words == "" {
		return "Trait"
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

// anyLanguage builds the condition for "at least one of these languages".
func anyLanguage(codes []string) string {
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%q in languages", code))
	}
	return strings.Join(parts, " or ")
}

// num renders a float without a trailing ".0", so a migrated rule reads with
// the number the user actually typed.
func num(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func trimmed(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}
