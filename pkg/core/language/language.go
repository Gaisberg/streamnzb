// Package language resolves the many spellings a language arrives in — the
// full name an indexer writes ("Icelandic"), an ISO 639-2 track tag ("isl"),
// a title token ("ice"), a regional qualifier ("Spanish (Latin America)") —
// to the one ISO 639-1 vocabulary the rest of StreamNZB speaks: jhin's
// languages list, rules like `"ja" in languages`, the formatter and the flags
// AIOStreams reads back out of a description.
package language

import (
	"regexp"
	"sort"
	"strings"
)

// languageFullNameToCode maps every language name an indexer might tag a
// release with to its ISO 639-1 code. The table covers the whole of ISO 639-1,
// not just the languages a filter can pick, because NormalizeLanguageToCode
// passes an unknown value through verbatim: an "Icelandic" that is not listed
// here lands in a stream's languages as the literal word "Icelandic" beside
// "en" and "fi", where no rule, flag or formatter recognises it.
var languageFullNameToCode = map[string]string{
	"multi subs": "multi subs", "multi audio": "multi audio", "dual audio": "dual audio",
	"english": "en", "japanese": "ja", "korean": "ko", "chinese": "zh", "french": "fr", "spanish": "es",
	"truefrench": "fr", "vfq": "fr", "vff": "fr",
	"portuguese": "pt", "italian": "it", "german": "de", "russian": "ru", "ukrainian": "uk", "dutch": "nl",
	"danish": "da", "finnish": "fi", "swedish": "sv", "norwegian": "no", "greek": "el", "lithuanian": "lt",
	"latvian": "lv", "estonian": "et", "polish": "pl", "czech": "cs", "slovak": "sk", "hungarian": "hu",
	"romanian": "ro", "bulgarian": "bg", "serbian": "sr", "croatian": "hr", "slovenian": "sl", "hindi": "hi",
	"telugu": "te", "tamil": "ta", "malayalam": "ml", "kannada": "kn", "marathi": "mr", "gujarati": "gu",
	"punjabi": "pa", "bengali": "bn", "vietnamese": "vi", "indonesian": "id", "thai": "th", "malay": "ms",
	"arabic": "ar", "turkish": "tr", "hebrew": "he", "persian": "fa",
	// The rest of ISO 639-1, with the alternate names indexers use.
	"abkhazian": "ab", "afar": "aa", "afrikaans": "af", "akan": "ak", "albanian": "sq",
	"amharic": "am", "aragonese": "an", "armenian": "hy", "assamese": "as", "avaric": "av",
	"avestan": "ae", "aymara": "ay", "azerbaijani": "az", "azeri": "az", "bambara": "bm",
	"bashkir": "ba", "basque": "eu", "belarusian": "be", "bihari": "bh", "bislama": "bi",
	"bosnian": "bs", "breton": "br", "burmese": "my", "myanmar": "my", "catalan": "ca",
	"chamorro": "ch", "chechen": "ce", "chichewa": "ny", "nyanja": "ny", "church slavic": "cu",
	"chuvash": "cv", "cornish": "kw", "corsican": "co", "cree": "cr", "divehi": "dv",
	"dhivehi": "dv", "maldivian": "dv", "dzongkha": "dz", "esperanto": "eo", "ewe": "ee",
	"faroese": "fo", "fijian": "fj", "filipino": "tl", "tagalog": "tl", "fulah": "ff",
	"galician": "gl", "georgian": "ka", "greenlandic": "kl", "kalaallisut": "kl", "guarani": "gn",
	"haitian": "ht", "haitian creole": "ht", "hausa": "ha", "herero": "hz", "hiri motu": "ho",
	"icelandic": "is", "ido": "io", "igbo": "ig", "inuktitut": "iu", "inupiaq": "ik",
	"interlingua": "ia", "interlingue": "ie", "irish": "ga", "gaelic": "gd", "scottish gaelic": "gd",
	"javanese": "jv", "kanuri": "kr", "kashmiri": "ks", "kazakh": "kk",
	"khmer": "km", "cambodian": "km", "kikuyu": "ki", "kinyarwanda": "rw", "kirghiz": "ky",
	"kyrgyz": "ky", "komi": "kv", "kongo": "kg", "kuanyama": "kj", "kurdish": "ku", "lao": "lo",
	"luxembourgish": "lb", "limburgish": "li", "lingala": "ln", "luba-katanga": "lu", "ganda": "lg",
	"macedonian": "mk", "malagasy": "mg", "maltese": "mt", "manx": "gv", "maori": "mi",
	"marshallese": "mh", "mongolian": "mn", "nauru": "na", "navajo": "nv", "ndonga": "ng",
	"nepali": "ne", "north ndebele": "nd", "northern sami": "se", "sami": "se", "occitan": "oc",
	"ojibwa": "oj", "oriya": "or", "odia": "or", "oromo": "om", "ossetian": "os", "pali": "pi",
	"pashto": "ps", "pushto": "ps", "quechua": "qu", "romansh": "rm", "rundi": "rn", "samoan": "sm",
	"sango": "sg", "sanskrit": "sa", "sardinian": "sc", "shona": "sn", "sichuan yi": "ii",
	"sindhi": "sd", "sinhala": "si", "sinhalese": "si", "somali": "so", "south ndebele": "nr",
	"southern sotho": "st", "sotho": "st", "sundanese": "su", "swahili": "sw", "swati": "ss",
	"tahitian": "ty", "tajik": "tg", "tatar": "tt", "tibetan": "bo", "tigrinya": "ti",
	"tonga": "to", "tongan": "to", "tsonga": "ts", "tswana": "tn", "turkmen": "tk", "twi": "tw",
	"uighur": "ug", "uyghur": "ug", "urdu": "ur", "uzbek": "uz", "venda": "ve", "volapük": "vo",
	"volapuk": "vo", "walloon": "wa", "welsh": "cy", "western frisian": "fy", "frisian": "fy",
	"wolof": "wo", "xhosa": "xh", "yiddish": "yi", "yoruba": "yo", "zhuang": "za", "zulu": "zu",
}

