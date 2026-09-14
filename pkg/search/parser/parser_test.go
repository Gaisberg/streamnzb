package parser

import (
	"strings"
	"testing"

	"github.com/dreulavelle/jhin"
)

func TestParseReleaseTitleRetainsEpisodeCollections(t *testing.T) {
	parsed := ParseReleaseTitle("The.Walking.Dead.S01E05E06.1080p.WEB-DL")
	if parsed == nil {
		t.Fatal("expected parsed release")
	}
	if parsed.Season != 1 {
		t.Fatalf("expected first season 1, got %d", parsed.Season)
	}
	if parsed.Episode != 5 {
		t.Fatalf("expected first episode 5, got %d", parsed.Episode)
	}
	if !parsed.HasSeason(1) {
		t.Fatal("expected parsed release to include season 1")
	}
	if !parsed.HasEpisode(5) || !parsed.HasEpisode(6) {
		t.Fatalf("expected parsed release to include episodes 5 and 6, got %v", parsed.Episodes)
	}
}

func TestParsedReleaseEpisodeMatchRank(t *testing.T) {
	tests := []struct {
		name   string
		parsed *ParsedRelease
		want   int
	}{
		{name: "exact episode", parsed: &ParsedRelease{Season: 1, Episode: 5, Seasons: []int{1}, Episodes: []int{5}}, want: 4},
		{name: "multi episode", parsed: &ParsedRelease{Season: 1, Episode: 5, Seasons: []int{1}, Episodes: []int{5, 6}}, want: 3},
		{name: "season pack", parsed: &ParsedRelease{Season: 1, Seasons: []int{1}, Result: &jhin.Result{Complete: true}}, want: 2},
		{name: "show pack", parsed: &ParsedRelease{Result: &jhin.Result{Complete: true}}, want: 1},
		{name: "wrong season", parsed: &ParsedRelease{Season: 2, Seasons: []int{2}, Result: &jhin.Result{Complete: true}}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.parsed.EpisodeMatchRank(1, 5); got != tt.want {
				t.Fatalf("EpisodeMatchRank() = %d, want %d", got, tt.want)
			}
		})
	}

}

// Season 0 is Stremio's Specials season, and a literal one: S00E01 serves it,
// S01E01 does not, and neither does a release naming no season at all. The
// seasonless path is a different question, asked through its own method.
func TestParsedReleaseEpisodeMatchRankSeasonZeroIsLiteral(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  int
	}{
		{name: "special", title: "Example.Show.S00E01.1080p.WEB-DL", want: 4},
		{name: "special among others", title: "Example.Show.S00E01E02.1080p.WEB-DL", want: 3},
		{name: "specials pack", title: "Example.Show.S00.1080p.WEB-DL", want: 2},
		{name: "complete pack", title: "Example.Show.Complete.Series.1080p.WEB-DL", want: 1},
		{name: "season one", title: "Example.Show.S01E01.1080p.WEB-DL", want: 0},
		{name: "season two", title: "Example.Show.S02E01.1080p.WEB-DL", want: 0},
		{name: "no season", title: "[Group] Example Show - 01 (1080p)", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseReleaseTitle(tt.title).EpisodeMatchRank(0, 1); got != tt.want {
				t.Fatalf("EpisodeMatchRank(0, 1) = %d, want %d", got, tt.want)
			}
		})
	}
}

