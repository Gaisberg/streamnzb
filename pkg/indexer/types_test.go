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
