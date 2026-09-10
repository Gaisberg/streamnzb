package stremio

import (
	"strings"

	"streamnzb/pkg/core/config/pttoptions"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

// languageFlagByCode maps an ISO 639-1 code to the flag emoji that stands for
// it in a stream's description.
//
// The flags are not decoration: a Stremio addon has no field for the languages
// of a release, so every aggregator that filters by language reads them out of
// the description text, and AIOStreams resolves each flag back to one language
// through its own table. A flag that resolves to the wrong language is worse
// than none — 🇹🇷 is Kurdish there and 🇮🇳 is Hindi, so Turkish, Persian and the
// Indian languages other than Hindi are deliberately absent rather than
// mislabelled. Their codes still ship in the stream's languages field.
var languageFlagByCode = map[string]string{
	"en": "🇬🇧", "ja": "🇯🇵", "ko": "🇰🇷", "zh": "🇨🇳", "zh-tw": "🇹🇼",
	"fr": "🇫🇷", "es": "🇪🇸", "es-419": "🇲🇽", "pt": "🇵🇹", "it": "🇮🇹",
	"de": "🇩🇪", "ru": "🇷🇺", "uk": "🇺🇦", "nl": "🇳🇱", "da": "🇩🇰",
	"fi": "🇫🇮", "sv": "🇸🇪", "no": "🇳🇴", "el": "🇬🇷", "lt": "🇱🇹",
	"lv": "🇱🇻", "et": "🇪🇪", "pl": "🇵🇱", "cs": "🇨🇿", "sk": "🇸🇰",
	"hu": "🇭🇺", "ro": "🇷🇴", "bg": "🇧🇬", "sr": "🇷🇸", "hr": "🇭🇷",
	"sl": "🇸🇮", "hi": "🇮🇳", "bn": "🇧🇩", "vi": "🇻🇳", "id": "🇮🇩",
	"th": "🇹🇭", "ms": "🇲🇾", "ar": "🇸🇦", "he": "🇮🇱",
}

// releaseLanguageCodes is the union of the languages parsed out of the release
// name and the ones the indexer tagged the release with, as ISO 639-1 codes.
func releaseLanguageCodes(rel *release.Release, meta *parser.ParsedRelease) []string {
	var reported, parsed []string
	if rel != nil {
		reported = rel.Languages
	}
	if meta != nil {
		parsed = meta.Languages
	}
	return pttoptions.MergeLanguageCodes(parsed, reported)
}

// languageFlags renders the flags for the codes that have one, in order and
// without repeats. Codes with no unambiguous flag are skipped.
func languageFlags(codes []string) []string {
	if len(codes) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		flag, ok := languageFlagByCode[strings.ToLower(strings.TrimSpace(code))]
		if !ok || seen[flag] {
			continue
		}
		seen[flag] = true
		out = append(out, flag)
	}
	return out
}

// languageFlagLine is the description line carrying a release's languages, or
// "" when none of them has a flag.
func languageFlagLine(codes []string) string {
	flags := languageFlags(codes)
	if len(flags) == 0 {
		return ""
	}
	return strings.Join(flags, " ")
}
