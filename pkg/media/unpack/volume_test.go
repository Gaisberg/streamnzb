package unpack

import (
	"context"
	"io"
	"testing"
)

func TestGetRARVolumeNumber(t *testing.T) {
	tests := []struct {
		filename string
		expected int
	}{
		{"movie.part01.rar", 1},
		{"movie.part02.rar", 2},
		{"movie.part10.rar", 10},
		{"movie.rar", 0},
		{"movie.r00", 1},
		{"movie.r01", 2},
		{"movie.r99", 100},
		{"movie.s00", 101},
		{"movie.s01", 102},
		{"movie.s99", 200},
		{"movie.t00", 201},
		{"movie.z00", 801},
		{"movie.z99", 900},
		{"movie.mkv", -1},
		{"movie.txt", -1},
	}

	for _, tt := range tests {
		got := GetRARVolumeNumber(tt.filename)
		if got != tt.expected {
			t.Errorf("GetRARVolumeNumber(%q) = %d; want %d", tt.filename, got, tt.expected)
		}
	}
}

func TestGet7zVolumeNumber(t *testing.T) {
	tests := []struct {
		filename string
		expected int
	}{
		{"archive.7z.001", 1},
		{"archive.7z.002", 2},
		{"archive.7z", 0},
		{"archive.rar", -1},
	}

	for _, tt := range tests {
		got := Get7zVolumeNumber(tt.filename)
		if got != tt.expected {
			t.Errorf("Get7zVolumeNumber(%q) = %d; want %d", tt.filename, got, tt.expected)
		}
	}
}

func TestArchiveBaseName(t *testing.T) {
	tests := []struct {
		filename string
		wantBase string
		wantKind ArchiveKind
	}{
		{"Some.Movie.part01.rar", "Some.Movie", KindRAR},
		{"Some.Movie.r00", "Some.Movie", KindRAR},
		{"Some.Movie.s05", "Some.Movie", KindRAR},
		{"Some.Movie.rar", "Some.Movie", KindRAR},
		{"Release.7z.001", "Release", Kind7z},
		{"Release.7z", "Release", Kind7z},
	}

	for _, tt := range tests {
		res := ArchiveBaseName(tt.filename)
		if res == nil {
			t.Fatalf("ArchiveBaseName(%q) returned nil; want base %q kind %q", tt.filename, tt.wantBase, tt.wantKind)
		}
		if res.Base != tt.wantBase || res.Kind != tt.wantKind {
			t.Errorf("ArchiveBaseName(%q) = {Base: %q, Kind: %q}; want {Base: %q, Kind: %q}",
				tt.filename, res.Base, res.Kind, tt.wantBase, tt.wantKind)
		}
	}
}

type dummyUnpackable struct {
	name string
	size int64
}

func (d *dummyUnpackable) Name() string                           { return d.name }
func (d *dummyUnpackable) Size() int64                            { return d.size }
func (d *dummyUnpackable) EnsureSegmentMap() error                { return nil }
func (d *dummyUnpackable) OpenStream() (io.ReadSeekCloser, error) { return nil, nil }
func (d *dummyUnpackable) OpenStreamCtx(ctx context.Context) (io.ReadSeekCloser, error) {
	return nil, nil
}
func (d *dummyUnpackable) OpenReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	return nil, nil
}
func (d *dummyUnpackable) ReadAt(p []byte, off int64) (n int, err error) {
	return 0, nil
}

func TestDedupeVolumeMembers(t *testing.T) {
	files := []UnpackableFile{
		&dummyUnpackable{name: "release.part01.rar", size: 1000},
		&dummyUnpackable{name: "release.part01.rar", size: 5000}, // complete candidate
		&dummyUnpackable{name: "release.part02.rar", size: 5000},
	}

	deduped := dedupeVolumeMembers(files)
	if len(deduped) != 2 {
		t.Fatalf("dedupeVolumeMembers returned %d files; want 2", len(deduped))
	}
	if deduped[0].Size() != 5000 {
		t.Errorf("dedupeVolumeMembers kept size %d; want 5000", deduped[0].Size())
	}
}
