package stremio

import (
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

// aioconvert translates AIOStreams custom-formatter templates
// (https://docs.aiostreams.viren070.me/reference/custom-formatter/) into
// StreamNZB Go result templates: {stream.field::modifier[...]} expressions,
// modifier chains, conditionals with ["true"||"false"] branches, and
// {? ... ?} optional groups. The conversion is best-effort — constructs with
// no StreamNZB counterpart behave like missing fields: conditionals fall back
// to their false/missing branch, bare expressions drop, and unsupported
// modifiers render their value unformatted. Everything dropped or
// approximated is reported as a warning.

// AIOFormatConversion is one converted template plus everything that did not
// translate cleanly.
type AIOFormatConversion struct {
	Template string   `json:"template"`
	Warnings []string `json:"warnings"`
}

// ConvertAIOStreamsFormat converts one AIOStreams formatter text into a
// StreamNZB Go template. It never fails: unconvertible pieces pass through
// verbatim with a warning.
func ConvertAIOStreamsFormat(text string) AIOFormatConversion {
	p := &aioParser{src: []rune(text)}
	nodes := p.parseSequence(false)
	e := &aioEmitter{seen: map[string]bool{}}
	out := e.emitNodes(nodes)
	// A converted template must always compile; if it does not, something in
	// the verbatim passthrough broke Go template syntax — surface it.
	if strings.TrimSpace(out) != "" {
		if _, err := template.New("converted").Funcs(formatTemplateFuncs).Parse(out); err != nil {
			e.warnf("converted template does not compile (%v) — please adjust it by hand", err)
		}
	}
	return AIOFormatConversion{Template: out, Warnings: e.warnings}
}

// --- Parse tree ---

type aioNode interface{}

// aioText is literal output text between expressions.
type aioText string

// aioModifier is one ::token in a chain. Comparators keep their symbol as the
// name (">=", "=", "~", "$", "^") with the comparison value in args[0].
type aioModifier struct {
	name string
	args []string
}

// aioExpr is one {field::modifiers[branches]} expression.
type aioExpr struct {
	field     string // lowercased dotted field, empty for literals
	literal   string // {'text'::mods} quoted-literal form
	isLiteral bool
	chain     []aioModifier
	branches  [][]aioNode // nil when not conditional; 2 or 3 entries
	raw       string      // original source, for verbatim fallback
}

// aioGroup is a {? ... ?} optional group.
type aioGroup struct {
	children []aioNode
}

// --- Parser ---

type aioParser struct {
	src []rune
	pos int
}

func (p *aioParser) eof() bool { return p.pos >= len(p.src) }

func (p *aioParser) peek() rune {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *aioParser) has(s string) bool {
	return strings.HasPrefix(string(p.src[p.pos:]), s)
}

func (p *aioParser) skipSpaces() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.pos++
	}
}

// parseSequence parses text + expressions until EOF, or until "?}" when
// inGroup is set (the terminator is left unconsumed).
func (p *aioParser) parseSequence(inGroup bool) []aioNode {
	var nodes []aioNode
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			nodes = append(nodes, aioText(text.String()))
			text.Reset()
		}
	}
	for !p.eof() {
		if inGroup && p.has("?}") {
			break
		}
		if p.peek() == '{' {
			if p.has("{?") {
				flush()
				nodes = append(nodes, p.parseGroup())
				continue
			}
			if expr, ok := p.parseExpr(); ok {
				flush()
				nodes = append(nodes, expr)
				continue
			}
			// Malformed expression: keep the brace as literal text.
		}
		text.WriteRune(p.peek())
		p.pos++
	}
	flush()
	return nodes
}

// parseGroup parses a {? ... ?} optional group; the opening "{?" is at p.pos.
func (p *aioParser) parseGroup() *aioGroup {
	p.pos += 2 // consume "{?"
	children := p.parseSequence(true)
	if p.has("?}") {
		p.pos += 2
	}
	return &aioGroup{children: children}
}

