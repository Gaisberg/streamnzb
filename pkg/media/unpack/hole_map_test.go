package unpack

import (
	"context"
	"testing"

	"streamnzb/pkg/media/ebml"
)

// holedVolume is a volume that knows which of its bytes the loader made up.
type holedVolume struct {
	*memoryUnpackableFile
	holes []ebml.Range
}

func (v *holedVolume) ZeroFilledRanges() []ebml.Range { return v.holes }

func volumeBytes(n int, fill byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = fill
	}
	return out
}

// A hole is a range of one volume's bytes; the stream needs it as a range of
// the movie. Only this layer knows the volume map that connects the two.
func TestVirtualStreamTranslatesVolumeHolesIntoStreamOffsets(t *testing.T) {
	// Two volumes of 1000 bytes each, of which the archive stores bytes
	// 100..900 as the movie: the stream is 1600 bytes long.
	vol1 := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "part1", data: volumeBytes(1000, 'a')},
		holes:                []ebml.Range{{Start: 50, End: 120}, {Start: 300, End: 400}},
	}
	vol2 := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "part2", data: volumeBytes(1000, 'b')},
		holes:                []ebml.Range{{Start: 100, End: 250}},
	}
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 800, VolFile: vol1, VolOffset: 100},
		{VirtualStart: 800, VirtualEnd: 1600, VolFile: vol2, VolOffset: 100},
	}, 1600, 0)
	defer stream.Close()

	want := []ebml.Range{
		// Clipped to the part: the volume's own header is not part of the movie.
		{Start: 0, End: 20},
		{Start: 200, End: 300},
		{Start: 800, End: 950},
	}
	got := stream.ZeroFilledRanges()
	if len(got) != len(want) {
		t.Fatalf("ranges = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranges = %+v, want %+v", got, want)
		}
	}
}

// A run of missing articles that straddles a volume boundary is two volumes'
// holes and one span of the movie; the repair has to see the span.
func TestVirtualStreamMergesAHoleAcrossAVolumeBoundary(t *testing.T) {
	vol1 := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "part1", data: volumeBytes(1000, 'a')},
		holes:                []ebml.Range{{Start: 900, End: 1000}},
	}
	vol2 := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "part2", data: volumeBytes(1000, 'b')},
		holes:                []ebml.Range{{Start: 0, End: 100}},
	}
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 1000, VolFile: vol1},
		{VirtualStart: 1000, VirtualEnd: 2000, VolFile: vol2},
	}, 2000, 0)
	defer stream.Close()

	got := stream.ZeroFilledRanges()
	if len(got) != 1 || got[0] != (ebml.Range{Start: 900, End: 1100}) {
		t.Fatalf("ranges = %+v, want one span 900-1100", got)
	}
}

// Encrypted parts decrypt to something other than the zeros the loader wrote,
// so there is no structure to repair and the stream reports no holes.
func TestVirtualStreamReportsNoHolesForEncryptedParts(t *testing.T) {
	vol := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "part1", data: volumeBytes(1000, 'a')},
		holes:                []ebml.Range{{Start: 100, End: 200}},
	}
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 1000, VolFile: vol, AesKey: make([]byte, 16)},
	}, 1000, 0)
	defer stream.Close()

	if got := stream.ZeroFilledRanges(); got != nil {
		t.Fatalf("an encrypted stream must report no holes, got %+v", got)
	}
}

// A volume that cannot say what it made up contributes nothing, rather than
// making the whole stream unrepairable.
func TestVirtualStreamIgnoresVolumesThatCannotReportHoles(t *testing.T) {
	quiet := &memoryUnpackableFile{name: "part1", data: volumeBytes(1000, 'a')}
	loud := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "part2", data: volumeBytes(1000, 'b')},
		holes:                []ebml.Range{{Start: 10, End: 20}},
	}
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 1000, VolFile: quiet},
		{VirtualStart: 1000, VirtualEnd: 2000, VolFile: loud},
	}, 2000, 0)
	defer stream.Close()

	got := stream.ZeroFilledRanges()
	if len(got) != 1 || got[0] != (ebml.Range{Start: 1010, End: 1020}) {
		t.Fatalf("ranges = %+v", got)
	}
}

// The repair reads structure around a hole; doing that must not move the
// position the stream is being served from, and must work across volumes.
func TestVirtualStreamReadAtSpansVolumesWithoutMovingThePosition(t *testing.T) {
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 3, VolFile: &memoryUnpackableFile{name: "part1", data: []byte("abc")}},
		{VirtualStart: 3, VirtualEnd: 7, VolFile: &memoryUnpackableFile{name: "part2", data: []byte("defg")}},
	}, 7, 0)
	defer stream.Close()

	buf := make([]byte, 4)
	if _, err := stream.Read(buf[:2]); err != nil {
		t.Fatalf("read: %v", err)
	}
	before, _ := stream.Seek(0, 1)

	n, err := stream.ReadAt(buf, 2)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf[:n]) != "cdef" {
		t.Fatalf("ReadAt = %q, want %q", buf[:n], "cdef")
	}
	if after, _ := stream.Seek(0, 1); after != before {
		t.Fatalf("ReadAt moved the position from %d to %d", before, after)
	}
}

// An obfuscated release is served through renamedFile, which embeds only the
// UnpackableFile interface — a capability it does not forward explicitly is
// invisible to the type assertion here, and the repair would be silently off
// for exactly the releases that already need the most from this layer.
func TestRenamedVolumesStillReportTheirHoles(t *testing.T) {
	vol := &holedVolume{
		memoryUnpackableFile: &memoryUnpackableFile{name: "abc123", data: volumeBytes(1000, 'a')},
		holes:                []ebml.Range{{Start: 100, End: 200}},
	}
	renamed := &renamedFile{UnpackableFile: vol, name: "movie.part01.rar"}
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 1000, VolFile: renamed},
	}, 1000, 0)
	defer stream.Close()

	got := stream.ZeroFilledRanges()
	if len(got) != 1 || got[0] != (ebml.Range{Start: 100, End: 200}) {
		t.Fatalf("ranges = %+v, want the wrapped volume's hole", got)
	}
}
