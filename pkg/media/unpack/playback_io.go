package unpack

import (
	"context"
	"io"
)

type playbackReaderAt interface {
	OpenPlaybackReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error)
}

type playbackPrefetcher interface {
	PrefetchPlaybackOffset(ctx context.Context, offset int64)
}

type playbackStreamOpener interface {
	OpenPlaybackStreamCtx(ctx context.Context) (io.ReadSeekCloser, error)
}

func openPlaybackReaderAt(f UnpackableFile, ctx context.Context, offset int64) (io.ReadCloser, error) {
	if o, ok := f.(playbackReaderAt); ok {
		return o.OpenPlaybackReaderAt(ctx, offset)
	}
	return f.OpenReaderAt(ctx, offset)
}

// openPlaybackStream is openPlaybackReaderAt for a whole-file stream: it asks
// for the playback read-ahead window when the file offers one, and otherwise
// falls back to the scan-sized default.
func openPlaybackStream(f UnpackableFile, ctx context.Context) (io.ReadSeekCloser, error) {
	if o, ok := f.(playbackStreamOpener); ok {
		return o.OpenPlaybackStreamCtx(ctx)
	}
	return f.OpenStreamCtx(ctx)
}
