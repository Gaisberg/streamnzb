package seek

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"testing"
)

func TestInspectStreamStartParsesMP4AndResetsOffset(t *testing.T) {
	data := makeTestMP4(1000, 10000)
	stream := bytes.NewReader(data)

	info, err := InspectStreamStart(stream, int64(len(data)), "video.mp4", len(data))
	if err != nil {
		t.Fatalf("InspectStreamStart returned error: %v", err)
	}
	if !info.HeaderValid {
		t.Fatal("expected MP4 header to be valid")
	}
	if !info.DurationKnown {
		t.Fatal("expected MP4 duration to be detected")
	}
	if info.DurationSec != 10 {
		t.Fatalf("expected duration 10s, got %v", info.DurationSec)
	}
	if pos, err := stream.Seek(0, io.SeekCurrent); err != nil {
		t.Fatalf("failed to read current offset: %v", err)
	} else if pos != 0 {
		t.Fatalf("expected stream to be reset to start, got offset %d", pos)
	}
}

func makeTestMP4(timescale, duration uint32) []byte {
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp[0:4], uint32(len(ftyp)))
	copy(ftyp[4:8], []byte("ftyp"))
	copy(ftyp[8:12], []byte("isom"))

	mvhd := make([]byte, 20)
	binary.BigEndian.PutUint32(mvhd[0:4], uint32(len(mvhd)))
	copy(mvhd[4:8], []byte("mvhd"))
	binary.BigEndian.PutUint32(mvhd[12:16], timescale)
	binary.BigEndian.PutUint32(mvhd[16:20], duration)

	moov := make([]byte, 8+len(mvhd))
	binary.BigEndian.PutUint32(moov[0:4], uint32(len(moov)))
	copy(moov[4:8], []byte("moov"))
	copy(moov[8:], mvhd)

	return append(ftyp, moov...)
}

// mkvmerge and ffmpeg both write a SeekHead ahead of the segment Info, and the
// SeekHead names Info by its ID: the duration is read from Info itself, not
// from that reference to it.
func TestInspectStreamStartReadsMKVDurationBehindSeekHead(t *testing.T) {
	for _, c := range []struct {
		name     string
		duration []byte // the Duration element's payload, nil for none
		want     float64
		known    bool
	}{
		{"float64", binary.BigEndian.AppendUint64(nil, math.Float64bits(2789792)), 2789.792, true},
		{"float32", binary.BigEndian.AppendUint32(nil, math.Float32bits(1440000)), 1440, true},
		{"no duration", nil, 0, false},
	} {
		data := makeTestMKV(c.duration)
		info, err := InspectStreamStart(bytes.NewReader(data), int64(len(data)), "video.mkv", len(data))
		if err != nil || !info.HeaderValid {
			t.Fatalf("%s: header %+v, %v", c.name, info, err)
		}
		if info.DurationKnown != c.known || math.Abs(info.DurationSec-c.want) > 1e-6 {
			t.Fatalf("%s: duration %v known=%v, want %v known=%v", c.name, info.DurationSec, info.DurationKnown, c.want, c.known)
		}
	}
}

// Info elements whose sizes overrun, truncate or break the buffer leave the
// duration unknown. The sizes past 2^31 panicked where int is 32 bits wide.
func TestMKVDurationUnknownForMalformedInfoSizes(t *testing.T) {
	duration := append([]byte{0x44, 0x89, 0x88}, binary.BigEndian.AppendUint64(nil, math.Float64bits(5000))...)
	for _, c := range []struct {
		name string
		info []byte
	}{
		{"size 2^32-1", []byte{0x2A, 0xD7, 0xB1, 0x01, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0x01}},
		{"size 2^31 ahead of duration", append([]byte{0xEC, 0x01, 0, 0, 0, 0x80, 0, 0, 0}, duration...)},
		{"largest known 8-byte size", []byte{0x2A, 0xD7, 0xB1, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE, 0x01}},
		{"truncated duration", duration[:6]},
		{"size vint cut off", []byte{0x44, 0x89, 0x01, 0x00}},
		{"invalid size vint", []byte{0x44, 0x89, 0x00, 0x01}},
	} {
		data := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80, 0x18, 0x53, 0x80, 0x67, 0xFF, 0x15, 0x49, 0xA9, 0x66, 0x80 | byte(len(c.info))}
		if d, ok := durationFromMKV(append(data, c.info...)); ok {
			t.Fatalf("%s: duration %v known, want unknown", c.name, d)
		}
	}
}

