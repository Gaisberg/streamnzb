package search

import (
	"testing"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
)

func TestNormalizedTitleMatches(t *testing.T) {
	logger.Init("ERROR")

	tests := []struct {
		expect   string
		gotTitle string
		want     bool
	}{
		{"Law & Order", "Law and Order", true},
		{"Law & Order", "Law and Order SVU", true},
		{"Star Trek: Starfleet Academy", "Star.Trek.Starfleet.Academy.S01E01", true},
		{"Star Trek: Starfleet Academy", "Starfleet Academy S01E01", false},
		{"Batman", "The Batman", false},
		{"Batman", "Batman Beyond", false},
		{"Batman", "Batman Forever", false},
		{"The Hunger Games: Mockingjay - Part 1", "The Hunger Games Mockingjay Part 2", false},
		{"The Walking Dead", "The Walking Dead S06E07", true},
		{"Some Show", "Other Show", false},
		{"Law and Order", "Law & Order", true},
		{"Pokémon", "Pokemon", true},
		{"Pokémon", "pokemon", true},
		{"Pokémon", "PokÃ©mon", true},
		{"Pokémon", "PokÃÂ©mon", true},
		{"Pokémon", "Pokmon", true},
		{"Pokémon", "Pokemon.S01E01.1080p.WEB-DL", true},
		{"Pokémon", "Pokmon.S01E01.1080p.WEB-DL", true},
		{"Pokémon", "Pokemon.Horizons.S01E01.1080p.WEB-DL", false},
		{"Pokémon", "Pokmon.Horizons.S01E01.1080p.WEB-DL", false},
		{"Pokémon", "Pokemon.Origins.S01E01.1080p.WEB-DL", false},
		{"Show 2024", "Show 2024 1080p", true},
		{"Interstellar", "The.Science.of.Interstellar", false},
		{"Interstellar", "Interstellar.2014.2160p.BluRay", true},
	}
	for _, tt := range tests {
		got := normalizedTitleMatches(tt.expect, tt.gotTitle)
		if got != tt.want {
			t.Errorf("normalizedTitleMatches(%q, %q) = %v, want %v", tt.expect, tt.gotTitle, got, tt.want)
		}
	}
}

func TestFilterResultsSeriesEpisodeRequestAcceptsPacks(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "The.Walking.Dead.S01E05.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.S01E05E06.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.S01.COMPLETE.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.Season.01.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.Season.1.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.S01.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.Complete.Series.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.COMPLETE.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.S02.COMPLETE.1080p.WEB-DL"},
		{Title: "Other.Show.S01E05.1080p.WEB-DL"},
	}

	filtered := ValidateSearchResults(releases, "series", "The Walking Dead S01E05", "1", "5", true, false)
	got := make([]string, 0, len(filtered))
	for _, rel := range filtered {
		if rel != nil {
			got = append(got, rel.Title)
		}
	}

	want := []string{
		"The.Walking.Dead.S01E05.1080p.WEB-DL",
		"The.Walking.Dead.S01E05E06.1080p.WEB-DL",
		"The.Walking.Dead.S01.COMPLETE.1080p.WEB-DL",
		"The.Walking.Dead.Season.01.1080p.WEB-DL",
		"The.Walking.Dead.Season.1.1080p.WEB-DL",
		"The.Walking.Dead.S01.1080p.WEB-DL",
		"The.Walking.Dead.Complete.Series.1080p.WEB-DL",
		"The.Walking.Dead.COMPLETE.1080p.WEB-DL",
	}

	if len(got) != len(want) {
		t.Fatalf("ValidateSearchResults() returned %d releases, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ValidateSearchResults()[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestFilterResultsSeriesEpisodeRequestRejectsWrongEpisodePacks(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "The.Walking.Dead.S01E06.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.S01E06E07.1080p.WEB-DL"},
		{Title: "The.Walking.Dead.S02.COMPLETE.1080p.WEB-DL"},
	}

	filtered := ValidateSearchResults(releases, "series", "The Walking Dead S01E05", "1", "5", true, false)
	if len(filtered) != 0 {
		t.Fatalf("expected no matches, got %d: %+v", len(filtered), filtered)
	}
}

func TestFilterResultsSeriesEpisodeRequestKeepsSTitlesIntact(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "Star.Trek.Strange.New.Worlds.S01E01.WEBRip.x265-ION265"},
		{Title: "Star.Trek.Starfleet.Academy.S01E01.WEBRip.x265-ION265"},
	}

	filtered := ValidateSearchResults(releases, "series", "Star Trek: Strange New Worlds S01E01", "1", "1", true, false)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Title != releases[0].Title {
		t.Fatalf("expected %q, got %q", releases[0].Title, filtered[0].Title)
	}
}

func TestFilterResultsSeriesEpisodeRequestRejectsSingleWordTitleVariants(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "Batman.S01E02.1080p.WEB-DL"},
		{Title: "The.Batman.S01E02.1080p.WEB-DL"},
		{Title: "Batman.Beyond.S01E02.1080p.WEB-DL"},
	}

	filtered := ValidateSearchResults(releases, "series", "Batman S01E02", "1", "2", true, false)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Title != releases[0].Title {
		t.Fatalf("expected %q, got %q", releases[0].Title, filtered[0].Title)
	}
}

