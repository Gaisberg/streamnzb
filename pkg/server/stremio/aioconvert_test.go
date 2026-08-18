package stremio

import (
	"strings"
	"testing"
	"text/template"
)

// convertAndRender converts an AIOStreams snippet and renders the resulting
// Go template over ctx, failing the test on compile/execute errors.
func convertAndRender(t *testing.T, aio string, ctx FormatContext) (converted, rendered string) {
	t.Helper()
	res := ConvertAIOStreamsFormat(aio)
	tpl, err := template.New("converted").Funcs(formatTemplateFuncs).Parse(res.Template)
	if err != nil {
		t.Fatalf("converted template %q does not compile: %v", res.Template, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, ctx); err != nil {
		t.Fatalf("converted template %q failed to execute: %v", res.Template, err)
	}
	return res.Template, b.String()
}

func TestConvertAIOStreamsExpressions(t *testing.T) {
	cases := []struct {
		aio  string
		want string
	}{
		{`{stream.filename}`, `{{.ReleaseTitle}}`},
		{`{stream.resolution}`, `{{.Resolution}}`},
		{`{stream.size::sbytes}`, `{{size .Size}}`},
		{`{stream.audioTags::lsort::join(' · ')}`, `{{join (sortAsc .Audio) " · "}}`},
		{`{stream.audioChannels::rsort::join(' · ')}`, `{{join (sortDesc .Channels) " · "}}`},
		{`{stream.title::title::truncate(20)}`, `{{truncate 20 (title .ParsedTitle)}}`},
		{`{stream.network::smallcaps}`, `{{smallcaps .Network}}`},
		{`{stream.encode::upper}`, `{{upper .Codec}}`},
		{`{stream.releaseGroup::default('unknown')}`, `{{default "unknown" .Group}}`},
		{`{stream.visualTags::join(' | ')}`, `{{join .HDR " | "}}`},
		{`{tools.newLine}`, "\n"},
		{`{stream.year::superscript}`, `{{translate "0123456789" "⁰¹²³⁴⁵⁶⁷⁸⁹" .Year}}`},
		{`{'n/a'::upper}`, `{{upper "n/a"}}`},
		{`{'literal'}`, `literal`},
	}
	for _, c := range cases {
		res := ConvertAIOStreamsFormat(c.aio)
		if res.Template != c.want {
			t.Errorf("convert(%s) = %q, want %q", c.aio, res.Template, c.want)
		}
	}
}

func TestConvertAIOStreamsConditionals(t *testing.T) {
	cases := []struct {
		aio  string
		want string
	}{
		{
			`{stream.visualTags::exists["📺 {stream.visualTags::join(' | ')}"||""]}`,
			`{{if exists .HDR}}📺 {{join .HDR " | "}}{{end}}`,
		},
		{
			`{stream.audioTags::length::>1["multi"||"single"]}`,
			`{{if gt (length .Audio) 1}}multi{{else}}single{{end}}`,
		},
		{
			`{service.cached::istrue["⚡"||"⏳"]}`,
			`{{if .Avail}}⚡{{else}}⏳{{end}}`,
		},
		{
			// The =p2p clause folds to a constant false and drops out of the or.
			`{service.cached::isfalse::or::stream.type::=p2p["Uncached"||"Cached"]}`,
			`{{if not .Avail}}Uncached{{else}}Cached{{end}}`,
		},
		{
			// Constant comparisons resolve at conversion time, case-insensitively.
			`{stream.type::=Usenet["📡 Usenet "||""]}`,
			`📡 Usenet `,
		},
		{
			`{stream.type::=p2p["[P2P] "||""]}`,
			``,
		},
		{
			`{stream.proxied::istrue["🕵️ "||""]}`,
			``,
		},
		{
			`{stream.resolution::exists["{stream.quality::exists["{stream.resolution} {stream.quality}"||"{stream.resolution}"]}"||"Unknown"]}`,
			`{{if exists .Resolution}}{{if exists .Quality}}{{.Resolution}} {{.Quality}}{{else}}{{.Resolution}}{{end}}{{else}}Unknown{{end}}`,
		},
		{
			`{stream.resolution::in('2160p','1080p')["hd"||"sd"]}`,
			`{{if or (eq .Resolution "2160p") (eq .Resolution "1080p")}}hd{{else}}sd{{end}}`,
		},
		{
			`{stream.indexer::~drunk["🥴"||""]}`,
			`{{if contains "drunk" .Indexer}}🥴{{end}}`,
		},
		{
			`{stream.seeders::>=100["hot"||"cold"||"n/a"]}`,
			`{{if ge .Grabs 100}}hot{{else if exists .Grabs}}cold{{else}}n/a{{end}}`,
		},
		{
			`{?📅 {stream.age} ?}`,
			`{{if exists .Age}}📅 {{.Age}} {{end}}`,
		},
	}
	for _, c := range cases {
		res := ConvertAIOStreamsFormat(c.aio)
		if res.Template != c.want {
			t.Errorf("convert(%s) = %q, want %q", c.aio, res.Template, c.want)
		}
	}
}

func TestConvertAIOStreamsRendersOverContext(t *testing.T) {
	ctx := FormatContext{
		ReleaseTitle: "Dune.Part.Two.2024.2160p.WEB-DL.DV.HDR10.HEVC.DDP5.1.Atmos-FLUX",
		Resolution:   "2160p",
		Quality:      "WEB-DL",
		HDR:          stringList{"DV", "HDR10"},
		Audio:        stringList{"DDP", "Atmos"},
		Channels:     stringList{"5.1"},
		Size:         24_800_000_000,
		Indexer:      "DrunkenSlug",
		Avail:        true,
	}
	aio := `{service.cached::istrue["⚡ "||""]}{stream.resolution} {stream.quality}` +
		`{stream.visualTags::exists[" · {stream.visualTags::sort::join('/')}"||""]}` +
		` · {stream.size::sbytes} · {stream.indexer}`
	_, rendered := convertAndRender(t, aio, ctx)
	want := "⚡ 2160p WEB-DL · DV/HDR10 · 24.80 GB · DrunkenSlug"
	if rendered != want {
		t.Errorf("rendered = %q, want %q", rendered, want)
	}
}

func TestConvertAIOStreamsDropsUnknownFields(t *testing.T) {
	// Unknown fields behave like missing fields: conditionals fall back to
	// their false branch, bare expressions drop — nothing leaks into results.
	res := ConvertAIOStreamsFormat(`{stream.seadex::istrue["best"||"alt"]} {debug.json}`)
	if res.Template != `alt ` {
		t.Errorf("unknown fields should fall back/drop, got %q", res.Template)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warnings for unknown fields")
	}

	res = ConvertAIOStreamsFormat(`{stream.rseMatched::exists["({stream.rseMatched::first})"||""]}`)
	if res.Template != `` {
		t.Errorf("unknown field conditional with empty false branch should vanish, got %q", res.Template)
	}
}

func TestConvertAIOStreamsUnsupportedModifierRendersValue(t *testing.T) {
	// Unsupported modifiers are skipped so the underlying value still shows.
	res := ConvertAIOStreamsFormat(`{stream.date::date('%B %o, %Y')}`)
	if res.Template != `{{.Date}}` {
		t.Errorf("unsupported modifier should render the bare value, got %q", res.Template)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning for the unsupported ::date modifier")
	}
}

func TestConvertAIOStreamsDuration(t *testing.T) {
	// {stream.duration::time} maps to the pre-humanized {{.Duration}} text and
	// the ::>0 guard becomes an existence test.
	res := ConvertAIOStreamsFormat(`{stream.duration::>0["⏱️ {stream.duration::time} "||""]}`)
	want := `{{if exists .Duration}}⏱️ {{.Duration}} {{end}}`
	if res.Template != want {
		t.Errorf("convert = %q, want %q", res.Template, want)
	}
}

// TestConvertAIOStreamsBuiltinFormatterCorpus feeds AIOStreams' own built-in
// formatter definitions (packages/core/src/utils/formatter-definitions.ts)
// through the converter. Every conversion must produce a template that
// compiles and executes; unmappable pieces surface as warnings, never as
// broken output.
func TestConvertAIOStreamsBuiltinFormatterCorpus(t *testing.T) {
	corpus := map[string]string{
		"torrentio.name":          `{stream.proxied::istrue["🕵️‍♂️ "||""]}{stream.private::istrue["🔑 "||""]}{stream.type::=p2p["[P2P] "||""]}{service.id::exists["[{service.shortName}"||""]}{service.cached::istrue["+] "||""]}{service.cached::isfalse[" download] "||""]}{addon.name} {stream.resolution::exists["{stream.resolution}"||"Unknown"]} {?{stream.visualTags::join(' | ')}?}`,
		"torrentio.description":   `{?ℹ️{stream.message}?} {?{stream.folderName}?} {?{stream.filename}?} {stream.size::>0["💾{stream.size::bytes2} "||""]}{stream.folderSize::>0["/ 💾{stream.folderSize::bytes2}"||""]}{stream.seeders::>=0["👤{stream.seeders} "||""]}{?📅{stream.age} ?}{?⚙️{stream.indexer}?} {?{stream.languageEmojis::join(' / ')}?}{stream.subtitles::exists::and::stream.languageEmojis::exists[" "||""]}{stream.subtitles::exists["Subs / {stream.subtitleEmojis::join(' / ')}"||""]}`,
		"torbox.name":             `{stream.proxied::istrue["🕵️‍♂️ "||""]}{stream.private::istrue["🔑 "||""]}{stream.type::=p2p["[P2P] "||""]}{addon.name}{stream.library::istrue[" (Your Media) "||""]}{service.cached::istrue[" (Instant "||""]}{service.cached::isfalse[" ("||""]}{service.id::exists["{service.shortName})"||""]}{? ({stream.resolution})?}`,
		"torbox.description":      `Quality: {stream.quality::exists["{stream.quality}"||"Unknown"]} Name: {stream.filename::exists["{stream.filename}"||"Unknown"]} Size: {stream.size::>0["{stream.size::bytes} "||""]}{stream.folderSize::>0["/ {stream.folderSize::bytes} "||""]}{?| Source: {stream.indexer} ?}{stream.duration::>0["| Duration: {stream.duration::time} "||""]} Languages: {?{stream.languages::join(', ')}?}{stream.subtitles::exists::and::stream.languages::exists[" | "||""]}{?Subtitles: {stream.subtitles::join(', ')}?} {?Message: {stream.message}?}`,
		"gdrive.description":      `{?🎥 {stream.quality} ?}{?🎞️ {stream.encode} ?}{?🏷️ {stream.releaseGroup} ?}{?📡 {stream.network} ?} {?📺 {stream.visualTags::join(' | ')} ?}{?🎧 {stream.audioTags::join(' | ')} ?}{?🔊 {stream.audioChannels::join(' | ')}?} {stream.size::>0["📦 {stream.size::sbytes} "||""]}{stream.folderSize::>0["/ {stream.folderSize::sbytes} "||""]}{stream.bitrate::>0["({stream.bitrate::sbitrate})"||""]}{stream.duration::>0["⏱️ {stream.duration::time} "||""]}{stream.seeders::>0["👥 {stream.seeders} "||""]}{?📅 {stream.age} ?}{?🔍 {stream.indexer}?} {?🌎 {stream.languages::join(' | ')}?}{?📝 {stream.subtitles::join(' | ')}?} {stream.filename::exists["📁"||""]} {?{stream.folderName}/?}{?{stream.filename}?} {?ℹ️ {stream.message}?}`,
		"lightgdrive.description": `{?📁 {stream.title::title}?}{? ({stream.year})?}{? {stream.seasonEpisode::join(' • ')}?} {?🎥 {stream.quality} ?}{?🎞️ {stream.encode} ?}{?🏷️ {stream.releaseGroup}?}{?📡 {stream.network} ?} {?📺 {stream.visualTags::join(' • ')} ?}{?🎧 {stream.audioTags::join(' • ')} ?}{?🔊 {stream.audioChannels::join(' • ')}?} {stream.size::>0["📦 {stream.size::sbytes} "||""]}{stream.folderSize::>0["/ {stream.folderSize::sbytes} "||""]}{stream.duration::>0["⏱️ {stream.duration::time} "||""]}{?📅 {stream.age} ?}{?🔍 {stream.indexer}?} {?🌐 {stream.languageEmojis::join(' / ')}?}{stream.subtitles::exists["📝 {stream.subtitleEmojis::join(' / ')}"||""]} {?ℹ️ {stream.message}?}`,
		"minimalisticgdrive.name": `{stream.resolution::exists["{stream.resolution::replace('2160p','✨ 4K')::replace('1440p','📀 2K')::replace('1080p','🧿1080p')::replace('720p','💿720p')}"||"N/A"]}{service.cached::istrue[" 🎫 "||""]}{service.cached::isfalse[" 🎟️ "||""]} {?{stream.quality::upper}?}`,
	}
	ctx := FormatContext{
		Service:      "StreamNZB",
		Stream:       "Standalone",
		ReleaseTitle: "Ted.Lasso.S04E01.NORDiC.1080p.ATV.WEB-DL.H.265-NORViNE",
		ParsedTitle:  "Ted Lasso",
		Resolution:   "1080p",
		Quality:      "WEB-DL",
		Codec:        "h265",
		Group:        "NORViNE",
		HDR:          stringList{},
		Audio:        stringList{"DDP"},
		Channels:     stringList{"5.1"},
		Languages:    stringList{"en", "fi"},
		Size:         1_832_627_684,
		Indexer:      "altHUB",
		Grabs:        37,
		Age:          "3d",
		Year:         2025,
		Avail:        true,
		Subbed:       true,
	}
	for name, text := range corpus {
		res := ConvertAIOStreamsFormat(text)
		tpl, err := template.New(name).Funcs(formatTemplateFuncs).Parse(res.Template)
		if err != nil {
			t.Errorf("%s: converted template does not compile: %v\n%s", name, err, res.Template)
			continue
		}
		var b strings.Builder
		if err := tpl.Execute(&b, ctx); err != nil {
			t.Errorf("%s: converted template failed to execute: %v\n%s", name, err, res.Template)
		}
	}
}

func TestConvertAIOStreamsPlainTextUntouched(t *testing.T) {
	text := "plain text • no expressions [brackets] {not a field} {{go braces}}"
	res := ConvertAIOStreamsFormat(text)
	if res.Template != text {
		t.Errorf("plain text should be unchanged, got %q", res.Template)
	}
}
