package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Rule actions.
//
// Score and reject are the split the profile as a whole is built on, made
// explicit per rule so a user can see which of the two a rule does without
// reading its condition. Limit is the one thing no condition can say — even a
// result-set condition: "at most three of these" is about the final score
// order, which only exists after every rule has run — so it is an action
// rather than a function inside a condition. Define is the absence of an
// action: a named condition kept only for other rules to reference through
// matched(), so a tier list of release groups has one home instead of a copy
// in every rule that cares. Prune is reject moved past the scoring: its
// condition runs after every point is in and the surviving releases are in
// final order, which is what lets it read finalScore and finalRank — values no
// scoring-pass rule may see, because a rule reading the score it is helping to
// build has no answer.
const (
	RuleActionScore  = "score"
	RuleActionReject = "reject"
	RuleActionLimit  = "limit"
	RuleActionDefine = "define"
	RuleActionPrune  = "prune"
)

// RuleScopeAll is the scope of a rule that applies to every content kind. The
// other scopes are the ranking content kinds ("movie", "series",
// "anime_movie", "anime_show").
const RuleScopeAll = "all"

// RuleConfig is one named rule in a filter profile: a condition over a
// release's attributes, and what to do when it holds.
//
// Rules are the general form of what weighted regex patterns used to be. They
// carry a name because a user maintaining a dozen release-group tiers needs to
// tell them apart — in the editor, in the score breakdown, and in custom
// result formats.
type RuleConfig struct {
	// Name identifies the rule to the user and is what custom formats see
	// when the rule pays out.
	Name string `json:"name"`
	// Scope limits the rule to one content kind. Empty or "all" applies
	// everywhere.
	Scope string `json:"scope,omitempty"`
	// When is the condition, in the rule expression language. It must
	// evaluate to a boolean.
	When string `json:"when"`
	// Action is what happens when When holds. Empty means score.
	Action string `json:"action,omitempty"`
	// Points is what a matching release earns, for score rules. May be
	// negative to push a release down without rejecting it.
	Points int `json:"points,omitempty"`
	// Count is how many matching releases survive, for limit rules. The best
	// Count by final score are kept and the rest are dropped, so a limit is
	// always a cap on the tail rather than an arbitrary selection.
	Count int `json:"count,omitempty"`
	// GroupBy splits a limit rule's cap into one bucket per value, so that
	// Count is kept per bucket rather than across the whole result set. It is
	// an expression over the same attributes a condition reads, which is what
	// lets one rule cap per resolution, per release group, or per combination
	// of the two without a separate rule for every value. Empty caps the
	// matching releases as a single set, which is what a limit rule has
	// always done.
	GroupBy string `json:"group_by,omitempty"`
	// Enabled turns the rule off without deleting it. Nil means enabled, so
	// a rule written before the flag existed keeps working.
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled reports whether the rule takes part in ranking.
func (r RuleConfig) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// EffectiveAction is the rule's action with the default applied.
func (r RuleConfig) EffectiveAction() string {
	switch strings.ToLower(strings.TrimSpace(r.Action)) {
	case RuleActionReject:
		return RuleActionReject
	case RuleActionLimit:
		return RuleActionLimit
	case RuleActionDefine:
		return RuleActionDefine
	case RuleActionPrune:
		return RuleActionPrune
	default:
		return RuleActionScore
	}
}

// EffectiveScope is the rule's scope with the default applied.
func (r RuleConfig) EffectiveScope() string {
	scope := strings.ToLower(strings.TrimSpace(r.Scope))
	if scope == "" {
		return RuleScopeAll
	}
	return scope
}

// migratePatternRanks converts a profile's jhin weighted patterns into named
// rules and clears them from the jhin spec, so each pattern is evaluated once
// and by the side that can give it a name. Reports whether anything moved.
//
// jhin's PatternRank carries only a pattern and a score; a rule carries a name
// and an enabled flag as well, which is what makes it editable and reportable.
// Migrated rules are named after their pattern because that is the only label
// the old schema had — the user renames them from there.
func (fp *FilterProfileConfig) migratePatternRanks() bool {
	if fp == nil || fp.Ranking == nil || len(fp.Ranking.PatternRanks) == 0 {
		return false
	}
	taken := make(map[string]bool, len(fp.Rules))
	for _, existing := range fp.Rules {
		taken[strings.ToLower(strings.TrimSpace(existing.Name))] = true
	}
	for _, pr := range fp.Ranking.PatternRanks {
		pattern := strings.TrimSpace(pr.Pattern)
		if pattern == "" {
			continue
		}
		fp.Rules = append(fp.Rules, RuleConfig{
			Name:   uniqueRuleName(patternRuleName(pattern), taken),
			When:   MatchesReleaseName(pattern),
			Action: RuleActionScore,
			Points: pr.Rank,
		})
	}
	fp.Ranking.PatternRanks = nil
	return true
}

// MatchesReleaseName builds the rule condition equivalent to jhin's weighted
// pattern matching: the pattern applied to the whole release name. Patterns
// wrapped in slashes are case-sensitive, matching how the Rules tab has always
// read them; everything else gets the case-insensitive flag.
func MatchesReleaseName(pattern string) string {
	expr := pattern
	if len(expr) > 1 && strings.HasPrefix(expr, "/") && strings.HasSuffix(expr, "/") {
		expr = expr[1 : len(expr)-1]
	} else if !strings.HasPrefix(expr, "(?i)") {
		expr = "(?i)" + expr
	}
	return fmt.Sprintf("releaseName matches %s", strconv.Quote(expr))
}

// patternRuleName is the display name a migrated pattern gets: the pattern
// with its regex scaffolding stripped, which for the presets the UI shipped
// ("Dual.?Audio", "(?i)\bIMAX\b") reads as the label the user picked it by.
func patternRuleName(pattern string) string {
	name := strings.TrimSpace(pattern)
	name = strings.TrimPrefix(name, "(?i)")
	name = strings.Trim(name, "/")
	name = strings.ReplaceAll(name, `\b`, "")
	name = strings.ReplaceAll(name, `.?`, " ")
	name = strings.ReplaceAll(name, `\.`, " ")
	name = strings.ReplaceAll(name, `\s+`, " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return "Pattern"
	}
	return name
}

// uniqueRuleName suffixes a name until it is free, so migrating two patterns
// that reduce to the same label does not produce two rules a user cannot tell
// apart. Names taken are recorded as they are handed out.
func uniqueRuleName(base string, taken map[string]bool) string {
	candidate := base
	for i := 2; taken[strings.ToLower(candidate)]; i++ {
		candidate = fmt.Sprintf("%s (%d)", base, i)
	}
	taken[strings.ToLower(candidate)] = true
	return candidate
}
