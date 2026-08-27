package rules

import (
	"testing"

	"streamnzb/pkg/core/config"
)

func TestNormalizeConditionEscapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no backslash", `group == "FraMeSToR"`, `group == "FraMeSToR"`},
		{"raw plus", `releaseName matches "SubsPlus\+?"`, `releaseName matches "SubsPlus\\+?"`},
		{"raw class", `releaseName matches "\d+\.\d"`, `releaseName matches "\\d+\\.\\d"`},
		{"word boundary", `releaseName matches "(?i)\bIMAX\b"`, `releaseName matches "(?i)\\bIMAX\\b"`},
		{"already escaped stays", `releaseName matches "SubsPlus\\+?"`, `releaseName matches "SubsPlus\\+?"`},
		{"string escapes stay", `releaseName matches "\x41\n\t"`, `releaseName matches "\x41\n\t"`},
		{"escaped quote stays", `releaseName matches "say \"hi\""`, `releaseName matches "say \"hi\""`},
		{"single quotes", `releaseName matches '\+' and group == "\+"`, `releaseName matches '\\+' and group == "\\+"`},
		{"other quote doubled", `releaseName matches "\'"`, `releaseName matches "\\'"`},
		{"backslash outside strings kept", `\ oops`, `\ oops`},
		{"backtick raw untouched", "releaseName matches `\\+ \" unclosed` and x == \"\\+\"", "releaseName matches `\\+ \" unclosed` and x == \"\\\\+\""},
		{"trailing backslash kept", `x == "\`, `x == "\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeConditionEscapes(tt.in)
			if got != tt.want {
				t.Errorf("normalizeConditionEscapes(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Idempotent: inlined references are re-serialized and recompiled,
			// so a second pass must not double anything again.
			if again := normalizeConditionEscapes(got); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// The reason the pass exists: conditions written in raw regex notation — the
// way community lists and generated define libraries write them — compile and
// mean what the regex says. \+ is a literal plus rather than a refused save,
// and \b is a word boundary rather than a backspace that never matches.
func TestRawRegexNotationInConditions(t *testing.T) {
	set, err := Compile([]config.RuleConfig{
		{Name: "SubsPlus", When: `releaseName matches "SubsPlus\+"`, Points: 10},
		{Name: "IMAX", When: `releaseName matches "(?i)\bIMAX\b"`, Points: 5},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := set.Evaluate(Env{ReleaseName: "Movie IMAX 2020 SubsPlus+ x264", Kind: "movie"}, "movie"); got.Points != 15 {
		t.Errorf("Points = %d, want 15 — raw \\+ and \\b must match as regex notation", got.Points)
	}
	if got := set.Evaluate(Env{ReleaseName: "Movie IMAXED SubsPlus x264", Kind: "movie"}, "movie"); got.Points != 0 {
		t.Errorf("Points = %d, want 0 — \\b is a boundary and \\+ requires the plus", got.Points)
	}
}

// A reference to a raw-notation rule survives the inline-and-recompile round
// trip: the referenced condition is normalized once, re-serialized with
// proper escaping, and never doubled again.
func TestRawRegexNotationThroughReferences(t *testing.T) {
	library := []config.RuleConfig{
		{Name: "SubsPlus tier", When: `releaseName matches "SubsPlus\+?"`, Action: config.RuleActionDefine},
	}
	set, err := Compile([]config.RuleConfig{
		{Name: "Uses it", When: `matched("SubsPlus tier")`, Points: 7},
	}, library...)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := set.Evaluate(Env{ReleaseName: "Show SubsPlus+ 1080p", Kind: "movie"}, "movie"); got.Points != 7 {
		t.Errorf("Points = %d, want 7 via the library reference", got.Points)
	}
}