// languageISO6392ToCode maps ISO 639-2 three-letter codes to the two-letter
// codes the rest of the pipeline speaks. Indexers report the newznab
// "language" attribute in whichever form their source used — "English",
// "eng" and "en" all occur — so a release tagged "ara" has to normalize to
// the same "ar" a title token would parse to. Both the bibliographic and the
// terminological form are listed where they differ (fre/fra, ger/deu).
//
// The table is the whole of ISO 639-2 with a 639-1 equivalent, with the
// departures the rest of StreamNZB relies on: Norwegian is one language, "no",
// as jhin folds Bokmål and Nynorsk into it, and Latin (lat) is absent because
// "la" means Latin American Spanish downstream (PTT's `latino`), not Latin.
var languageISO6392ToCode = map[string]string{
	"aar": "aa", "abk": "ab", "afr": "af", "aka": "ak", "alb": "sq", "sqi": "sq",
	"amh": "am", "ara": "ar", "arg": "an", "arm": "hy", "hye": "hy", "asm": "as",
	"ava": "av", "ave": "ae", "aym": "ay", "aze": "az", "bak": "ba", "bam": "bm",
	"baq": "eu", "eus": "eu", "bel": "be", "ben": "bn", "bih": "bh", "bis": "bi",
	"bos": "bs", "bre": "br", "bul": "bg", "bur": "my", "mya": "my", "cat": "ca",
	"cha": "ch", "che": "ce", "chi": "zh", "zho": "zh", "chu": "cu", "chv": "cv",
	"cor": "kw", "cos": "co", "cre": "cr", "cze": "cs", "ces": "cs", "dan": "da",
	"div": "dv", "dut": "nl", "nld": "nl", "dzo": "dz", "eng": "en", "epo": "eo",
	"est": "et", "ewe": "ee", "fao": "fo", "fij": "fj", "fin": "fi", "fre": "fr",
	"fra": "fr", "fry": "fy", "ful": "ff", "geo": "ka", "kat": "ka", "ger": "de",
	"deu": "de", "gla": "gd", "gle": "ga", "glg": "gl", "glv": "gv", "gre": "el",
	"ell": "el", "grn": "gn", "guj": "gu", "hat": "ht", "hau": "ha", "heb": "he",
	"her": "hz", "hin": "hi", "hmo": "ho", "hrv": "hr", "hun": "hu", "ibo": "ig",
	"ice": "is", "isl": "is", "ido": "io", "iii": "ii", "iku": "iu", "ile": "ie",
	"ina": "ia", "ind": "id", "ipk": "ik", "ita": "it", "jav": "jv", "jpn": "ja",
	"kal": "kl", "kan": "kn", "kas": "ks", "kau": "kr", "kaz": "kk", "khm": "km",
	"kik": "ki", "kin": "rw", "kir": "ky", "kom": "kv", "kon": "kg", "kor": "ko",
	"kua": "kj", "kur": "ku", "lao": "lo", "lav": "lv", "lim": "li",
	"lin": "ln", "lit": "lt", "ltz": "lb", "lub": "lu", "lug": "lg", "mac": "mk",
	"mkd": "mk", "mah": "mh", "mal": "ml", "mao": "mi", "mri": "mi", "mar": "mr",
	"may": "ms", "msa": "ms", "mlg": "mg", "mlt": "mt", "mon": "mn", "nau": "na",
	"nav": "nv", "nbl": "nr", "nde": "nd", "ndo": "ng", "nep": "ne", "nno": "no",
	"nob": "no", "nor": "no", "nya": "ny", "oci": "oc", "oji": "oj", "ori": "or",
	"orm": "om", "oss": "os", "pan": "pa", "per": "fa", "fas": "fa", "pli": "pi",
	"pol": "pl", "por": "pt", "pus": "ps", "que": "qu", "roh": "rm", "rum": "ro",
	"ron": "ro", "run": "rn", "rus": "ru", "sag": "sg", "san": "sa", "sin": "si",
	"slo": "sk", "slk": "sk", "slv": "sl", "sme": "se", "smo": "sm", "sna": "sn",
	"snd": "sd", "som": "so", "sot": "st", "spa": "es", "srd": "sc", "srp": "sr",
	"ssw": "ss", "sun": "su", "swa": "sw", "swe": "sv", "tah": "ty", "tam": "ta",
	"tat": "tt", "tel": "te", "tgk": "tg", "tgl": "tl", "fil": "tl", "tha": "th",
	"tib": "bo", "bod": "bo", "tir": "ti", "ton": "to", "tsn": "tn", "tso": "ts",
	"tuk": "tk", "tur": "tr", "twi": "tw", "uig": "ug", "ukr": "uk", "urd": "ur",
	"uzb": "uz", "ven": "ve", "vie": "vi", "vol": "vo", "wel": "cy", "cym": "cy",
	"wln": "wa", "wol": "wo", "xho": "xh", "yid": "yi", "yor": "yo", "zha": "za",
	"zul": "zu",
	// Retired ISO 639-2 codes still found on older muxes.
	"scr": "hr", "scc": "sr",
}

