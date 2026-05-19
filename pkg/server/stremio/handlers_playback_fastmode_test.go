package stremio

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
)

func TestPlaybackStartupTimeoutDoublesWhenFastModeDisabled(t *testing.T) {
	s := &Server{config: &config.Config{
		PlaybackStartupTimeoutSeconds: 7,
		FailoverFastMode:              false,
	}}

	if got := s.playbackStartupTimeout(); got != 14*time.Second {
		t.Fatalf("playbackStartupTimeout() = %v, want %v", got, 14*time.Second)
	}
}

func TestPlaybackStartupTimeoutUsesConfiguredWhenFastModeEnabled(t *testing.T) {
	s := &Server{config: &config.Config{
		PlaybackStartupTimeoutSeconds: 7,
		FailoverFastMode:              true,
	}}

	if got := s.playbackStartupTimeout(); got != 7*time.Second {
		t.Fatalf("playbackStartupTimeout() = %v, want %v", got, 7*time.Second)
	}
}

func TestShouldReportBadReleaseToAvailNZBRespectsFastMode(t *testing.T) {
	slow := &Server{config: &config.Config{FailoverFastMode: false}}
	if !slow.shouldReportBadReleaseToAvailNZB(errors.New("EOF")) {
		t.Fatalf("expected EOF to be reportable when fast mode is disabled")
	}

	fast := &Server{config: &config.Config{FailoverFastMode: true}}
	if fast.shouldReportBadReleaseToAvailNZB(errors.New("EOF")) {
		t.Fatalf("expected EOF to be skipped when fast mode is enabled")
	}
	if !fast.shouldReportBadReleaseToAvailNZB(ErrFirstSegmentUnavailable) {
		t.Fatalf("expected definitive unavailable error to stay reportable in fast mode")
	}
	if !fast.shouldReportBadReleaseToAvailNZB(errors.New("[rapidyenc] data corruption detected")) {
		t.Fatalf("expected data corruption to stay reportable in fast mode")
	}
}

func TestIsPlayPrepareCancellationAllowsFailoverForLazyNZBTimeout(t *testing.T) {
	wrapped := errors.Join(context.DeadlineExceeded, errors.New("failed to lazy download NZB: failed to read NZB data from NZBgeek"))
	if isPlayPrepareCancellation(wrapped) {
		t.Fatalf("expected lazy NZB timeout to be treated as recoverable failover error")
	}
}
