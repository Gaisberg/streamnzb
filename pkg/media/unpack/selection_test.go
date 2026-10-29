package unpack

import (
	"errors"
	"testing"
)

func TestSelectEpisodeCandidatePrefersExactEpisode(t *testing.T) {
	target := EpisodeTarget{Season: 1, Episode: 5}
	best, ok := selectEpisodeCandidate([]namedEpisodeCandidate{
		{Name: "Show.S01.COMPLETE.mkv", Size: 900, Order: 0},
		{Name: "Show.S01E05.mkv", Size: 500, Order: 1},
		{Name: "Show.S01E05E06.mkv", Size: 700, Order: 2},
	}, target)
	if !ok {
		t.Fatal("expected episode candidate match")
	}
	if best.Name != "Show.S01E05.mkv" {
		t.Fatalf("expected exact episode match, got %q", best.Name)
	}
}

func TestSelectEpisodeCandidateMatchesAbsoluteNumberedAnime(t *testing.T) {
	// S10E01 == absolute 337; the file carries only the absolute number.
	target := EpisodeTarget{Season: 10, Episode: 1, Absolute: 337}
	best, ok := selectEpisodeCandidate([]namedEpisodeCandidate{
		{Name: "One Piece - 336 [1080p].mkv", Size: 900, Order: 0},
		{Name: "One Piece - 337 [1080p].mkv", Size: 800, Order: 1},
	}, target)
	if !ok {
		t.Fatal("expected absolute episode candidate match")
	}
	if best.Name != "One Piece - 337 [1080p].mkv" {
		t.Fatalf("expected absolute-numbered episode match, got %q", best.Name)
	}
}

// Season 0 is Stremio's Specials season, a literal target: the S00E01 file
// serves it, and a same-numbered S01E01 file alongside does not.
func TestSelectEpisodeCandidateTargetsSpecials(t *testing.T) {
	target := EpisodeTarget{Season: 0, Episode: 1}
	best, ok := selectEpisodeCandidate([]namedEpisodeCandidate{
		{Name: "Show.S01E01.mkv", Size: 900, Order: 0},
		{Name: "Show.S00E01.Christmas.Special.mkv", Size: 500, Order: 1},
	}, target)
	if !ok {
		t.Fatal("expected specials candidate match")
	}
	if best.Name != "Show.S00E01.Christmas.Special.mkv" {
		t.Fatalf("expected the S00E01 file, got %q", best.Name)
	}

	_, ok = selectEpisodeCandidate([]namedEpisodeCandidate{
		{Name: "Show.S01E01.mkv", Size: 900, Order: 0},
		{Name: "Show.S02E01.mkv", Size: 900, Order: 1},
	}, target)
	if ok {
		t.Fatal("S01E01 and S02E01 must not serve a season 0 target")
	}
}

func TestSelectMainFilePrefersRequestedEpisodeOverLargest(t *testing.T) {
	target := EpisodeTarget{Season: 1, Episode: 5}
	best, err := selectMainFile([]filePart{
		{name: "Show.S01E06.mkv", packedSize: 900, isMedia: true},
		{name: "Show.S01E05.mkv", packedSize: 500, isMedia: true},
	}, target)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if best != "Show.S01E05.mkv" {
		t.Fatalf("expected requested episode file, got %q", best)
	}
}

