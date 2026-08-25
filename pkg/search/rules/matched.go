package rules

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr/ast"

	"streamnzb/pkg/core/config"
)

// A rule reference — matched("Movies UHD BluRay T1") — reuses the
// classification another rule already expresses, so a tier list of release
// groups is written once and referred to from everywhere else that cares
// about it.
//
// References are resolved at compile time by inlining the referenced rule's
// condition, which is why one works anywhere a condition does: on its own,
// inside a result-set call, inside a limit's grouping. Nothing is evaluated
// twice and rule order still does not matter.
//
// What a reference asks is whether the other rule's *condition* holds for this
// release, not what its action did with it. For a score or reject rule those
// are the same thing. For a limit rule they are not — whether a release
// survives a cap is decided after every rule has run and the set is in final
// score order — so matched() on a cap means "this release counts against it",
// which is the only part of a cap that is knowable here.

// matchedIdent is the function a condition calls to reference another rule.
const matchedIdent = "matched"

// maxExpandedCondition caps how large a condition may grow once its references
// are inlined. Cycles are refused outright, but a reference is a copy rather
// than a call, so a chain where each rule names the one below it twice doubles
// in size at every step. The cap turns a config that would otherwise hang
// compilation into an error naming the rule.
const maxExpandedCondition = 64 << 10

// refExpander resolves rule references against a profile's rules. It is built
// from the full rule list, including the disabled ones, so that switching a
// rule off changes what a reference to it evaluates to rather than breaking
// the profile that refers to it.
type refExpander struct {
	cfgs []config.RuleConfig
	// byName maps a rule name — trimmed and lowercased, the same way the
	// editor keeps names unique — to the rules carrying it.
	byName map[string][]int
	// expanded memoizes each rule's fully inlined condition, so a tier list
	// referenced from five rules is expanded once.
	expanded map[int]string
	// stack is the chain of rules currently being expanded, which is what
	// turns a cycle into an error naming the rules in it.
	stack []int
}

func newRefExpander(cfgs []config.RuleConfig) *refExpander {
	e := &refExpander{
		cfgs:     cfgs,
		byName:   make(map[string][]int, len(cfgs)),
		expanded: make(map[int]string, len(cfgs)),
	}
	for i := range cfgs {
		key := strings.ToLower(strings.TrimSpace(cfgs[i].Name))
		if key == "" {
			continue
		}
		e.byName[key] = append(e.byName[key], i)
	}
	return e
}

// condition is one rule's condition with every reference in it inlined.
func (e *refExpander) condition(i int) (string, error) {
	if done, ok := e.expanded[i]; ok {
		return done, nil
	}
	for at, seen := range e.stack {
		if seen == i {
			return "", fmt.Errorf("rules refer to each other in a circle: %s", e.circle(at, i))
		}
	}
	when := strings.TrimSpace(e.cfgs[i].When)
	if when == "" {
		return "", fmt.Errorf("rule %q has no condition", ruleLabel(e.cfgs[i]))
	}
	e.stack = append(e.stack, i)
	out, err := e.expand(when)
	e.stack = e.stack[:len(e.stack)-1]
	if err != nil {
		return "", err
	}
	e.expanded[i] = out
	return out, nil
}

// circle names the rules in a reference cycle, from where it closes back round
// to itself.
func (e *refExpander) circle(at, back int) string {
	var names []string
	for _, i := range e.stack[at:] {
		names = append(names, ruleLabel(e.cfgs[i]))
	}
	names = append(names, ruleLabel(e.cfgs[back]))
	return strings.Join(names, " -> ")
}

// expand replaces every reference in a condition with the referenced rule's
// own condition. Source that does not parse is returned untouched, so the
// compiler downstream reports the canonical syntax error rather than this
// pass reporting a worse one.
func (e *refExpander) expand(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return source, nil
	}
	tree, err := parseCondition(source)
	if err != nil {
		return source, nil
	}
	v := &refPatcher{exp: e}
	ast.Walk(&tree.Node, v)
	if v.err != nil {
		return "", v.err
	}
	if !v.found {
		return source, nil
	}
	out := tree.Node.String()
	if len(out) > maxExpandedCondition {
		return "", fmt.Errorf("this condition expands to %d characters of rule references; simplify it or reference fewer rules", len(out))
	}
	return out, nil
}

// reference is the subtree a matched(name) call becomes.
func (e *refExpander) reference(name string) (ast.Node, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, fmt.Errorf("%s needs the name of another rule", matchedIdent)
	}
	found := e.byName[key]
	switch {
	case len(found) == 0:
		return nil, fmt.Errorf("no rule is named %q", name)
	case len(found) > 1:
		// Two rules under one name have no answer to which one was meant, and
		// picking either silently would make the reference mean whichever the
		// user happened to write first.
		return nil, fmt.Errorf("more than one rule is named %q", name)
	}
	i := found[0]
	rc := e.cfgs[i]
	// A rule that is switched off classifies nothing. Reading a disabled rule
	// as "never matches" is both the honest answer and what keeps the promise
	// that a broken rule which is turned off does not block a save: its
	// condition is never looked at.
	if !rc.IsEnabled() {
		return &ast.BoolNode{Value: false}, nil
	}
	inner, err := e.condition(i)
	if err != nil {
		return nil, err
	}
	tree, err := parseCondition(inner)
	if err != nil {
		return nil, fmt.Errorf("rule %q: %w", ruleLabel(rc), err)
	}
	node := tree.Node
	// A scoped rule does not run outside its content kind, so neither does a
	// reference to it. Without this a reject rule applying to everything would
	// read a movie-only tier list as holding for series too.
	if scope := rc.EffectiveScope(); scope != config.RuleScopeAll {
		node = &ast.BinaryNode{
			Operator: "and",
			Left: &ast.BinaryNode{
				Operator: "==",
				Left:     &ast.IdentifierNode{Value: "kind"},
				Right:    &ast.StringNode{Value: scope},
			},
			Right: node,
		}
	}
	return node, nil
}

// refPatcher replaces matched(name) calls with the referenced condition. The
// walk is post-order and a patched subtree is not revisited, which is safe
// because what it patches in is already fully expanded.
type refPatcher struct {
	exp   *refExpander
	found bool
	err   error
}

func (v *refPatcher) Visit(node *ast.Node) {
	if v.err != nil {
		return
	}
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	ident, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || ident.Value != matchedIdent {
		return
	}
	if len(call.Arguments) != 1 {
		v.err = fmt.Errorf("%s takes exactly one rule name", matchedIdent)
		return
	}
	name, ok := call.Arguments[0].(*ast.StringNode)
	if !ok {
		// The name has to be a literal: it is resolved once at compile time,
		// which is what lets a reference be a copy of the other condition
		// rather than a lookup on every release.
		v.err = fmt.Errorf(`%s takes a rule name in quotes, as in %s("Movies UHD BluRay T1")`, matchedIdent, matchedIdent)
		return
	}
	repl, err := v.exp.reference(name.Value)
	if err != nil {
		v.err = err
		return
	}
	ast.Patch(node, repl)
	v.found = true
}

// ruleLabel is how a rule is named in an error, matching what Compile calls an
// unnamed one.
func ruleLabel(rc config.RuleConfig) string {
	if name := strings.TrimSpace(rc.Name); name != "" {
		return name
	}
	return "unnamed"
}