// parseExpr parses one {field::modifiers[branches]} expression starting at
// the "{" under p.pos. On malformed input it restores the position and
// reports false so the caller treats the brace as text.
func (p *aioParser) parseExpr() (*aioExpr, bool) {
	start := p.pos
	p.pos++ // consume "{"
	expr := &aioExpr{}

	if p.peek() == '\'' || p.peek() == '"' {
		lit, ok := p.parseQuoted(p.peek())
		if !ok {
			p.pos = start
			return nil, false
		}
		expr.isLiteral = true
		expr.literal = lit
	} else {
		fieldStart := p.pos
		for !p.eof() && p.peek() != ':' && p.peek() != '[' && p.peek() != '}' && p.peek() != '{' && p.peek() != '\n' {
			p.pos++
		}
		field := strings.TrimSpace(string(p.src[fieldStart:p.pos]))
		if field == "" || !strings.Contains(field, ".") {
			p.pos = start
			return nil, false
		}
		expr.field = strings.ToLower(field)
	}

	for p.has("::") {
		p.pos += 2
		mod, ok := p.parseModifier()
		if !ok {
			p.pos = start
			return nil, false
		}
		expr.chain = append(expr.chain, mod)
	}

	if p.peek() == '[' {
		branches, ok := p.parseBranches()
		if !ok {
			p.pos = start
			return nil, false
		}
		expr.branches = branches
	}

	if p.peek() != '}' {
		p.pos = start
		return nil, false
	}
	p.pos++
	expr.raw = string(p.src[start:p.pos])
	return expr, true
}

// parseQuoted reads a 'quoted' or "quoted" string with backslash escapes; the
// opening quote is at p.pos.
func (p *aioParser) parseQuoted(quote rune) (string, bool) {
	p.pos++
	var b strings.Builder
	for !p.eof() {
		r := p.peek()
		if r == '\\' && p.pos+1 < len(p.src) {
			p.pos += 2
			b.WriteRune(p.src[p.pos-1])
			continue
		}
		if r == quote {
			p.pos++
			return b.String(), true
		}
		b.WriteRune(r)
		p.pos++
	}
	return "", false
}

// aioComparators are the check prefixes; longest first so ">=" wins over ">".
var aioComparators = []string{">=", "<=", ">", "<", "=", "~", "$", "^"}

func (p *aioParser) parseModifier() (aioModifier, bool) {
	for _, cmp := range aioComparators {
		if p.has(cmp) {
			p.pos += len(cmp)
			valStart := p.pos
			for !p.eof() && p.peek() != ':' && p.peek() != '[' && p.peek() != '}' {
				p.pos++
			}
			val := strings.TrimSpace(string(p.src[valStart:p.pos]))
			val = strings.Trim(val, `'"`)
			return aioModifier{name: cmp, args: []string{val}}, true
		}
	}

	nameStart := p.pos
	for !p.eof() {
		r := p.peek()
		if r == '(' || r == ':' || r == '[' || r == '}' {
			break
		}
		if !(r == '.' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return aioModifier{}, false
		}
		p.pos++
	}
	name := string(p.src[nameStart:p.pos])
	if name == "" {
		return aioModifier{}, false
	}
	mod := aioModifier{name: strings.ToLower(name)}
	if strings.Contains(name, ".") {
		// A field token following ::and/::or in a chained condition — keep
		// the original case-insensitive dotted name.
		mod.name = strings.ToLower(name)
	}

	if p.peek() == '(' {
		p.pos++
		for {
			p.skipSpaces()
			if p.eof() {
				return aioModifier{}, false
			}
			if p.peek() == ')' {
				p.pos++
				break
			}
			var arg string
			if p.peek() == '\'' || p.peek() == '"' {
				quoted, ok := p.parseQuoted(p.peek())
				if !ok {
					return aioModifier{}, false
				}
				arg = quoted
			} else {
				argStart := p.pos
				for !p.eof() && p.peek() != ',' && p.peek() != ')' {
					p.pos++
				}
				arg = strings.TrimSpace(string(p.src[argStart:p.pos]))
			}
			mod.args = append(mod.args, arg)
			p.skipSpaces()
			if p.peek() == ',' {
				p.pos++
			}
		}
	}
	return mod, true
}

// parseBranches parses ["true"||"false"] or ["true"||"false"||"missing"];
// the "[" is at p.pos. Branch text is quote-delimited but quotes inside
// nested {...}/[...] constructs need no escaping, so the closing quote only
// counts at nesting depth zero.
func (p *aioParser) parseBranches() ([][]aioNode, bool) {
	p.pos++ // consume "["
	var branches [][]aioNode
	for {
		p.skipSpaces()
		if p.eof() {
			return nil, false
		}
		quote := p.peek()
		if quote != '"' && quote != '\'' {
			return nil, false
		}
		raw, ok := p.scanBranchText(quote)
		if !ok {
			return nil, false
		}
		sub := &aioParser{src: []rune(raw)}
		branches = append(branches, sub.parseSequence(false))
		p.skipSpaces()
		if p.has("||") {
			p.pos += 2
			continue
		}
		if p.peek() == ']' {
			p.pos++
			return branches, true
		}
		return nil, false
	}
}

