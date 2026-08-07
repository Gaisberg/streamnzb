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
