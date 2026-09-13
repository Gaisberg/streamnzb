package api

import "testing"

// TestTMDBListIDRejectsLookAlikeHosts pins the CodeRabbit-flagged fix: the
// host must match themoviedb.org exactly (or its www. form), not merely
// contain that string — a look-alike host like themoviedb.org.evil.com would
// otherwise be accepted here and then fail every catalog request downstream,
// since tmdb.FetchPublicListPage enforces the exact host anyway.
func TestTMDBListIDRejectsLookAlikeHosts(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		wantID string
		wantOK bool
	}{
		{"bare host", "https://themoviedb.org/list/8552549", "8552549", true},
		{"www host", "https://www.themoviedb.org/list/8552549-my-list", "8552549", true},
		{"case insensitive", "https://WWW.THEMOVIEDB.ORG/list/8552549", "8552549", true},
		{"look-alike suffix host", "https://themoviedb.org.evil.com/list/8552549", "", false},
		{"look-alike prefix host", "https://evil-themoviedb.org/list/8552549", "", false},
		{"not a list path", "https://themoviedb.org/movie/603", "", false},
		{"malformed url", "://not a url", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := tmdbListID(tc.url)
			if id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("tmdbListID(%q) = (%q, %v), want (%q, %v)", tc.url, id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}
