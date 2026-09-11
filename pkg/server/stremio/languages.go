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

// releaseSubtitleCodes is the union of the subtitle languages the release
// name spells out ("Arabic.Subs"), the ones the indexer tagged the release
// with, and the tracks ffprobe found when the file has been opened, as ISO
// 639-1 codes.
func releaseSubtitleCodes(rel *release.Release, meta *parser.ParsedRelease, probed *release.MediaCaps) []string {
	var parsed, reported, measured []string
	if meta != nil {
		parsed = meta.Subtitles
	}
	if rel != nil {
		reported = rel.Subtitles
	}
	if probed != nil && probed.TracksProbed {
		measured = probed.SubtitleLanguages
	}
	return pttoptions.MergeLanguageCodes(parsed, reported, measured)
}

// streamMediaInfo is the AIOStreams parsedMediaInfo block for a release, or
// nil when there is nothing to put in it. Its Newznab integration builds the
// same block from the feed's language and subs attributes, and its subtitle
// filters read nothing else — a stream without one has "Unknown" subtitles
// and fails every required-subtitle filter (issue #283).
func streamMediaInfo(languages, subtitles []string) *StreamMediaInfo {
	if len(languages) == 0 && len(subtitles) == 0 {
		return nil
	}
	return &StreamMediaInfo{Quality: "indexer", Languages: languages, Subtitles: subtitles}
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
