package stremio

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
)

// FormatContext is the data one result exposes to custom name/description
// templates. Fields mirror what the built-in format has access to: the release
// as returned by the indexer, jhin's parse of its title, the final score, and
// the request context. Library-only extras (Caps) are empty for fresh indexer
// hits because their real media properties are unknown until playback.
type FormatContext struct {
	Service      string     // addon name override, or "StreamNZB"
	Stream       string     // stream config name, e.g. "Standalone"
	Content      string     // requested title, e.g. "Ted Lasso"
	ReleaseTitle string     // full release name
	Index        int        // 1-based position in the result list
	Count        int        // total results in the list
	Score        int        // final ranking score (library bonus included)
	Avail        bool       // AvailNZB-verified available
	Library      bool       // served from the local release library
	Size         int64      // bytes
	Indexer      string     // indexer display name
	Grabs        int        // indexer-reported grab count
	Age          string     // humanized age from pub date, e.g. "2y", "37d"
	Duration     string     // humanized runtime, e.g. "1h 52m" (indexer-reported, e.g. Easynews)
	Languages    stringList // parsed language codes
	Caps         string     // ffprobe-verified caps summary (library releases only)

	ParsedTitle string // content title as parsed out of the release name
	Resolution  string
	Quality     string
	Codec       string
	BitDepth    string
	Bitrate     string
	Container   string
	Extension   string
	Group       string
	Edition     string
	Network     string
	Site        string
	Country     string
	Region      string
	Date        string
	Year        int
	Audio       stringList
	Channels    stringList
	HDR         stringList
	Proper      bool
	Repack      bool
	Remastered  bool
	Upscaled    bool
	ThreeD      bool
	Scene       bool
	Retail      bool
	Hardcoded   bool
	Dubbed      bool
	Subbed      bool
	Commentary  bool
	Complete    bool
	Documentary bool
	Unrated     bool
	Uncensored  bool
	PPV         bool
	Season      int
	Episode     int
	Seasons     intList
	Episodes    intList
	EpisodeCode string
	Volumes     intList
}

// stringList and intList render as comma-separated text instead of Go's
// bracketed "[a b]" slice format, so {{.HDR}} or {{.Languages}} never leak
// brackets into results. They stay plain slices for {{range}}, {{index}} and
// {{join}}.
type stringList []string

func (l stringList) String() string { return strings.Join(l, ", ") }

type intList []int

func (l intList) String() string {
	parts := make([]string, len(l))
	for i, n := range l {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ", ")
}

// templateText coerces any template value to the text it renders as, so the
// string helpers also accept list fields and other non-string values:
// {{.HDR | upper}}, {{replace .Languages "en" "english"}}.
func templateText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []string:
		return stringList(t).String()
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// sortedStrings and sortedInts return sorted copies so templates can never
// mutate the FormatContext slices they render.
func sortedStrings(src []string, desc bool) stringList {
	out := slices.Clone(src)
	slices.SortStableFunc(out, func(a, b string) int {
		c := strings.Compare(strings.ToLower(a), strings.ToLower(b))
		if c == 0 {
			c = strings.Compare(a, b)
		}
		if desc {
			c = -c
		}
		return c
	})
	return stringList(out)
}

func sortedInts(src []int, desc bool) intList {
	out := slices.Clone(src)
	slices.Sort(out)
	if desc {
		slices.Reverse(out)
	}
	return intList(out)
}

// sortedCopy sorts list values (case-insensitively for strings, numerically
// for ints) and passes everything else through untouched, so {{sortAsc .HDR}}
// composes with join/range/index without special cases.
func sortedCopy(v any, desc bool) any {
	switch t := v.(type) {
	case stringList:
		return sortedStrings(t, desc)
	case []string:
		return sortedStrings(t, desc)
	case intList:
		return sortedInts(t, desc)
	case []int:
		return sortedInts(t, desc)
	}
	return v
}

// templateExists reports whether a value would render as something visible:
// non-empty lists, non-blank strings, non-zero numbers, true booleans.
func templateExists(v any) bool {
	switch t := v.(type) {
	case stringList:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case intList:
		return len(t) > 0
	case []int:
		return len(t) > 0
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	}
	return strings.TrimSpace(templateText(v)) != ""
}

