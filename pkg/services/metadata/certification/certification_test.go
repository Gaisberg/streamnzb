package certification

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		country, label string
		age            int
		ok             bool
	}{
		{"US", "G", 0, true},
		{"US", "PG", 7, true},
		{"US", "PG-13", 13, true},
		{"US", "R", 17, true},
		{"US", "NC-17", 18, true},
		{"US", "TV-Y", 0, true},
		{"US", "TV-Y7", 7, true},
		{"US", "TV-G", 0, true},
		{"US", "TV-PG", 7, true},
		{"US", "TV-14", 14, true},
		{"US", "TV-MA", 17, true},
		{"USA", "TV-MA", 17, true}, // TVDB uses 3-letter country names
		{"us", "pg-13", 13, true},  // case-insensitive
		{"GB", "U", 0, true},
		{"GB", "12A", 12, true},
		{"GB", "15", 15, true},
		{"UK", "18", 18, true},
		{"DE", "FSK 16", 16, true}, // FSK prefix stripped
		{"DE", "16", 16, true},     // numeric labels are ages for any country
		{"DE", "0", 0, true},
		{"NL", "12", 12, true},
		{"FI", "K-16", 0, false}, // unmapped vocabulary stays unknown
		{"FR", "16+", 16, true},  // trailing + stripped
		{"JP", "R", 17, true},    // US vocabulary fallback for loose labels
		{"US", "", 0, false},
		{"US", "NR", 0, false},
		{"US", "UNRATED", 0, false},
	}
	for _, c := range cases {
		age, ok := Normalize(c.country, c.label)
		if age != c.age || ok != c.ok {
			t.Errorf("Normalize(%q, %q) = (%d, %v), want (%d, %v)", c.country, c.label, age, ok, c.age, c.ok)
		}
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name    string
		entries []Entry
		age     int
		known   bool
	}{
		{"empty", nil, 0, false},
		{"us wins over stricter foreign", []Entry{{"DE", "18"}, {"US", "PG-13"}}, 13, true},
		{"no us takes max", []Entry{{"DE", "6"}, {"GB", "15"}}, 15, true},
		{"unknown labels skipped", []Entry{{"FI", "K-16"}, {"DE", "12"}}, 12, true},
		{"all unknown", []Entry{{"FI", "K-16"}, {"SE", "Btl"}}, 0, false},
		{"usa alias wins", []Entry{{"DEU", "18"}, {"USA", "TV-PG"}}, 7, true},
	}
	for _, c := range cases {
		age, known := Resolve(c.entries)
		if age != c.age || known != c.known {
			t.Errorf("%s: Resolve = (%d, %v), want (%d, %v)", c.name, age, known, c.age, c.known)
		}
	}
}

func TestCapAllows(t *testing.T) {
	cap13, ok := CapForID("13", false)
	if !ok {
		t.Fatal("cap id 13 not found")
	}
	if !cap13.Allows(13, true) {
		t.Error("PG-13 should pass a 13 cap")
	}
	if cap13.Allows(17, true) {
		t.Error("R should not pass a 13 cap")
	}
	if cap13.Allows(0, false) {
		t.Error("unrated must fail closed by default")
	}
	capOpen, _ := CapForID("13", true)
	if !capOpen.Allows(0, false) {
		t.Error("unrated should pass when AllowUnrated is set")
	}
	if _, ok := CapForID("", false); ok {
		t.Error("empty id must not resolve to a cap")
	}
	if _, ok := CapForID("42", false); ok {
		t.Error("unknown id must not resolve to a cap")
	}
}

func TestNormalizeKitsu(t *testing.T) {
	cases := []struct {
		rating string
		nsfw   bool
		age    int
		known  bool
	}{
		{"G", false, 0, true},
		{"PG", false, 7, true},
		{"R", false, 17, true},
		{"R18", false, 18, true},
		{"", false, 0, false},
		{"", true, 18, true}, // nsfw alone is authoritative
		{"weird", false, 0, false},
	}
	for _, c := range cases {
		age, known := NormalizeKitsu(c.rating, c.nsfw)
		if age != c.age || known != c.known {
			t.Errorf("NormalizeKitsu(%q, %v) = (%d, %v), want (%d, %v)", c.rating, c.nsfw, age, known, c.age, c.known)
		}
	}
}

func TestOptionsAscendingAndStable(t *testing.T) {
	opts := Options()
	if len(opts) == 0 {
		t.Fatal("no options")
	}
	for i := 1; i < len(opts); i++ {
		if opts[i].Age <= opts[i-1].Age {
			t.Errorf("options not ascending at %d", i)
		}
	}
	// IDs are stable config keys; changing one orphans saved profiles.
	want := []string{"0", "7", "13", "16", "18"}
	for i, opt := range opts {
		if opt.ID != want[i] {
			t.Errorf("option %d id = %q, want %q", i, opt.ID, want[i])
		}
	}
}
