// Command fetch-ffprobe downloads the prebuilt ffprobe binary for every target
// platform into pkg/media/ffprobe/bin/ using the platform-suffixed names the
// per-platform //go:embed directives expect. Run it before a release build:
//
//	go run ./tools/fetch-ffprobe
//	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embedffprobe ./cmd/streamnzb
//
// It is safe to re-run; existing files are overwritten. Building without the
// `embedffprobe` tag does not require these files.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"streamnzb/pkg/media/ffprobe"
)

type target struct {
	goos, goarch string
	// file is the destination name inside bin/, matching the //go:embed directive.
	file string
}

var targets = []target{
	{"windows", "amd64", "ffprobe-windows-amd64.exe"},
	{"linux", "amd64", "ffprobe-linux-amd64"},
	{"linux", "arm64", "ffprobe-linux-arm64"},
	{"darwin", "amd64", "ffprobe-darwin-amd64"},
	{"darwin", "arm64", "ffprobe-darwin-arm64"},
}

func main() {
	// Resolve bin/ relative to this source file so the tool works from any CWD.
	binDir := "pkg/media/ffprobe/bin"
	if len(os.Args) > 1 {
		binDir = os.Args[1]
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "create bin dir: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var failed bool
	for _, t := range targets {
		dest := filepath.Join(binDir, t.file)
		fmt.Printf("downloading ffprobe %s/%s -> %s\n", t.goos, t.goarch, dest)
		if err := ffprobe.DownloadFFprobeBinary(ctx, t.goos, t.goarch, dest); err != nil {
			fmt.Fprintf(os.Stderr, "  FAILED %s/%s: %v\n", t.goos, t.goarch, err)
			failed = true
			continue
		}
		if fi, err := os.Stat(dest); err == nil {
			fmt.Printf("  ok (%d bytes)\n", fi.Size())
		}
	}

	if failed {
		os.Exit(1)
	}
	fmt.Println("all ffprobe binaries fetched")
}