// ISO6392ToCode resolves an ISO 639-2 code (either form) to the 639-1 code the
// pipeline speaks, so a probe's track tags and an indexer's attribute go
// through the one table.
func ISO6392ToCode(code string) (string, bool) {
	c, ok := languageISO6392ToCode[strings.ToLower(strings.TrimSpace(code))]
	return c, ok
}

// languageCodes is every code the tables produce — the 639-1 codes and the
// regional variants the pipeline tells apart — so a bare code resolves in
// whatever case the indexer wrote it ("IS" is Icelandic, "zh-TW" is zh-tw,
// neither a word to pass through).
var languageCodes = func() map[string]bool {
	codes := make(map[string]bool)
	for _, c := range languageFullNameToCode {
		codes[c] = true
	}
	for _, c := range languageISO6392ToCode {
		codes[c] = true
	}
	for _, variants := range languageRegionVariants {
		for _, c := range variants {
			codes[c] = true
		}
	}
	return codes
}()

// LanguageNameWords returns every word a release name spells a language out
// with — the full names and the three-letter ISO 639-2 codes — in a stable
// order for regex building. Two-letter codes are left out on purpose: "NO",
// "IT" and "ID" are English words far more often than they are languages.
func LanguageNameWords() []string {
	words := make([]string, 0, len(languageFullNameToCode)+len(languageISO6392ToCode))
	for name := range languageFullNameToCode {
		if strings.Contains(name, " ") {
			continue // "multi subs" and friends are not one language
		}
		words = append(words, name)
	}
	for code := range languageISO6392ToCode {
		words = append(words, code)
	}
	sort.Strings(words)
	return words
}

// LanguageAliases maps release-title alias words to the language codes they represent.
// These are group/region terms commonly used in release names (e.g. "NORDIC" in a title
// means the release includes Danish, Finnish, Norwegian and Swedish audio/subs).
var LanguageAliases = map[string][]string{
	"nordic":       {"da", "fi", "no", "sv"},
	"scandinavian": {"da", "no", "sv"},
	"baltic":       {"et", "lv", "lt"},
	"benelux":      {"nl", "be"},
	"iberian":      {"es", "pt"},
	"slavic":       {"ru", "uk", "pl", "cs", "sk", "bg", "sr", "hr", "sl"},
	"multi":        {"fr"},
	"truefrench":   {"fr"},
}

// LanguageAliasWords returns the alias keys in a stable order for regex building.
func LanguageAliasWords() []string {
	return []string{"nordic", "scandinavian", "baltic", "benelux", "iberian", "slavic", "multi", "truefrench"}
}

