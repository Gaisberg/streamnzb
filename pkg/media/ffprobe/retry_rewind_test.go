package ffprobe

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeFFprobe stands in for an ffprobe too old to know stream_side_data. It
// rejects the modern query the way 4.x does — during option parsing, without
// reading a byte of stdin — and answers the legacy one by recording the first
// bytes it was actually given.
const fakeFFprobe = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    *stream_side_data*)
      echo "No match for section 'stream_side_data'" >&2
      exit 1
      ;;
  esac
done
head -c 8 > "$FFPROBE_RECORD"
cat > /dev/null
printf '{"streams":[{"codec_type":"video","codec_name":"hevc","width":3840,"height":2160}],"format":{"duration":"10.0"}}'
`

// TestRetryRewindsStreamOnPipePath is the regression guard for a probe that
// reported a 40GB HEVC remux as audio-only.
//
// The first run dies in ffprobe's option parsing, but exec has already started
// copying the stream into the child's stdin pipe and drains a pipe buffer's
// worth before noticing the process is gone. Retrying without rewinding hands
// ffprobe a stream starting mid-container: no header, no duration, and
// whatever elementary stream it stumbles into — which the validation layer
// reads as definitive proof the release has no video track.
func TestRetryRewindsStreamOnPipePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake probe binary is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(bin, []byte(fakeFFprobe), 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "seen")
	t.Setenv("FFPROBE_RECORD", record)

	// Far larger than a pipe buffer, so a leaked first run is detectable.
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	copy(payload, []byte("HEADER!!"))

	// QuickHeader keeps this on the pipe path, which is what the serve-time
	// validation in pkg/playback uses.
	res, err := ProbeStreamWithOptions(context.Background(), bytes.NewReader(payload), bin, ProbeOptions{QuickHeader: true})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !res.HasVideo || res.Width != 3840 {
		t.Fatalf("probe result: %+v", res)
	}

	seen, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the retry never read the stream: %v", err)
	}
	if !bytes.Equal(seen, []byte("HEADER!!")) {
		t.Fatalf("retry started at the wrong offset: read %q, want the first 8 bytes of the stream", seen)
	}
}
