package rules

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	jhinrules "github.com/dreulavelle/jhin/rules"
)

// Lookaround, without a lookaround.
//
// Conditions are the answer to most of what PCRE lookahead is used for in a
// release-name regex: `\bDV\b(?!.*HDR10)` is `dolbyVision and not hdrFallback`,
// and a whole-name exclusion is `matches A and not matches B`. What neither
// says is the third form, the one an imported classification list is built
// out of: this token, except where it is part of that phrase. `\bMA\b` is a
// streaming service until it is the tail of `DTS-HD MA`, and `\bMAX\b` is one
// until it is the tail of `HBO Max` — and a name can carry both, so
// `matches "\bMA\b" and not matches "DTS-HD MA"` is not it. That condition
// reads the whole name and rejects the standalone token because something
// else in the name happens to look like the phrase.
//
// matchesExcept is per occurrence instead of per name: the pattern holds when
// it matches somewhere the exclusion does not cover. Positive lookaround needs
// nothing — a rule only asks whether the pattern matched, never what it
// consumed, so `A(?=B)` is `AB` written out — which leaves this as the only
// form RE2 cannot express on its own.
const matchesExceptFunc = "matchesExcept"

// patternArgs names, per function, which of its arguments are regular
// expressions. The compile-time check reads it to know what to validate, and
// adding a second pattern function means adding it here rather than teaching
// the scanner about it.
var patternArgs = map[string][]int{
	matchesExceptFunc: {1, 2},
}

// registerPatternFuncs adds the pattern vocabulary to a registry. Both the
// scoring and the prune registries are built from the same function, so a rule
// in either pass can call these.
func registerPatternFuncs(reg *jhinrules.Registry) {
	// No tier: the answer depends only on the text it is handed, so whether a
	// rule calling it can be judged is decided by the field that text came
	// from — releaseName always, probed.hdr only for a release that was
	// opened — exactly as it would be for a bare `matches`.
	reg.Func(matchesExceptFunc,
		[]jhinrules.Type{jhinrules.Str, jhinrules.Str, jhinrules.Str}, jhinrules.Bool,
		func(_ jhinrules.Facts, args []jhinrules.Value) (jhinrules.Value, error) {
			pattern, err := compilePattern(args[1].Str())
			if err != nil {
				return jhinrules.Value{}, err
			}
			except, err := compilePattern(args[2].Str())
			if err != nil {
				return jhinrules.Value{}, err
			}
			return jhinrules.BoolOf(matchesOutside(args[0].Str(), pattern, except)), nil
		})
}

// matchesOutside reports whether pattern matches text somewhere the except
// pattern covers no part of. It is the lookaround the RE2 engine cannot be
// asked for, decided after the fact instead: find every occurrence of both,
// and accept the first occurrence of pattern that no exclusion overlaps.
func matchesOutside(text string, pattern, except *regexp.Regexp) bool {
	found := pattern.FindAllStringIndex(text, -1)
	if len(found) == 0 {
		return false
	}
	excluded := except.FindAllStringIndex(text, -1)
	for _, at := range found {
		if !covered(at, excluded) {
			return true
		}
	}
	return false
}

// covered reports whether any region overlaps the match. An empty region
// covers nothing: a pattern that can match the empty string — `x?`, or the
// empty pattern itself — otherwise excludes everything by matching between
// every pair of characters, which is the opposite of "except this phrase".
func covered(at []int, regions [][]int) bool {
	for _, region := range regions {
		if region[1] > region[0] && at[0] < region[1] && region[0] < at[1] {
			return true
		}
	}
	return false
}

// compiledPatterns keeps the regexes the pattern functions build, keyed by the
// pattern text. jhin compiles the right-hand side of `matches` once, when the
// profile is saved, because it insists the pattern is written out; a function
// is handed values instead, so the compile happens on first use and the result
// is kept for every release after it.
var compiledPatterns sync.Map // string -> *compiledPattern

// patternCacheSize caps that cache. Patterns come from rule text and a profile
// holds tens of rules, so the cap is never reached by ordinary use — it is
// there because a condition may build a pattern from the release itself, and a
// cache keyed by something a search result controls has to have a ceiling.
const patternCacheSize = 512

var patternCacheCount atomic.Int64

type compiledPattern struct {
	re  *regexp.Regexp
	err error
}

