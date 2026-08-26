package rules

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	exprparser "github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/diag"
	"streamnzb/pkg/search/triage"
)

// Rule is one compiled condition and what it does when it holds.
type Rule struct {
	Name   string
	Scope  string
	Action string
	Points int
	// Count is how many matching releases a limit rule keeps.
	Count int

	program *vm.Program
	// groupProgram splits a limit rule's cap into buckets. Nil caps the
	// matching releases as one set.
	groupProgram *vm.Program
	// needsProbe, needsAvail, needsIndexer and needsSeadex record that the
	// condition reads a tier the release may not have. Such a rule is skipped
	// rather than evaluated against zero values — see Evaluate.
	needsProbe   bool
	needsAvail   bool
	needsIndexer bool
	needsSeadex  bool
	// aggIndices are the set-wide result-set conditions this rule reads. A
	// rule whose aggregate could not be judged is skipped the same way a rule
	// reading a missing tier is.
	aggIndices []int
}

// Set is a profile's rules, compiled once and reused for every request.
type Set struct {
	rules []Rule
	// aggs are the result-set conditions lifted out of the rules, shared
	// across them and computed once per request — see ComputeAggregates.
	aggs []aggregate
}

// Len reports how many enabled rules the set holds.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.rules)
}

// NeedsSeadex reports whether any rule reads the seadex tier, so a caller can
// skip the SeaDex lookup for profiles that never ask about it.
func (s *Set) NeedsSeadex() bool {
	if s == nil {
		return false
	}
	for i := range s.rules {
		if s.rules[i].needsSeadex {
			return true
		}
	}
	return false
}

// Error is a rule that would not compile, named so the editor can point at the
// row rather than at the profile.
type Error struct {
	Rule string
	Err  error
}