// templateLength returns element count for lists and rune count for
// everything else (unlike the built-in len, which counts bytes on strings).
func templateLength(v any) int {
	switch t := v.(type) {
	case stringList:
		return len(t)
	case []string:
		return len(t)
	case intList:
		return len(t)
	case []int:
		return len(t)
	}
	return len([]rune(templateText(v)))
}

// firstElem/lastElem pick a list edge element; non-list values pass through.
func firstElem(v any) any {
	switch t := v.(type) {
	case stringList:
		if len(t) == 0 {
			return ""
		}
		return t[0]
	case []string:
		if len(t) == 0 {
			return ""
		}
		return t[0]
	case intList:
		if len(t) == 0 {
			return ""
		}
		return t[0]
	case []int:
		if len(t) == 0 {
			return ""
		}
		return t[0]
	}
	return v
}

func lastElem(v any) any {
	switch t := v.(type) {
	case stringList:
		if len(t) == 0 {
			return ""
		}
		return t[len(t)-1]
	case []string:
		if len(t) == 0 {
			return ""
		}
		return t[len(t)-1]
	case intList:
		if len(t) == 0 {
			return ""
		}
		return t[len(t)-1]
	case []int:
		if len(t) == 0 {
			return ""
		}
		return t[len(t)-1]
	}
	return v
}

// smallcapsLetters maps a-z to their Unicode small-caps forms (q and x have no
// dedicated small-caps codepoint; ǫ and x are the conventional stand-ins).
var smallcapsLetters = []rune("ᴀʙᴄᴅᴇꜰɢʜɪᴊᴋʟᴍɴᴏᴘǫʀꜱᴛᴜᴠᴡxʏᴢ")

// New multi-argument helpers take the value LAST (like default) so they chain
// in pipelines: {{.ParsedTitle | title | truncate 24}}. replace and join keep
// their original value-first signatures for backwards compatibility.
var formatTemplateFuncs = template.FuncMap{
	"size":  humanSize,
	"score": formatScoreSigned,
	"join":  strings.Join,
	"upper": func(v any) string { return strings.ToUpper(templateText(v)) },
	"lower": func(v any) string { return strings.ToLower(templateText(v)) },
	"trim":  func(v any) string { return strings.TrimSpace(templateText(v)) },
	"replace": func(v any, old, new string) string {
		return strings.ReplaceAll(templateText(v), old, new)
	},
	// default renders val, or def when val is empty. Pipeline-friendly:
	// {{.Group | default "unknown"}}.
	"default": func(def string, val any) string {
		if s := templateText(val); strings.TrimSpace(s) != "" {
			return s
		}
		return def
	},
	"exists":   templateExists,
	"length":   templateLength,
	"sort":     func(v any) any { return sortedCopy(v, false) },
	"sortAsc":  func(v any) any { return sortedCopy(v, false) },
	"sortDesc": func(v any) any { return sortedCopy(v, true) },
	"first":    firstElem,
	"last":     lastElem,
	"title":    func(v any) string { return cases.Title(language.Und).String(templateText(v)) },
	// truncate cuts to n runes and marks the cut with an ellipsis, matching
	// what stream titles need most: {{.ParsedTitle | truncate 24}}.
	"truncate": func(n int, v any) string {
		if n <= 0 {
			return ""
		}
		runes := []rune(templateText(v))
		if len(runes) <= n {
			return string(runes)
		}
		return string(runes[:n]) + "…"
	},
	// translate maps runes by position, "0123456789" → "₀₁₂₃₄₅₆₇₈₉"; runes in
	// from with no counterpart in to are removed.
	"translate": func(from, to string, v any) string {
		fromRunes, toRunes := []rune(from), []rune(to)
		mapped := make(map[rune]rune, len(fromRunes))
		deleted := make(map[rune]bool)
		for i, r := range fromRunes {
			if i < len(toRunes) {
				mapped[r] = toRunes[i]
			} else {
				deleted[r] = true
			}
		}
		var b strings.Builder
		for _, r := range templateText(v) {
			if deleted[r] {
				continue
			}
			if m, ok := mapped[r]; ok {
				r = m
			}
			b.WriteRune(r)
		}
		return b.String()
	},
	"remove": func(sub string, v any) string {
		return strings.ReplaceAll(templateText(v), sub, "")
	},
	"smallcaps": func(v any) string {
		var b strings.Builder
		for _, r := range strings.ToLower(templateText(v)) {
			if r >= 'a' && r <= 'z' {
				r = smallcapsLetters[r-'a']
			}
			b.WriteRune(r)
		}
		return b.String()
	},
	"contains": func(sub string, v any) bool {
		return strings.Contains(templateText(v), sub)
	},
	"hasPrefix": func(prefix string, v any) bool {
		return strings.HasPrefix(templateText(v), prefix)
	},
	"hasSuffix": func(suffix string, v any) bool {
		return strings.HasSuffix(templateText(v), suffix)
	},
}

