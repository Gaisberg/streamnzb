package unpack

import (
	"fmt"
	"io"

	"streamnzb/pkg/media/seek"
)

// ProbeSize is the number of bytes to read when probing a media stream before serving.
// Enough to cover MKV/MP4/WebM container headers and to trigger "relevant segments"
// for RAR/7z (first volume's first chunk of extracted data).
const ProbeSize = 256 * 1024

// ProbeMediaStream reads the first ProbeSize bytes, validates container headers for the
// given filename (MKV/MP4/direct), then seeks back to 0. For RAR/7z the stream is
// the extracted content so this checks that the first segments delivered valid headers.
// Returns an error if read/seek fails or headers are invalid.
func ProbeMediaStream(stream io.ReadSeeker, name string, size int64) error {
	if size <= 0 {
		return fmt.Errorf("probe: invalid size %d", size)
	}
	info, err := seek.InspectStreamStart(stream, size, name, ProbeSize)
	if err != nil {
		return fmt.Errorf("probe inspect: %w", err)
	}
	if !info.HeaderValid {
		return fmt.Errorf("probe: invalid container header for %s", name)
	}
	return nil
}
