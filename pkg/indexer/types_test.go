package indexer

import "testing"

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

// The language attribute is written in whatever form the indexer's source
// used. It normalizes on the way in so a filter never has to know whether a
// given indexer says "Arabic", "ara" or "ar".
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
		{"unknown passes through", "Klingon", []string{"Klingon"}},
		{"absent", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &Item{Title: "Movie 2020 1080p BluRay-GRP"}
			if tt.value != "" {
				item.Attributes = []Attribute{{Name: "language", Value: tt.value}}
			}
			got := item.ToRelease().Languages
			if len(got) != len(tt.want) {
				t.Fatalf("Languages = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Languages = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
