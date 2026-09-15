package rules

import (
	"sort"

	jhinrules "github.com/dreulavelle/jhin/rules"

	"streamnzb/pkg/core/config"
)

// The machine-readable account of what a rule may say in this build.
//
// It exists for the compatibility question a client cannot otherwise answer:
// a template generator or a third-party profile editor has to know whether
// `probed.subtitleLanguages` and `matchesExcept` are understood here before it
// writes a profile that names them. Guessing from the release version does not
// work — the vocabulary grows in patch releases — and hardcoding a copy means
// the copy drifts.
//
// Fields and tiers are read from the registries the live pipeline compiles
// against, so they cannot disagree with what a rule is actually checked
// against. Functions and the result-set forms are declared here because jhin
// keeps them in unexported maps with no accessor; TestVocabularyFunctions
// compiles a call to every one of them, so a declaration that goes stale
// fails the build rather than misleading a client.

// FieldInfo is one attribute a condition may name.
type FieldInfo struct {
	Name string `json:"name"`
	// Type is the expression language's type as jhin renders it: "bool",
	// "num", "string", "list<string>" or "list<num>".
	Type string `json:"type"`
	// Tier is the confidence group that decides when this attribute is
	// answerable, empty for one every release carries. A rule naming a field
	// whose tier is absent is skipped rather than judged.
	Tier string `json:"tier,omitempty"`
	// Values is the complete set this attribute can hold, when it is closed.
	// Absent means open — any string is a well-formed comparison.
	Values []string `json:"values,omitempty"`
	// PruneOnly marks an attribute only a prune rule may read, because it
	// does not exist until scoring is done.
	PruneOnly bool `json:"prune_only,omitempty"`
}

// TierInfo is one confidence group and what its absence means. Description
// completes "needs …" in the reason a skipped rule reports.
type TierInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// FuncInfo is one function or result-set form the expression language offers.
type FuncInfo struct {
	Name string `json:"name"`
	// Signature is the call written out, e.g. `min(num, …) -> num`.
	Signature   string `json:"signature"`
	Description string `json:"description"`
	// Kind is "function" for a plain call, "collection" for the form that
	// runs a condition per element of a list with `#` standing for it,
	// "aggregate" for the form that asks about the whole result set, and
	// "reference" for matched(), which is inlined rather than called. A name
	// can be more than one: count is overloaded across collection and
	// aggregate, so it appears once per kind.
	Kind string `json:"kind"`
}

// Vocabulary is everything a profile's rules may name in this build.
type Vocabulary struct {
	Fields    []FieldInfo `json:"fields"`
	Tiers     []TierInfo  `json:"tiers"`
	Functions []FuncInfo  `json:"functions"`
	// Actions are what a rule may do when its condition holds.
	Actions []string `json:"actions"`
	// Scopes are the content kinds a rule can be narrowed to, "all" first.
	Scopes []string `json:"scopes"`
}

// Describe reports the vocabulary. Fields are sorted by name and the whole
// result is freshly allocated, so a caller may not mutate a shared copy.
func Describe() Vocabulary {
	return Vocabulary{
		Fields:    describeFields(),
		Tiers:     append([]TierInfo(nil), tiers...),
		Functions: append([]FuncInfo(nil), functions...),
		Actions: []string{
			config.RuleActionScore, config.RuleActionReject, config.RuleActionLimit,
			config.RuleActionPrune, config.RuleActionDefine,
		},
		Scopes: describeScopes(),
	}
}

