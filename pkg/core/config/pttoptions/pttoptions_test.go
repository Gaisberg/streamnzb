package pttoptions

import "testing"

// Indexers report the newznab language attribute in whichever form their
// source used, so every form has to land on the code the title parser emits.
func TestNormalizeLanguageToCodeAcceptsEveryForm(t *testing.T) {
	cases := map[string]string{
		"Arabic":  "ar",
		"ara":     "ar",
		"ar":      "ar",
		"eng":     "en",
		"English": "en",
		"ger":     "de",
		"deu":     "de",
		"zh-TW":   "zh-tw",
		"Klingon": "Klingon", // unknown values pass through untouched
	}
	for in, want := range cases {
		if got := NormalizeLanguageToCode(in); got != want {
			t.Errorf("NormalizeLanguageToCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeLanguageCodes(t *testing.T) {
	got := MergeLanguageCodes([]string{"fr"}, []string{"Arabic", "fre"}, nil)
	if len(got) != 2 || got[0] != "fr" || got[1] != "ar" {
		t.Errorf("MergeLanguageCodes = %v, want [fr ar]", got)
	}
	if got := MergeLanguageCodes(nil, nil); len(got) != 0 {
		t.Errorf("MergeLanguageCodes of nothing = %v, want empty", got)
	}
}
