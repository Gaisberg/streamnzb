package ffprobe

import "strings"

// iso6392to1 maps ISO 639-2 codes — both the bibliographic (ger, fre, chi)
// and terminologic (deu, fra, zho) forms — to the codes the rest of StreamNZB
// speaks: jhin's languages list, rules like `"ja" in languages`, and the
// formatter. That vocabulary is ISO 639-1 with two deliberate departures a
// probe has to follow so that "the same codes languages uses" stays true:
//
//   - Norwegian is one language, "no". jhin folds Bokmål and Nynorsk into it,
//     so nob and nno land on "no" here as well, never on nb/nn.
//   - "la" means Latin American Spanish in jhin (PTT's `latino`), not Latin.
//     A Latin track (lat) therefore maps to nothing rather than to a code a
//     rule would read as Latino.
//
// Every other 639-2 code with a 639-1 equivalent is listed. A code without
// one passes through only when already two letters and is dropped otherwise.
var iso6392to1 = map[string]string{
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
}

// NormalizeLanguageTag turns a track language tag into an ISO 639-1 code, or
// "" when the tag carries no language: empty, "und" (undetermined), "mul"
// (multiple), "zxx" (no linguistic content), "mis" (uncoded), or something
// unrecognised. A region suffix ("pt-BR", "en_US") is dropped; the base
// language is what rules and formatters compare against.
func NormalizeLanguageTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "", "und", "mul", "zxx", "mis", "lat":
		return ""
	}
	if code, ok := iso6392to1[tag]; ok {
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
