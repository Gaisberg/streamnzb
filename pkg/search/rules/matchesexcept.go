package rules

import (
	"fmt"
	"regexp"
	"sync"

	jhinrules "github.com/dreulavelle/jhin/rules"
)

// matchesExcept is the lookaround-equivalent text operator.
//
// RE2 has no lookahead or lookbehind, and for most of what upstream regex
// lists use them for the answer is to stop using a regex: `\bDV\b(?!.*HDR10)`
// is `dolbyVision and not hdrFallback`. What that answer does not cover is a
// lookaround protecting a token against a collision with a *longer token that
// contains it*, because the protection is positional and `and not` is not:
//
//	releaseName matches "(?i)\bMA\b" and not (releaseName matches "(?i)DTS-HD MA")
//
// reads the whole name twice and so rejects Show.S01E01.MA.WEB-DL.DTS-HD.MA.5.1,
// where the service tag and the audio codec are both present and only one of
// them is the MA the rule meant. `(?<!DTS-HD.)MA` gets that right, and this is
// how to say it here:
//
//	matchesExcept(releaseName, "(?i)\bMA\b", "(?i)DTS-HD.MA")
//
// It holds when pattern matches at least once at an offset no match of except
// covers. Because except is matched against the name rather than anchored to
// one side of the token, the same call replaces a lookbehind, a lookahead, or
// both at once — the colliding text is written out whole instead of being
// split around the token.
const matchesExceptName = "matchesExcept"

func registerMatchesExcept(reg *jhinrules.Registry) {
	reg.Func(matchesExceptName,
		[]jhinrules.Type{jhinrules.Str, jhinrules.Str, jhinrules.Str},
		jhinrules.Bool,
		func(_ jhinrules.Facts, args []jhinrules.Value) (jhinrules.Value, error) {
			pattern, err := compileRulePattern(args[1].Str())
			if err != nil {
				return jhinrules.Value{}, fmt.Errorf("%s: pattern: %w", matchesExceptName, err)
			}
			except, err := compileRulePattern(args[2].Str())
			if err != nil {
				return jhinrules.Value{}, fmt.Errorf("%s: except: %w", matchesExceptName, err)
			}
			return jhinrules.BoolOf(matchesUncovered(args[0].Str(), pattern, except)), nil
		})
}

// matchesUncovered reports whether pattern matches text somewhere except
// overlaps nothing of.
func matchesUncovered(text string, pattern, except *regexp.Regexp) bool {
	hits := pattern.FindAllStringIndex(text, -1)
	if len(hits) == 0 {
		return false
	}
	covers := except.FindAllStringIndex(text, -1)
	if len(covers) == 0 {
		return true
	}
	for _, hit := range hits {
		covered := false
		for _, cover := range covers {
			// Half-open ranges touching end to start are neighbours, not an
			// overlap: "MA" directly after a match of except is exactly the
			// token a lookbehind would have kept. The same test reads a
			// zero-width hit — `\b`, `$` — as covered only where it sits
			// strictly inside a range, which is the answer a lookaround
			// anchored at that position would give.
			if hit[0] < cover[1] && cover[0] < hit[1] {
				covered = true
				break
			}
		}
		if !covered {
			return true
		}
	}
	return false
}

type cachedRulePattern struct {
	re  *regexp.Regexp
	err error
}

// rulePatternCache holds the regexes handed to matchesExcept, keyed by their
// source. Unlike `matches`, whose right side must be a literal jhin compiles
// once at check time, a function argument is an ordinary expression evaluated
// per release — without this the same pattern would be recompiled for every
// result in every search.
//
// The keys are rule text, so a stored profile contributes a fixed handful.
// The preview endpoint compiles whatever is being typed, which is why the map
// is emptied once it grows past a size no real profile reaches.
var rulePatternCache sync.Map

// rulePatternCacheLimit is generous next to any profile and small next to the
// cost of holding an editing session's abandoned drafts forever.
const rulePatternCacheLimit = 512

func compileRulePattern(src string) (*regexp.Regexp, error) {
	if v, ok := rulePatternCache.Load(src); ok {
		cached := v.(*cachedRulePattern)
		return cached.re, cached.err
	}
	re, err := regexp.Compile(src)
	if err != nil {
		re = nil
	}
	if countRulePatterns() >= rulePatternCacheLimit {
		rulePatternCache.Range(func(k, _ any) bool {
			rulePatternCache.Delete(k)
			return true
		})
	}
	rulePatternCache.Store(src, &cachedRulePattern{re: re, err: err})
	return re, err
}

func countRulePatterns() int {
	n := 0
	rulePatternCache.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
