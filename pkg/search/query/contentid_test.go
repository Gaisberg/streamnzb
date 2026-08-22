package query

import "testing"

// These branches decided which episode a whole search went after, and until the
// parsing moved here they could not be called without standing up a server and
// three metadata clients. Every id form the addon actually receives is listed,
// with both arities of each, because the short and long forms differ only in
// whether a season and episode are read — which is exactly the pair that gets
// confused.
func TestParseContentID(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
		want ContentID
	}{
		{"imdb movie", "tt1375666", ContentID{IMDbID: "tt1375666"}},
		{"imdb episode", "tt0903747:2:4", ContentID{IMDbID: "tt0903747", Season: "2", Episode: "4"}},

		{"tmdb movie", "tmdb:27205", ContentID{TMDBID: "27205"}},
		{"tmdb episode", "tmdb:1396:2:4", ContentID{TMDBID: "1396", Season: "2", Episode: "4"}},

		{"tvdb series", "tvdb:81189", ContentID{TVDBID: "81189"}},
		{"tvdb episode", "tvdb:81189:2:4", ContentID{TVDBID: "81189", Season: "2", Episode: "4"}},

		// Kitsu numbers episodes within the entry, so the number lands in both
		// KitsuEpisode and Episode: anime-lists may later replace Episode with
		// the series number, and KitsuEpisode has to survive that to be mapped.
		{"kitsu entry", "kitsu:49016", ContentID{KitsuID: "49016"}},
		{"kitsu episode", "kitsu:49016:3", ContentID{KitsuID: "49016", KitsuEpisode: "3", Episode: "3"}},

		// A bare number is a TMDB id, matching what the meta handler assumes.
		{"bare numeric", "27205", ContentID{TMDBID: "27205"}},
		{"bare numeric episode", "1396:2:4", ContentID{TMDBID: "1396", Season: "2", Episode: "4"}},

		// A tvdb id is numeric too. Without the guard in the fallback it would
		// come back claiming to be a TMDB id of the same number, and the search
		// would query TMDB for an unrelated title.
		{"tvdb id is not also a tmdb id", "tvdb:81189:1:1",
			ContentID{TVDBID: "81189", Season: "1", Episode: "1"}},

		// Nothing recognisable is empty rather than an error: the search path
		// has always answered an unusable id with a search that finds nothing.
		{"empty", "", ContentID{}},
		{"unknown prefix", "anilist:1234", ContentID{}},
		{"prefix with no id", "tmdb:", ContentID{}},
		{"kitsu with no id", "kitsu:", ContentID{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseContentID(tc.id); got != tc.want {
				t.Fatalf("ParseContentID(%q) =\n  %+v\nwant\n  %+v", tc.id, got, tc.want)
			}
		})
	}
}

// A season without an episode is not a form Stremio sends, but a truncated id
// must not be read as if the season were the episode.
func TestParseContentIDIgnoresATruncatedSeasonEpisodePair(t *testing.T) {
	for _, id := range []string{"tt0903747:2", "tmdb:1396:2", "tvdb:81189:2", "1396:2"} {
		got := ParseContentID(id)
		if got.Season != "" || got.Episode != "" {
			t.Fatalf("ParseContentID(%q) read a partial pair: season %q episode %q", id, got.Season, got.Episode)
		}
	}
}

func TestIsNumericID(t *testing.T) {
	for in, want := range map[string]bool{
		"27205":     true,
		"  27205  ": true,
		"0":         true,
		"-1":        true,
		"tt1375666": false,
		"1396:2":    false,
		"":          false,
		"   ":       false,
	} {
		if got := isNumericID(in); got != want {
			t.Fatalf("isNumericID(%q) = %v, want %v", in, got, want)
		}
	}
}
