package ffprobe

import "strings"

// iso6392to1 maps the ISO 639-2 codes muxers write on tracks — both the
// bibliographic (ger, fre, chi) and terminologic (deu, fra, zho) forms — to
// the ISO 639-1 codes the rest of StreamNZB speaks: jhin's languages list,
// rules like `"ja" in languages`, and the formatter. Only languages that show
// up on media tracks in practice are listed; anything else passes through
// unchanged when it is already two letters and is dropped otherwise.
var iso6392to1 = map[string]string{
	"eng": "en", "jpn": "ja", "ara": "ar", "chi": "zh", "zho": "zh", "kor": "ko",
	"fre": "fr", "fra": "fr", "ger": "de", "deu": "de", "spa": "es", "ita": "it",
	"por": "pt", "rus": "ru", "hin": "hi", "tur": "tr", "pol": "pl", "dut": "nl",
	"nld": "nl", "swe": "sv", "nor": "no", "nob": "no", "nno": "no", "dan": "da",
	"fin": "fi", "hun": "hu", "cze": "cs", "ces": "cs", "gre": "el", "ell": "el",
	"heb": "he", "tha": "th", "vie": "vi", "ind": "id", "may": "ms", "msa": "ms",
	"ukr": "uk", "rum": "ro", "ron": "ro", "bul": "bg", "hrv": "hr", "srp": "sr",
	"slv": "sl", "slo": "sk", "slk": "sk", "lit": "lt", "lav": "lv", "est": "et",
	"per": "fa", "fas": "fa", "tam": "ta", "tel": "te", "mal": "ml", "kan": "kn",
	"mar": "mr", "guj": "gu", "pan": "pa", "ben": "bn", "urd": "ur", "fil": "tl",
	"tgl": "tl", "cat": "ca", "baq": "eu", "eus": "eu", "glg": "gl", "ice": "is",
	"isl": "is", "alb": "sq", "sqi": "sq", "mac": "mk", "mkd": "mk", "lat": "la",
}

// NormalizeLanguageTag turns a track language tag into an ISO 639-1 code, or
// "" when the tag carries no language: empty, "und" (undetermined), "mul"
// (multiple), "zxx" (no linguistic content), or something unrecognised. A
// region suffix ("pt-BR", "en_US") is dropped; the base language is what
// rules and formatters compare against.
func NormalizeLanguageTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "", "und", "mul", "zxx", "mis":
		return ""
	}
	if code, ok := iso6392to1[tag]; ok {
		return code
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
