package unpack

import (
	"context"
	"errors"
)

// Blueprint is a cached plan for reaching the requested media inside a release
// without rescanning it. Scanning a large RAR set is the expensive part of
// starting playback, so the plan is built once, serialized into the library,
// and replayed on later plays.
//
// Each concrete blueprint knows how to reopen its own stream. Open receives the
// loader files because the direct path streams one of them by index while the
// archive paths read through their own recorded volume map.
type Blueprint interface {
	// TargetEpisode reports which episode this blueprint was built for, so a
	// cached plan is never reused for a different request.
	TargetEpisode() EpisodeTarget

	// Open reopens the media stream this blueprint describes, returning the
	// stream, the inner file name and its size.
	Open(ctx context.Context, files []UnpackableFile, password string) (ReadSeekCloser, string, int64, error)

	// Kind is a short label used in logs ("rar", "7z", "direct", "failed").
	Kind() string
}

var (
	_ Blueprint = (*ArchiveBlueprint)(nil)
	_ Blueprint = (*SevenZipBlueprint)(nil)
	_ Blueprint = (*DirectBlueprint)(nil)
	_ Blueprint = (*FailedBlueprint)(nil)
)

func (b *ArchiveBlueprint) TargetEpisode() EpisodeTarget { return b.Target }
func (b *ArchiveBlueprint) Kind() string                 { return "rar" }

func (b *ArchiveBlueprint) Open(ctx context.Context, _ []UnpackableFile, password string) (ReadSeekCloser, string, int64, error) {
	return StreamFromBlueprint(ctx, b, password)
}

func (b *SevenZipBlueprint) TargetEpisode() EpisodeTarget { return b.Target }
func (b *SevenZipBlueprint) Kind() string                 { return "7z" }

func (b *SevenZipBlueprint) Open(ctx context.Context, _ []UnpackableFile, password string) (ReadSeekCloser, string, int64, error) {
	if len(b.Files) == 0 {
		return nil, "", 0, errors.New("7z set empty for cached blueprint")
	}
	stream, name, size, err := Open7zStreamFromBlueprint(ctx, b, password)
	if err != nil {
		err = maybeMarkArchiveFastProbe(ctx, err)
	}
	return stream, name, size, err
}

func (b *DirectBlueprint) TargetEpisode() EpisodeTarget { return b.Target }
func (b *DirectBlueprint) Kind() string                 { return "direct" }

// ErrBlueprintFileIndexOutOfRange means the cached file index no longer matches
// the release's file list, so the plan must be rebuilt by a fresh scan.
var ErrBlueprintFileIndexOutOfRange = errors.New("blueprint file index out of range")

func (b *DirectBlueprint) Open(ctx context.Context, files []UnpackableFile, _ string) (ReadSeekCloser, string, int64, error) {
	if b.FileIndex < 0 || b.FileIndex >= len(files) {
		return nil, "", 0, ErrBlueprintFileIndexOutOfRange
	}
	f := files[b.FileIndex]
	stream, err := f.OpenStreamCtx(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	return stream, b.FileName, f.Size(), nil
}

func (b *FailedBlueprint) TargetEpisode() EpisodeTarget { return b.Target }
func (b *FailedBlueprint) Kind() string                 { return "failed" }

// Open replays the scan failure this blueprint recorded, so a release already
// proven unusable fails immediately instead of being rescanned.
func (b *FailedBlueprint) Open(context.Context, []UnpackableFile, string) (ReadSeekCloser, string, int64, error) {
	return nil, "", 0, b.Err
}
