package rules

import "strings"

// Conditions are written by people copying regular expressions out of regex
// tools, community lists and generated files, and those are written in raw
// regex notation: \+ for a literal plus, \d for a digit, \b for a word
// boundary. The expression language's string literals have Go's escape rules,
// where \+ is a refused save and \b is a backspace character — a regex that
// silently never matches. Requiring every backslash to be doubled would make
// the string layer's rules everyone's problem, including every generator that
// produces a define library.
//
// normalizeConditionEscapes makes raw notation mean what it says: inside a
// quoted string, a backslash before anything that is not a real string escape
// is taken literally. \b is taken literally too — the string layer reads it
// as backspace, but no release name contains a backspace and every regex
// author means a word boundary, so the string meaning is always the bug.
// Escapes the string layer genuinely needs keep working: \\ stays an escaped
// backslash (so \\+ still reaches the regex as \+), the active quote stays
// escapable, and \n, \t, \x41 and friends name the same characters in a
// string as in a regex, so preserving them changes nothing.
//
// The pass is idempotent — \\+ normalizes to itself — which matters because
// inlined references are re-serialized and recompiled.

// stringEscapes are the escape continuations left for the string layer to
// interpret, beyond \\ and the active quote. Deliberately without 'b'.
const stringEscapes = "afnrtvxuU01234567"

func normalizeConditionEscapes(source string) string {
	if !strings.Contains(source, `\`) {
		return source
	}
	var out strings.Builder
	out.Grow(len(source) + 8)
	var quote byte // the string delimiter currently open, 0 outside strings
	for i := 0; i < len(source); i++ {
		c := source[i]
		switch {
		case quote == '`':
			// Backtick strings are raw: no escapes to fix, but their content
			// must be skipped so a quote inside one does not open a "string".
			if c == '`' {
				quote = 0
			}
			out.WriteByte(c)
		case quote != 0:
			if c == '\\' && i+1 < len(source) {
				next := source[i+1]
				if next == quote || next == '\\' || (next != 'b' && strings.IndexByte(stringEscapes, next) >= 0) {
					out.WriteByte(c)
				} else {
					out.WriteString(`\\`)
				}
				out.WriteByte(next)
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			out.WriteByte(c)
		default:
			if c == '"' || c == '\'' || c == '`' {
				quote = c
			}
			out.WriteByte(c)
		}
	}
	return out.String()
}
