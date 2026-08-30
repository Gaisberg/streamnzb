package stremio

import (
	"strings"
	"testing"
	"text/template"

	"github.com/dreulavelle/jhin"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/ranking"
	"streamnzb/pkg/search/triage"
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

func TestFormatTemplateExtendedHelpers(t *testing.T) {
	ctx := FormatContext{
		ParsedTitle: "the lord of the rings",
		Resolution:  "2160p",
		Network:     "Netflix",
		Group:       "",
		HDR:         stringList{"HDR10", "DV"},
		Audio:       stringList{"dts", "Atmos", "DDP"},
		Channels:    stringList{"7.1", "5.1"},
		Languages:   stringList{},
		Seasons:     intList{3, 1, 2},
		Score:       2850,
	}
	cases := map[string]string{
		`{{if exists .HDR}}yes{{end}}`:                   "yes",
		`{{if exists .Languages}}yes{{else}}no{{end}}`:   "no",
		`{{if exists .Group}}yes{{else}}no{{end}}`:       "no",
		`{{length .HDR}}`:                                "2",
		`{{length .Resolution}}`:                         "5",
		`{{if gt (length .HDR) 1}}multi{{end}}`:          "multi",
		`{{join (sortAsc .Audio) " · "}}`:                "Atmos · DDP · dts",
		`{{join (sort .Audio) " · "}}`:                   "Atmos · DDP · dts",
		`{{join (sortDesc .Audio) " · "}}`:               "dts · DDP · Atmos",
		`{{sortAsc .Seasons}}`:                           "1, 2, 3",
		`{{sortDesc .Seasons}}`:                          "3, 2, 1",
		`{{first .Audio}}`:                               "dts",
		`{{last .Audio}}`:                                "DDP",
		`{{first .Languages}}`:                           "",
		`{{title .ParsedTitle}}`:                         "The Lord Of The Rings",
		`{{truncate 8 .ParsedTitle}}`:                    "the lord…",
		`{{.ParsedTitle | truncate 80}}`:                 "the lord of the rings",
		`{{translate "0123456789" "₀₁₂₃₄₅₆₇₈₉" .Score}}`: "₂₈₅₀",
		`{{remove "DD" .Audio | trim}}`:                  "dts, Atmos, P",
		`{{smallcaps .Network}}`:                         "ɴᴇᴛꜰʟɪx",
		`{{if contains "DV" .HDR}}dv{{end}}`:             "dv",
		`{{if hasPrefix "2160" .Resolution}}4k{{end}}`:   "4k",
		`{{if hasSuffix "p" .Resolution}}p{{end}}`:       "p",
	}
	for text, want := range cases {
		if got := renderFormat(t, text, ctx); got != want {
			t.Errorf("%s = %q, want %q", text, got, want)
		}
	}
	// Sorting must never mutate the context's slices.
	if got := ctx.Audio.String(); got != "dts, Atmos, DDP" {
		t.Errorf("sort mutated context list: %q", got)
	}
	if got := ctx.Seasons.String(); got != "3, 1, 2" {
		t.Errorf("sort mutated context int list: %q", got)
	}
}

// Math helpers take the value last like the string helpers, so they chain:
// subtraction and division read as "sub N from the value", "div the value by
// N". All of them are total — a zero divisor yields 0 and junk coerces to 0 —
// because an execution error would silently swap in the built-in format.
func TestFormatTemplateMathHelpers(t *testing.T) {
	ctx := FormatContext{
		Score:   2850,
		Size:    1_832_627_684,
		Season:  4,
		Bitrate: "not a number",
	}
	cases := map[string]string{
		`{{add 100 .Score}}`:            "2950",
		`{{sub 100 .Score}}`:            "2750",
		`{{mul 2 .Season}}`:             "8",
		`{{div 1000 .Score}}`:           "2",
		`{{mod 1000 .Score}}`:           "850",
		`{{min 3 .Season}}`:             "3",
		`{{max 10 .Season}}`:            "10",
		`{{.Score | div 100 | min 20}}`: "20",
		`{{div 0 .Score}}`:              "0",
		`{{mod 0 .Score}}`:              "0",
		`{{add 1 .Bitrate}}`:            "1",
		// int64 fields coerce like ints: bytes → whole gigabytes.
		`{{div 1000000000 .Size}}`: "1",
	}
	for text, want := range cases {
		if got := renderFormat(t, text, ctx); got != want {
			t.Errorf("%s = %q, want %q", text, got, want)
		}
	}
}

func TestFormatTemplateRepeatAndStars(t *testing.T) {
	cases := map[string]string{
		`{{repeat "★" 3}}`:                 "★★★",
		`{{repeat "▰" 0}}`:                 "",
		`{{repeat "▰" -2}}`:                "",
		`{{div 1000 .Score | repeat "▰"}}`: "▰▰",
		`{{stars 5 5000 .Score}}`:          "★★★☆☆",
		`{{stars 5 2850 .Score}}`:          "★★★★★",
		`{{stars 5 5000 9999}}`:            "★★★★★",
		`{{stars 5 5000 -200}}`:            "☆☆☆☆☆",
		`{{stars 5 0 .Score}}`:             "☆☆☆☆☆",
		`{{stars 0 5000 .Score}}`:          "",
		`{{stars 10 5000 .Score}}`:         "★★★★★★☆☆☆☆",
		// Scaling against the list's actual winner needs no hand-picked ceiling.
		`{{stars 5 .TopScore .Score}}`:    "★★★☆☆",
		`{{stars 5 .TopScore .TopScore}}`: "★★★★★",
	}
	ctx := FormatContext{Score: 2850, TopScore: 4200}
	for text, want := range cases {
		if got := renderFormat(t, text, ctx); got != want {
			t.Errorf("%s = %q, want %q", text, got, want)
		}
	}
	// A runaway count stays inside the response cap instead of allocating.
	if got := renderFormat(t, `{{repeat "ab" 100000}}`, ctx); len([]rune(got)) > maxFormattedResultRunes {
		t.Errorf("repeat emitted %d runes, cap is %d", len([]rune(got)), maxFormattedResultRunes)
	}
}

// The live render path feeds templates the best score of the list that
// survived filtering, so {{stars 5 .TopScore .Score}} rates against the
// actual winner — the top result always paints full.
func TestBuildStreamsExposesTopScore(t *testing.T) {
	list := &playlistResult{Candidates: []triage.Candidate{
		{Release: &release.Release{Title: "Movie.2160p-GRP"}, Score: 4200},
		{Release: &release.Release{Title: "Movie.1080p-GRP"}, Score: 2850},
	}}
	tpl, err := template.New("desc").Funcs(formatTemplateFuncs).Parse(`{{stars 5 .TopScore .Score}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	key := StreamSlotKey{StreamID: "Standalone", ContentType: "movie", ID: "tt1"}
	streams := buildStreamsFromPlaylist(list, key, "Standalone", DefaultServiceName, "http://host", true, &resultFormat{description: tpl})
	if len(streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(streams))
	}
	if streams[0].Description != "★★★★★" {
		t.Errorf("top result = %q, want all filled", streams[0].Description)
	}
	if streams[1].Description != "★★★☆☆" {
		t.Errorf("runner-up = %q, want ★★★☆☆", streams[1].Description)
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

// Release titles almost never spell out a bitrate, so {{.Bitrate}} estimates
// one: from the indexer-reported file duration when there is one, else from
// the requested title's metadata runtime — judging packs per episode and
// staying silent when the episode count is unknown.
func TestFormatContextEstimatesBitrate(t *testing.T) {
	metaFor := func(res *jhin.Result) *parser.ParsedRelease {
		return parser.FromResult("x", res)
	}
	cases := []struct {
		name    string
		cand    triage.Candidate
		runtime float64
		want    string
	}{
		{
			name: "indexer-reported duration",
			cand: triage.Candidate{Release: &release.Release{Title: "Movie 2160p", Size: 15_000_000_000, Duration: 6000}},
			want: "20.0 Mbps",
		},
		{
			name:    "metadata runtime for a movie",
			cand:    triage.Candidate{Release: &release.Release{Title: "Movie 2160p", Size: 9_000_000_000}, Verdict: triage.Verdict{Kind: ranking.KindMovie}},
			runtime: 3600,
			want:    "20.0 Mbps",
		},
		{
			name: "multi-episode release judged per episode",
			cand: triage.Candidate{
				Release:  &release.Release{Title: "Show S01E01-E02", Size: 4_500_000_000},
				Metadata: metaFor(&jhin.Result{Seasons: []int{1}, Episodes: []int{1, 2}}),
				Verdict:  triage.Verdict{Kind: ranking.KindSeries},
			},
			runtime: 1800,
			want:    "10.0 Mbps",
		},
		{
			name: "season pack of unknown episode count stays silent",
			cand: triage.Candidate{
				Release:  &release.Release{Title: "Show S01", Size: 40_000_000_000},
				Metadata: metaFor(&jhin.Result{Seasons: []int{1}}),
				Verdict:  triage.Verdict{Kind: ranking.KindSeries},
			},
			runtime: 1800,
			want:    "",
		},
		{
			name: "probed library duration and media file size beat the estimate",
			cand: triage.Candidate{
				Release: &release.Release{
					Title: "Movie 2160p", Size: 10_000_000_000, IsLibrary: true,
					SourceIndexer: &persistence.LibraryItem{MediaFileSize: 9_000_000_000},
				},
				Verdict: triage.Verdict{Kind: ranking.KindMovie, Probed: &release.MediaCaps{DurationSeconds: 3600}},
			},
			runtime: 7200, // the metadata runtime would halve it; the measurement wins
			want:    "20.0 Mbps",
		},
		{
			name: "bitrate parsed from the title wins",
			cand: triage.Candidate{
				Release:  &release.Release{Title: "Movie", Size: 15_000_000_000, Duration: 6000},
				Metadata: metaFor(&jhin.Result{Bitrate: "448kbps"}),
			},
			want: "448kbps",
		},
		{
			name: "no duration and no runtime says nothing",
			cand: triage.Candidate{Release: &release.Release{Title: "Movie 2160p", Size: 9_000_000_000}},
			want: "",
		},
	}
	for _, c := range cases {
		ctx := newFormatContext(c.cand, 1, 1, 0, DefaultServiceName, "Standalone", "Movie", "", false, c.runtime)
		if ctx.Bitrate != c.want {
			t.Errorf("%s: Bitrate = %q, want %q", c.name, ctx.Bitrate, c.want)
		}
	}
}

// A probed library release fills {{.Duration}} from the container's own
// duration; an indexer-reported one (e.g. Easynews) still wins when present.
func TestFormatContextProbedDurationFillsDuration(t *testing.T) {
	probed := triage.Verdict{Probed: &release.MediaCaps{DurationSeconds: 6900}}
	cand := triage.Candidate{
		Release: &release.Release{Title: "Movie 2160p", Size: 9_000_000_000, IsLibrary: true},
		Verdict: probed,
	}
	if ctx := newFormatContext(cand, 1, 1, 0, DefaultServiceName, "Standalone", "Movie", "", false, 0); ctx.Duration != "1h 55m" {
		t.Errorf("Duration = %q, want %q", ctx.Duration, "1h 55m")
	}

	cand.Release.Duration = 7200
	if ctx := newFormatContext(cand, 1, 1, 0, DefaultServiceName, "Standalone", "Movie", "", false, 0); ctx.Duration != "2h 0m" {
		t.Errorf("Duration with an indexer-reported runtime = %q, want %q", ctx.Duration, "2h 0m")
	}
}

func TestFormatContextExposesMergedCopies(t *testing.T) {
	rel := &release.Release{
		Title:      "Movie.2160p.Remux-GRP",
		DetailsURL: "https://geek.invalid/1",
		Indexer:    "NZBGeek",
		Variants: []*release.Release{
			{Title: "Movie.2160p.Remux-GRP", DetailsURL: "https://slug.invalid/2", Indexer: "DrunkenSlug"},
		},
	}
	ctx := newFormatContext(triage.Candidate{Release: rel}, 1, 1, 0, DefaultServiceName, "Standalone", "Movie", "", false, 0)

	if got := renderFormat(t, "{{.Variants}}", ctx); got != "2" {
		t.Errorf("{{.Variants}} = %q, want %q", got, "2")
	}
	if got := renderFormat(t, "{{.VariantIndexers}}", ctx); got != "NZBGeek, DrunkenSlug" {
		t.Errorf("{{.VariantIndexers}} = %q, want the playing copy first", got)
	}
	// A lone release still reads as one copy, so {{if gt .Variants 1}} is the
	// natural guard rather than a zero check.
	lone := newFormatContext(triage.Candidate{Release: &release.Release{Title: rel.Title, Indexer: "NZBGeek"}}, 1, 1, 0, DefaultServiceName, "Standalone", "Movie", "", false, 0)
	if got := renderFormat(t, "{{.Variants}}", lone); got != "1" {
		t.Errorf("{{.Variants}} for a single copy = %q, want %q", got, "1")
	}
}