// describeFields reads both registries. postRegistry is the superset — the
// scoring vocabulary plus what only exists after scoring — so it supplies the
// list, and absence from the scoring registry is what marks a field
// prune-only.
func describeFields() []FieldInfo {
	names := postRegistry.Fields()
	out := make([]FieldInfo, 0, len(names))
	for _, name := range names {
		f, ok := postRegistry.Lookup(name)
		if !ok {
			continue
		}
		_, scorable := registry.Lookup(name)
		out = append(out, FieldInfo{
			Name:      name,
			Type:      f.Type.String(),
			Tier:      f.Tier,
			Values:    append([]string(nil), f.Values...),
			PruneOnly: !scorable,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// describeScopes is "all" followed by the ranking content kinds, taken from
// the per-kind limit list so the two cannot name different kinds.
func describeScopes() []string {
	out := make([]string, 0, len(config.LimitKinds))
	out = append(out, config.RuleScopeAll)
	for _, kind := range config.LimitKinds {
		if kind == config.LimitKindDefault {
			continue
		}
		out = append(out, kind)
	}
	return out
}

// functions is the call vocabulary: jhin's builtins, its result-set and
// collection forms, and the one function StreamNZB registers. jhin holds the
// first three in unexported maps, so they are written out here and verified
// by compiling each one.
var functions = []FuncInfo{
	{Name: "len", Signature: "len(list | string) -> num", Kind: "function",
		Description: "how many elements a list has, or characters a string has"},
	{Name: "lower", Signature: "lower(string) -> string", Kind: "function",
		Description: "the text in lower case"},
	{Name: "upper", Signature: "upper(string) -> string", Kind: "function",
		Description: "the text in upper case"},
	{Name: "trim", Signature: "trim(string) -> string", Kind: "function",
		Description: "the text without leading or trailing whitespace"},
	{Name: "abs", Signature: "abs(num) -> num", Kind: "function",
		Description: "the number without its sign"},
	{Name: "floor", Signature: "floor(num) -> num", Kind: "function",
		Description: "the number rounded down"},
	{Name: "ceil", Signature: "ceil(num) -> num", Kind: "function",
		Description: "the number rounded up"},
	{Name: "round", Signature: "round(num) -> num", Kind: "function",
		Description: "the number rounded to the nearest whole"},
	{Name: "min", Signature: "min(num, ...) -> num", Kind: "function",
		Description: "the smallest of its arguments"},
	{Name: "max", Signature: "max(num, ...) -> num", Kind: "function",
		Description: "the largest of its arguments"},
	{Name: "string", Signature: "string(any) -> string", Kind: "function",
		Description: "any value as text"},
	{Name: "num", Signature: "num(string) -> num", Kind: "function",
		Description: "text as a number, zero when it does not parse"},
	{Name: matchesExceptName, Signature: matchesExceptName + "(string, string, string) -> bool", Kind: "function",
		Description: "the first pattern matches somewhere the second does not cover — the positional form of " +
			"\"A but not the A inside B\", which RE2 cannot express"},

	{Name: "count", Signature: "count(list, cond) -> num", Kind: "collection",
		Description: "how many elements satisfy the condition, with # standing for the element"},
	{Name: "any", Signature: "any(list, cond) -> bool", Kind: "collection",
		Description: "at least one element satisfies the condition"},
	{Name: "all", Signature: "all(list, cond) -> bool", Kind: "collection",
		Description: "every element satisfies the condition"},
	{Name: "none", Signature: "none(list, cond) -> bool", Kind: "collection",
		Description: "no element satisfies the condition"},

	{Name: "count", Signature: "count(cond) -> num", Kind: "aggregate",
		Description: "how many releases in the result set satisfy the condition"},
	{Name: "exists", Signature: "exists(cond) -> bool", Kind: "aggregate",
		Description: "some release in the result set satisfies the condition"},
	{Name: "any", Signature: "any(cond) -> bool", Kind: "aggregate",
		Description: "some release in the result set satisfies the condition"},
	{Name: "none", Signature: "none(cond) -> bool", Kind: "aggregate",
		Description: "no release in the result set satisfies the condition"},
	// matched is neither a call nor a question about the set: it is resolved
	// by inlining the named rule's condition before the checker runs, which
	// is why a define rule costs nothing at evaluation time.
	{Name: "matched", Signature: "matched(\"rule name\") -> bool", Kind: "reference",
		Description: "another rule's condition holds for this release, so a shared definition has one home"},
}

// compileProbe compiles one condition against the prune vocabulary, which is
// the superset. It backs the test that keeps functions honest.
func compileProbe(when string) error {
	_, err := jhinrules.Compile(postRegistry, []jhinrules.Rule{
		{Name: "probe", When: when, Action: jhinrules.ActionReject},
	})
	return err
}
