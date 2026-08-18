// Package certification normalizes country-specific age certifications
// (US MPAA/TV, GB BBFC, DE FSK, Kitsu, ...) onto one comparable minimum-age
// ordinal, and evaluates them against a profile's parental cap.
package certification

import (
	"strconv"
	"strings"
)

// Entry is one country-labeled certification as reported by a metadata
// provider (TMDB release_dates/content_ratings, TVDB contentRatings).
type Entry struct {
	Country string // ISO 3166-1 alpha-2, any case
	Label   string // e.g. "PG-13", "TV-MA", "FSK 16"
}

// Cap is a profile's parental limit. The zero value allows nothing rated
// above age 0 and blocks unrated content — always build one via CapForID.
type Cap struct {
	MaxAge       int
	AllowUnrated bool
}

// Allows reports whether content with the given normalized age passes the
// cap. Unknown certifications fail closed unless AllowUnrated is set: this is
// a parental control, so the burden of proof is on the content. That is the
// deliberate opposite of the fail-open doctrine release limits follow.
func (c Cap) Allows(age int, known bool) bool {
	if !known {
		return c.AllowUnrated
	}
	return age <= c.MaxAge
}

// Option is one selectable cap level. Served to the frontend via the API so
// there is no JS mirror to keep in step.
type Option struct {
	ID    string `json:"id"`
	Age   int    `json:"age"`
	Label string `json:"label"`
}

// options is the fixed cap ladder. IDs are stable config keys — a profile
// stores the ID, not the age.
var options = []Option{
	{ID: "0", Age: 0, Label: "All ages (G)"},
	{ID: "7", Age: 7, Label: "7+ (PG)"},
	{ID: "13", Age: 13, Label: "13+ (PG-13)"},
	{ID: "16", Age: 16, Label: "16+"},
	{ID: "18", Age: 18, Label: "18+"},
}

// Options returns the selectable cap levels in ascending order.
func Options() []Option {
	out := make([]Option, len(options))
	copy(out, options)
	return out
}

// CapForID resolves a stored cap ID to a Cap. ok is false for unknown IDs
// and for "" (no cap configured).
func CapForID(id string, allowUnrated bool) (cap Cap, ok bool) {
	for _, opt := range options {
		if opt.ID == id {
			return Cap{MaxAge: opt.Age, AllowUnrated: allowUnrated}, true
		}
	}
	return Cap{}, false
}

// usLabels covers US MPAA movie ratings and US TV parental guidelines —
// the labels TMDB and TVDB report for country US.
var usLabels = map[string]int{
	"G":        0,
	"PG":       7,
	"PG-13":    13,
	"R":        17,
	"NC-17":    18,
	"TV-Y":     0,
	"TV-Y7":    7,
	"TV-Y7-FV": 7,
	"TV-G":     0,
	"TV-PG":    7,
	"TV-14":    14,
	"TV-MA":    17,
}

// gbLabels covers BBFC certificates.
var gbLabels = map[string]int{
	"U":   0,
	"PG":  7,
	"12":  12,
	"12A": 12,
	"15":  15,
	"18":  18,
	"R18": 18,
}

// Normalize maps one country-specific certification label onto a minimum
// age. Unknown labels return ok=false — the caller decides what unknown
// means (Cap.Allows fails closed).
func Normalize(country, label string) (age int, ok bool) {
	label = strings.ToUpper(strings.TrimSpace(label))
	label = strings.TrimPrefix(label, "FSK")
	label = strings.TrimSuffix(label, "+")
	label = strings.TrimSpace(label)
	if label == "" {
		return 0, false
	}
	country = strings.ToUpper(strings.TrimSpace(country))

	if a, found := lookupCountry(country, label); found {
		return a, true
	}
	// Purely numeric labels (DE FSK, NL Kijkwijzer, FR, ES, ...) are minimum
	// ages already, for any country.
	if n, err := strconv.Atoi(label); err == nil && n >= 0 && n <= 21 {
		return n, true
	}
	// Last resort: the US vocabulary shows up under other countries too
	// (TVDB in particular labels loosely).
	if a, found := usLabels[label]; found {
		return a, true
	}
	return 0, false
}

func lookupCountry(country, label string) (int, bool) {
	switch country {
	case "US", "USA":
		a, found := usLabels[label]
		return a, found
	case "GB", "UK", "GBR":
		a, found := gbLabels[label]
		return a, found
	}
	return 0, false
}

// Resolve picks the age for a set of provider entries: a known US label wins
// outright; otherwise the maximum normalized age across all known labels —
// conservative, matching the fail-closed default.
func Resolve(entries []Entry) (age int, known bool) {
	maxAge, found := 0, false
	for _, e := range entries {
		a, ok := Normalize(e.Country, e.Label)
		if !ok {
			continue
		}
		c := strings.ToUpper(strings.TrimSpace(e.Country))
		if c == "US" || c == "USA" {
			return a, true
		}
		if !found || a > maxAge {
			maxAge, found = a, true
		}
	}
	return maxAge, found
}

// kitsuLabels maps Kitsu's ageRating enum.
var kitsuLabels = map[string]int{
	"G":   0,
	"PG":  7,
	"R":   17,
	"R18": 18,
}

// NormalizeKitsu maps Kitsu's ageRating attribute (plus its nsfw flag) onto
// an age. nsfw alone is authoritative for 18.
func NormalizeKitsu(ageRating string, nsfw bool) (age int, known bool) {
	if nsfw {
		return 18, true
	}
	a, found := kitsuLabels[strings.ToUpper(strings.TrimSpace(ageRating))]
	return a, found
}
