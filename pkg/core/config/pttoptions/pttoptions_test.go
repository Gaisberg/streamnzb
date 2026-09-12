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

func TestResolveLanguagesUnionsEveryAccount(t *testing.T) {
	codes, source := ResolveLanguages([]string{"ja"}, []string{"Arabic"}, []string{"fr"})
	if len(codes) != 3 || codes[0] != "ja" || codes[1] != "ar" || codes[2] != "fr" {
		t.Errorf("codes = %v, want [ja ar fr] — strongest account first", codes)
	}
	if source != LanguagesMeasured {
		t.Errorf("source = %q, want %q", source, LanguagesMeasured)
	}
}

// A probe proves a language is there, never that one is absent: untagged
// tracks, a probe too old to read tracks, or a pack whose other episodes were
// never opened would each erase a language the name got right.
func TestResolveLanguagesNeverShrinksTheList(t *testing.T) {
	codes, source := ResolveLanguages([]string{"en"}, nil, []string{"de"})
	if len(codes) != 2 || codes[0] != "en" || codes[1] != "de" {
		t.Errorf("codes = %v, want [en de] — the measured track does not evict the named language", codes)
	}
	if source != LanguagesMeasured {
		t.Errorf("source = %q, want %q", source, LanguagesMeasured)
	}
}

func TestResolveLanguagesReportsWhatAnswered(t *testing.T) {
	cases := []struct {
		name                         string
		measured, reported, inferred []string
		want                         LanguageSource
	}{
		{name: "nothing said anything", want: LanguagesUnknown},
		{name: "the name alone", inferred: []string{"fr"}, want: LanguagesInferred},
		{name: "the indexer tag", reported: []string{"ara"}, inferred: []string{"fr"}, want: LanguagesReported},
		{name: "the file itself", measured: []string{"ja"}, reported: []string{"ara"}, want: LanguagesMeasured},
		// Tracks that carry no usable language are not a measurement.
		{name: "untagged tracks", measured: []string{""}, inferred: []string{"fr"}, want: LanguagesInferred},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := ResolveLanguages(tc.measured, tc.reported, tc.inferred); got != tc.want {
				t.Errorf("source = %q, want %q", got, tc.want)
			}
		})
	}
}