func (e *Error) Error() string { return "rule " + e.Rule + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Compile turns a profile's rule configs into an evaluable set. Disabled rules
// are dropped here rather than skipped later, so a broken rule that is turned
// off does not block a save.
//
// Compilation is strict: the condition must type-check against the environment
// and must yield a boolean. A rule that cannot compile fails the whole call,
// because a profile that silently drops one of its rules filters differently
// than the user configured and gives no sign of it.
func Compile(cfgs []config.RuleConfig) (*Set, error) {
	if len(cfgs) == 0 {
		return nil, nil
	}
	set := &Set{}
	aggBySource := map[string]int{}
	// References are resolved against every rule, disabled ones included, so
	// that switching a rule off changes what a reference to it means rather
	// than breaking the rules that refer to it.
	refs := newRefExpander(cfgs)
	for i, rc := range cfgs {
		if !rc.IsEnabled() {
			continue
		}
		name := ruleLabel(rc)
		if strings.TrimSpace(rc.When) == "" {
			return nil, &Error{Rule: name, Err: fmt.Errorf("condition is empty")}
		}
		if rc.EffectiveAction() == config.RuleActionLimit && rc.Count < 1 {
			return nil, &Error{Rule: name, Err: fmt.Errorf("a limit rule has to keep at least one release")}
		}
		// matched("Other rule") is inlined before anything else looks at the
		// condition, so a reference works wherever a condition does and the
		// tiers it reads are the tiers of everything it pulls in.
		when, err := refs.condition(i)
		if err != nil {
			return nil, &Error{Rule: name, Err: err}
		}
		// A define rule is a named condition kept for other rules to reference
		// and nothing else. It is validated as strictly as any rule — a broken
		// definition must fail the save whether or not something references it
		// yet — but it never joins the set: no release is judged by it, so it
		// pays nothing out, rejects nothing, and shows up nowhere.
		if rc.EffectiveAction() == config.RuleActionDefine {
			if strings.TrimSpace(rc.GroupBy) != "" {
				return nil, &Error{Rule: name, Err: fmt.Errorf("only a limit rule can group by %s", strings.TrimSpace(rc.GroupBy))}
			}
			if err := validateCondition(when); err != nil {
				return nil, &Error{Rule: name, Err: err}
			}
			continue
		}
		// Result-set calls are lifted out first: the rule is compiled against
		// their precomputed values, and the inner conditions become their own
		// per-release programs. Identical conditions share one aggregate, so
		// two rules asking about the same thing count it once.
		var ruleAggs []int
		when, err = rewriteAggregates(when, func(inner string) (int, error) {
			idx, ok := aggBySource[inner]
			if !ok {
				program, err := expr.Compile(inner, expr.Env(Env{}), expr.AsBool())
				if err != nil {
					return 0, err
				}
				tiers, err := tiersUsed(inner)
				if err != nil {
					return 0, err
				}
				idx = len(set.aggs)
				set.aggs = append(set.aggs, aggregate{program: program, tiers: tiers, source: inner})
				aggBySource[inner] = idx
			}
			for _, have := range ruleAggs {
				if have == idx {
					return idx, nil
				}
			}
			ruleAggs = append(ruleAggs, idx)
			return idx, nil
		})
		if err != nil {
			return nil, &Error{Rule: name, Err: err}
		}
		program, err := expr.Compile(when, expr.Env(Env{}), expr.AsBool())
		if err != nil {
			return nil, &Error{Rule: name, Err: err}
		}
		// Tiers are judged on the rewritten condition: a rule asking whether
		// the set holds a probed release does not itself need this release to
		// be probed.
		tiers, err := tiersUsed(when)
		if err != nil {
			return nil, &Error{Rule: name, Err: err}
		}
		groupBy, err := refs.expand(strings.TrimSpace(rc.GroupBy))
		if err != nil {
			return nil, &Error{Rule: name, Err: fmt.Errorf("group by: %w", err)}
		}
		groupProgram, groupTiers, err := compileGroupBy(rc, groupBy)
		if err != nil {
			return nil, &Error{Rule: name, Err: err}
		}
		// A grouped cap is judged by its condition and its grouping together,
		// so it reads whichever tiers either half touches: grouping by
		// probed.height has to skip an unprobed release for the same reason a
		// condition reading it does.
		tiers = tiers.merge(groupTiers)
		set.rules = append(set.rules, Rule{
			Name:         name,
			Scope:        rc.EffectiveScope(),
			Action:       rc.EffectiveAction(),
			Points:       rc.Points,
			Count:        rc.Count,
			program:      program,
			groupProgram: groupProgram,
			needsProbe:   tiers.probe,
			needsAvail:   tiers.avail,
			needsIndexer: tiers.indexer,
			needsSeadex:  tiers.seadex,
			aggIndices:   ruleAggs,
		})
	}
	if len(set.rules) == 0 {
		return nil, nil
	}
	return set, nil
}

// validateCondition compiles a condition and discards the result — the strict
// half of what Compile does for an acting rule, applied to one that exists
// only to be referenced. Result-set calls are rewritten against a throwaway
// index so the program type-checks; nothing is registered anywhere, so a
// definition nobody references never costs a per-request aggregate.
func validateCondition(when string) error {
	rewritten, err := rewriteAggregates(when, func(inner string) (int, error) {
		if _, err := expr.Compile(inner, expr.Env(Env{}), expr.AsBool()); err != nil {
			return 0, err
		}
		return 0, nil
	})
	if err != nil {
		return err
	}
	_, err = expr.Compile(rewritten, expr.Env(Env{}), expr.AsBool())
	return err
}

// compileGroupBy compiles a limit rule's grouping expression and reports which
// attribute tiers it reads. A rule without one groups nothing and compiles to
// nil, which is every rule written before grouping existed.
//
// The expression is not required to yield any particular type — resolution is
// a string, year an int, hdr a list — because what a bucket needs is an
// identity, not a value, and every type has one once it is written out. It is
// checked against the same environment a condition sees, so a grouping naming
// an attribute that does not exist is caught at save time rather than turning
// every release into its own bucket at search time.
//
// Grouping is refused on anything but a limit rule. There is nothing sensible
// for a score or reject rule to do with it, and accepting it silently would
// leave a user who set it on the wrong rule looking at a cap that never
// buckets, with nothing anywhere saying why.
func compileGroupBy(rc config.RuleConfig, groupBy string) (*vm.Program, tierUse, error) {
	if groupBy == "" {
		return nil, tierUse{}, nil
	}
	if rc.EffectiveAction() != config.RuleActionLimit {
		// Named as the user wrote it, not as it expanded: a grouping that
		// pulled in another rule reads back as pages of inlined condition.
		return nil, tierUse{}, fmt.Errorf("only a limit rule can group by %s", strings.TrimSpace(rc.GroupBy))
	}
	program, err := expr.Compile(groupBy, expr.Env(Env{}))
	if err != nil {
		return nil, tierUse{}, fmt.Errorf("group by: %w", err)
	}
	tiers, err := tiersUsed(groupBy)
	if err != nil {
		return nil, tierUse{}, fmt.Errorf("group by: %w", err)
	}
	return program, tiers, nil
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

// LimitMatch is one cap a release counts against.
type LimitMatch struct {
	Name  string
	Count int
	// Group is the bucket the release falls in, for a cap that groups. Count
	// is kept per bucket, so two releases counting against the same rule with
	// different groups are not competing. Empty for an ungrouped cap, which
	// puts every match in one bucket.
	Group string
}

// Evaluate runs the rules that apply to this content kind.
//
// A rule is skipped, not failed, when it reads a tier the release has nothing
// in: probed.* on a release that was never opened, avail.* on one nobody has
// reported. Evaluating those against zero values would let a single rule like
// `probed.height < 1080` reject every fresh indexer hit, which is the opposite
// of what the user asked for. This is the same fail-open contract the NZB
// attribute limits already keep, made explicit because here it is the common
// case rather than the exception.
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
	kind = strings.ToLower(strings.TrimSpace(kind))
	for i := range s.rules {
		r := &s.rules[i]
		if r.Scope != config.RuleScopeAll && r.Scope != kind {
			continue
		}
		if r.needsProbe && !env.Verified {
			out.Skipped = append(out.Skipped, r.Name+": release has not been probed")
			continue
		}
		if r.needsAvail && !env.Avail.Known {
			out.Skipped = append(out.Skipped, r.Name+": availability is unknown")
			continue
		}
		if r.needsSeadex && !env.Seadex.Checked {
			out.Skipped = append(out.Skipped, r.Name+": no SeaDex lookup for this request")
			continue
		}
		// Size, age and grab count come from the NZB, which a bare release
		// name does not have. In a real search every release carries one, so
		// this only ever fires in the preview — where evaluating against zeros
		// would show a rule doing something it will not do live, or nothing
		// when it will.
		if r.needsIndexer && !env.HasIndexerData {
			out.Skipped = append(out.Skipped, r.Name+": needs size, age or grabs, which a release name does not carry")
			continue
		}
		if msg := s.aggregateSkip(r, env); msg != "" {
			out.Skipped = append(out.Skipped, r.Name+": "+msg)
			continue
		}
		result, err := expr.Run(r.program, env)
		if err != nil {
			// A compiled, type-checked rule that fails at run time has hit
			// data no test covered. Skipping matches the fail-open rule
			// above: an inconclusive check never removes a release.
			continue
		}
		matched, _ := result.(bool)
		if !matched {
			continue
		}
		switch r.Action {
		case config.RuleActionReject:
			out.Rejections = append(out.Rejections, diag.RuleRejectionPrefix+r.Name)
		case config.RuleActionLimit:
			group, ok := r.groupOf(env)
			if !ok {
				// A grouping that fails at run time cannot say which bucket
				// this release belongs in, and a cap that does not know that
				// cannot count it. Dropping the match rather than guessing at
				// a bucket keeps the same promise the tier checks above make:
				// a rule that cannot be judged never removes a release.
				continue
			}
			out.Limits = append(out.Limits, LimitMatch{Name: r.Name, Count: r.Count, Group: group})
		default:
			out.Points += r.Points
			out.Matched = append(out.Matched, triage.RuleMatch{Name: r.Name, Score: r.Points})
		}
	}
	return out
}

// groupOf is the bucket this release falls in for a grouped cap, and whether
// the grouping could be answered at all. An ungrouped rule reports the empty
// bucket, which every one of its matches shares.
//
// The value is written out rather than compared as itself because a bucket
// only ever needs to tell two releases apart, and because the expression may
// yield any type: "2160p", 2020, or the whole of hdr as a list. A grouping on
// a list therefore buckets by the entire list, which is rarely what a user
// means but is at least what they wrote.
func (r *Rule) groupOf(env Env) (string, bool) {
	if r.groupProgram == nil {
		return "", true
	}
	value, err := expr.Run(r.groupProgram, env)
	if err != nil {
		return "", false
	}
	return fmt.Sprint(value), true
}

// indexerAttributes are the attributes that come from the NZB rather than from
// the release name. A condition reading any of them cannot be judged without
// one.
var indexerAttributes = map[string]bool{
	"sizeGB": true, "sizePerEpisodeGB": true, "ageDays": true,
	"grabs": true, "passworded": true, "indexer": true,
	"querySource": true, "library": true,
}

type tierUse struct {
	probe   bool
	avail   bool
	indexer bool
	seadex  bool
}

// merge is the union of two tier uses, for a rule whose condition and grouping
// read different tiers. Every tier has to be carried: this runs for every rule,
// grouped or not, so a tier dropped here is a tier no rule ever reports.
func (t tierUse) merge(other tierUse) tierUse {
	return tierUse{
		probe:   t.probe || other.probe,
		avail:   t.avail || other.avail,
		indexer: t.indexer || other.indexer,
		seadex:  t.seadex || other.seadex,
	}
}

// tiersUsed reports which optional attribute tiers a condition reads. It walks
// the parsed expression rather than searching the text so that a pattern
// mentioning "probed." inside a string literal is not mistaken for an
// attribute reference.
func tiersUsed(source string) (tierUse, error) {
	tree, err := exprparser.Parse(source)
	if err != nil {
		return tierUse{}, err
	}
	v := &identVisitor{}
	ast.Walk(&tree.Node, v)
	return v.use, nil
}

type identVisitor struct {
	use tierUse
}

func (v *identVisitor) Visit(node *ast.Node) {
	ident, ok := (*node).(*ast.IdentifierNode)
	if !ok {
		return
	}
	switch {
	case ident.Value == "probed":
		v.use.probe = true
	case ident.Value == "avail":
		v.use.avail = true
	case ident.Value == "seadex":
		v.use.seadex = true
	case indexerAttributes[ident.Value]:
		v.use.indexer = true
	}
}
