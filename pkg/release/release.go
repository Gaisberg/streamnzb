package release

import (
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func IsPrivateReleaseURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return true
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Hostname()
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsPrivate() || ip.IsLoopback()
	}
	lower := strings.ToLower(host)
	return lower == "localhost" || strings.HasSuffix(lower, ".local")
}

type Release struct {
	Title         string
	Link          string
	DetailsURL    string
	Size          int64
	Indexer       string
	SourceIndexer interface{}
	IsLibrary     bool

	PubDate     string
	GUID        string
	QuerySource string
	Grabs       int
	Languages   []string

	Available *bool
	Duration  float64
}

func (r *Release) IsLibraryResult() bool {
	if r == nil {
		return false
	}
	return r.IsLibrary
}

func NormalizeTitleForDedup(s string) string {
	return strings.Join(normalizeTitleWords(s, true), "")
}

// NormalizeTitleLettersOnly returns a lowercase, letters-and-spaces-only form for fuzzy matching.
// Numbers, punctuation, and "&" (normalized to "and") are handled so years/versions don't affect title match.
// Dots and common separators become spaces so "Star.Trek.Starfleet" keeps word boundaries.
// Season/episode/year are filtered separately in FilterResults.
func NormalizeTitleLettersOnly(s string) string {
	return strings.Join(normalizeTitleWords(s, false), " ")
}

func NormalizeTitleWordsForMatch(s string) []string {
	return normalizeTitleWords(s, true)
}

func normalizeTitleWords(s string, keepDigits bool) []string {
	s = normalizeTitleForMatchBase(s)
	s = strings.ReplaceAll(s, "&", " and ")
	for _, sep := range []string{".", "-", "_", ":", "  "} {
		s = strings.ReplaceAll(s, sep, " ")
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || (keepDigits && unicode.IsNumber(r)) {
			b.WriteRune(r)
		} else if r == ' ' || r == '\t' {
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	for i, word := range words {
		words[i] = canonicalizeCommonTitleWord(word)
	}
	return words
}

func normalizeTitleForMatchBase(s string) string {
	s = strings.TrimSpace(s)
	s = repairMojibakeUTF8(s)
	s = strings.ToLower(s)
	s = stripDiacritics(s)
	return NormalizeTitleForFilename(s)
}

func canonicalizeCommonTitleWord(word string) string {
	switch word {
	case "pokmon", "pokamon":
		return "pokemon"
	default:
		return word
	}
}

func repairMojibakeUTF8(s string) string {
	best := s
	for range 2 {
		candidate, ok := decodeLatin1AsUTF8(best)
		if !ok || mojibakeScore(candidate) >= mojibakeScore(best) {
			break
		}
		best = candidate
	}
	return best
}

func decodeLatin1AsUTF8(s string) (string, bool) {
	buf := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 255 {
			return "", false
		}
		buf = append(buf, byte(r))
	}
	if !utf8.Valid(buf) {
		return "", false
	}
	return string(buf), true
}

func mojibakeScore(s string) int {
	count := 0
	for _, r := range s {
		switch r {
		case 'Ã', 'Â', 'ã', 'â', '©', '€', '™', '�':
			count++
		}
	}
	return count
}

func stripDiacritics(s string) string {
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

var filenameReplacer = strings.NewReplacer(
	"ü", "ue", "Ü", "UE", "ö", "oe", "Ö", "OE", "ä", "ae", "Ä", "AE", "ß", "ss",
	"á", "a", "à", "a", "â", "a", "ã", "a", "é", "e", "è", "e", "ê", "e", "í", "i",
	"ó", "o", "ò", "o", "ô", "o", "ú", "u", "ù", "u", "û", "u", "ñ", "n", "ç", "c",
)

func NormalizeTitleForFilename(s string) string {
	return filenameReplacer.Replace(s)
}

// NormalizeTitleForSearchQuery prepares a metadata title for outgoing text
// searches and validation baselines. It keeps letters and numbers, collapses
// punctuation into spaces, and normalizes common filename replacements so
// "König" becomes "Koenig" and "Friends & Neighbors" becomes
// "Friends Neighbors".
func NormalizeTitleForSearchQuery(s string) string {
	s = strings.TrimSpace(NormalizeTitleForFilename(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			b.WriteRune(r)
			lastSpace = false
		case isTitleJoinerRune(r):
			// Keep contractions together so "Don't" becomes "Dont"
			// instead of "Don t".
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		default:
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isTitleJoinerRune(r rune) bool {
	switch r {
	case '\'', '’', '‘', 'ʼ', '‛', '`', '´':
		return true
	default:
		return false
	}
}

func IsFullDiscRelease(title string) bool {
	lower := strings.ToLower(title)
	// Match common full Blu-ray disc keywords or ISO extensions
	if strings.Contains(lower, "complete.uhd.bluray") ||
		strings.Contains(lower, "complete.bluray") ||
		strings.Contains(lower, "complete.bd") ||
		strings.Contains(lower, "bd25") ||
		strings.Contains(lower, "bd50") ||
		strings.Contains(lower, "bdmv") ||
		strings.Contains(lower, "complete.mpeg") ||
		strings.HasSuffix(lower, ".iso") ||
		strings.Contains(lower, ".iso.") ||
		strings.Contains(lower, "-iso") ||
		strings.Contains(lower, " iso ") ||
		strings.Contains(lower, " iso.") {
		return true
	}
	return false
}
