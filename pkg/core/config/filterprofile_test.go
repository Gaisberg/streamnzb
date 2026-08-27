package config

import (
	"strings"
	"testing"
)

// The source record is written by the importer, so validation only has to
// refuse what the importer could never have produced — anything else is a
// hand-edited config or a hostile save.
func TestProfileSourceValidate(t *testing.T) {
	valid := func(ps *ProfileSourceConfig, prefix string) {
		t.Helper()
		if err := ps.Validate(prefix); err != nil {
			t.Errorf("Validate(%+v, %q) = %v, want nil", ps, prefix, err)
		}
	}
	invalid := func(ps *ProfileSourceConfig, prefix, want string) {
		t.Helper()
		err := ps.Validate(prefix)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%+v, %q) = %v, want it to mention %q", ps, prefix, err, want)
		}
	}

	valid(nil, FilterShareCodePrefix)
	valid(&ProfileSourceConfig{URL: "https://raw.githubusercontent.com/u/r/main/profile.txt"}, FilterShareCodePrefix)
	valid(&ProfileSourceConfig{
		URL:       "https://example.com/profile.txt",
		Code:      "SNZBP1:H4sIAAAA",
		CheckedAt: "2026-08-26T12:00:00Z",
		AppliedAt: "2026-08-26T12:00:00Z",
	}, FilterShareCodePrefix)
	// A format profile's snapshot is a format code — its own prefix, not the
	// filter one. Both directions have to hold.
	valid(&ProfileSourceConfig{URL: "https://example.com/f.txt", Code: "SNZBF1:H4sIAAAA"}, FormatShareCodePrefix)
	invalid(&ProfileSourceConfig{URL: "https://example.com/f.txt", Code: "SNZBF1:H4sIAAAA"}, FilterShareCodePrefix, "SNZBP1")
	invalid(&ProfileSourceConfig{URL: "https://example.com/p.txt", Code: "SNZBP1:H4sIAAAA"}, FormatShareCodePrefix, "SNZBF1")

	// http would let anyone on the path rewrite what the profile refreshes to.
	invalid(&ProfileSourceConfig{URL: "http://example.com/profile.txt"}, FilterShareCodePrefix, "https")
	invalid(&ProfileSourceConfig{URL: ""}, FilterShareCodePrefix, "https")
	invalid(&ProfileSourceConfig{URL: "not a url at all\x7f"}, FilterShareCodePrefix, "invalid source URL")
	invalid(&ProfileSourceConfig{URL: "https://" + strings.Repeat("a", 2048) + ".com/p"}, FilterShareCodePrefix, "too long")
	invalid(&ProfileSourceConfig{URL: "https://example.com/p", Code: "not-a-code"}, FilterShareCodePrefix, "SNZBP1")
	invalid(&ProfileSourceConfig{
		URL:  "https://example.com/p",
		Code: "SNZBP1:" + strings.Repeat("A", maxSourceCodeBytes),
	}, FilterShareCodePrefix, "too large")
}
