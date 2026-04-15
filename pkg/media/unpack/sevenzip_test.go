package unpack

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"

	"streamnzb/pkg/core/logger"
)

type nopReadSeekCloser struct {
	*bytes.Reader
}

func (n *nopReadSeekCloser) Close() error { return nil }

type memoryUnpackableFile struct {
	name string
	data []byte
}

type sizedUnpackableFile struct {
	*memoryUnpackableFile
	size         int64
	resolvedSize int64
	ensureCalls  int
}

func (f *memoryUnpackableFile) Name() string { return f.name }

func (f *memoryUnpackableFile) Size() int64 { return int64(len(f.data)) }

func (f *memoryUnpackableFile) EnsureSegmentMap() error { return nil }

func (f *memoryUnpackableFile) OpenStream() (io.ReadSeekCloser, error) {
	return f.OpenStreamCtx(context.Background())
}

func (f *memoryUnpackableFile) OpenStreamCtx(ctx context.Context) (io.ReadSeekCloser, error) {
	return &nopReadSeekCloser{Reader: bytes.NewReader(f.data)}, nil
}

func (f *memoryUnpackableFile) OpenReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(f.data)) {
		offset = int64(len(f.data))
	}
	return io.NopCloser(bytes.NewReader(f.data[offset:])), nil
}

func (f *memoryUnpackableFile) ReadAt(p []byte, off int64) (int, error) {
	return bytes.NewReader(f.data).ReadAt(p, off)
}

func (f *sizedUnpackableFile) Size() int64 { return f.size }

func (f *sizedUnpackableFile) EnsureSegmentMap() error {
	f.ensureCalls++
	f.size = f.resolvedSize
	return nil
}

func TestFilesToPartsSplitSevenZipUsesFirstVolumeSizeForMiddleParts(t *testing.T) {
	first := &sizedUnpackableFile{
		memoryUnpackableFile: &memoryUnpackableFile{name: "release.7z.001"},
		size:                 110,
		resolvedSize:         100,
	}
	middle := &sizedUnpackableFile{
		memoryUnpackableFile: &memoryUnpackableFile{name: "release.7z.002"},
		size:                 130,
		resolvedSize:         120,
	}
	last := &sizedUnpackableFile{
		memoryUnpackableFile: &memoryUnpackableFile{name: "release.7z.003"},
		size:                 90,
		resolvedSize:         80,
	}

	parts, err := filesToParts(context.Background(), []UnpackableFile{first, middle, last})
	if err != nil {
		t.Fatalf("filesToParts returned error: %v", err)
	}

	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	if parts[0].Size != 100 || parts[1].Size != 100 || parts[2].Size != 80 {
		t.Fatalf("expected part sizes [100 100 80], got [%d %d %d]", parts[0].Size, parts[1].Size, parts[2].Size)
	}
	if first.ensureCalls != 1 {
		t.Fatalf("expected first volume EnsureSegmentMap once, got %d", first.ensureCalls)
	}
	if middle.ensureCalls != 0 {
		t.Fatalf("expected middle volume EnsureSegmentMap to be skipped, got %d", middle.ensureCalls)
	}
	if last.ensureCalls != 1 {
		t.Fatalf("expected last volume EnsureSegmentMap once, got %d", last.ensureCalls)
	}
}

func TestGetMediaStreamForEpisodeUsesCachedSevenZipBlueprintFiles(t *testing.T) {
	oldLogger := logger.Log
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	defer func() {
		logger.Log = oldLogger
	}()

	bp := &SevenZipBlueprint{
		MainFileName: "episode.mkv",
		TotalSize:    4,
		FileOffset:   2,
		Files: []UnpackableFile{
			&memoryUnpackableFile{name: "episode.7z.001", data: []byte("abcd")},
			&memoryUnpackableFile{name: "episode.7z.002", data: []byte("efgh")},
		},
	}

	stream, name, size, cached, err := GetMediaStreamForEpisode(context.Background(), nil, bp, "", EpisodeTarget{})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "episode.mkv" {
		t.Fatalf("expected stream name %q, got %q", "episode.mkv", name)
	}
	if size != 4 {
		t.Fatalf("expected stream size 4, got %d", size)
	}
	if cached != bp {
		t.Fatal("expected cached blueprint to be returned")
	}

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read cached 7z stream: %v", err)
	}
	if string(data) != "cdef" {
		t.Fatalf("expected mapped stream %q, got %q", "cdef", string(data))
	}
}

func TestGetMediaStreamForEpisodeSkipsCachedSevenZipBlueprintForDifferentTarget(t *testing.T) {
	oldLogger := logger.Log
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	defer func() {
		logger.Log = oldLogger
	}()

	files := []UnpackableFile{
		&memoryUnpackableFile{name: "Show.S01E01.mkv", data: []byte("ep1")},
		&memoryUnpackableFile{name: "Show.S01E04.mkv", data: []byte("ep4")},
	}
	cachedBP := &SevenZipBlueprint{
		MainFileName: "Show.S01E04.mkv",
		TotalSize:    3,
		FileOffset:   0,
		Files:        []UnpackableFile{&memoryUnpackableFile{name: "pack.7z.001", data: []byte("ep4")}},
		Target:       EpisodeTarget{Season: 1, Episode: 4},
	}

	stream, name, _, bp, err := GetMediaStreamForEpisode(context.Background(), files, cachedBP, "", EpisodeTarget{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("GetMediaStreamForEpisode returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected requested episode file, got %q", name)
	}
	if bp == cachedBP {
		t.Fatal("expected cached 7z blueprint to be replaced")
	}
}
