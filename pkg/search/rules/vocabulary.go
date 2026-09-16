package rules

import (
	"sort"
	"strings"

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
	// Tier is set for a call that depends on a confidence group the way
	// naming one of its fields would, and empty for one always answerable.
	Tier string `json:"tier,omitempty"`
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
		Tiers:     describeTiers(),
		Functions: describeFunctions(),
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

// describeTiers reports the confidence groups from the registry rather than
// from the slice they were declared in, so the report states what the engine
// was actually given.
func describeTiers() []TierInfo {
	reported := postRegistry.TierDetails()
	out := make([]TierInfo, 0, len(reported))
	for _, t := range reported {
		if t.Name == "" {
			continue // the always-present tier, which needs no description
		}
		out = append(out, TierInfo{Name: t.Name, Description: t.Description})
	}
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

// funcDescriptions is the human half of the call vocabulary, keyed by name and
// form. The set of calls is read from the registry — it cannot drift from what
// compiles — so only the prose lives here, and TestDescribeFunctions fails if
// the registry reports a call this map has no words for.
var funcDescriptions = map[string]string{
	"function/len":    "how many elements a list has, or characters a string has",
	"function/lower":  "the text in lower case",
	"function/upper":  "the text in upper case",
	"function/trim":   "the text without leading or trailing whitespace",
	"function/abs":    "the number without its sign",
	"function/floor":  "the number rounded down",
	"function/ceil":   "the number rounded up",
	"function/round":  "the number rounded to the nearest whole",
	"function/min":    "the smallest of its arguments",
	"function/max":    "the largest of its arguments",
	"function/string": "any value as text",
	"function/num":    "text as a number, zero when it does not parse",
	"function/" + matchesExceptName: "the first pattern matches somewhere the second does not cover — the positional form of " +
		"\"A but not the A inside B\", which RE2 cannot express",

	"collection/count": "how many elements satisfy the condition, with # standing for the element",
	"collection/any":   "at least one element satisfies the condition",
	"collection/all":   "every element satisfies the condition",
	"collection/none":  "no element satisfies the condition",

	"aggregate/count":  "how many releases in the result set satisfy the condition",
	"aggregate/exists": "some release in the result set satisfies the condition",
	"aggregate/any":    "some release in the result set satisfies the condition",
	"aggregate/none":   "no release in the result set satisfies the condition",

	"reference/matched": "another rule's condition holds for this release, so a shared definition has one home",
}

// matchedReference is the one call the registry does not report, because it is
// not a call: matched() names a rule and is resolved by inlining that rule's
// condition before the checker ever runs, which is why a define rule costs
// nothing at evaluation time.
var matchedReference = FuncInfo{
	Name:      "matched",
	Signature: `matched("rule name") -> bool`,
	Kind:      "reference",
}

// describeFunctions reads the call vocabulary from the prune registry, which
// is the superset, and dresses it with the prose above.
func describeFunctions() []FuncInfo {
	reported := postRegistry.Funcs()
	out := make([]FuncInfo, 0, len(reported)+1)
	for _, fn := range reported {
		info := FuncInfo{
			Name:      fn.Name,
			Signature: renderSignature(fn),
			Kind:      fn.Form,
			Tier:      fn.Tier,
		}
		info.Description = funcDescriptions[info.Kind+"/"+info.Name]
		out = append(out, info)
	}
	ref := matchedReference
	ref.Description = funcDescriptions["reference/matched"]
	return append(out, ref)
}

// renderSignature writes a call out the way a rule author types it. A
// collection form is shown with its placeholder rather than as a bare list and
// bool, because `any(hdr, # == "DV")` is the thing being described and
// `any(list, bool)` is not recognisable as it.
func renderSignature(fn jhinrules.FuncInfo) string {
	if fn.Form == jhinrules.FormCollection {
		return fn.Name + "(list, cond) -> " + fn.Result.String()
	}
	if fn.Form == jhinrules.FormAggregate {
		return fn.Name + "(cond) -> " + fn.Result.String()
	}
	params := make([]string, 0, len(fn.Params))
	for _, p := range fn.Params {
		params = append(params, paramName(p))
	}
	if fn.Variadic {
		params = append(params, "...")
	}
	return fn.Name + "(" + strings.Join(params, ", ") + ") -> " + fn.Result.String()
}

// paramName renders one parameter type. jhin marks "takes anything" with an
// invalid kind, which prints as "invalid" and would read as a defect.
func paramName(t jhinrules.Type) string {
	switch {
	case t.K == jhinrules.KInvalid:
		return "any"
	case t.K == jhinrules.KList && t.Elem == jhinrules.KInvalid:
		return "list"
	default:
		return t.String()
	}
}
