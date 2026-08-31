package rules

import (
	"errors"
	"strconv"
	"strings"

	jhinrules "github.com/dreulavelle/jhin/rules"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/triage"
)

// Tier names. A field belongs to a tier, and a rule reading a tier the
// release carries nothing in is skipped rather than judged against zero
// values — jhin's engine enforces the contract, these name the groups.
const (
	tierMeasured = "measured"
	tierAvail    = "avail"
	tierSeadex   = "seadex"
	tierIndexer  = "indexer"
)

// registry declares StreamNZB's rule vocabulary: jhin's core release-name
// attributes plus every fact source the application adds. The descriptions
// are user-facing — they become the "skipped: needs …" reasons.
//
// Names are permanent API: renaming a field breaks every stored rule that
// reads it.
var registry = buildRegistry()

func buildRegistry() *jhinrules.Registry {
	reg := jhinrules.Core()
	reg.Tier(tierMeasured, "a probed file, which this release is not")
	reg.Tier(tierAvail, "an availability record, which this release has none of")
	reg.Tier(tierSeadex, "a SeaDex lookup, which this request did not run")
	reg.Tier(tierIndexer, "size, age or grabs, which a release name does not carry")

	// Verified reports whether the merged bare attributes (resolution, codec,
	// hdr, bitDepth) came from the file rather than from its name. It is
	// always answerable, so it carries no tier.
	reg.Field("verified", jhinrules.Bool, "")

	// parsed.* is the release name's own account of the merged attributes,
	// for rules that must not accept a probe's word for it (or vice versa).
	reg.Namespace("parsed", "").
		Str("resolution").Str("codec").StrList("hdr").Num("bitDepth").
		Str("title").Bool("dolbyVision").Bool("hdrFallback")

	// Reported by the indexer. A bare release name has none of these, which
	// is why they share a tier: the preview builds releases from titles alone
	// and must skip rules about them rather than judge against zeros.
	reg.Field("sizeGB", jhinrules.Num, tierIndexer)
	reg.Field("sizePerEpisodeGB", jhinrules.Num, tierIndexer)
	reg.Field("ageDays", jhinrules.Num, tierIndexer)
	reg.Field("grabs", jhinrules.Num, tierIndexer)
	reg.Field("passworded", jhinrules.Bool, tierIndexer)
	reg.Field("indexer", jhinrules.Str, tierIndexer)
	reg.Field("querySource", jhinrules.Str, tierIndexer)
	reg.Field("library", jhinrules.Bool, tierIndexer)

	// Community: the availability database and SeaDex. Separate tiers because
	// their presence differs — avail is known per release, seadex per request.
	reg.Namespace("avail", tierAvail).
		Str("status").Bool("known").Bool("onMyBackbone").
		Num("checkedDaysAgo").Str("compression")
	reg.Namespace("seadex", tierSeadex).
		Bool("known").Bool("best").Bool("alternative")

	// Measured: what ffprobe found in the file itself.
	reg.Namespace("probed", tierMeasured).
		Str("videoCodec").Str("audioCodec").Num("width").Num("height").
		Str("profile").Num("bitDepth").Str("hdr").Bool("dolbyVision").
		Bool("hasHDRFallback").Str("dynamicRange")

	// Request context, the same for every release in one result set. title
	// shadows jhin's core field: here it is the requested title, and the
	// release name's own is parsed.title.
	reg.Field("kind", jhinrules.Str, "")
	reg.Field("isAnime", jhinrules.Bool, "")
	reg.Field("season", jhinrules.Num, "")
	reg.Field("episode", jhinrules.Num, "")

	if err := reg.Err(); err != nil {
		// A registration that fails is a programming error in this file, not
		// a condition any profile can cause.
		panic(err)
	}
	return reg
}

// Set is a profile's rules, compiled once and reused for every request.
type Set struct {
	eng *jhinrules.Engine
}

// Len reports how many enabled rules the set holds.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return s.eng.Len()
}

// NeedsSeadex reports whether any rule reads the seadex tier, so a caller can
// skip the SeaDex lookup for profiles that never ask about it.
func (s *Set) NeedsSeadex() bool {
	return s != nil && s.eng.ReadsTier(tierSeadex)
}