func humanSize(size int64) string {
	if size <= 0 {
		return ""
	}
	if gb := float64(size) / 1e9; gb >= 1 {
		return fmt.Sprintf("%.2f GB", gb)
	}
	return fmt.Sprintf("%.0f MB", float64(size)/1e6)
}

func formatScoreSigned(score int) string {
	if score > 0 {
		return fmt.Sprintf("+%d", score)
	}
	return fmt.Sprintf("%d", score)
}

// humanDuration renders an indexer-reported runtime in seconds as "1h 52m",
// "52m" or "45s", or "" when unknown.
func humanDuration(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	total := int(seconds + 0.5)
	hours, minutes := total/3600, (total%3600)/60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return fmt.Sprintf("%ds", total)
	}
}

// humanAge renders how long ago a newznab pubDate was, or "" when unparseable.
func humanAge(pubDate string) string {
	parsed, ok := release.ParseDate(pubDate)
	if !ok {
		return ""
	}
	age := time.Since(parsed)
	switch {
	case age < 0:
		return ""
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	case age < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	default:
		return fmt.Sprintf("%.1fy", age.Hours()/(365*24))
	}
}

func newFormatContext(cand triage.Candidate, index, count int, service, streamID, contentTitle, caps string, avail bool) FormatContext {
	if service == "" {
		service = DefaultServiceName
	}
	ctx := FormatContext{
		Service: service,
		Stream:  streamID,
		Content: contentTitle,
		Index:   index,
		Count:   count,
		Score:   cand.Score,
		Avail:   avail,
		Caps:    caps,
	}
	rel := cand.Release
	if rel != nil {
		ctx.ReleaseTitle = rel.Title
		ctx.Library = rel.IsLibraryResult()
		ctx.Size = rel.Size
		ctx.Indexer = indexerNameFromRelease(rel)
		ctx.Grabs = rel.Grabs
		ctx.Age = humanAge(rel.PubDate)
		ctx.Duration = humanDuration(rel.Duration)
		ctx.Languages = rel.Languages
	}
	meta := cand.Metadata
	if meta == nil && rel != nil {
		meta = parser.ParseReleaseTitle(rel.Title)
	}
	if meta != nil {
		ctx.ParsedTitle = meta.Title
		ctx.Resolution = meta.Resolution
		ctx.Quality = meta.Quality
		ctx.Codec = meta.Codec
		ctx.BitDepth = meta.BitDepth
		ctx.Bitrate = meta.Bitrate
		ctx.Container = meta.Container
		ctx.Extension = meta.Extension
		ctx.Group = meta.Group
		ctx.Edition = meta.Edition
		ctx.Network = meta.Network
		ctx.Site = meta.Site
		ctx.Country = meta.Country
		ctx.Region = meta.Region
		ctx.Date = meta.Date
		ctx.Year = meta.Year
		ctx.Audio = meta.Audio
		ctx.Channels = meta.Channels
		ctx.HDR = meta.HDR
		ctx.Proper = meta.Proper
		ctx.Repack = meta.Repack
		ctx.Remastered = meta.Remastered
		ctx.Upscaled = meta.Upscaled
		ctx.ThreeD = meta.ThreeD
		ctx.Scene = meta.Scene
		ctx.Retail = meta.Retail
		ctx.Hardcoded = meta.Hardcoded
		ctx.Dubbed = meta.Dubbed
		ctx.Subbed = meta.Subbed
		ctx.Commentary = meta.Commentary
		ctx.Complete = meta.Complete
		ctx.Documentary = meta.Documentary
		ctx.Unrated = meta.Unrated
		ctx.Uncensored = meta.Uncensored
		ctx.PPV = meta.PPV
		ctx.Season = meta.Season
		ctx.Episode = meta.Episode
		ctx.Seasons = meta.Seasons
		ctx.Episodes = meta.Episodes
		ctx.EpisodeCode = meta.EpisodeCode
		ctx.Volumes = meta.Volumes
		if len(meta.Languages) > 0 {
			ctx.Languages = meta.Languages
		}
	}
	return ctx
}

