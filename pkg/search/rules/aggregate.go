package rules

import (
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/conf"
	exprparser "github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

// A result-set condition — count(...), exists(...), any(...) or none(...) —
// judges the whole result set where the rest of a rule judges one release.
// The inner condition is lifted out at compile time and evaluated once per
// request against every release; the rule itself is rewritten to read the
// precomputed value, so evaluation stays a single pass whatever the rule says
// about the set.

// aggsIdent is the environment field the rewrite reads. The name is reserved:
// a condition that mentions it directly would alias whatever aggregate happens
// to share the index, so compilation refuses it.
const aggsIdent = "__aggs"

// aggregateNames are the result-set functions a condition may call. any is an
// alias for exists. count and none double as expr's collection builtins; the
// one-argument form is ours, the two-argument form over a list field is left
// alone.
var aggregateNames = map[string]bool{
	"count":  true,
	"exists": true,
	"any":    true,
	"none":   true,
}

// aggregate is one lifted result-set condition, deduplicated across the set's
// rules by source text.
type aggregate struct {
	program *vm.Program
	// tiers are the optional attribute tiers the inner condition reads. A
	// release missing one of them cannot be judged and does not count; a set
	// where no release carries them leaves the aggregate unknown.
	tiers tierUse
	// source is the inner condition as its parsed form prints, kept so a
	// report can say which condition a number belongs to.
	source string
}

// AggregateState is one request's computed result-set values, shared by every
// release in the set.
type AggregateState struct {
	values []int
	known  []bool
}

// Inject hands the computed values to one release's environment. Safe on a
// nil state, which is what a set without aggregates computes.
func (st *AggregateState) Inject(env *Env) {
	if st == nil {
		return
	}
	env.Aggs = st.values
	env.AggsKnown = st.known
}

// HasAggregates reports whether any rule in the set reads the result set.
func (s *Set) HasAggregates() bool {
	return s != nil && len(s.aggs) > 0
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
// releases. It runs before the per-release pass, so the counts are the same
// for every release and no rule can change them by rejecting.
//
// A release missing a tier the condition reads is not judged, the same
// fail-open contract Evaluate keeps: it neither matches nor rules the
// aggregate out. Only when no release at all carries the tier is the
// aggregate unknown, and rules reading it are skipped rather than fed a zero
// that would fire them.
func (s *Set) ComputeAggregates(envs []Env) *AggregateState {
	st, _ := s.computeAggregates(envs, false)
	return st
}

// ReportAggregates is ComputeAggregates plus the answer to "which releases
// made it come out that way": one report per condition, naming the releases it
// counted. The capture is what the preview shows; the live path calls
// ComputeAggregates and pays nothing for it.
func (s *Set) ReportAggregates(envs []Env) (*AggregateState, []AggregateReport) {
	return s.computeAggregates(envs, true)
}

func (s *Set) computeAggregates(envs []Env, report bool) (*AggregateState, []AggregateReport) {
	if !s.HasAggregates() {
		return nil, nil
	}
	st := &AggregateState{
		values: make([]int, len(s.aggs)),
		known:  make([]bool, len(s.aggs)),
	}
	var reports []AggregateReport
	if report {
		reports = make([]AggregateReport, len(s.aggs))
	}
	for k := range s.aggs {
		a := &s.aggs[k]
		for i := range envs {
			env := &envs[i]
			if a.tiers.probe && !env.Verified {
				continue
			}
			if a.tiers.avail && !env.Avail.Known {
				continue
			}
			if a.tiers.indexer && !env.HasIndexerData {
				continue
			}
			st.known[k] = true
			result, err := expr.Run(a.program, *env)
			if err != nil {
				// Same contract as Evaluate: a run-time failure is
				// inconclusive, and an inconclusive check never counts.
				continue
			}
			if matched, _ := result.(bool); matched {
				st.values[k]++
				if report {
					reports[k].Matched = append(reports[k].Matched, env.ReleaseName)
				}
			}
		}
		if report {
			reports[k].Source = a.source
			reports[k].Known = st.known[k]
			reports[k].Count = st.values[k]
		}
	}
	return st, reports
}

// aggregateSkip reports why a rule's result-set conditions cannot be judged
// against this environment, or "" when they can.
func (s *Set) aggregateSkip(r *Rule, env Env) string {
	for _, idx := range r.aggIndices {
		if idx < len(env.AggsKnown) && env.AggsKnown[idx] {
			continue
		}
		tiers := s.aggs[idx].tiers
		switch {
		case tiers.probe:
			return "no release in this set has been probed"
		case tiers.avail:
			return "no release in this set has an availability record"
		case tiers.indexer:
			return "needs size, age or grabs, which no release in this set carries"
		default:
			return "the result set has not been judged"
		}
	}
	return ""
}

// aggAlloc registers one lifted condition and returns its set-wide index.
type aggAlloc func(inner string) (int, error)

// parseCondition parses a rule condition.
//
// It parses twice at most. expr's own parser owns any, none and count as
// collection builtins and refuses their one-argument form outright, so when
// the plain parse fails the source is re-parsed with those names overridden
// into ordinary calls. A condition that only uses the collection forms parses
// plainly and is left exactly as written. Source that fails both ways reports
// the plain parser's error, which is the one the compiler downstream will
// give.
func parseCondition(source string) (*exprparser.Tree, error) {
	tree, err := exprparser.Parse(source)
	if err == nil {
		return tree, nil
	}
	cfg := conf.CreateNew()
	for name := range aggregateNames {
		cfg.Functions[name] = &builtin.Function{Name: name}
	}
	retry, retryErr := exprparser.ParseWithConfig(source, cfg)
	if retryErr != nil {
		return nil, err
	}
	return retry, nil
}

// rewriteAggregates lifts the result-set calls out of a condition. It returns
// the condition with each call replaced by a read of its precomputed value,
// or the source untouched when there is nothing to lift — including when it
// does not parse, so the compiler downstream reports the canonical error.
func rewriteAggregates(source string, alloc aggAlloc) (string, error) {
	tree, err := parseCondition(source)
	if err != nil {
		return source, nil
	}
	v := &aggPatcher{alloc: alloc}
	ast.Walk(&tree.Node, v)
	if v.err != nil {
		return "", v.err
	}
	if !v.found {
		return source, nil
	}
	return tree.Node.String(), nil
}

// aggPatcher replaces result-set calls with reads of their computed values.
// The walk is post-order, so an aggregate's argument is inspected before the
// call is patched and the identifiers the patch introduces are never
// revisited — any __aggs the visitor sees was written by the user.
type aggPatcher struct {
	alloc aggAlloc
	found bool
	err   error
}

func (v *aggPatcher) Visit(node *ast.Node) {
	if v.err != nil {
		return
	}
	switch n := (*node).(type) {
	case *ast.IdentifierNode:
		if n.Value == aggsIdent {
			v.err = fmt.Errorf("%s is reserved", aggsIdent)
		}
	case *ast.CallNode:
		ident, ok := n.Callee.(*ast.IdentifierNode)
		if !ok || !aggregateNames[ident.Value] {
			return
		}
		v.patch(node, ident.Value, n.Arguments)
	case *ast.BuiltinNode:
		if !aggregateNames[n.Name] {
			return
		}
		// Two arguments is expr's own collection form over a list field,
		// e.g. any(hdr, # == "DV"); it needs no lifting.
		if len(n.Arguments) >= 2 {
			return
		}
		v.patch(node, n.Name, n.Arguments)
	}
}

func (v *aggPatcher) patch(node *ast.Node, name string, args []ast.Node) {
	if len(args) != 1 {
		v.err = fmt.Errorf("%s takes exactly one condition", name)
		return
	}
	arg := args[0]
	// count([proper, repack]) is expr's builtin counting trues in a literal
	// list, not a condition over the set.
	if _, isList := arg.(*ast.ArrayNode); isList {
		return
	}
	if refersToAggs(arg) {
		v.err = fmt.Errorf("%s: result-set conditions cannot nest", name)
		return
	}
	inner := arg.String()
	idx, err := v.alloc(inner)
	if err != nil {
		v.err = fmt.Errorf("in %s(%s): %w", name, inner, err)
		return
	}
	ref := func() ast.Node {
		return &ast.MemberNode{
			Node:     &ast.IdentifierNode{Value: aggsIdent},
			Property: &ast.IntegerNode{Value: idx},
		}
	}
	var repl ast.Node
	switch name {
	case "exists", "any":
		repl = &ast.BinaryNode{Operator: ">", Left: ref(), Right: &ast.IntegerNode{Value: 0}}
	case "none":
		repl = &ast.BinaryNode{Operator: "==", Left: ref(), Right: &ast.IntegerNode{Value: 0}}
	default: // count
		repl = ref()
	}
	ast.Patch(node, repl)
	v.found = true
}

// refersToAggs reports whether a subtree reads the aggregate values — either
// a result-set call the post-order walk already patched, or a hand-written
// __aggs.
func refersToAggs(node ast.Node) bool {
	v := &aggsRefVisitor{}
	ast.Walk(&node, v)
	return v.found
}

type aggsRefVisitor struct {
	found bool
}

func (v *aggsRefVisitor) Visit(node *ast.Node) {
	if ident, ok := (*node).(*ast.IdentifierNode); ok && ident.Value == aggsIdent {
		v.found = true
	}
}