// languageRegionVariants maps a language plus the region an indexer wrote in
// parentheses after it — "Spanish (Latin America)", "Chinese (TW)" — to the
// regional code the rest of the pipeline distinguishes. Any other qualifier
// is dropped and the bare language kept: "Arabic (SA)" is Arabic, and a
// filter asking for "ar" has to see it as such.
var languageRegionVariants = map[string]map[string]string{
	"es": {"latin america": "es-419", "latam": "es-419", "la": "es-419", "419": "es-419", "mx": "es-419"},
	"zh": {"traditional": "zh-tw", "tw": "zh-tw", "hk": "zh-tw", "taiwan": "zh-tw", "hong kong": "zh-tw"},
}

// qualifiedLanguagePattern splits "Language (Qualifier)" into its two parts.
var qualifiedLanguagePattern = regexp.MustCompile(`^(.*\S)\s*\(([^()]+)\)$`)

// NormalizeLanguageToCode resolves one spelling of a language to its code. A
// value nothing recognises comes back untouched rather than dropped, so a
// language the tables miss is still visible in the output where it can be
// reported, instead of silently vanishing.
func NormalizeLanguageToCode(value string) string {
	v := strings.TrimSpace(strings.ToLower(value))
	if v == "" {
		return value
	}
	if code, ok := lookupLanguageCode(v); ok {
		return code
	}
	// A regional qualifier the tables do not spell out: resolve the language
	// on its own and keep the region only where the pipeline tells them
	// apart. Each pass strips one qualifier, so this always terminates.
	for m := qualifiedLanguagePattern.FindStringSubmatch(v); m != nil; m = qualifiedLanguagePattern.FindStringSubmatch(v) {
		v = strings.TrimSpace(m[1])
		qualifier := strings.TrimSpace(m[2])
		code, ok := lookupLanguageCode(v)
		if !ok {
			continue
		}
		if regional, ok := languageRegionVariants[code][qualifier]; ok {
			return regional
		}
		return code
	}
	return value
}

// lookupLanguageCode resolves one lowercased, trimmed spelling of a language
// — full name, ISO 639-2 or a code the pipeline already speaks — to its code.
func lookupLanguageCode(lower string) (string, bool) {
	if code, ok := languageFullNameToCode[lower]; ok {
		return code, true
	}
	if code, ok := languageISO6392ToCode[lower]; ok {
		return code, true
	}
	if languageCodes[lower] {
		return lower, true
	}
	return "", false
}

func NormalizeLanguageSlice(s []string) []string {
	if len(s) == 0 {
		return s
	}
	out := make([]string, 0, len(s))
	seen := make(map[string]bool)
	for _, v := range s {
		code := NormalizeLanguageToCode(v)
		if code != "" && !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
}

// MergeLanguageCodes normalizes and concatenates language lists into one
// deduplicated list, earlier lists first. The parsed title and the indexer's
// own tag each know languages the other does not — a dub is regularly tagged
// by the indexer and left out of the release name — so a release's languages
// are the union of both.
func MergeLanguageCodes(lists ...[]string) []string {
	var all []string
	for _, list := range lists {
		all = append(all, list...)
	}
	return NormalizeLanguageSlice(all)
}

// LanguageSource names the strongest account that contributed to a resolved
// language list, so a rule or a caller can ask how the list was arrived at
// rather than trusting every entry equally.
type LanguageSource string

const (
	// LanguagesUnknown is an empty list: nothing said anything.
	LanguagesUnknown LanguageSource = ""
	// LanguagesInferred is the release name alone.
	LanguagesInferred LanguageSource = "inferred"
	// LanguagesReported means the indexer's own language tag contributed.
	LanguagesReported LanguageSource = "reported"
	// LanguagesMeasured means the file was opened and its audio tracks read.
	LanguagesMeasured LanguageSource = "measured"
)

// ResolveLanguages settles what a release is spoken in from the three accounts
// of it, strongest first: the audio tracks a probe read out of the file, the
// indexer's own language tag, and the tokens in the release name.
//
// The result is their union, not the strongest account that answered.
// Measurement outranks the rest on what is *present* — a tagged German track
// is proof of German — but never on what is absent: tracks tagged "und", an
// older probe that read no tracks at all, or a season pack whose other
// episodes were never opened would each erase a language the name got right.
// So nothing here can shrink the list, and a rule that matched on a name goes
// on matching once the file has been probed.
//
// The source returned is the strongest account that contributed anything,
// which is how a caller asks whether the list was measured or merely claimed.
func ResolveLanguages(measured, reported, inferred []string) ([]string, LanguageSource) {
	codes := MergeLanguageCodes(measured, reported, inferred)
	switch {
	case len(codes) == 0:
		return codes, LanguagesUnknown
	case len(NormalizeLanguageSlice(measured)) > 0:
		return codes, LanguagesMeasured
	case len(NormalizeLanguageSlice(reported)) > 0:
		return codes, LanguagesReported
	default:
		return codes, LanguagesInferred
	}
}
