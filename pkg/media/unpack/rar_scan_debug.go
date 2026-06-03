package unpack

import (
	"context"
	"encoding/hex"
	"io"

	"streamnzb/pkg/core/logger"
)

func logRARScanDebug(f UnpackableFile, err error) {
	attrs := []any{
		"err", err,
		"virtual_size", f.Size(),
		"stream_read_pos", ScanTraceLastReadPos(),
	}
	if preview, ok := readVolumeHeaderPreview(f); ok {
		attrs = append(attrs, "header_hex", preview)
	}
	logger.Debug("RAR scan debug", attrs...)
}

func readVolumeHeaderPreview(f UnpackableFile) (string, bool) {
	const previewLen = 32
	buf := make([]byte, previewLen)
	n, err := f.ReadAt(buf, 0)
	if err != nil && n == 0 {
		if reader, openErr := f.OpenReaderAt(context.Background(), 0); openErr == nil {
			defer reader.Close()
			n, err = io.ReadFull(reader, buf)
		}
	}
	if n <= 0 {
		return "", false
	}
	return hex.EncodeToString(buf[:n]), true
}
