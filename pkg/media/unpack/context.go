package unpack

import (
	"context"
	"errors"
	"fmt"
)

type archiveScanIOTraceContextKey struct{}
type skipGapProbingContextKey struct{}

const MaxNestDepth = 3

type nestDepthContextKey struct{}

func WithNestDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, nestDepthContextKey{}, depth)
}

func NestDepthFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	depth, _ := ctx.Value(nestDepthContextKey{}).(int)
	return depth
}

func WithSkipGapProbing(ctx context.Context, enabled bool) context.Context {
	if !enabled {
		return ctx
	}
	return context.WithValue(ctx, skipGapProbingContextKey{}, true)
}

func IsSkipGapProbingEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(skipGapProbingContextKey{}).(bool)
	return enabled
}

func WithArchiveScanIOTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, archiveScanIOTraceContextKey{}, true)
}

func IsArchiveScanIOTraceEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(archiveScanIOTraceContextKey{}).(bool)
	return enabled
}

// playbackSegmentMapCtx returns a context for on-demand segment-map detection during
// playback reads/seeks. It skips expensive gap probing that is only needed for
// one-time archive sizing.
func playbackSegmentMapCtx(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return WithSkipGapProbing(ctx, true)
}

var ErrArchiveFastProbe = errors.New("archive fast probe incomplete")

func markArchiveFastProbe(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrArchiveFastProbe, err)
}

// maybeMarkArchiveFastProbe wraps non-nil errors with ErrArchiveFastProbe.
// Fast failover mode is always enabled, so archive scan errors are always
// marked as fast-probe incomplete so callers can avoid reporting them to
// AvailNZB as definitive bad-release signals.
func maybeMarkArchiveFastProbe(_ context.Context, err error) error {
	if err == nil {
		return err
	}
	return markArchiveFastProbe(err)
}

type contextAwareSegmentMapEnsurer interface {
	EnsureSegmentMapCtx(ctx context.Context) error
}

func ensureSegmentMap(ctx context.Context, f UnpackableFile) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if ensurer, ok := f.(contextAwareSegmentMapEnsurer); ok {
		return ensurer.EnsureSegmentMapCtx(ctx)
	}
	return f.EnsureSegmentMap()
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
