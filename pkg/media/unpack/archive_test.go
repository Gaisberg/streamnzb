package unpack

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestGetMediaStreamForEpisodeSkipsCachedDirectBlueprintForDifferentTarget(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E01.mkv", data: []byte("ep1")},
		&memoryUnpackableFile{name: "Show.S01E04.mkv", data: []byte("ep4")},
	}
	cachedBP := &DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 1, Target: EpisodeTarget{Season: 1, Episode: 4}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected requested episode file, got %q", name)
	}
	if bp == cachedBP {
		t.Fatal("expected cached direct blueprint to be replaced")
	}
	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	if string(data) != "ep1" {
		t.Fatalf("expected episode 1 stream data, got %q", string(data))
	}
}

func TestGetMediaStreamForEpisodeSkipsCachedFailureForDifferentTarget(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E01.mkv", data: []byte("ep1")},
	}
	cachedBP := &FailedBlueprint{Err: io.EOF, Target: EpisodeTarget{Season: 1, Episode: 4}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected requested episode file, got %q", name)
	}
	if bp == cachedBP {
		t.Fatal("expected cached failed blueprint to be replaced")
	}
}

func TestGetMediaStreamForEpisodeFailsWhenRequestedEpisodeMissingFromDirectFiles(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E04.mkv", data: []byte("ep4")},
		&memoryUnpackableFile{name: "Show.S01E06.mkv", data: []byte("ep6")},
	}

	stream, name, _, _, err := GetMediaStreamForEpisode(context.Background(), files, nil, "", EpisodeTarget{Season: 1, Episode: 1})
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("expected missing-episode error")
	}
	if !errors.Is(err, ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
	if name != "" {
		t.Fatalf("expected no selected file, got %q", name)
	}
	if stream != nil {
		t.Fatal("expected no stream on missing episode")
	}
}

func TestGetMediaStreamRejectsSuspiciousLargestDirectFallback(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "release.par2", data: []byte("par2")},
			size:                 90 * 1024 * 1024,
		},
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "0)", data: []byte("bad")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisode(context.Background(), files, nil, "", EpisodeTarget{})
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("expected suspicious direct fallback to be rejected")
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if name != "" {
		t.Fatalf("expected no selected file, got %q", name)
	}
	if stream != nil {
		t.Fatal("expected no stream for suspicious direct fallback")
	}
}

func TestGetMediaStreamAllowsPlausibleLargestDirectFallback(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: []byte("video")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisode(context.Background(), files, nil, "", EpisodeTarget{})
	if err != nil {
		t.Fatalf("expected plausible direct fallback to succeed, got %v", err)
	}
	defer stream.Close()

	if name != "abc12345" {
		t.Fatalf("expected plausible fallback name, got %q", name)
	}
}

func TestGetMediaStreamForEpisodeWithHintsRefusesLargestDirectFallbackWhenDisabled(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: []byte("video")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisodeWithHints(
		context.Background(),
		files,
		nil,
		"",
		EpisodeTarget{},
		StreamSelectionHints{AllowLargestDirectFallback: false},
	)
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("expected disabled largest direct fallback to fail")
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if name != "" {
		t.Fatalf("expected no selected file, got %q", name)
	}
	if stream != nil {
		t.Fatal("expected no stream when fallback is disabled")
	}
}

func TestGetMediaStreamForEpisodeAllowsLargestDirectFallbackWhenHinted(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&sizedUnpackableFile{
			memoryUnpackableFile: &memoryUnpackableFile{name: "abc12345", data: []byte("video")},
			size:                 80 * 1024 * 1024,
		},
	}

	stream, name, _, _, err := GetMediaStreamForEpisodeWithHints(
		context.Background(),
		files,
		nil,
		"",
		EpisodeTarget{Season: 1, Episode: 1},
		StreamSelectionHints{AllowLargestDirectFallback: true},
	)
	if err != nil {
		t.Fatalf("expected hinted targeted fallback to succeed, got %v", err)
	}
	defer stream.Close()

	if name != "abc12345" {
		t.Fatalf("expected plausible fallback name, got %q", name)
	}
}

func TestPlausibleLargestDirectFallbackAllowsCommonReleasePunctuation(t *testing.T) {
	if !isPlausibleLargestDirectFallbackName("Movie, Title '11 [1080p]") {
		t.Fatal("expected common release punctuation to remain plausible")
	}
}
