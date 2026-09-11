package rules_test

import (
	"errors"
	"strings"
	"testing"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/search/rules"
)

// The case a whole-name exclusion gets wrong: a name carrying both the token
// and the phrase it collides with. `matches "\bMA\b" and not matches "DTS-HD
// MA"` reads false for the third title here, because it judges the name rather
// than the occurrence.
func TestMatchesExceptJudgesTheOccurrence(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "MA the service",
		When:   `matchesExcept(releaseName, "(?i)\\bMA\\b", "(?i)DTS[-. ]?HD[-. ]?MA")`,
		Points: 500,
	})

	tests := []struct {
		title string
		want  int
	}{
		{"Movie 2020 1080p MA WEB-DL x264-GRP", 500},
		{"Movie 2020 2160p BluRay REMUX DTS-HD MA 5.1 HEVC-GRP", 0},
		{"Movie 2020 2160p MA WEB-DL DTS-HD MA 5.1 HEVC-GRP", 500},
		{"Movie 2020 2160p BluRay REMUX TrueHD 7.1-GRP", 0},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			if got := set.Evaluate(envFor(tt.title, nil), "movie"); got.Points != tt.want {
				t.Errorf("Points = %d, want %d", got.Points, tt.want)
			}
		})
	}
}

// The other half of the issue's examples: a bare service token against the
// service whose name contains it.
func TestMatchesExceptOnServiceAlias(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "MAX",
		When:   `matchesExcept(releaseName, "(?i)\\bMAX\\b", "(?i)HBO[. -]?Max")`,
		Action: config.RuleActionReject,
	})

	tests := []struct {
		title      string
		wantReject bool
	}{
		{"Show S01E01 1080p MAX WEB-DL DDP5.1-GRP", true},
		{"Show S01E01 1080p HBO.Max WEB-DL DDP5.1-GRP", false},
		{"Show S01E01 1080p HBO Max WEB-DL DDP5.1 MAX-GRP", true},
		{"Show S01E01 1080p AMZN WEB-DL DDP5.1-GRP", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := set.Evaluate(envFor(tt.title, nil), "movie")
			if rejected := len(got.Rejections) > 0; rejected != tt.wantReject {
				t.Errorf("rejected = %v, want %v", rejected, tt.wantReject)
			}
		})
	}
}

// An exclusion that matches nothing leaves the pattern alone, and one that can
// match the empty string excludes nothing rather than everything.
func TestMatchesExceptEmptyExclusion(t *testing.T) {
	for _, except := range []string{"", "NOTHERE", "x?"} {
		t.Run("except "+except, func(t *testing.T) {
			set := compile(t, config.RuleConfig{
				Name:   "IMAX",
				When:   `matchesExcept(releaseName, "(?i)\\bIMAX\\b", "` + except + `")`,
				Points: 100,
			})
			if got := set.Evaluate(envFor("Movie 2020 IMAX 2160p BluRay-GRP", nil), "movie"); got.Points != 100 {
				t.Errorf("Points = %d, want 100", got.Points)
			}
		})
	}
}

// The pattern can be any text the release carries, not only its name.
func TestMatchesExceptOnAnotherField(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "Group is not an anime fansub",
		When:   `matchesExcept(group, "(?i)^SubsPlease$", "(?i)^SubsPlease-raw$")`,
		Points: 10,
	})
	if got := set.Evaluate(envFor("Show S01E01 1080p WEB-DL-SubsPlease", nil), "movie"); got.Points != 10 {
		t.Errorf("Points = %d, want 10", got.Points)
	}
}

// A pattern that will not compile is refused when the profile is saved, the
// same as one written out for the matches operator.
func TestMatchesExceptRefusesBadPattern(t *testing.T) {
	_, err := rules.Compile([]config.RuleConfig{{
		Name: "Broken",
		When: `matchesExcept(releaseName, "(?i)\\bMA\\b", "DTS-HD (MA")`,
	}})
	if err == nil {
		t.Fatal("Compile accepted an unparseable pattern")
	}
	var ruleErr *rules.Error
	if !errors.As(err, &ruleErr) || ruleErr.Rule != "Broken" {
		t.Fatalf("error = %v, want one naming the rule", err)
	}
	if !strings.Contains(err.Error(), "DTS-HD (MA") {
		t.Errorf("error = %v, want it to quote the pattern", err)
	}
}

// A disabled rule is not compiled, so a half-written pattern in one does not
// block the save either.
func TestMatchesExceptSkipsDisabledRule(t *testing.T) {
	off := false
	if _, err := rules.Compile([]config.RuleConfig{
		{Name: "Broken", When: `matchesExcept(releaseName, "(", "")`, Enabled: &off},
		{Name: "Fine", When: `releaseName matches "(?i)IMAX"`, Points: 1},
	}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// The scan reads calls, not text: a condition that merely mentions the
// function inside a string carries no pattern to check.
func TestMatchesExceptIgnoresCallInsideString(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "Names it",
		When:   `releaseName contains 'matchesExcept(x, "(", "")'`,
		Points: 1,
	})
	if got := set.Evaluate(envFor("Movie 2020 1080p-GRP", nil), "movie"); got.Points != 0 {
		t.Errorf("Points = %d, want 0", got.Points)
	}
}

// A pattern the profile computes cannot be checked when it is saved, so it is
// reported as a rule that could not be judged rather than taking the release
// with it.
func TestMatchesExceptComputedBadPatternSkips(t *testing.T) {
	set := compile(t, config.RuleConfig{
		Name:   "Computed",
		When:   `matchesExcept(releaseName, "(" + "", "")`,
		Action: config.RuleActionReject,
	})
	got := set.Evaluate(envFor("Movie 2020 1080p-GRP", nil), "movie")
	if len(got.Rejections) != 0 {
		t.Errorf("Rejections = %v, want none", got.Rejections)
	}
	if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "bad pattern") {
		t.Errorf("Skipped = %v, want one bad-pattern reason", got.Skipped)
	}
}