func TestFilterResultsSeriesEpisodeRequestMatchesPokemonAccentVariants(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "Pokemon.S01E01.1080p.WEB-DL"},
		{Title: "PokÃ©mon.S01E01.1080p.WEB-DL"},
		{Title: "Pokmon.S01E01.1080p.WEB-DL"},
		{Title: "Pokemon.Horizons.S01E01.1080p.WEB-DL"},
		{Title: "Pokemon.Origins.S01E01.1080p.WEB-DL"},
	}

	filtered := ValidateSearchResults(releases, "series", "Pokémon S01E01", "1", "1", true, false)
	if len(filtered) != 3 {
		t.Fatalf("expected 3 matches, got %d: %+v", len(filtered), filtered)
	}
	for i, want := range []string{releases[0].Title, releases[1].Title, releases[2].Title} {
		if filtered[i].Title != want {
			t.Fatalf("expected filtered[%d] = %q, got %q", i, want, filtered[i].Title)
		}
	}
}

func TestFilterResultsMovieRejectsNumberedTitleVariants(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "The.Hunger.Games.Mockingjay.Part.1.2014.2160p.UHD.BluRay.x265-TERMiNAL"},
		{Title: "The.Hunger.Games.Mockingjay.Part.2.2015.2160p.UHD.BluRay.x265-TERMiNAL"},
	}

	filtered := ValidateSearchResults(releases, "movie", "The Hunger Games: Mockingjay - Part 1 2014", "", "", true, true)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].Title != releases[0].Title {
		t.Fatalf("expected %q, got %q", releases[0].Title, filtered[0].Title)
	}
}

func TestFilterResultsMovieYearRange(t *testing.T) {
	logger.Init("ERROR")

	releases := []*release.Release{
		{Title: "Batman.1080p.BluRay"},
		{Title: "Batman.1993.1080p.BluRay"},
		{Title: "Batman.1994.1080p.BluRay"},
		{Title: "Batman.1995.1080p.BluRay"},
		{Title: "Batman.2026.1080p.BluRay"},
		{Title: "Other.Movie.1994.1080p.BluRay"},
	}

	filtered := ValidateSearchResults(releases, "movie", "Batman 1994", "", "", true, true)
	got := make([]string, 0, len(filtered))
	for _, rel := range filtered {
		if rel != nil {
			got = append(got, rel.Title)
		}
	}

	want := []string{
		"Batman.1080p.BluRay",
		"Batman.1993.1080p.BluRay",
		"Batman.1994.1080p.BluRay",
		"Batman.1995.1080p.BluRay",
	}

	if len(got) != len(want) {
		t.Fatalf("ValidateSearchResults() returned %d releases, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ValidateSearchResults()[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestFilterResultsMovieRejectsWrongIMDBMetadata(t *testing.T) {
	logger.Init("ERROR")

	// Simulate the bug: indexer returns "Dying Of The Light" for IMDB tt0816692 (Interstellar).
	// FilterResults must reject it because the title doesn't match.
	releases := []*release.Release{
		{Title: "Dying.Of.The.Light.2015.NORDiC.1080p.BluRay.HEVC.x265.DTS-TWA"},
		{Title: "Interstellar.2014.2160p.BluRay.REMUX.HEVC.DTS-HD.MA.5.1-FGT"},
		{Title: "Interstellar.2014.1080p.BluRay.x264-SPARKS"},
	}

	filtered := ValidateSearchResults(releases, "movie", "Interstellar 2014", "", "", true, true)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(filtered), filtered)
	}
	for _, rel := range filtered {
		if rel.Title == releases[0].Title {
			t.Fatalf("expected %q to be rejected, but it was kept", releases[0].Title)
		}
	}
}

func TestMergeAndDedupeSearchResultsKeepsFirstOccurrenceOrder(t *testing.T) {
	releases := []*release.Release{
		{Title: "First A", DetailsURL: "https://idx/details/a"},
		{Title: "First B", GUID: "https://idx/details/b"},
		{Title: "Dup A later", DetailsURL: "https://idx/details/a"},
		{Title: "Dup B later", GUID: "https://idx/details/b"},
		{Title: "Unique C", DetailsURL: "https://idx/details/c"},
	}

	got := MergeAndDedupeSearchResults(releases)
	if len(got) != 3 {
		t.Fatalf("expected 3 deduped releases, got %d: %+v", len(got), got)
	}
	if got[0].Title != "First A" || got[1].Title != "First B" || got[2].Title != "Unique C" {
		t.Fatalf("expected first occurrences to remain in order, got titles %q, %q, %q", got[0].Title, got[1].Title, got[2].Title)
	}
}

func TestMergeAndDedupeSearchResultsDoesNotUseTitleMatching(t *testing.T) {
	releases := []*release.Release{
		{Title: "The.Patriot.2000.1080p.BluRay", DetailsURL: "https://idx/details/1"},
		{Title: "The Patriot (2000) 1080p", DetailsURL: "https://idx/details/2"},
	}

	got := MergeAndDedupeSearchResults(releases)
	if len(got) != 2 {
		t.Fatalf("expected both distinct detail URLs to remain, got %d: %+v", len(got), got)
	}
}

func TestMergeAndDedupeSearchResultsPrioritizesLibrary(t *testing.T) {
	indexerRel := &release.Release{Title: "House.Of.The.Dragon.S01E01.1080p", DetailsURL: "https://idx/details/hotd1", Indexer: "NZBGeek"}
	libraryRel := &release.Release{Title: "House.Of.The.Dragon.S01E01.1080p", DetailsURL: "https://idx/details/hotd1", Indexer: "StreamNZB Library - NZBGeek", SourceIndexer: "library_struct", IsLibrary: true}

	releases := []*release.Release{indexerRel, libraryRel}
	got := MergeAndDedupeSearchResults(releases)
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped release, got %d", len(got))
	}
	if !got[0].IsLibraryResult() {
		t.Fatalf("expected library release to win during deduplication, got %#v", got[0])
	}
}