// resultFormat holds the compiled custom templates for one response. A nil
// *resultFormat (or a nil template inside) means the built-in format applies.
type resultFormat struct {
	name        *template.Template
	description *template.Template
}

type cachedFormatTemplate struct {
	tpl *template.Template
	err error
}

// formatTemplateCache holds compiled config templates keyed by their text. The
// config only ever contributes a handful of distinct texts, so the map stays
// tiny; preview compiles bypass it (arbitrary user input while typing).
var formatTemplateCache sync.Map

func compileFormatTemplate(text string) (*template.Template, error) {
	if v, ok := formatTemplateCache.Load(text); ok {
		cached := v.(*cachedFormatTemplate)
		return cached.tpl, cached.err
	}
	tpl, err := template.New("format").Funcs(formatTemplateFuncs).Parse(text)
	if err != nil {
		tpl = nil
	}
	formatTemplateCache.Store(text, &cachedFormatTemplate{tpl: tpl, err: err})
	return tpl, err
}

// resultFormatForStream resolves the custom templates for one stream — its
// bound format profile first, the legacy inline templates as the fallback —
// or nil when none are set (or none compile) so callers keep the built-in
// format. A binding to a since-deleted profile also renders built-in: the
// stale name must not resurrect whatever inline templates the migration
// cleared.
func (s *Server) resultFormatForStream(stream *auth.Stream) *resultFormat {
	if stream == nil {
		return nil
	}
	nameText := strings.TrimSpace(stream.ResultNameTemplate)
	descText := strings.TrimSpace(stream.ResultDescriptionTemplate)
	if profileName := strings.TrimSpace(stream.FormatProfileName); profileName != "" {
		nameText, descText = "", ""
		if fp := s.currentConfig().FormatProfileByName(profileName); fp != nil {
			nameText = strings.TrimSpace(fp.ResultNameTemplate)
			descText = strings.TrimSpace(fp.ResultDescriptionTemplate)
		}
	}
	if nameText == "" && descText == "" {
		return nil
	}
	rf := &resultFormat{}
	if nameText != "" {
		if tpl, err := compileFormatTemplate(nameText); err == nil {
			rf.name = tpl
		} else {
			logger.Debug("Result name template invalid, using built-in format", "err", err)
		}
	}
	if descText != "" {
		if tpl, err := compileFormatTemplate(descText); err == nil {
			rf.description = tpl
		} else {
			logger.Debug("Result description template invalid, using built-in format", "err", err)
		}
	}
	if rf.name == nil && rf.description == nil {
		return nil
	}
	return rf
}

// maxFormattedResultRunes caps template output so a runaway template cannot
// bloat stream responses.
const maxFormattedResultRunes = 1000

// collapseBlankLines drops whitespace-only lines and trailing per-line spaces
// from rendered output, so a conditional that renders nothing doesn't leave an
// empty line behind ({{if .HDR}}…{{end}} on its own line with no HDR).
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// renderResultTemplate executes tpl over ctx. Any failure — nil template,
// execution error, empty output — falls back to the built-in string so a bad
// template can never break stream responses.
func renderResultTemplate(tpl *template.Template, ctx FormatContext, fallback string) string {
	if tpl == nil {
		return fallback
	}
	var b strings.Builder
	if err := tpl.Execute(&b, ctx); err != nil {
		logger.Debug("Result template execution failed, using built-in format", "err", err)
		return fallback
	}
	out := strings.TrimSpace(collapseBlankLines(b.String()))
	if out == "" {
		return fallback
	}
	if runes := []rune(out); len(runes) > maxFormattedResultRunes {
		out = string(runes[:maxFormattedResultRunes])
	}
	return out
}

// ValidateResultTemplates reports whether the supplied template texts compile.
// Empty texts are valid (they mean the built-in format). Used by the save
// paths so a stream config can never persist templates that would silently
// fall back at render time.
func ValidateResultTemplates(nameText, descText string) error {
	if text := strings.TrimSpace(nameText); text != "" {
		if _, err := template.New("name").Funcs(formatTemplateFuncs).Parse(text); err != nil {
			return fmt.Errorf("result name template: %w", err)
		}
	}
	if text := strings.TrimSpace(descText); text != "" {
		if _, err := template.New("description").Funcs(formatTemplateFuncs).Parse(text); err != nil {
			return fmt.Errorf("result description template: %w", err)
		}
	}
	return nil
}

