package ffprobe

import (
	"strings"

	"streamnzb/pkg/core/language"
)

// NormalizeLanguageTag turns a track language tag into an ISO 639-1 code, or
// "" when the tag carries no language: empty, "und" (undetermined), "mul"
// (multiple), "zxx" (no linguistic content), "mis" (uncoded), or something
// unrecognised. A region suffix ("pt-BR", "en_US") is dropped; the base
// language is what rules and formatters compare against.
//
// The codes are the ones the rest of StreamNZB speaks — jhin's languages list,
// rules like `"ja" in languages`, the formatter — so ISO 639-2 tags go through
// the same table the indexer attributes do (language.ISO6392ToCode), with
// its two deliberate departures a probe has to follow: Norwegian is one
// language, "no", never nb/nn; and "la" means Latin American Spanish in jhin
// (PTT's `latino`), not Latin, so a Latin track maps to nothing rather than to
// a code a rule would read as Latino.
func NormalizeLanguageTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "", "und", "mul", "zxx", "mis", "lat":
		return ""
	}
	if code, ok := language.ISO6392ToCode(tag); ok {
		return code
	}
	switch tag {
	case "nb", "nn":
		return "no" // jhin's single Norwegian
	case "la":
		return "" // Latin as a two-letter tag; "la" is Latino downstream
	}
	if len(tag) == 2 && isASCIILetters(tag) {
		return tag
	}
	return ""
}

func isASCIILetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}
