package indexer

import (
	"strings"
	"testing"
)

// ToRelease reads the newznab attributes the limits filter on: password (0
// means none, anything else protected) and usenetdate (preferred over pubDate
// when it parses, since retention runs from the usenet post date).
func TestToReleasePasswordAndUsenetDate(t *testing.T) {
	tests := []struct {
		name         string
		attrs        []Attribute
		wantPassword bool
		wantPubDate  string
	}{
		{"no attributes", nil, false, "Sun, 01 Jun 2025 10:00:00 +0000"},
		{"password zero", []Attribute{{Name: "password", Value: "0"}}, false, "Sun, 01 Jun 2025 10:00:00 +0000"},
		{"password set", []Attribute{{Name: "password", Value: "1"}}, true, "Sun, 01 Jun 2025 10:00:00 +0000"},
		{"password inner archive", []Attribute{{Name: "password", Value: "2"}}, true, "Sun, 01 Jun 2025 10:00:00 +0000"},
		{"usenetdate preferred", []Attribute{{Name: "usenetdate", Value: "Sat, 31 May 2025 08:00:00 +0000"}}, false, "Sat, 31 May 2025 08:00:00 +0000"},
		{"unparseable usenetdate ignored", []Attribute{{Name: "usenetdate", Value: "not a date"}}, false, "Sun, 01 Jun 2025 10:00:00 +0000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &Item{
				Title:      "Movie 2020 1080p BluRay-GRP",
				PubDate:    "Sun, 01 Jun 2025 10:00:00 +0000",
				Attributes: tt.attrs,
			}
			rel := item.ToRelease()
			if rel.Password != tt.wantPassword {
				t.Errorf("Password = %v, want %v", rel.Password, tt.wantPassword)
			}
			if rel.PubDate != tt.wantPubDate {
				t.Errorf("PubDate = %q, want %q", rel.PubDate, tt.wantPubDate)
			}
		})
	}
}

// The language and subs attributes are written in whatever form the indexer's
// source used. They normalize on the way in so a filter never has to know
// whether a given indexer says "Arabic", "ara" or "ar".
func TestToReleaseNormalizesLanguages(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{"full name", "Arabic", []string{"ar"}},
		{"iso 639-2", "ara", []string{"ar"}},
		{"list", "English, ger", []string{"en", "de"}},
		{"duplicates collapse", "English,eng,en", []string{"en"}},
		{"regional variant", "Arabic (SA)", []string{"ar"}},
		{"unknown passes through", "Klingon", []string{"Klingon"}},
		{"absent", "", nil},
		// NZBgeek writes one attribute per language rather than one list
		// (AIOStreams issue #1152); the first match alone would say
		// "English" for a release carrying Arabic subtitles.
		{"repeated attributes", "English|Arabic (SA)|German|Spanish (Latin America)|Portuguese (BR)",
			[]string{"en", "ar", "de", "es-419", "pt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Issue #283: the subs attribute is the only place a release's
			// subtitle languages are ever written down, so it reads the
			// same way the language attribute does.
			for _, attr := range []string{"language", "subs"} {
				item := &Item{Title: "Movie 2020 1080p BluRay-GRP"}
				if tt.value != "" {
					for _, value := range strings.Split(tt.value, "|") {
						item.Attributes = append(item.Attributes, Attribute{Name: attr, Value: value})
					}
				}
				rel := item.ToRelease()
				got, other := rel.Languages, rel.Subtitles
				if attr == "subs" {
					got, other = rel.Subtitles, rel.Languages
				}
				if len(got) != len(tt.want) {
					t.Fatalf("%s: got %v, want %v", attr, got, tt.want)
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Fatalf("%s: got %v, want %v", attr, got, tt.want)
					}
				}
				if other != nil {
					t.Fatalf("%s attribute leaked into the other list: %v", attr, other)
				}
			}
		})
	}
}

// poster and usenetdate travel verbatim to AvailNZB, which mints the Warden
// dead-release fingerprint from the pair. An item missing either one leaves
// that field empty rather than substituting anything — a half pair mints
// nothing, and a guessed poster would mint the wrong thing.
func TestToReleaseCarriesPosterAndUsenetDate(t *testing.T) {
	const poster = "someone@example.com"
	const usenetDate = "Sat, 31 May 2025 08:00:00 +0000"

	tests := []struct {
		name           string
		attrs          []Attribute
		wantPoster     string
		wantUsenetDate string
	}{
		{"both present", []Attribute{{Name: "poster", Value: poster}, {Name: "usenetdate", Value: usenetDate}}, poster, usenetDate},
		{"poster only", []Attribute{{Name: "poster", Value: poster}}, poster, ""},
		{"usenetdate only", []Attribute{{Name: "usenetdate", Value: usenetDate}}, "", usenetDate},
		{"neither", nil, "", ""},
		{"whitespace trimmed", []Attribute{{Name: "poster", Value: "  " + poster + "  "}}, poster, ""},
		// An unparseable date is still what the indexer said, so it is kept
		// verbatim; the report layer is what refuses to send it.
		{"unparseable usenetdate kept verbatim", []Attribute{{Name: "usenetdate", Value: "not a date"}}, "", "not a date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &Item{
				Title:      "Movie 2020 1080p BluRay-GRP",
				PubDate:    "Sun, 01 Jun 2025 10:00:00 +0000",
				Attributes: tt.attrs,
			}
			rel := item.ToRelease()
			if rel.Poster != tt.wantPoster {
				t.Errorf("Poster = %q, want %q", rel.Poster, tt.wantPoster)
			}
			if rel.UsenetDate != tt.wantUsenetDate {
				t.Errorf("UsenetDate = %q, want %q", rel.UsenetDate, tt.wantUsenetDate)
			}
		})
	}
}