// scanBranchText reads one quoted branch, tracking {} nesting so quotes
// inside nested expressions don't terminate it; the opening quote is at
// p.pos. Only braces count for nesting — square brackets appear unbalanced as
// literal text in real formatters ("[P2P] "), and nested conditionals'
// brackets always sit inside braces anyway.
func (p *aioParser) scanBranchText(quote rune) (string, bool) {
	p.pos++
	depth := 0
	var b strings.Builder
	for !p.eof() {
		r := p.peek()
		switch {
		case r == '\\' && p.pos+1 < len(p.src):
			p.pos += 2
			b.WriteRune(p.src[p.pos-1])
			continue
		case r == '{':
			depth++
		case r == '}':
			if depth > 0 {
				depth--
			}
		case r == quote && depth == 0:
			p.pos++
			return b.String(), true
		}
		b.WriteRune(r)
		p.pos++
	}
	return "", false
}

// --- Emitter ---

// aioKind tracks the Go-side type of the expression being built, so checks
// and transforms emit valid template code.
type aioKind int

const (
	kindString aioKind = iota
	kindStrList
	kindIntList
	kindNumber
	kindBool
	kindConst // pre-quoted Go string literal, e.g. `"usenet"`
)

type aioField struct {
	expr string
	kind aioKind
	note string // non-empty: approximation, warned once on use
}

