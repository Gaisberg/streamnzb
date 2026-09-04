package release

import (
	"net"
	"net/url"
	"strings"
	"time"
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
	// Password reports the indexer flagged the release as password protected.
	// False also covers indexers that never report the attribute.
	Password bool

	Available *bool
	Duration  float64

	// Variants are other indexers' copies of the same release, best first.
	// Deduplication used to discard them; keeping them makes a duplicate
	// recovery ammunition instead — playback walks the copies of one release
	// before moving on to a different release. A variant never carries
	// variants of its own, so the copies of a release are always the primary
	// plus this slice.
	Variants []*Release

	// UniqueHit marks a release no other indexer carried a copy of, decided on
	// the merged list at search time. It is stamped on every copy so it
	// survives failover to a variant, and read back at playback to tell an
	// indexer's exclusive releases apart from the ones everyone has.
	UniqueHit bool
}

// CopyCount is how many interchangeable copies of this release are available,
// counting the primary itself.
func (r *Release) CopyCount() int {
	if r == nil {
		return 0
	}
	return 1 + len(r.Variants)
}

// Copies returns the primary followed by its variants.
func (r *Release) Copies() []*Release {
	if r == nil {
		return nil
	}
	out := make([]*Release, 0, r.CopyCount())
	out = append(out, r)
	out = append(out, r.Variants...)
	return out
}

// CopyAt returns the nth copy (0 is the primary), or nil when n is past the
// last variant.
func (r *Release) CopyAt(n int) *Release {
	if r == nil || n < 0 {
		return nil
	}
	if n == 0 {
		return r
	}
	if n-1 >= len(r.Variants) {
		return nil
	}
	return r.Variants[n-1]
}

// HasCopyURL reports whether any copy of this release carries detailsURL. The
// caches key releases by details URL, so a lookup that only checked the
// primary would miss a release that is being played through one of its
// variants.
func (r *Release) HasCopyURL(detailsURL string) bool {
	if r == nil || detailsURL == "" {
		return false
	}
	for _, c := range r.Copies() {
		if c != nil && c.DetailsURL == detailsURL {
			return true
		}
	}
	return false
}

// Clone deep-copies a release, including its variants, so a cached result can
// be handed out without a caller's edit reaching back into the cache.
func (r *Release) Clone() *Release {
	if r == nil {
		return nil
	}
	next := *r
	if r.Languages != nil {
		next.Languages = append([]string(nil), r.Languages...)
	}
	if r.Available != nil {
		available := *r.Available
		next.Available = &available
	}
	if r.Variants != nil {
		next.Variants = make([]*Release, 0, len(r.Variants))
		for _, variant := range r.Variants {
			// Variants never nest, so cloning one is a plain field copy.
			next.Variants = append(next.Variants, variant.Clone())
		}
	}
	return &next
}

// releaseDateLayouts are the formats indexers use for pubDate/usenetdate values.
var releaseDateLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC3339, time.RFC822Z, time.RFC822}

// ParseDate parses a newznab pubDate/usenetdate string; ok is false when the
// value is empty or in none of the known layouts.
func ParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range releaseDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// PublishedAt parses the release's PubDate; ok is false when the release
// carries no parseable date (library results, indexers without dates).
func (r *Release) PublishedAt() (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	return ParseDate(r.PubDate)
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
