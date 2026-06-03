package unpack

import (
	"context"
	"errors"
	"fmt"
)

type archiveFastFailoverContextKey struct{}
type archiveScanIOTraceContextKey struct{}
type skipGapProbingContextKey struct{}

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

func WithArchiveFastFailoverMode(ctx context.Context, enabled bool) context.Context {
	if !enabled {
		return ctx
	}
	return context.WithValue(ctx, archiveFastFailoverContextKey{}, true)
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

func IsArchiveFastFailoverModeEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(archiveFastFailoverContextKey{}).(bool)
	return enabled
}

var ErrArchiveFastProbe = errors.New("archive fast probe incomplete")

func markArchiveFastProbe(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrArchiveFastProbe, err)
}

func maybeMarkArchiveFastProbe(ctx context.Context, err error) error {
	if err == nil || !IsArchiveFastFailoverModeEnabled(ctx) {
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