// HasAggregates reports whether any rule in the set reads the result set.
func (s *Set) HasAggregates() bool {
	return s != nil && s.eng.HasAggregates()
}

// Error is a rule that would not compile, named so the editor can point at the
// row rather than at the profile.
type Error struct {
	Rule string
	Err  error
}

func (e *Error) Error() string { return "rule " + e.Rule + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Compile turns a profile's rule configs into an evaluable set. Compilation,
// reference inlining, tier tracking and validation are jhin's; this converts
// the config schema and keeps the registry.
//
// library carries the define rules of the config's shared define libraries.
// They exist only to be referenced: matched() resolves against them after the
// profile's own rules — a profile rule under the same name shadows the
// library's — and they never join the set. A library is validated by
// compiling it on its own, which is also what keeps it self-contained.
func Compile(cfgs []config.RuleConfig, library ...config.RuleConfig) (*Set, error) {
	if len(cfgs) == 0 {
		return nil, nil
	}
	eng, err := jhinrules.Compile(registry, toJhinRules(cfgs), toJhinRules(library)...)
	if err != nil {
		var jerr *jhinrules.Error
		if errors.As(err, &jerr) {
			return nil, &Error{Rule: jerr.Rule, Err: jerr.Err}
		}
		return nil, err
	}
	if eng == nil {
		return nil, nil
	}
	return &Set{eng: eng}, nil
}

// toJhinRules converts the config schema to jhin's. The fields map one to
// one; the differences are shape, not meaning — jhin scores by expression
// where the config carries flat points, and scopes a rule by list where the
// config has one kind or "all".
func toJhinRules(cfgs []config.RuleConfig) []jhinrules.Rule {
	out := make([]jhinrules.Rule, 0, len(cfgs))
	for _, rc := range cfgs {
		r := jhinrules.Rule{
			Name:    rc.Name,
			When:    rc.When,
			Action:  rc.EffectiveAction(),
			Count:   rc.Count,
			GroupBy: rc.GroupBy,
			Enabled: rc.Enabled,
		}
		if r.Action == jhinrules.ActionScore {
			r.Score = strconv.Itoa(rc.Points)
		}
		if scope := rc.EffectiveScope(); scope != config.RuleScopeAll {
			r.Scope = []string{scope}
		}
		out = append(out, r)
	}
	return out
}

// Outcome is what a set's rules did to one release.
type Outcome struct {
	// Points is the sum of every score rule that matched.
	Points int
	// Matched names the score rules that paid out, in configuration order.
	Matched []triage.RuleMatch
	// Rejections names the reject rules that fired, phrased for the same
	// rejection lists jhin and the NZB limits write into.
	Rejections []string
	// Skipped names the rules that did not run because the release carries
	// nothing in the tier they read. It exists so the bench can say "this rule
	// needs a probed file" instead of leaving the user to wonder why a
	// correct-looking rule never fires.
	Skipped []string
	// Limits are the caps this release falls under. Whether it survives them
	// cannot be decided here — that needs the whole result set in final score
	// order — so the match is recorded and the counting happens once, after
	// sorting.
	Limits []LimitMatch
}

// LimitMatch is one cap a release counts against. Count is kept per bucket,
// so two releases counting against the same rule with different groups are
// not competing. Group is empty for an ungrouped cap, which puts every match
// in one bucket.
type LimitMatch = jhinrules.LimitMatch

// AggregateState is one request's computed result-set values, shared by every
// release in the set.
type AggregateState struct {
	st *jhinrules.AggregateState
}

// Inject hands the computed values to one release's environment. Safe on a
// nil state, which is what a set without aggregates computes.
func (st *AggregateState) Inject(env *Env) {
	if st == nil {
		return
	}
	env.aggs = st.st
}

// AggregateReport is one result-set condition as a request computed it: the
// inner condition, whether any release could be judged, how many satisfied it,
// and which ones by name. It exists for the preview — a rule built on
// exists()/none()/count() is otherwise debugged by guessing which releases the
// number came from. See ReportAggregates.
type AggregateReport struct {
	// Source is the inner condition as its parsed form prints, so two
	// spellings of the same condition read identically and are counted once.
	Source string `json:"source"`
	// Known is false when no release in the set carries the tiers the
	// condition reads, which skips every rule depending on it.
	Known bool `json:"known"`
	// Count is how many releases satisfied the condition — the value count()
	// reads, and what exists() and none() judge against zero.
	Count int `json:"count"`
	// Matched names the releases counted, in set order.
	Matched []string `json:"matched,omitempty"`
}

// ComputeAggregates evaluates every lifted condition once against the given
// releases, before the per-release pass, so the counts are the same for every
// release and no rule can change them by rejecting. kind is the request's
// content kind, matched against the scope of any rule an inner condition
// references.
func (s *Set) ComputeAggregates(envs []Env, kind string) *AggregateState {
	if !s.HasAggregates() {
		return nil
	}
	return &AggregateState{st: s.eng.ComputeAggregates(factsOf(envs), kind)}
}

// ReportAggregates is ComputeAggregates plus the answer to "which releases
// made it come out that way": one report per condition, naming the releases it
// counted. The capture is what the preview shows; the live path calls
// ComputeAggregates and pays nothing for it.
func (s *Set) ReportAggregates(envs []Env, kind string) (*AggregateState, []AggregateReport) {
	if !s.HasAggregates() {
		return nil, nil
	}
	facts := factsOf(envs)
	st := s.eng.ComputeAggregates(facts, kind)
	base := s.eng.Aggregates(st)
	reports := make([]AggregateReport, len(base))
	for i, r := range base {
		reports[i] = AggregateReport{Source: r.Source, Known: r.Known, Count: r.Count}
	}
	// The engine reports totals, not who was counted, so each release is
	// asked again alone: a single-release set answers 1 exactly when this
	// release was counted in the full set, and stays unanswered when the
	// release misses a tier the condition reads — the same rule that kept it
	// out of the full count. The report order is the engine's aggregate
	// order both times, so the indexes line up.
	for i := range envs {
		single := s.eng.Aggregates(s.eng.ComputeAggregates(facts[i:i+1], kind))
		for k := range single {
			if single[k].Known && single[k].Count > 0 {
				reports[k].Matched = append(reports[k].Matched, envs[i].ReleaseName)
			}
		}
	}
	return &AggregateState{st: st}, reports
}

// factsOf adapts one environment per release. The pointer matters: Env
// answers the Facts interface by pointer, and copying it per lookup would be
// waste.
func factsOf(envs []Env) []jhinrules.Facts {
	out := make([]jhinrules.Facts, len(envs))
	for i := range envs {
		out[i] = &envs[i]
	}
	return out
}

// Evaluate runs the rules that apply to this content kind.
//
// A rule is skipped, not failed, when it reads a tier the release has nothing
// in: probed.* on a release that was never opened, avail.* on one nobody has
// reported. Evaluating those against zero values would let a single rule like
// `probed.height < 1080` reject every fresh indexer hit, which is the opposite
// of what the user asked for. jhin's engine keeps that contract; this converts
// its outcome into the shapes the pipeline records.
//
// A set with result-set conditions expects the caller to have computed them —
// ComputeAggregates over the whole set, Inject into each environment — before
// evaluating. Rules reading an aggregate nobody could judge are skipped under
// the same contract.
func (s *Set) Evaluate(env Env, kind string) Outcome {
	var out Outcome
	if s == nil {
		return out
	}
	res := s.eng.Evaluate(&env, kind, env.aggs)
	out.Points = res.Points
	out.Limits = res.Limits
	for _, m := range res.Matched {
		out.Matched = append(out.Matched, triage.RuleMatch{Name: m.Name, Score: m.Score})
	}
	for _, rej := range res.Rejections {
		// jhin writes "rule:Name"; the pipeline's rejection lists read
		// "rule: Name" everywhere else, so the prefix is restated.
		out.Rejections = append(out.Rejections,
			diag.RuleRejectionPrefix+strings.TrimPrefix(rej, jhinrules.RejectionPrefix))
	}
	for _, sk := range res.Skipped {
		out.Skipped = append(out.Skipped, sk.Name+": "+sk.Reason)
	}
	return out
}
