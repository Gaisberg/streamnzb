package unpack

import (
	"context"
	"io"
	"testing"
)

// stubFile is a minimal UnpackableFile for blueprint (de)serialization tests.
type stubFile struct {
	name string
	size int64
}

func (f *stubFile) Name() string                                             { return f.name }
func (f *stubFile) Size() int64                                              { return f.size }
func (f *stubFile) EnsureSegmentMap() error                                  { return nil }
func (f *stubFile) OpenStream() (io.ReadSeekCloser, error)                   { return nil, nil }
func (f *stubFile) OpenStreamCtx(context.Context) (io.ReadSeekCloser, error) { return nil, nil }
func (f *stubFile) OpenReaderAt(context.Context, int64) (io.ReadCloser, error) {
	return nil, nil
}
func (f *stubFile) ReadAt([]byte, int64) (int, error) { return 0, nil }

func TestSerializeRehydrateArchiveBlueprintRoundTrip(t *testing.T) {
	vol1 := &stubFile{name: "xxx.part01.rar", size: 1000}
	vol2 := &stubFile{name: "xxx.part02.rar", size: 1000}
	orig := &ArchiveBlueprint{
		MainFileName: "movie.mkv",
		TotalSize:    2000,
		Target:       EpisodeTarget{Season: 1, Episode: 3},
		Parts: []VirtualPartDef{
			{VirtualStart: 0, VirtualEnd: 999, VolFile: vol1, VolOffset: 50},
			{VirtualStart: 1000, VirtualEnd: 1999, VolFile: vol2, VolOffset: 60},
		},
	}

	data, ok := SerializeArchiveBlueprint(orig)
	if !ok {
		t.Fatal("expected plaintext RAR blueprint to serialize")
	}

	// Fresh files (new handles), matched by name, possibly in a different order.
	fresh := []UnpackableFile{
		&stubFile{name: "xxx.part02.rar", size: 1000},
		&stubFile{name: "other.rar", size: 5},
		&stubFile{name: "xxx.part01.rar", size: 1000},
	}
	bp, ok := RehydrateArchiveBlueprint(data, fresh)
	if !ok {
		t.Fatal("expected rehydrate to succeed")
	}
	if bp.MainFileName != "movie.mkv" || bp.TotalSize != 2000 {
		t.Fatalf("header not restored: %+v", bp)
	}
	if bp.Target != (EpisodeTarget{Season: 1, Episode: 3}) {
		t.Fatalf("target not restored: %+v", bp.Target)
	}
	if len(bp.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(bp.Parts))
	}
	if bp.Parts[0].VolFile.Name() != "xxx.part01.rar" || bp.Parts[0].VolOffset != 50 {
		t.Fatalf("part0 mis-linked: %+v", bp.Parts[0])
	}
	if bp.Parts[1].VolFile.Name() != "xxx.part02.rar" || bp.Parts[1].VolOffset != 60 {
		t.Fatalf("part1 mis-linked: %+v", bp.Parts[1])
	}
	// The rehydrated part must reference the FRESH handle, not the original.
	if bp.Parts[0].VolFile == vol1 {
		t.Fatal("expected a freshly-linked volume handle, got the original")
	}
}

func TestRehydrateFailsClosedOnMissingVolume(t *testing.T) {
	orig := &ArchiveBlueprint{
		MainFileName: "movie.mkv",
		TotalSize:    1000,
		Parts:        []VirtualPartDef{{VirtualEnd: 999, VolFile: &stubFile{name: "a.part01.rar"}}},
	}
	data, ok := SerializeArchiveBlueprint(orig)
	if !ok {
		t.Fatal("serialize failed")
	}
	// No matching volume -> must fall back (nil,false), never a partial blueprint.
	if _, ok := RehydrateArchiveBlueprint(data, []UnpackableFile{&stubFile{name: "different.rar"}}); ok {
		t.Fatal("expected rehydrate to fail closed when a volume is missing")
	}
}

func TestSerializeSkipsEncryptedAndCompressed(t *testing.T) {
	enc := &ArchiveBlueprint{MainFileName: "m", AnyEncrypted: true, Parts: []VirtualPartDef{{VolFile: &stubFile{name: "x"}}}}
	if _, ok := SerializeArchiveBlueprint(enc); ok {
		t.Error("encrypted blueprint must not be serialized for reuse")
	}
	comp := &ArchiveBlueprint{MainFileName: "m", IsCompressed: true, Parts: []VirtualPartDef{{VolFile: &stubFile{name: "x"}}}}
	if _, ok := SerializeArchiveBlueprint(comp); ok {
		t.Error("compressed blueprint must not be serialized for reuse")
	}
	if _, ok := SerializeArchiveBlueprint(&DirectBlueprint{}); ok {
		t.Error("non-RAR blueprint must not be serialized by SerializeArchiveBlueprint")
	}
}
