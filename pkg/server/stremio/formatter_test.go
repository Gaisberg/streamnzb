package stremio

import (
	"strings"
	"testing"
	"text/template"
)

// renderFormat compiles and executes a template over ctx, failing the test on
// any compile/execute error so cases stay one-liners.
func renderFormat(t *testing.T, text string, ctx FormatContext) string {
	t.Helper()
	tpl, err := template.New("test").Funcs(formatTemplateFuncs).Parse(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, ctx); err != nil {
		t.Fatalf("execute %q: %v", text, err)
	}
	return b.String()
}

func TestFormatTemplateListsRenderWithoutBrackets(t *testing.T) {
	ctx := FormatContext{
		Languages: stringList{"en", "fi"},
		HDR:       stringList{"DV", "HDR10"},
		Seasons:   intList{1, 2, 3},
	}
	cases := map[string]string{
		"{{.Languages}}": "en, fi",
		"{{.HDR}}":       "DV, HDR10",
		"{{.Seasons}}":   "1, 2, 3",
	}
	for text, want := range cases {
		if got := renderFormat(t, text, ctx); got != want {
			t.Errorf("%s = %q, want %q", text, got, want)
		}
	}
}

func TestFormatTemplateHelpersAcceptAnyValue(t *testing.T) {
	ctx := FormatContext{
		Resolution: "1080p",
		HDR:        stringList{"DV", "HDR10"},
		Languages:  stringList{},
		Group:      " FLUX ",
	}
	cases := map[string]string{
		`{{replace .Resolution "1080p" "HD"}}`: "HD",
		`{{replace .HDR "DV" "DolbyVision"}}`:  "DolbyVision, HDR10",
		`{{.HDR | upper}}`:                     "DV, HDR10",
		`{{.HDR | lower}}`:                     "dv, hdr10",
		`{{.Group | trim}}`:                    "FLUX",
		`{{.Languages | default "none"}}`:      "none",
		`{{.HDR | default "none"}}`:            "DV, HDR10",
		`{{join .HDR "|"}}`:                    "DV|HDR10",
		`{{range .HDR}}[{{.}}]{{end}}`:         "[DV][HDR10]",
	}
	for text, want := range cases {
		if got := renderFormat(t, text, ctx); got != want {
			t.Errorf("%s = %q, want %q", text, got, want)
		}
	}
}

func TestRenderResultTemplateDropsBlankLines(t *testing.T) {
	// Conditionals on their own lines (issue #187): a false conditional must
	// not leave an empty line behind, and trailing spaces from end-of-line
	// conditionals must not survive.
	cases := []struct {
		text string
		ctx  FormatContext
		want string
	}{
		{
			text: "{{.ReleaseTitle}}\n{{if .HDR}}▶︎ ({{join .HDR \"/\"}}){{end}}\n{{if .Audio}}♫ {{join .Audio \" · \"}}{{end}}\n{{.Indexer}}",
			ctx:  FormatContext{ReleaseTitle: "Some.Release", Indexer: "Indexer"},
			want: "Some.Release\nIndexer",
		},
		{
			text: "{{.ReleaseTitle}}\n{{if .HDR}}▶︎ ({{join .HDR \"/\"}}){{end}}\n{{if .Audio}}♫ {{join .Audio \" · \"}}{{end}}",
			ctx:  FormatContext{ReleaseTitle: "Some.Release", HDR: stringList{"DV", "HDR10"}, Audio: stringList{"DDP", "Atmos"}},
			want: "Some.Release\n▶︎ (DV/HDR10)\n♫ DDP · Atmos",
		},
		{
			text: "{{if .Quality}}📡 {{.Quality}} {{end}}\n{{.Indexer}}",
			ctx:  FormatContext{Quality: "WEB-DL", Indexer: "Indexer"},
			want: "📡 WEB-DL\nIndexer",
		},
	}
	for _, c := range cases {
		tpl, err := template.New("test").Funcs(formatTemplateFuncs).Parse(c.text)
		if err != nil {
			t.Fatalf("parse %q: %v", c.text, err)
		}
		if got := renderResultTemplate(tpl, c.ctx, "fallback"); got != c.want {
			t.Errorf("renderResultTemplate(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestFormatTemplateConditionals(t *testing.T) {
	cases := []struct {
		text string
		ctx  FormatContext
		want string
	}{
		{`{{if .Avail}}✅ Verified{{else}}⚡ StreamNZB{{end}}`, FormatContext{Avail: true}, "✅ Verified"},
		{`{{if .Avail}}✅ Verified{{else}}⚡ StreamNZB{{end}}`, FormatContext{}, "⚡ StreamNZB"},
		{`{{if not .Avail}}⚡ StreamNZB{{end}}`, FormatContext{}, "⚡ StreamNZB"},
		{`{{if .Library}}library{{end}}`, FormatContext{Library: true}, "library"},
		{`{{if .Size}}{{size .Size}}{{end}}`, FormatContext{}, ""},
		{`{{if .Size}}{{size .Size}}{{end}}`, FormatContext{Size: 2_000_000_000}, "2.00 GB"},
		{`{{if .HDR}}yes{{end}}`, FormatContext{HDR: stringList{"DV"}}, "yes"},
		{`{{if .HDR}}yes{{end}}`, FormatContext{}, ""},
	}
	for _, c := range cases {
		if got := renderFormat(t, c.text, c.ctx); got != c.want {
			t.Errorf("%s over %+v = %q, want %q", c.text, c.ctx, got, c.want)
		}
	}
}
