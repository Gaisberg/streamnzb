package unpack

import (
	"context"
	"errors"
)

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