// A request with no season — an absolute-numbered anime episode — takes a
// release that names no season or season 1, and nothing with another
// explicit season: S02E01 is not episode 1 of the show.
func TestParsedReleaseSeasonlessEpisodeMatchRank(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  int
	}{
		{name: "no season", title: "[Group] Anime Name - 1085 (1080p)", want: 4},
		{name: "season one", title: "Anime.Name.S01E1085.1080p.WEB-DL", want: 4},
		{name: "complete pack", title: "Anime.Name.Complete.Series.1080p.WEB-DL", want: 1},
		{name: "wrong season", title: "Anime.Name.S02E1085.1080p.WEB-DL", want: 0},
		{name: "specials", title: "Anime.Name.S00E1085.1080p.WEB-DL", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseReleaseTitle(tt.title).SeasonlessEpisodeMatchRank(1085); got != tt.want {
				t.Fatalf("SeasonlessEpisodeMatchRank(1085) = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParsedReleaseTargetMatchRank(t *testing.T) {
	absolute := ParseReleaseTitle("[Judas] One Piece - 63 (1080p) [ABCD1234]")
	standard := ParseReleaseTitle("One.Piece.S02E02.1080p.WEB-DL.x264-GROUP")
	special := ParseReleaseTitle("One.Piece.S00E02.1080p.WEB-DL.x264-GROUP")

	target := EpisodeTarget{Season: 2, Episode: 2, Absolute: 63}
	if got := absolute.TargetMatchRank(target); got != 4 {
		t.Fatalf("absolute-numbered release rank = %d, want 4", got)
	}
	if got := standard.TargetMatchRank(target); got != 4 {
		t.Fatalf("season/episode release rank = %d, want 4", got)
	}
	if got := special.TargetMatchRank(target); got != 0 {
		t.Fatalf("special rank for a season 2 target = %d, want 0", got)
	}

	// A seasonless target reads its episode by the seasonless rule and never
	// as season 0.
	seasonless := EpisodeTarget{Seasonless: true, Episode: 63}
	if got := absolute.TargetMatchRank(seasonless); got != 4 {
		t.Fatalf("seasonless target against absolute-numbered release rank = %d, want 4", got)
	}
	if got := ParseReleaseTitle("One.Piece.S00E63.1080p.WEB-DL").TargetMatchRank(seasonless); got != 0 {
		t.Fatalf("seasonless target must not read as season 0, got rank %d", got)
	}
	if seasonless.Valid() {
		t.Fatal("a seasonless episode with no absolute number cannot pick a file")
	}
	if !(EpisodeTarget{Season: 0, Episode: 1}).Valid() {
		t.Fatal("season 0 episode 1 is a literal target")
	}
}

// Dashed anime numbering ("S4 - 03") is parsed by jhin itself since v0.5.0;
// this pins the behavior the old in-house fallback existed for.
func TestParseReleaseTitleRecognizesDashedSeasonEpisodePattern(t *testing.T) {
	parsed := ParseReleaseTitle("[SubsPlease] Tensei Shitara Slime Datta Ken S4 - 03 (720p) [370B1C65]")
	if parsed == nil {
		t.Fatal("expected parsed release")
	}
	if parsed.Season != 4 {
		t.Fatalf("expected season 4, got %d", parsed.Season)
	}
	if parsed.Episode != 3 {
		t.Fatalf("expected episode 3, got %d", parsed.Episode)
	}
	if !parsed.HasSeason(4) {
		t.Fatal("expected parsed release to include season 4")
	}
	if !parsed.HasEpisode(3) {
		t.Fatalf("expected parsed release to include episode 3, got %v", parsed.Episodes)
	}
	if strings.Contains(parsed.Title, "S4 - 03") {
		t.Fatalf("expected parsed title to drop dashed season/episode suffix, got %q", parsed.Title)
	}
}

// A compact dashed range names seasons, not an episode: S03-08 is a pack of
// seasons 3 through 8, and nothing may invent an episode for it — the old
// fallback read the same characters as S3E8, which turned season packs into
// episode releases.
func TestDashedSeasonRangeStaysASeasonPack(t *testing.T) {
	parsed := ParseReleaseTitle("Show.S03-08.1080p.WEB-DL-GRP")
	if parsed == nil {
		t.Fatal("expected parsed release")
	}
	if len(parsed.Episodes) != 0 || parsed.Episode != 0 {
		t.Fatalf("expected no episodes for a season range, got %v", parsed.Episodes)
	}
	if !parsed.HasSeason(3) || !parsed.HasSeason(8) {
		t.Fatalf("expected seasons 3 through 8, got %v", parsed.Seasons)
	}
}

func TestParseReleaseTitleExpandsLanguageAliases(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		want    []string
		wantAny []string
	}{
		{
			name:    "nordic expands to da fi no sv",
			title:   "Some.Movie.2024.1080p.BluRay.NORDIC.x264-RG",
			wantAny: []string{"da", "fi", "no", "sv"},
		},
		{
			name:    "scandinavian expands to da no sv",
			title:   "Some.Movie.2024.1080p.BluRay.SCANDINAVIAN.x264-RG",
			wantAny: []string{"da", "no", "sv"},
		},
		{
			name:    "baltic expands to et lv lt",
			title:   "Some.Movie.2024.1080p.BluRay.BALTIC.x264-RG",
			wantAny: []string{"et", "lv", "lt"},
		},
		{
			name:    "multi expands to fr",
			title:   "Some.Movie.2024.1080p.BluRay.MULTi.x264-RG",
			wantAny: []string{"fr"},
		},
		{
			name:    "truefrench expands to fr",
			title:   "Some.Movie.2024.1080p.BluRay.TRUEFRENCH.x264-RG",
			wantAny: []string{"fr"},
		},
		{
			name:  "no alias leaves languages from ptt only",
			title: "Some.Movie.2024.1080p.BluRay.FRENCH.x264-RG",
			want:  []string{"fr"},
		},
		{
			name:  "alias with explicit language merges both",
			title: "Some.Movie.2024.1080p.BluRay.NORDIC.FRENCH.x264-RG",
			want:  []string{"fr", "da", "fi", "no", "sv"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed := ParseReleaseTitle(tc.title)
			if parsed == nil {
				t.Fatal("expected parsed release")
			}
			if tc.want != nil {
				if !sameSet(parsed.Languages, tc.want) {
					t.Fatalf("expected languages %v, got %v", tc.want, parsed.Languages)
				}
			}
			if tc.wantAny != nil {
				for _, code := range tc.wantAny {
					if !containsString(parsed.Languages, code) {
						t.Fatalf("expected languages to include %q, got %v", code, parsed.Languages)
					}
				}
			}
		})
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			return false
		}
	}
	return true
}

func TestResolutionGroupHandlesFullDimensions(t *testing.T) {
	tests := []struct {
		resolution string
		want       string
	}{
		{"2160p", "4k"},
		{"1080p", "1080p"},
		{"720p", "720p"},
		{"480p", "sd"},
		// The parser reports both dimensions when a title spells them out;
		// grouping must key off the height, not the width.
		{"720x480p", "sd"},
		{"1916x1080p", "1080p"},
		{"3840x2160p", "4k"},
	}

	for _, tt := range tests {
		t.Run(tt.resolution, func(t *testing.T) {
			p := &ParsedRelease{Result: &jhin.Result{Resolution: tt.resolution}}
			if got := p.ResolutionGroup(); got != tt.want {
				t.Fatalf("ResolutionGroup(%q) = %q, want %q", tt.resolution, got, tt.want)
			}
		})
	}
}

// A language named right before a subtitle word is a subtitle language. jhin
// reports "Arabic.Subs" as Arabic and subbed without tying the two together,
// which is the standalone half of issue #283: a subtitle filter needs to know
// which language the subtitles are in. Arabic in particular has to come out
// as "ar" whichever way the name spells it.
func TestParseReleaseTitleLiftsSubtitleLanguages(t *testing.T) {
	tests := []struct {
		title string
		want  []string
	}{
		{"Movie.2020.1080p.BluRay.Arabic.Subs-GRP", []string{"ar"}},
		{"Movie.2020.1080p.BluRay.ARA.Subbed-GRP", []string{"ar"}},
		{"Movie 2020 1080p French Subtitles-GRP", []string{"fr"}},
		{"Movie.2020.1080p.Eng.Subs.Ger.Subs-GRP", []string{"en", "de"}},
		{"Movie.2020.1080p.ENGSubs-GRP", []string{"en"}},
		// "No.Subs" is not Norwegian subtitles, and MULTi is not a language.
		{"Movie.2020.1080p.No.Subs-GRP", nil},
		{"Movie.2020.1080p.MULTi.SUBS-GRP", nil},
		// A plain language is audio, not a subtitle track.
		{"Movie.2020.1080p.Arabic-GRP", nil},
	}
	for _, tt := range tests {
		got := ParseReleaseTitle(tt.title).Subtitles
		if len(got) != len(tt.want) {
			t.Errorf("%s: subtitles %v, want %v", tt.title, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: subtitles %v, want %v", tt.title, got, tt.want)
				break
			}
		}
	}
	// Arabic in the name still parses as a language, however it is spelled.
	for _, title := range []string{
		"Movie.2020.1080p.BluRay.ARABIC-GRP",
		"Movie.2020.1080p.BluRay.Arabic.Subs-GRP",
		"Movie.2020.1080p.BluRay.ARA-GRP",
		"Movie.2020.1080p.DUAL.ARA-ENG-GRP",
	} {
		langs := ParseReleaseTitle(title).Languages
		found := false
		for _, l := range langs {
			if l == "ar" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: languages %v, want ar among them", title, langs)
		}
	}
}
