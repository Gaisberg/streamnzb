package rules_test

import (
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/rules"
)

// The case from the request: MA is a streaming service and also the tail of
// DTS-HD MA, and a release can carry both. `and not` reads the whole name
// twice and cannot tell those apart; matchesExcept can, because it asks where
// the match landed.
func TestMatchesExceptSeparatesATokenFromItsCollision(t *testing.T) {
	positional := compile(t, config.RuleConfig{
		Name:   "MA service",
		When:   `matchesExcept(releaseName, "(?i)\\bMA\\b", "(?i)DTS-HD.MA")`,
		Points: 100,
	})
	naive := compile(t, config.RuleConfig{
		Name:   "MA service",
		When:   `releaseName matches "(?i)\\bMA\\b" and not (releaseName matches "(?i)DTS-HD.MA")`,
		Points: 100,
	})

	tests := []struct {
		title           string
		want            bool
		wantNaiveAgrees bool
	}{
		// The service tag alone: both forms match.
		{"Show.S01E01.1080p.MA.WEB-DL.DDP5.1-GRP", true, true},
		// The audio codec alone: both forms decline.
		{"Show.S01E01.1080p.AMZN.WEB-DL.DTS-HD.MA.5.1-GRP", false, true},
		// Both at once. The MA the rule meant is there, so the rule holds —
		// and this is the release the whole-name form gets wrong.
		{"Show.S01E01.1080p.MA.WEB-DL.DTS-HD.MA.5.1-GRP", true, false},
		// Neither: nothing to match in the first place.
		{"Show.S01E01.1080p.AMZN.WEB-DL.DDP5.1-GRP", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			env := envFor(tt.title, nil)
			if got := positional.Evaluate(env, "movie").Points > 0; got != tt.want {
				t.Errorf("matchesExcept matched = %v, want %v", got, tt.want)
			}
			naiveMatched := naive.Evaluate(env, "movie").Points > 0
			if (naiveMatched == tt.want) != tt.wantNaiveAgrees {
				t.Errorf("and-not form matched = %v, want agreement with %v to be %v",
					naiveMatched, tt.want, tt.wantNaiveAgrees)
			}
		})
	}
}

// The second example from the request, and the one a lookbehind would have
// written: MAX is a service, Max is the tail of HBO Max.
func TestMatchesExceptHandlesAPrefixCollision(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "MAX service",
		When:   `matchesExcept(releaseName, "(?i)\\bMAX\\b", "(?i)HBO.?MAX")`,
		Action: config.RuleActionReject,
	})

	tests := []struct {
		title      string
		wantReject bool
	}{
		{"Movie.2024.2160p.MAX.WEB-DL.DDP5.1.Atmos-GRP", true},
		{"Movie.2024.2160p.HBO.Max.WEB-DL.DDP5.1.Atmos-GRP", false},
		{"Movie.2024.2160p.HBOMax.WEB-DL-GRP", false},
		{"Movie.2024.2160p.AMZN.WEB-DL-GRP", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := len(set.Evaluate(envFor(tt.title, nil), "movie").Rejections) > 0
			if got != tt.wantReject {
				t.Errorf("rejected = %v, want %v", got, tt.wantReject)
			}
		})
	}
}

// An except pattern that matches nothing leaves the match untouched, and one
// that matches everything removes every hit. The two ends of the range are
// what say the coverage test is coverage and not a second `matches`.
func TestMatchesExceptCoverageBounds(t *testing.T) {
	const title = "Movie.2024.2160p.MAX.WEB-DL-GRP"

	tests := []struct {
		name string
		when string
		want bool
	}{
		{"except matches nothing", `matchesExcept(releaseName, "(?i)\\bMAX\\b", "(?i)NOPE")`, true},
		{"except swallows the name", `matchesExcept(releaseName, "(?i)\\bMAX\\b", ".*")`, false},
		{"except is the pattern", `matchesExcept(releaseName, "(?i)\\bMAX\\b", "(?i)\\bMAX\\b")`, false},
		{"except abuts the match", `matchesExcept(releaseName, "(?i)MAX", "(?i)2160p\\.")`, true},
		{"pattern matches nothing", `matchesExcept(releaseName, "(?i)\\bIMAX\\b", "(?i)NOPE")`, false},
		// A zero-width hit is a position, not an extent, so it is judged by
		// where it sits rather than by what it spans.
		{"zero-width hit outside except", `matchesExcept(releaseName, "$", "(?i)\\bMAX\\b")`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := compile(t, config.RuleConfig{Name: tt.name, When: tt.when, Points: 10})
			if got := set.Evaluate(envFor(title, nil), "movie").Points > 0; got != tt.want {
				t.Errorf("matched = %v, want %v", got, tt.want)
			}
		})
	}
}

// matchesExcept reads any text attribute, not only releaseName, and takes any
// string expression — the patterns are arguments, not a literal the checker
// pins down.
func TestMatchesExceptReadsAnyTextAttribute(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "Group",
		When:   `matchesExcept(lower(group), "framestor", "notframestor")`,
		Points: 50,
	})
	if got := set.Evaluate(envFor("Movie.2024.2160p.BluRay.REMUX-FraMeSToR", nil), "movie"); got.Points != 50 {
		t.Errorf("Points = %d, want 50", got.Points)
	}
}

// The checker knows the function's shape, so a misspelt or misused call fails
// when the profile is saved rather than when a search runs.
func TestMatchesExceptCompileErrors(t *testing.T) {
	tests := []struct {
		name string
		when string
		want string
	}{
		{"too few arguments", `matchesExcept(releaseName, "MA")`, "argument"},
		{"wrong argument type", `matchesExcept(releaseName, "MA", 3)`, "argument 3"},
		{"misspelt", `matchesExcpet(releaseName, "MA", "DTS-HD MA")`, "unknown function"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rules.Compile([]config.RuleConfig{{Name: tt.name, When: tt.when, Points: 1}})
			if err == nil {
				t.Fatalf("compiled %q, want an error", tt.when)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A pattern that is not a valid regex cannot be caught at compile time — it is
// a function argument, not the literal `matches` pins down — so it has to
// surface at evaluation instead of silently never matching. jhin reports a
// condition that errors as a skip, which is what the editor shows beside the
// rule.
func TestMatchesExceptReportsABadPatternAsASkip(t *testing.T) {
	for _, when := range []string{
		`matchesExcept(releaseName, "(?i)\\bMA\\b(", "x")`,
		`matchesExcept(releaseName, "(?i)\\bMA\\b", "x(")`,
	} {
		set := compile(t, config.RuleConfig{Name: "Bad", When: when, Points: 10})
		got := set.Evaluate(envFor("Show.S01E01.MA.WEB-DL-GRP", nil), "movie")
		if got.Points != 0 {
			t.Errorf("%s: scored %d on an uncompilable pattern", when, got.Points)
		}
		if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "matchesExcept") {
			t.Errorf("%s: Skipped = %+v, want one reason naming matchesExcept", when, got.Skipped)
		}
	}
}
