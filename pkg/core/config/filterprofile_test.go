package config

import (
	"strings"
	"testing"
)

// The source record is written by the importer, so validation only has to
// refuse what the importer could never have produced — anything else is a
// hand-edited config or a hostile save.
func TestProfileSourceValidate(t *testing.T) {
	valid := func(ps *ProfileSourceConfig) {
		t.Helper()
		if err := ps.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", ps, err)
		}
	}
	invalid := func(ps *ProfileSourceConfig, want string) {
		t.Helper()
		err := ps.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%+v) = %v, want it to mention %q", ps, err, want)
		}
	}

	valid(nil)
	valid(&ProfileSourceConfig{URL: "https://raw.githubusercontent.com/u/r/main/profile.txt"})
	valid(&ProfileSourceConfig{
		URL:       "https://example.com/profile.txt",
		Code:      "SNZBP1:H4sIAAAA",
		CheckedAt: "2026-08-26T12:00:00Z",
		AppliedAt: "2026-08-26T12:00:00Z",
	})

	// http would let anyone on the path rewrite what the profile refreshes to.
	invalid(&ProfileSourceConfig{URL: "http://example.com/profile.txt"}, "https")
	invalid(&ProfileSourceConfig{URL: ""}, "https")
	invalid(&ProfileSourceConfig{URL: "not a url at all\x7f"}, "invalid source URL")
	invalid(&ProfileSourceConfig{URL: "https://" + strings.Repeat("a", 2048) + ".com/p"}, "too long")
	invalid(&ProfileSourceConfig{URL: "https://example.com/p", Code: "not-a-code"}, "SNZBP1")
	invalid(&ProfileSourceConfig{
		URL:  "https://example.com/p",
		Code: "SNZBP1:" + strings.Repeat("A", maxSourceCodeBytes),
	}, "too large")
}
