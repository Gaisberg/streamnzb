package ffprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
)

// Minimal valid PCM WAV.
func tinyWAV() []byte {
	var b bytes.Buffer
	data := make([]byte, 16000) // 0.5s of 16kHz mono s16
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(16000))
	binary.Write(&b, binary.LittleEndian, uint32(32000))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

func TestProbeSeekableStreamViaLoopback(t *testing.T) {
	bin := os.Getenv("FFPROBE_BIN")
	if bin == "" {
		t.Skip("FFPROBE_BIN not set; run with FFPROBE_BIN=/path/to/ffprobe for the end-to-end loopback probe")
	}
	// Thorough probe rides the loopback server; the quick serve-time probe
	// stays on the bounded pipe. Both must work.
	for _, opts := range []ProbeOptions{{}, {QuickHeader: true}} {
		res, err := ProbeStreamWithOptions(context.Background(), bytes.NewReader(tinyWAV()), bin, opts)
		if err != nil {
			t.Fatalf("probe (quick=%v) failed: %v", opts.QuickHeader, err)
		}
		if !res.HasAudio {
			t.Fatalf("probe (quick=%v): expected audio stream, got %+v", opts.QuickHeader, res)
		}
		t.Logf("probe ok (quick=%v): audio_codec=%s duration=%.2fs", opts.QuickHeader, res.AudioCodec, res.DurationSeconds)
	}
}