// A Duration or TimestampScale that yields no finite positive length leaves
// the duration unknown; NaN in particular slips past a plain <= 0 check.
func TestMKVDurationUnknownForNonFiniteOrNonPositiveValues(t *testing.T) {
	f64 := func(v float64) []byte {
		return append([]byte{0x44, 0x89, 0x88}, binary.BigEndian.AppendUint64(nil, math.Float64bits(v))...)
	}
	f32 := func(v float32) []byte {
		return append([]byte{0x44, 0x89, 0x84}, binary.BigEndian.AppendUint32(nil, math.Float32bits(v))...)
	}
	scale := func(v uint64) []byte {
		return append([]byte{0x2A, 0xD7, 0xB1, 0x88}, binary.BigEndian.AppendUint64(nil, v)...)
	}
	for _, c := range []struct {
		name  string
		info  []byte
		want  float64
		known bool
	}{
		{"float64 NaN", f64(math.NaN()), 0, false},
		{"float64 +Inf", f64(math.Inf(1)), 0, false},
		{"float64 -Inf", f64(math.Inf(-1)), 0, false},
		{"float64 zero", f64(0), 0, false},
		{"float64 negative", f64(-5000), 0, false},
		{"float32 NaN", f32(float32(math.NaN())), 0, false},
		{"float32 +Inf", f32(float32(math.Inf(1))), 0, false},
		{"float32 -Inf", f32(float32(math.Inf(-1))), 0, false},
		{"zero TimestampScale", append(scale(0), f64(5000)...), 0, false},
		{"scaling overflows", append(scale(1e18), f64(1e300)...), 0, false},
		{"float64 valid", f64(5000), 5, true},
		{"float32 valid", f32(1440000), 1440, true},
		{"second TimestampScale", append(scale(1e9), f64(2.5)...), 2.5, true},
	} {
		data := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80, 0x18, 0x53, 0x80, 0x67, 0xFF, 0x15, 0x49, 0xA9, 0x66, 0x80 | byte(len(c.info))}
		if d, ok := durationFromMKV(append(data, c.info...)); ok != c.known || d != c.want {
			t.Errorf("%s: duration %v known=%v, want %v known=%v", c.name, d, ok, c.want, c.known)
		}
	}
}

// makeTestMKV lays a Matroska head out the way mkvmerge does: EBML header,
// then a Segment opening with a SeekHead that points at Info, a Void, and
// Info with a millisecond TimestampScale and, when given, a Duration.
func makeTestMKV(duration []byte) []byte {
	el := func(id []byte, payload ...byte) []byte {
		return append(append(append([]byte{}, id...), 0x01, 0, 0, 0, 0, 0, 0, byte(len(payload))), payload...)
	}
	info := el([]byte{0x2A, 0xD7, 0xB1}, 0x0F, 0x42, 0x40)
	if duration != nil {
		info = append(info, el([]byte{0x44, 0x89}, duration...)...)
	}
	seek := append(el([]byte{0x53, 0xAB}, 0x15, 0x49, 0xA9, 0x66), el([]byte{0x53, 0xAC}, 0x10, 0x00)...)
	segment := el([]byte{0x11, 0x4D, 0x9B, 0x74}, el([]byte{0x4D, 0xBB}, seek...)...)
	segment = append(segment, el([]byte{0xEC}, make([]byte, 64)...)...)
	segment = append(segment, el([]byte{0x15, 0x49, 0xA9, 0x66}, info...)...)
	out := el([]byte{0x1A, 0x45, 0xDF, 0xA3}, el([]byte{0x42, 0x82}, []byte("matroska")...)...)
	// The Segment's size is unknown, as a muxer that has not finished writes it.
	return append(append(out, 0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF), segment...)
}
