package unpack

import (
	"bytes"
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

// ProbeMediaStreamByContent validates stream bytes primarily by container
// headers and not only by filename. This allows playback of obfuscated
// filenames when the underlying bytes clearly represent media containers.
func ProbeMediaStreamByContent(stream io.ReadSeeker, name string, size int64) error {
	if err := ProbeMediaStream(stream, name, size); err == nil {
		return nil
	}
	if size <= 0 {
		return fmt.Errorf("probe: invalid size %d", size)
	}
	const sniffSize = 4096
	readSize := sniffSize
	if size < int64(readSize) {
		readSize = int(size)
	}
	buf := make([]byte, readSize)
	n, err := io.ReadFull(stream, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return err
	}
	buf = buf[:n]
	if _, err := stream.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if hasKnownMediaHeader(buf) {
		return nil
	}
	return fmt.Errorf("probe: invalid container header for %s", name)
}

func hasKnownMediaHeader(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	if seek.ValidateContainerHeader(data, "probe.mkv") {
		return true
	}
	if seek.ValidateContainerHeader(data, "probe.mp4") {
		return true
	}
	// AVI RIFF header: "RIFF" .... "AVI "
	if len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("AVI ")) {
		return true
	}
	// MPEG-TS sync byte at packet starts.
	if len(data) >= 376 && data[0] == 0x47 && data[188] == 0x47 {
		return true
	}
	return false
}
