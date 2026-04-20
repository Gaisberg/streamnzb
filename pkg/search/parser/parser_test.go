package parser

import (
	"strings"
	"testing"
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
		{name: "season pack", parsed: &ParsedRelease{Season: 1, Seasons: []int{1}, Complete: true}, want: 2},
		{name: "show pack", parsed: &ParsedRelease{Complete: true}, want: 1},
		{name: "wrong season", parsed: &ParsedRelease{Season: 2, Seasons: []int{2}, Complete: true}, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.parsed.EpisodeMatchRank(1, 5); got != tt.want {
				t.Fatalf("EpisodeMatchRank() = %d, want %d", got, tt.want)
			}
		})
	}
}

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

func TestDashedSeasonEpisodePatternDoesNotFalseMatchInsideLongerToken(t *testing.T) {
	if matches := dashedSeasonEpisodePattern.FindStringSubmatch("Example.Show.S1 - 24bit.1080p.WEB-DL"); len(matches) != 0 {
		t.Fatalf("expected no regex match inside longer token, got %v", matches)
	}
}