func TestSelectMainFileFailsWhenRequestedEpisodeMissing(t *testing.T) {
	target := EpisodeTarget{Season: 1, Episode: 5}
	best, err := selectMainFile([]filePart{
		{name: "Show.S01E06.mkv", packedSize: 900, isMedia: true},
		{name: "Show.S01E07.mkv", packedSize: 500, isMedia: true},
	}, target)
	if !errors.Is(err, ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
	if best != "" {
		t.Fatalf("expected no selected file, got %q", best)
	}
	if err.Error() == "" {
		t.Fatal("expected descriptive error")
	}
}

func TestSelectDirectFileIndexFailsWhenRequestedEpisodeMissing(t *testing.T) {
	target := EpisodeTarget{Season: 1, Episode: 5}
	idx, err := selectDirectFileIndex([]UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E06.mkv", data: []byte("ep6")},
		&memoryUnpackableFile{name: "Show.S01E07.mkv", data: []byte("ep7")},
	}, target)
	if !errors.Is(err, ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
	if idx != -1 {
		t.Fatalf("expected no selected index, got %d", idx)
	}
}

// From the field logs: a S01E01E02 pack whose sole inner file was obfuscated.
// ParseReleaseTitle read the hex stem "...00361e72" as episode 72, so the
// single-candidate escape hatch did not fire and a release that did contain the
// requested episode failed outright — and was banned for the bad-release TTL.
func TestSelectEpisodeCandidateAcceptsLoneObfuscatedFile(t *testing.T) {
	target := EpisodeTarget{Season: 1, Episode: 1}
	candidates := []namedEpisodeCandidate{
		{Name: "cc3299e0692fb5e3a8737f1c00361e72.mkv", Size: 700, Order: 0},
	}
	if _, _, err := selectEpisodeCandidateOrError(candidates, target, "rar_main_media"); err != nil {
		t.Fatalf("lone obfuscated media file must not fail the release, got %v", err)
	}
}

// The escape hatch is only for the ambiguous case: a readable filename that
// names a different episode is still a real mismatch.
func TestSelectEpisodeCandidateRejectsLoneNamedWrongEpisode(t *testing.T) {
	target := EpisodeTarget{Season: 1, Episode: 1}
	candidates := []namedEpisodeCandidate{
		{Name: "Show.S01E06.mkv", Size: 700, Order: 0},
	}
	if _, _, err := selectEpisodeCandidateOrError(candidates, target, "rar_main_media"); !errors.Is(err, ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
}

// Daily shows name their files by air date (#382). A lone date-named file is
// the episode whether or not the date is known; with it known, several dated
// files resolve to the right night.
func TestSelectEpisodeCandidateMatchesAirDate(t *testing.T) {
	target := EpisodeTarget{Season: 24, Episode: 2524, AirDate: "2026-10-26"}
	best, err := selectMainFile([]filePart{
		{name: "Jimmy.Kimmel.Live.2026.10.27.1080p.WEB.h264-GROUP.mkv", packedSize: 900, isMedia: true},
		{name: "Jimmy.Kimmel.Live.2026.10.26.1080p.WEB.h264-GROUP.mkv", packedSize: 800, isMedia: true},
	}, target)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if best != "Jimmy.Kimmel.Live.2026.10.26.1080p.WEB.h264-GROUP.mkv" {
		t.Fatalf("expected the file for the air date, got %q", best)
	}

	idx, err := selectDirectFileIndex([]UnpackableFile{
		&memoryUnpackableFile{name: "Show.2026.10.25.mkv", data: []byte("mon")},
		&memoryUnpackableFile{name: "Show.2026.10.26.mkv", data: []byte("tue")},
	}, target)
	if err != nil || idx != 1 {
		t.Fatalf("direct selection = %d, %v; want index 1", idx, err)
	}

	for _, lone := range []EpisodeTarget{target, {Season: 24, Episode: 2524}} {
		candidates := []namedEpisodeCandidate{{Name: "Jimmy.Kimmel.Live.2026.10.26.1080p.WEB.h264-GROUP.mkv", Size: 700}}
		if _, _, err := selectEpisodeCandidateOrError(candidates, lone, "rar_main_media"); err != nil {
			t.Fatalf("lone daily file (air date %q) must not fail the release, got %v", lone.AirDate, err)
		}
	}
}

// A lone file is played whatever date its name carries, as before #382:
// the release was already accepted by its title, and a metadata date that
// disagrees with the file name is weaker evidence than that. Several files
// with nothing to tell them apart are still never guessed between.
func TestSelectEpisodeCandidateDailyFilesFailOpenOnlyWhenAlone(t *testing.T) {
	target := EpisodeTarget{Season: 24, Episode: 2524, AirDate: "2026-10-26"}

	lone := []namedEpisodeCandidate{{Name: "Jimmy.Kimmel.Live.2026.10.27.1080p.WEB.h264-GROUP.mkv", Size: 700}}
	for _, tc := range []EpisodeTarget{target, {Season: 24, Episode: 2524}} {
		if _, _, err := selectEpisodeCandidateOrError(lone, tc, "rar_main_media"); err != nil {
			t.Fatalf("lone file dated another night (target air date %q) must not fail the release, got %v", tc.AirDate, err)
		}
	}

	best, err := selectMainFile([]filePart{
		{name: "Jimmy.Kimmel.Live.Part.A.mkv", packedSize: 900, isMedia: true},
		{name: "Jimmy.Kimmel.Live.Part.B.mkv", packedSize: 800, isMedia: true},
	}, target)
	if !errors.Is(err, ErrEpisodeTargetNotFound) || best != "" {
		t.Fatalf("ambiguous files: got %q, %v; want ErrEpisodeTargetNotFound", best, err)
	}
	best, err = selectMainFile([]filePart{
		{name: "Jimmy.Kimmel.Live.2026.10.25.mkv", packedSize: 900, isMedia: true},
		{name: "Jimmy.Kimmel.Live.2026.10.27.mkv", packedSize: 800, isMedia: true},
	}, target)
	if !errors.Is(err, ErrEpisodeTargetNotFound) || best != "" {
		t.Fatalf("other nights only: got %q, %v; want ErrEpisodeTargetNotFound", best, err)
	}
}
