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

func openPlaybackReaderAt(f UnpackableFile, ctx context.Context, offset int64) (io.ReadCloser, error) {
	if o, ok := f.(playbackReaderAt); ok {
		return o.OpenPlaybackReaderAt(ctx, offset)
	}
	return f.OpenReaderAt(ctx, offset)
}
