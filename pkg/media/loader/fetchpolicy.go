package loader

import (
	"context"
	"errors"
)

// This file owns the segment-fetch policy knobs that higher layers (unpack)
// need to influence. They live here, in the layer that actually fetches
// segments, so the archive layer depends on the segment layer and not the
// other way around.

// ErrTooManyZeroFills marks a read that gave up after too many segments could
// not be fetched, so the gaps were zero-filled past the tolerated threshold.
// Callers treat it as evidence the release itself is bad, not a transient blip.
var ErrTooManyZeroFills = errors.New("too many failed segments")

// PlaybackReadAheadSegments is how many segments ahead of a playback seek/read
// position we prefetch when crossing multi-volume archive boundaries.
const PlaybackReadAheadSegments = 24

type skipGapProbingContextKey struct{}

// WithSkipGapProbing marks ctx as a playback-time read, where the expensive
// gap probing used for one-time archive sizing is skipped.
func WithSkipGapProbing(ctx context.Context, enabled bool) context.Context {
	if !enabled {
		return ctx
	}
	return context.WithValue(ctx, skipGapProbingContextKey{}, true)
}

// IsSkipGapProbingEnabled reports whether ctx asked to skip gap probing.
func IsSkipGapProbingEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(skipGapProbingContextKey{}).(bool)
	return enabled
}