// --- Preview ---

type FormatPreviewSample struct {
	Label       string `json:"label"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type FormatPreviewResult struct {
	Samples          []FormatPreviewSample `json:"samples"`
	NameError        string                `json:"name_error,omitempty"`
	DescriptionError string                `json:"description_error,omitempty"`
}

type formatPreviewFixture struct {
	label    string
	content  string
	title    string
	indexer  string
	size     int64
	grabs    int
	pubDate  string
	score    int
	avail    bool
	library  bool
	caps     string
	duration float64
}

func formatPreviewFixtures() []formatPreviewFixture {
	return []formatPreviewFixture{
		{
			label:    "4K movie · fresh indexer hit",
			content:  "Dune: Part Two",
			title:    "Dune.Part.Two.2024.2160p.WEB-DL.DV.HDR10.HEVC.DDP5.1.Atmos-FLUX",
			indexer:  "DrunkenSlug",
			size:     24_800_000_000,
			grabs:    154,
			pubDate:  time.Now().Add(-40 * 24 * time.Hour).Format(time.RFC1123Z),
			score:    4200,
			duration: 165 * 60,
		},
		{
			label:   "1080p episode · AvailNZB verified",
			content: "Ted Lasso",
			title:   "Ted.Lasso.S04E01.NORDiC.1080p.ATV.WEB-DL.H.265-NORViNE",
			indexer: "altHUB",
			size:    1_832_627_684,
			grabs:   37,
			pubDate: time.Now().Add(-3 * 24 * time.Hour).Format(time.RFC1123Z),
			score:   2850,
			avail:   true,
		},
		{
			label:   "Library hit · ffprobe verified",
			content: "Ted Lasso",
			title:   "Ted.Lasso.S04E01.NORDiC.1080p.ATV.WEB-DL.H.265-NORViNE",
			indexer: "StreamNZB Library - altHUB",
			size:    1_775_983_877,
			grabs:   37,
			pubDate: time.Now().Add(-3 * 24 * time.Hour).Format(time.RFC1123Z),
			score:   3350,
			avail:   true,
			library: true,
			caps:    "hevc Main 10 1080p 10-bit",
		},
	}
}

// RenderFormatPreview compiles the supplied templates and renders them over
// canned sample results, exactly as the live stream path would. Empty template
// text (or one that fails to compile) previews the built-in format instead, so
// the preview always shows what a client would actually receive.
func RenderFormatPreview(nameText, descText string) *FormatPreviewResult {
	res := &FormatPreviewResult{}
	var nameTpl, descTpl *template.Template
	if text := strings.TrimSpace(nameText); text != "" {
		tpl, err := template.New("name").Funcs(formatTemplateFuncs).Parse(text)
		if err != nil {
			res.NameError = err.Error()
		} else {
			nameTpl = tpl
		}
	}
	if text := strings.TrimSpace(descText); text != "" {
		tpl, err := template.New("description").Funcs(formatTemplateFuncs).Parse(text)
		if err != nil {
			res.DescriptionError = err.Error()
		} else {
			descTpl = tpl
		}
	}

	fixtures := formatPreviewFixtures()
	for i, fx := range fixtures {
		rel := &release.Release{
			Title:     fx.title,
			Size:      fx.size,
			Indexer:   fx.indexer,
			IsLibrary: fx.library,
			PubDate:   fx.pubDate,
			Grabs:     fx.grabs,
			Duration:  fx.duration,
		}
		cand := triage.Candidate{Release: rel, Score: fx.score, Metadata: parser.ParseReleaseTitle(fx.title)}
		ctx := newFormatContext(cand, i+1, len(fixtures), DefaultServiceName, "Standalone", fx.content, fx.caps, fx.avail)

		builtinName := "StreamNZB\nStandalone"
		if fx.avail {
			builtinName = "⚡ " + builtinName
		}
		builtinDesc := buildAIOStreamDescription(fx.content, fx.title, ctx.Indexer, fx.score, true, fx.caps)

		res.Samples = append(res.Samples, FormatPreviewSample{
			Label:       fx.label,
			Name:        renderResultTemplate(nameTpl, ctx, builtinName),
			Description: renderResultTemplate(descTpl, ctx, builtinDesc),
		})
	}
	return res
}
