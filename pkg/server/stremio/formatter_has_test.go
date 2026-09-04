package stremio

import (
	"bytes"
	"testing"
	"text/template"
)

// has is membership; contains is a substring test over the rendered list.
// The documented original-audio badge must use the former: with contains,
// "en" lit up inside ["French"] and the genuine "ko" in ["Korean"] did not.
func TestHasIsMembershipNotSubstring(t *testing.T) {
	render := func(orig string, langs ...string) string {
		tpl := template.Must(template.New("t").Funcs(formatTemplateFuncs).Parse(
			`{{if and .OriginalLanguage (has .OriginalLanguage .Languages)}}🎙{{end}}`))
		var out bytes.Buffer
		if err := tpl.Execute(&out, FormatContext{OriginalLanguage: orig, Languages: langs}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	for _, tc := range []struct {
		orig  string
		langs []string
		want  bool
	}{
		{"en", []string{"en"}, true},
		{"en", []string{"ja", "en"}, true},
		{"en", []string{"French"}, false},   // contains would say yes
		{"es", []string{"Japanese"}, false}, // contains would say yes
		{"ko", []string{"Korean"}, false},   // a word, not a code: honest no
		{"ko", []string{"ko"}, true},
		{"", []string{"en"}, false},
	} {
		if got := render(tc.orig, tc.langs...) != ""; got != tc.want {
			t.Errorf("orig=%q langs=%v: badge=%v, want %v", tc.orig, tc.langs, got, tc.want)
		}
	}
}