var aioFieldMap = map[string]aioField{
	"config.addonname":  {expr: ".Service", kind: kindString},
	"addon.name":        {expr: ".Service", kind: kindString, note: "{addon.name} mapped to {{.Service}}"},
	"service.id":        {expr: ".Service", kind: kindString, note: "{service.*} mapped to {{.Service}} — StreamNZB has no debrid services"},
	"service.shortname": {expr: ".Service", kind: kindString, note: "{service.*} mapped to {{.Service}} — StreamNZB has no debrid services"},
	"service.name":      {expr: ".Service", kind: kindString, note: "{service.*} mapped to {{.Service}} — StreamNZB has no debrid services"},
	"service.cached":    {expr: ".Avail", kind: kindBool, note: "{service.cached} mapped to {{.Avail}} (AvailNZB-verified)"},

	"stream.type":       {expr: `"usenet"`, kind: kindConst, note: `{stream.type} is always "usenet" in StreamNZB`},
	"stream.proxied":    {expr: "false", kind: kindBool, note: "{stream.proxied} is always false in StreamNZB"},
	"stream.private":    {expr: "false", kind: kindBool, note: "{stream.private} is always false in StreamNZB"},
	"stream.freeleech":  {expr: "false", kind: kindBool, note: "{stream.freeleech} is always false in StreamNZB"},
	"stream.message":    {expr: `""`, kind: kindConst, note: "{stream.message} has no StreamNZB equivalent and renders empty"},
	"stream.library":    {expr: ".Library", kind: kindBool},
	"stream.indexer":    {expr: ".Indexer", kind: kindString},
	"stream.seadex":     {expr: "(or .Seadex.Best .Seadex.Alternative)", kind: kindBool, note: "{stream.seadex} mapped to the per-title SeaDex recommendation (best or alternative)"},
	"stream.filename":   {expr: ".ReleaseTitle", kind: kindString},
	"stream.foldername": {expr: ".ReleaseTitle", kind: kindString, note: "{stream.folderName} mapped to {{.ReleaseTitle}}"},
	"stream.title":      {expr: ".ParsedTitle", kind: kindString},
	"stream.size":       {expr: ".Size", kind: kindNumber},
	"stream.foldersize": {expr: ".Size", kind: kindNumber, note: "{stream.folderSize} mapped to {{.Size}}"},
	"stream.bitrate":    {expr: ".Bitrate", kind: kindString, note: "{stream.bitrate} mapped to the {{.Bitrate}} text (parsed or estimated)"},
	"stream.container":  {expr: ".Container", kind: kindString},
	"stream.extension":  {expr: ".Extension", kind: kindString},

	"stream.quality":    {expr: ".Quality", kind: kindString},
	"stream.resolution": {expr: ".Resolution", kind: kindString},
	"stream.visualtags": {expr: ".HDR", kind: kindStrList},
	"stream.encode":     {expr: ".Codec", kind: kindString},
	"stream.network":    {expr: ".Network", kind: kindString},

	"stream.audiotags":     {expr: ".Audio", kind: kindStrList},
	"stream.audiochannels": {expr: ".Channels", kind: kindStrList},

	"stream.languages":          {expr: ".Languages", kind: kindStrList},
	"stream.languagecodes":      {expr: ".Languages", kind: kindStrList},
	"stream.smalllanguagecodes": {expr: "(smallcaps .Languages)", kind: kindString},
	"stream.ulanguages":         {expr: ".Languages", kind: kindStrList, note: "user-configured language lists mapped to {{.Languages}}"},
	"stream.ulanguagecodes":     {expr: ".Languages", kind: kindStrList, note: "user-configured language lists mapped to {{.Languages}}"},
	"stream.usmalllanguagecodes": {expr: "(smallcaps .Languages)", kind: kindString,
		note: "user-configured language lists mapped to {{.Languages}}"},
	"stream.languageemojis":  {expr: ".Languages", kind: kindStrList, note: "language emojis are not available; mapped to {{.Languages}} codes"},
	"stream.ulanguageemojis": {expr: ".Languages", kind: kindStrList, note: "language emojis are not available; mapped to {{.Languages}} codes"},
	"stream.dubbed":          {expr: ".Dubbed", kind: kindBool},
	"stream.subbed":          {expr: ".Subbed", kind: kindBool},
	"stream.subtitles":       {expr: ".Subbed", kind: kindBool, note: "subtitle language lists are not available; {stream.subtitles} mapped to the {{.Subbed}} flag"},
	"stream.usubtitles":      {expr: ".Subbed", kind: kindBool, note: "subtitle language lists are not available; {stream.subtitles} mapped to the {{.Subbed}} flag"},

	"stream.year":         {expr: ".Year", kind: kindNumber},
	"stream.country":      {expr: ".Country", kind: kindString},
	"stream.date":         {expr: ".Date", kind: kindString},
	"stream.releasegroup": {expr: ".Group", kind: kindString},
	"stream.editions":     {expr: ".Edition", kind: kindString, note: "{stream.editions} mapped to the single {{.Edition}} value"},
	"stream.repack":       {expr: ".Repack", kind: kindBool},
	"stream.proper":       {expr: ".Proper", kind: kindBool},
	"stream.uncensored":   {expr: ".Uncensored", kind: kindBool},
	"stream.unrated":      {expr: ".Unrated", kind: kindBool},
	"stream.upscaled":     {expr: ".Upscaled", kind: kindBool},
	"stream.regraded":     {expr: ".Remastered", kind: kindBool, note: "{stream.regraded} mapped to {{.Remastered}}"},

	"stream.seasonpack":        {expr: ".Complete", kind: kindBool, note: "{stream.seasonPack} mapped to {{.Complete}}"},
	"stream.seasons":           {expr: ".Seasons", kind: kindIntList},
	"stream.episodes":          {expr: ".Episodes", kind: kindIntList},
	"stream.folderseasons":     {expr: ".Seasons", kind: kindIntList, note: "{stream.folderSeasons} mapped to {{.Seasons}}"},
	"stream.folderepisodes":    {expr: ".Episodes", kind: kindIntList, note: "{stream.folderEpisodes} mapped to {{.Episodes}}"},
	"stream.formattedseasons":  {expr: ".EpisodeCode", kind: kindString, note: "formatted season/episode fields mapped to {{.EpisodeCode}}"},
	"stream.formattedepisodes": {expr: ".EpisodeCode", kind: kindString, note: "formatted season/episode fields mapped to {{.EpisodeCode}}"},
	"stream.seasonepisode":     {expr: ".EpisodeCode", kind: kindString, note: "formatted season/episode fields mapped to {{.EpisodeCode}}"},

	"stream.seeders":  {expr: ".Grabs", kind: kindNumber, note: "{stream.seeders} mapped to {{.Grabs}}"},
	"stream.age":      {expr: ".Age", kind: kindString},
	"stream.duration": {expr: ".Duration", kind: kindString, note: "{stream.duration} is only available from indexers that report a runtime (e.g. Easynews)"},

	"stream.regexscore": {expr: ".Score", kind: kindNumber, note: "regex score fields mapped to {{.Score}}"},
	"stream.sescore":    {expr: ".Score", kind: kindNumber, note: "regex score fields mapped to {{.Score}}"},

	"metadata.title":   {expr: ".Content", kind: kindString},
	"metadata.season":  {expr: ".Season", kind: kindNumber},
	"metadata.episode": {expr: ".Episode", kind: kindNumber},
	"metadata.year":    {expr: ".Year", kind: kindNumber, note: "{metadata.year} mapped to the release {{.Year}}"},
}

