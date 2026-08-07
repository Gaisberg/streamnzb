package nzb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamnzb/pkg/core/logger"
)

func TestCompressionType_posterAttribute(t *testing.T) {

	logger.Init("warn")

	path := filepath.Join("..", "..", "..", "The.Hobbit.The.Desolation.Of.Smaug.Extended.(2013).HDR.10bit.2160p.BT2020.DTS.HD.MA-VISIONPLUSHDR1000.NLsubs.nzb")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("NZB file not found: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	nzb, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ct := nzb.CompressionType()
	if ct != "rar" {
		t.Errorf("CompressionType() = %q, want %q", ct, "rar")
	}
	contentFiles := nzb.GetContentFiles()
	if len(contentFiles) == 0 {
		t.Error("GetContentFiles() returned empty, expected RAR parts")
	}
}

func TestGetContentFilesForEpisodePrefersMatchingEpisode(t *testing.T) {
	logger.Init("ERROR")

	n := &NZB{Files: []File{
		{Subject: "Show.S01E06.1080p.mkv", Segments: []Segment{{ID: "<a>", Bytes: 600}}},
		{Subject: "Show.S01E05.1080p.mkv", Segments: []Segment{{ID: "<b>", Bytes: 500}}},
	}}

	files := n.GetContentFilesForEpisode(1, 5)
	if len(files) != 1 {
		t.Fatalf("expected one matching file, got %d", len(files))
	}
	if !strings.Contains(files[0].Filename, "S01E05") {
		t.Fatalf("expected episode 5 file, got %q", files[0].Filename)
	}
}

func TestGetContentFilesForEpisodePrefersSeasonPackAndOrdersFirstVolume(t *testing.T) {
	logger.Init("ERROR")

	n := &NZB{Files: []File{
		{Subject: "Show.S02E01.1080p.mkv", Segments: []Segment{{ID: "<a>", Bytes: 900}}},
		{Subject: "Show.S01.COMPLETE.part02.rar", Segments: []Segment{{ID: "<b>", Bytes: 350}}},
		{Subject: "Show.S01.COMPLETE.part01.rar", Segments: []Segment{{ID: "<c>", Bytes: 300}}},
	}}

	files := n.GetContentFilesForEpisode(1, 5)
	if len(files) != 2 {
		t.Fatalf("expected season pack archive set, got %d files", len(files))
	}
	if !strings.Contains(files[0].Filename, "part01") {
		t.Fatalf("expected first archive volume first, got %q", files[0].Filename)
	}
	if !strings.Contains(strings.ToLower(files[0].Filename), "show.s01.complete") {
		t.Fatalf("expected season pack selection, got %q", files[0].Filename)
	}
}

func TestGetPlaybackFileForEpisodePrefersRequestedEpisodeOverLargerOtherEpisode(t *testing.T) {
	logger.Init("ERROR")

	n := &NZB{Files: []File{
		{Subject: "Show.S01E06.1080p.mkv", Segments: []Segment{{ID: "<a>", Bytes: 900}}},
		{Subject: "Show.S01E05.1080p.mkv", Segments: []Segment{{ID: "<b>", Bytes: 500}}},
	}}

	info := n.GetPlaybackFileForEpisode(1, 5)
	if info == nil {
		t.Fatal("expected playback file")
	}
	if !strings.Contains(info.Filename, "S01E05") {
		t.Fatalf("expected episode 5 playback file, got %q", info.Filename)
	}
}

func TestCompressionTypeForEpisodeUsesSelectedPackGroup(t *testing.T) {
	logger.Init("ERROR")

	n := &NZB{Files: []File{
		{Subject: "Show.S01E05.1080p.mkv", Segments: []Segment{{ID: "<a>", Bytes: 300}}},
		{Subject: "Show.S02.COMPLETE.part01.rar", Segments: []Segment{{ID: "<b>", Bytes: 900}}},
		{Subject: "Show.S02.COMPLETE.part02.rar", Segments: []Segment{{ID: "<c>", Bytes: 950}}},
	}}

	if ct := n.CompressionTypeForEpisode(1, 5); ct != "direct" {
		t.Fatalf("expected direct compression for requested episode, got %q", ct)
	}
}

func TestGetSessionContentFilesForEpisodeKeepsAllCandidatesWhenNoEpisodeMatchExists(t *testing.T) {
	logger.Init("ERROR")

	n := &NZB{Files: []File{
		{Subject: "Altered.Carbon.Release.A.part02.rar", Segments: []Segment{{ID: "<a>", Bytes: 310}}},
		{Subject: "Altered.Carbon.Release.B.part01.rar", Segments: []Segment{{ID: "<b>", Bytes: 420}}},
		{Subject: "Altered.Carbon.Release.A.part01.rar", Segments: []Segment{{ID: "<c>", Bytes: 300}}},
		{Subject: "Altered.Carbon.Release.B.part02.rar", Segments: []Segment{{ID: "<d>", Bytes: 410}}},
	}}

	files := n.GetSessionContentFilesForEpisode(2, 1, 0)
	if len(files) != 4 {
		t.Fatalf("expected all content candidates, got %d", len(files))
	}
	var sawA, sawB bool
	for _, file := range files {
		if strings.Contains(file.Filename, "Release.A") {
			sawA = true
		}
		if strings.Contains(file.Filename, "Release.B") {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected files from both fallback groups, sawA=%v sawB=%v", sawA, sawB)
	}
}

func TestGetSessionContentFilesForEpisodeMatchesAbsoluteNumberedAnime(t *testing.T) {
	logger.Init("ERROR")

	// One Piece S10E01 == absolute 337; the release carries only the absolute
	// number, alongside a decoy episode from the same show.
	n := &NZB{Files: []File{
		{Subject: "One Piece - 337 - Plunging into the Devils Sea [1080p][x264].mkv", Segments: []Segment{{ID: "<a>", Bytes: 900}}},
		{Subject: "One Piece - 338 - Landing to Get to Fish-Man Island [1080p][x264].mkv", Segments: []Segment{{ID: "<b>", Bytes: 950}}},
	}}

	files := n.GetSessionContentFilesForEpisode(10, 1, 337)
	if len(files) != 1 {
		t.Fatalf("expected the absolute-numbered episode group, got %d files", len(files))
	}
	if !strings.Contains(files[0].Filename, "337") {
		t.Fatalf("expected episode 337 file, got %q", files[0].Filename)
	}
}