// compilePattern compiles a pattern, or returns what it failed with last time.
// A failure is cached alongside a success: an unparseable pattern is a rule
// the user has to fix, and retrying the compile for every release in every
// search until they do buys nothing.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if cached, ok := compiledPatterns.Load(pattern); ok {
		entry := cached.(*compiledPattern)
		return entry.re, entry.err
	}
	entry := &compiledPattern{}
	if entry.re, entry.err = regexp.Compile(pattern); entry.err != nil {
		entry.err = fmt.Errorf("bad pattern %q: %w", pattern, entry.err)
	}
	if patternCacheCount.Load() < patternCacheSize {
		if _, loaded := compiledPatterns.LoadOrStore(pattern, entry); !loaded {
			patternCacheCount.Add(1)
		}
	}
	return entry.re, entry.err
}

// checkPatterns reports a pattern a function is called with that will not
// compile, so a broken one is refused when the profile is saved rather than
// reported as a rule that quietly could not be judged. jhin checks the pattern
// of a `matches` operator itself; it cannot check a function's arguments,
// which are values it only sees while a search runs.
//
// Only a pattern written out is checked. One built from an attribute has no
// value yet, and is left to fail at evaluation on the release that produced it.
func checkPatterns(reg *jhinrules.Registry, src string) error {
	for _, literal := range patternLiterals(src) {
		// Compiled by jhin rather than by regexp directly, so the pattern the
		// check sees is the one evaluation will see: unescaping a rule string
		// is jhin's lexer's job, and a second implementation of it here would
		// eventually disagree with the first.
		_, err := jhinrules.Compile(reg, []jhinrules.Rule{{
			Name:   "pattern",
			When:   "releaseName matches " + literal,
			Action: jhinrules.ActionDefine,
		}})
		if err == nil {
			continue
		}
		var ruleErr *jhinrules.Error
		if errors.As(err, &ruleErr) {
			err = ruleErr.Err
		}
		return err
	}
	return nil
}

// patternLiterals finds every written-out pattern a pattern function is called
// with in an expression. It is a scan and not a parse for the same reason
// namesRef is: jhin owns the grammar, and this needs to recognise one call
// shape in it, not re-implement it. Anything it cannot make sense of — an
// unbalanced call, a computed argument — it passes over, leaving jhin to
// report the syntax and the evaluation to report the rest.
func patternLiterals(src string) []string {
	var out []string
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"' || c == '\'' || c == '`':
			// A call named inside a string is text, not a call.
			i = skipStringLiteral(src, i)
		case identStart(c):
			start := i
			for i < len(src) && identByte(src[i]) {
				i++
			}
			which, ok := patternArgs[src[start:i]]
			if !ok {
				continue
			}
			open := i
			for open < len(src) && isSpace(src[open]) {
				open++
			}
			if open >= len(src) || src[open] != '(' {
				continue
			}
			args, end := callArgs(src, open)
			i = end
			for _, idx := range which {
				if idx < len(args) && isStringLiteral(args[idx]) {
					out = append(out, args[idx])
				}
			}
		default:
			i++
		}
	}
	return out
}

// callArgs splits the arguments of the call whose open parenthesis is at open,
// returning each as written and the index just past the closing parenthesis.
// Nested calls and strings are stepped over whole, so a comma inside either
// does not split an argument. An unbalanced call yields nothing.
func callArgs(src string, open int) ([]string, int) {
	var args []string
	depth, start := 0, open+1
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '"', '\'', '`':
			i = skipStringLiteral(src, i) - 1
		case '(', '[':
			depth++
		case ')', ']':
			if depth--; depth == 0 {
				return append(args, strings.TrimSpace(src[start:i])), i + 1
			}
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(src[start:i]))
				start = i + 1
			}
		}
	}
	return nil, len(src)
}

// isStringLiteral reports whether an argument is one string and nothing else,
// which is what makes its pattern knowable before a search runs.
func isStringLiteral(arg string) bool {
	if len(arg) < 2 || !isQuote(arg[0]) {
		return false
	}
	return skipStringLiteral(arg, 0) == len(arg)
}

// skipStringLiteral returns the index just past the string starting at i, or
// the end of src for one that is never closed. Escaping follows jhin's lexer:
// a backslash escapes the next byte in a quoted string and means nothing in a
// raw one.
func skipStringLiteral(src string, i int) int {
	quote := src[i]
	for i++; i < len(src); i++ {
		if src[i] == '\\' && quote != '`' {
			i++
			continue
		}
		if src[i] == quote {
			return i + 1
		}
	}
	return len(src)
}

func isQuote(c byte) bool { return c == '"' || c == '\'' || c == '`' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// identStart reports whether a byte can open an attribute or function name,
// which identByte alone cannot say: it accepts the digits and dots that may
// only follow one.
func identStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