// aioUnsupportedModifiers have no StreamNZB equivalent; they are skipped with
// a warning so the underlying value still renders.
var aioUnsupportedModifiers = map[string]bool{
	"reverse": true, "random": true, "base64": true, "hex": true,
	"octal": true, "binary": true, "star": true, "pstar": true,
	"date": true, "languagecode": true, "languageemoji": true,
}

type aioEmitter struct {
	warnings []string
	seen     map[string]bool
}

func (e *aioEmitter) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if e.seen[msg] {
		return
	}
	e.seen[msg] = true
	e.warnings = append(e.warnings, msg)
}

func (e *aioEmitter) emitNodes(nodes []aioNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch t := n.(type) {
		case aioText:
			b.WriteString(string(t))
		case *aioExpr:
			b.WriteString(e.emitExpr(t))
		case *aioGroup:
			b.WriteString(e.emitGroup(t))
		}
	}
	return b.String()
}

// emitGroup renders {? ... ?} as an if-guard over every mapped field the
// group references, mirroring "renders only when every field has a value". A
// field that can never have a value ({stream.message}) hides the whole group.
func (e *aioEmitter) emitGroup(g *aioGroup) string {
	var kept []string
	for _, c := range e.collectGroupConds(g.children) {
		switch c {
		case "true":
		case "false":
			return ""
		default:
			kept = append(kept, c)
		}
	}
	body := e.emitNodes(g.children)
	if len(kept) == 0 {
		return body
	}
	cond := kept[0]
	for _, c := range kept[1:] {
		cond = foldCond("and", cond, c)
	}
	return "{{if " + stripOuterParens(cond) + "}}" + body + "{{end}}"
}

func (e *aioEmitter) collectGroupConds(nodes []aioNode) []string {
	var conds []string
	seen := map[string]bool{}
	var walk func(nodes []aioNode)
	walk = func(nodes []aioNode) {
		for _, n := range nodes {
			switch t := n.(type) {
			case *aioExpr:
				if t.isLiteral {
					continue
				}
				if field, ok := aioFieldMap[t.field]; ok {
					cond := existsCond(field.expr, field.kind)
					if !seen[cond] {
						seen[cond] = true
						conds = append(conds, cond)
					}
				}
				for _, branch := range t.branches {
					walk(branch)
				}
			case *aioGroup:
				walk(t.children)
			}
		}
	}
	walk(nodes)
	return conds
}

// existsCond builds a truthiness condition for one field expression.
// Constants fold statically to "true"/"false" so downstream conditionals can
// resolve at conversion time.
func existsCond(expr string, kind aioKind) string {
	switch kind {
	case kindBool:
		return expr
	case kindConst:
		if s, err := strconv.Unquote(expr); err == nil {
			if strings.TrimSpace(s) != "" {
				return "true"
			}
			return "false"
		}
	}
	return fmt.Sprintf("(exists %s)", expr)
}

// foldCond combines two conditions, statically folding "true"/"false"
// operands that arise from constant fields ({stream.type::=p2p} can never
// hold in StreamNZB).
func foldCond(op, a, b string) string {
	switch op {
	case "and":
		if a == "false" || b == "false" {
			return "false"
		}
		if a == "true" {
			return b
		}
		if b == "true" {
			return a
		}
	case "or":
		if a == "true" || b == "true" {
			return "true"
		}
		if a == "false" {
			return b
		}
		if b == "false" {
			return a
		}
	}
	return fmt.Sprintf("(%s %s %s)", op, a, b)
}

func stripOuterParens(s string) string {
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return s
	}
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(s)-1 {
				return s
			}
		}
	}
	return s[1 : len(s)-1]
}

