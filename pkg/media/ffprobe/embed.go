package ffprobe

import (
	"os"
	"path/filepath"

	"streamnzb/pkg/core/logger"
)

// extractEmbeddedBinary is the indirection FindFFprobeBinary goes through so a
// test can stand in for a build that really embeds a binary: the embedffprobe
// tag and its bin/ payload are not available to an ordinary `go test` run.
var extractEmbeddedBinary = ExtractEmbeddedBinary

// ExtractEmbeddedBinary writes the embedded ffprobe binary (if this build was
// compiled with the `embedffprobe` build tag and a matching binary was present
// in bin/) to disk next to the executable and returns its path.
//
// Without the build tag, embeddedFFprobeBinary is the no-op stub in
// embed_disabled.go and this returns ("", false), leaving FindFFprobeBinary to
// fall back to PATH or the runtime downloader (installer.go).
func ExtractEmbeddedBinary() (string, bool) {
	data, name, ok := embeddedFFprobeBinary()
	if !ok || len(data) == 0 {
		return "", false
	}

	destDir := os.TempDir()
	if ex, err := os.Executable(); err == nil {
		destDir = filepath.Dir(ex)
	}

	outPath := filepath.Join(destDir, name)

	// Reuse an already-extracted binary of the same size to avoid rewriting a
	// large file on every startup.
	if fi, err := os.Stat(outPath); err == nil && fi.Size() == int64(len(data)) {
		return outPath, true
	}

	if err := os.WriteFile(outPath, data, 0755); err != nil {
		logger.Warn("Failed to write embedded ffprobe binary", "path", outPath, "err", err)
		return "", false
	}
	logger.Info("Extracted embedded ffprobe binary", "path", outPath, "bytes", len(data))
	return outPath, true
}
