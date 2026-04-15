package unpack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"streamnzb/pkg/core/logger"
)

func discardTestLogger(t *testing.T) {
	t.Helper()
	oldLogger := logger.Log
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Cleanup(func() {
		logger.Log = oldLogger
	})
}

type trackingUnpackableFile struct {
	*memoryUnpackableFile
	ensureCalls int
}

func (f *trackingUnpackableFile) EnsureSegmentMap() error {
	f.ensureCalls++
	return nil
}

func TestAggregateRemainingVolumesFromStartSkipsSegmentDetectionWhenProbeProvidesPackedSize(t *testing.T) {
	discardTestLogger(t)

	firstFile := &trackingUnpackableFile{memoryUnpackableFile: &memoryUnpackableFile{name: "release.part001.rar", data: []byte("first")}}
	secondFile := &trackingUnpackableFile{memoryUnpackableFile: &memoryUnpackableFile{name: "release.part002.rar", data: []byte("second")}}
	thirdFile := &trackingUnpackableFile{memoryUnpackableFile: &memoryUnpackableFile{name: "release.part003.rar", data: []byte("third")}}

	parts, err := aggregateRemainingVolumesFromStart(
		context.Background(),
		[]filePart{{name: "movie.mkv", unpackedSize: 1000, dataOffset: 100, packedSize: 200, volFile: firstFile, volName: firstFile.Name(), isMedia: true}},
		[]UnpackableFile{firstFile, secondFile, thirdFile},
		0,
		"movie.mkv",
		1000,
		continuationProbe{dataOffset: 24, packedSize: 300},
	)
	if err != nil {
		t.Fatalf("aggregateRemainingVolumesFromStart returned error: %v", err)
	}

	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	if got := parts[1].packedSize; got != 300 {
		t.Fatalf("expected second continuation packed size 300, got %d", got)
	}
	if got := parts[2].packedSize; got != 500 {
		t.Fatalf("expected final continuation packed size 500, got %d", got)
	}
	if secondFile.ensureCalls != 0 || thirdFile.ensureCalls != 0 {
		t.Fatalf("expected no EnsureSegmentMap calls with probe metadata, got second=%d third=%d", secondFile.ensureCalls, thirdFile.ensureCalls)
	}
}

func TestAggregateRemainingVolumesFromStartFallsBackToSegmentDetectionWithoutProbePackedSize(t *testing.T) {
	discardTestLogger(t)

	firstFile := &trackingUnpackableFile{memoryUnpackableFile: &memoryUnpackableFile{name: "release.part001.rar", data: []byte("first")}}
	secondFile := &trackingUnpackableFile{memoryUnpackableFile: &memoryUnpackableFile{name: "release.part002.rar", data: make([]byte, 350)}}

	parts, err := aggregateRemainingVolumesFromStart(
		context.Background(),
		[]filePart{{name: "movie.mkv", unpackedSize: 500, dataOffset: 100, packedSize: 200, volFile: firstFile, volName: firstFile.Name(), isMedia: true}},
		[]UnpackableFile{firstFile, secondFile},
		0,
		"movie.mkv",
		500,
		continuationProbe{dataOffset: 50},
	)
	if err != nil {
		t.Fatalf("aggregateRemainingVolumesFromStart returned error: %v", err)
	}

	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if got := secondFile.ensureCalls; got != 1 {
		t.Fatalf("expected one EnsureSegmentMap call in fallback path, got %d", got)
	}
	if got := parts[1].packedSize; got != 300 {
		t.Fatalf("expected fallback packed size 300, got %d", got)
	}
}

func TestGetMediaStreamForEpisodeSkipsCachedArchiveBlueprintForDifferentTarget(t *testing.T) {
	discardTestLogger(t)

	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E01.mkv", data: []byte("ep1")},
		&memoryUnpackableFile{name: "Show.S01E04.mkv", data: []byte("ep4")},
	}
	cachedBP := &ArchiveBlueprint{MainFileName: "Show.S01E04.mkv", Target: EpisodeTarget{Season: 1, Episode: 4}}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected requested episode file, got %q", name)
	}
	if bp == cachedBP {
		t.Fatal("expected cached archive blueprint to be replaced")
	}
}

func TestTryNestedArchiveFailsWhenRequestedEpisodeMissing(t *testing.T) {
	discardTestLogger(t)

	_, err := tryNestedArchive(context.Background(), []filePart{
		{name: "Show.S01E04.rar", packedSize: 100},
		{name: "Show.S01E04.r00", packedSize: 100},
	}, "", EpisodeTarget{Season: 1, Episode: 1})
	if !errors.Is(err, ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
}

func TestScanArchiveReturnsContextErrorWhenCanceled(t *testing.T) {
	discardTestLogger(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ScanArchive(ctx, []UnpackableFile{
		&memoryUnpackableFile{name: "release.part01.rar", data: []byte("ignored")},
	}, "", EpisodeTarget{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