func aioQuote(s string) string { return strconv.Quote(s) }

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// emitExpr converts one expression. An unknown field or unconvertible check
// behaves like a missing field: conditionals fall back to their false/missing
// branch, bare expressions drop, and a warning records what happened.
func (e *aioEmitter) emitExpr(x *aioExpr) string {
	if !x.isLiteral {
		switch x.field {
		case "tools.newline":
			return "\n"
		case "tools.removeline":
			// StreamNZB drops empty lines from rendered output already.
			return ""
		}
	}

	var cur string
	var kind aioKind
	if x.isLiteral {
		cur = aioQuote(x.literal)
		kind = kindConst
	} else {
		field, ok := aioFieldMap[x.field]
		if !ok {
			e.warnf("{%s} has no StreamNZB equivalent and was removed", x.field)
			return e.fallbackBranch(x)
		}
		if field.note != "" {
			e.warnf("%s", field.note)
		}
		cur = field.expr
		kind = field.kind
	}
	baseExpr, baseKind := cur, kind

	// Walk the modifier chain, building the value expression and collecting
	// condition clauses (joined by ::and / ::or).
	var conds []string
	var condOps []string // connector before conds[i+1]
	transformed := false
	pendingConnector := false
	flushClauseCond := func(check string) {
		conds = append(conds, check)
	}

	for _, mod := range x.chain {
		if pendingConnector {
			// Expect a field token starting the next clause.
			field, ok := aioFieldMap[mod.name]
			if !ok {
				e.warnf("chained condition references unknown field %q; {%s} was removed", mod.name, x.raw)
				return e.fallbackBranch(x)
			}
			if field.note != "" {
				e.warnf("%s", field.note)
			}
			cur, kind = field.expr, field.kind
			pendingConnector = false
			continue
		}
		switch mod.name {
		case "and", "or":
			if len(conds) == len(condOps) {
				// No explicit check yet for this clause: use truthiness.
				flushClauseCond(existsCond(cur, kind))
			}
			condOps = append(condOps, mod.name)
			pendingConnector = true
			continue
		case "xor":
			e.warnf("::xor conditions are not supported; {%s} was removed", x.raw)
			return e.fallbackBranch(x)
		}

		if check, isCheck, ok := e.buildCheck(cur, kind, mod, x.raw); isCheck {
			if !ok {
				return e.fallbackBranch(x)
			}
			if len(conds) > len(condOps) {
				// The clause already has a condition; a follow-up check
				// refines it ({field::exists::isfalse} negates the
				// existence test).
				switch mod.name {
				case "exists", "istrue":
					// A boolean condition is already truthy as-is.
				case "isfalse":
					last := conds[len(conds)-1]
					switch last {
					case "true":
						conds[len(conds)-1] = "false"
					case "false":
						conds[len(conds)-1] = "true"
					default:
						conds[len(conds)-1] = fmt.Sprintf("(not %s)", last)
					}
				default:
					e.warnf("stacked ::%s check is not supported; {%s} was removed", mod.name, x.raw)
					return e.fallbackBranch(x)
				}
				continue
			}
			flushClauseCond(check)
			continue
		}

		cur, kind = e.applyTransform(cur, kind, mod)
		transformed = true
	}

	// A trailing clause with no explicit check contributes its truthiness
	// ({a::exists::or::b[...]}: the b clause is a bare existence test).
	if len(condOps) > 0 && len(conds) == len(condOps) {
		flushClauseCond(existsCond(cur, kind))
	}

	// Combine condition clauses left-to-right, folding constant clauses away.
	var cond string
	if len(conds) > 0 {
		cond = conds[0]
		for i, c := range conds[1:] {
			op := "and"
			if i < len(condOps) {
				op = condOps[i]
			}
			cond = foldCond(op, cond, c)
		}
	}

	if x.branches != nil {
		if cond == "" {
			cond = existsCond(cur, kind)
		}
		return e.emitConditional(cond, baseExpr, baseKind, x.branches)
	}
	if cond != "" {
		// A bare check without branches renders true/false, like AIOStreams.
		return "{{" + stripOuterParens(cond) + "}}"
	}
	if kind == kindConst && !transformed {
		// A bare literal or constant field renders as its plain text.
		if unquoted, err := strconv.Unquote(cur); err == nil {
			return unquoted
		}
	}
	return "{{" + stripOuterParens(cur) + "}}"
}

// fallbackBranch renders what a conditional falls back to when its field or
// check has no StreamNZB equivalent: the missing branch when present,
// otherwise the false branch, otherwise nothing — mirroring how AIOStreams
// itself renders a missing field.
func (e *aioEmitter) fallbackBranch(x *aioExpr) string {
	if len(x.branches) > 2 {
		return e.emitNodes(x.branches[2])
	}
	if len(x.branches) > 1 {
		return e.emitNodes(x.branches[1])
	}
	return ""
}

func (e *aioEmitter) emitConditional(cond, baseExpr string, baseKind aioKind, branches [][]aioNode) string {
	// Conditions over constant fields resolve at conversion time
	// ({stream.type::=p2p[...]} can never hold), so only the surviving branch
	// is emitted.
	switch cond {
	case "true":
		return e.emitNodes(branches[0])
	case "false":
		if len(branches) > 1 {
			return e.emitNodes(branches[1])
		}
		return ""
	}
	branchText := make([]string, len(branches))
	for i, branch := range branches {
		branchText[i] = e.emitNodes(branch)
	}
	trueText := branchText[0]
	falseText := ""
	if len(branchText) > 1 {
		falseText = branchText[1]
	}
	missingText := ""
	if len(branchText) > 2 {
		missingText = branchText[2]
	}

	var b strings.Builder
	b.WriteString("{{if " + stripOuterParens(cond) + "}}")
	b.WriteString(trueText)
	if missingText != "" {
		// Three-way branch: false means the check failed on a present value,
		// missing means the field itself is absent.
		b.WriteString("{{else if " + stripOuterParens(existsCond(baseExpr, baseKind)) + "}}")
		b.WriteString(falseText)
		b.WriteString("{{else}}")
		b.WriteString(missingText)
	} else if falseText != "" {
		b.WriteString("{{else}}")
		b.WriteString(falseText)
	}
	b.WriteString("{{end}}")
	return b.String()
}

// buildCheck maps an AIOStreams check modifier to a template condition.
// Returns isCheck=false when mod is not a check; ok=false when the check is a
// known kind but cannot be converted.
func (e *aioEmitter) buildCheck(cur string, kind aioKind, mod aioModifier, raw string) (check string, isCheck, ok bool) {
	arg := func() string {
		if len(mod.args) > 0 {
			return mod.args[0]
		}
		return ""
	}
	switch mod.name {
	case "exists":
		return existsCond(cur, kind), true, true
	case "istrue":
		return existsCond(cur, kind), true, true
	case "isfalse":
		return fmt.Sprintf("(not %s)", existsCond(cur, kind)), true, true
	case "=":
		if kind == kindConst {
			// Both sides are known at conversion time; AIOStreams field
			// values compare case-insensitively in practice (::=Usenet).
			if s, err := strconv.Unquote(cur); err == nil {
				if strings.EqualFold(s, arg()) {
					return "true", true, true
				}
				return "false", true, true
			}
		}
		if kind == kindStrList || kind == kindIntList {
			e.warnf("equality checks on list fields are not supported; {%s} was removed", raw)
			return "", true, false
		}
		operand := aioQuote(arg())
		if kind == kindNumber && isNumeric(arg()) {
			operand = arg()
		}
		return fmt.Sprintf("(eq %s %s)", cur, operand), true, true
	case ">", ">=", "<", "<=":
		fns := map[string]string{">": "gt", ">=": "ge", "<": "lt", "<=": "le"}
		if kind != kindNumber || !isNumeric(arg()) {
			// ::>0 on a non-numeric field is the common "has a value" idiom
			// ({stream.bitrate::>0[...]}) — an existence test covers it.
			if mod.name == ">" && arg() == "0" {
				return existsCond(cur, kind), true, true
			}
			e.warnf("numeric comparison %s%s needs a numeric field; {%s} was removed", mod.name, arg(), raw)
			return "", true, false
		}
		return fmt.Sprintf("(%s %s %s)", fns[mod.name], cur, arg()), true, true
	case "~":
		return fmt.Sprintf("(contains %s %s)", aioQuote(arg()), cur), true, true
	case "$":
		return fmt.Sprintf("(hasPrefix %s %s)", aioQuote(arg()), cur), true, true
	case "^":
		return fmt.Sprintf("(hasSuffix %s %s)", aioQuote(arg()), cur), true, true
	case "in":
		if len(mod.args) == 0 {
			return "", true, false
		}
		parts := make([]string, len(mod.args))
		for i, a := range mod.args {
			operand := aioQuote(a)
			if kind == kindNumber && isNumeric(a) {
				operand = a
			}
			parts[i] = fmt.Sprintf("(eq %s %s)", cur, operand)
		}
		if len(parts) == 1 {
			return parts[0], true, true
		}
		return "(or " + strings.Join(parts, " ") + ")", true, true
	}
	return "", false, true
}

// applyTransform maps one AIOStreams value modifier onto the expression being
// built. Modifiers with no StreamNZB equivalent are skipped with a warning —
// the value renders without them rather than dropping the whole expression.
func (e *aioEmitter) applyTransform(cur string, kind aioKind, mod aioModifier) (string, aioKind) {
	arg := func(i int) string {
		if i < len(mod.args) {
			return mod.args[i]
		}
		return ""
	}
	switch mod.name {
	case "string":
		return cur, kind
	case "upper", "lower", "title", "smallcaps":
		return fmt.Sprintf("(%s %s)", mod.name, cur), kindString
	case "subscript":
		return fmt.Sprintf("(translate %s %s %s)", aioQuote("0123456789"), aioQuote("₀₁₂₃₄₅₆₇₈₉"), cur), kindString
	case "superscript":
		return fmt.Sprintf("(translate %s %s %s)", aioQuote("0123456789"), aioQuote("⁰¹²³⁴⁵⁶⁷⁸⁹"), cur), kindString
	case "join":
		sep := arg(0)
		if sep == "" {
			sep = ", "
		}
		switch kind {
		case kindStrList:
			return fmt.Sprintf("(join %s %s)", cur, aioQuote(sep)), kindString
		case kindIntList:
			e.warnf("::join on numeric lists renders comma-separated in StreamNZB")
			return cur, kind
		default:
			return cur, kind
		}
	case "sort", "lsort":
		if mod.name == "lsort" {
			e.warnf("::lsort mapped to case-insensitive sortAsc")
		}
		return fmt.Sprintf("(sortAsc %s)", cur), kind
	case "rsort":
		return fmt.Sprintf("(sortDesc %s)", cur), kind
	case "first":
		return fmt.Sprintf("(first %s)", cur), elemKind(kind)
	case "last":
		return fmt.Sprintf("(last %s)", cur), elemKind(kind)
	case "slice":
		if !isNumeric(arg(0)) {
			e.warnf("::slice needs numeric bounds; modifier skipped")
			return cur, kind
		}
		if len(mod.args) > 1 && isNumeric(arg(1)) {
			return fmt.Sprintf("(slice %s %s %s)", cur, arg(0), arg(1)), kind
		}
		return fmt.Sprintf("(slice %s %s)", cur, arg(0)), kind
	case "length":
		return fmt.Sprintf("(length %s)", cur), kindNumber
	case "truncate":
		if !isNumeric(arg(0)) {
			e.warnf("::truncate needs a numeric length; modifier skipped")
			return cur, kind
		}
		return fmt.Sprintf("(truncate %s %s)", arg(0), cur), kindString
	case "replace":
		return fmt.Sprintf("(replace %s %s %s)", cur, aioQuote(arg(0)), aioQuote(arg(1))), kindString
	case "remove":
		out := cur
		for _, a := range mod.args {
			out = fmt.Sprintf("(remove %s %s)", aioQuote(a), out)
		}
		return out, kindString
	case "translate":
		return fmt.Sprintf("(translate %s %s %s)", aioQuote(arg(0)), aioQuote(arg(1)), cur), kindString
	case "default":
		return fmt.Sprintf("(default %s %s)", aioQuote(arg(0)), cur), kindString
	case "bytes", "bytes2", "bytes10", "sbytes", "sbytes2", "sbytes10", "rbytes", "rbytes2", "rbytes10":
		if kind == kindNumber {
			return fmt.Sprintf("(size %s)", cur), kindString
		}
		return cur, kind
	case "bitrate", "sbitrate", "rbitrate":
		// The {{.Bitrate}} text is already display-formatted.
		if cur != ".Bitrate" {
			e.warnf("::%s has no StreamNZB equivalent; value rendered without it", mod.name)
		}
		return cur, kind
	case "time":
		// The humanized {{.Duration}} text is already display-formatted.
		if cur != ".Duration" {
			e.warnf("::time has no StreamNZB equivalent; value rendered without it")
		}
		return cur, kind
	}
	if !aioUnsupportedModifiers[mod.name] {
		e.warnf("unknown modifier ::%s ignored", mod.name)
		return cur, kind
	}
	e.warnf("::%s has no StreamNZB equivalent; value rendered without it", mod.name)
	return cur, kind
}

func elemKind(kind aioKind) aioKind {
	switch kind {
	case kindIntList:
		return kindNumber
	case kindStrList:
		return kindString
	}
	return kind
}
