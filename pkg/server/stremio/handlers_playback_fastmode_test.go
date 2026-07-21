package stremio

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/media/unpack"
)

func TestPlaybackStartupTimeoutUsesConfiguredValue(t *testing.T) {
	s := &Server{config: &config.Config{
		PlaybackStartupTimeoutSeconds: 7,
	}}

	if got := s.playbackStartupTimeout(); got != 7*time.Second {
		t.Fatalf("playbackStartupTimeout() = %v, want %v", got, 7*time.Second)
	}
}

func TestShouldReportBadReleaseToAvailNZBFastModeAlwaysOn(t *testing.T) {
	s := &Server{config: &config.Config{}}
	if s.shouldReportBadReleaseToAvailNZB(errors.New("EOF")) {
		t.Fatalf("expected EOF to be skipped in fast mode")
	}
	if !s.shouldReportBadReleaseToAvailNZB(ErrFirstSegmentUnavailable) {
		t.Fatalf("expected definitive unavailable error to stay reportable in fast mode")
	}
	if !s.shouldReportBadReleaseToAvailNZB(errors.New("[rapidyenc] data corruption detected")) {
		t.Fatalf("expected data corruption to stay reportable in fast mode")
	}
}

func TestShouldReportBadReleaseToAvailNZBSkipsArchiveFastProbe(t *testing.T) {
	s := &Server{config: &config.Config{}}
	if s.shouldReportBadReleaseToAvailNZB(unpack.ErrArchiveFastProbe) {
		t.Fatalf("expected archive fast probe errors to be skipped")
	}
}

func TestIsPlayPrepareCancellationAllowsFailoverForLazyNZBTimeout(t *testing.T) {
	wrapped := errors.Join(context.DeadlineExceeded, errors.New("failed to lazy download NZB: failed to read NZB data from NZBgeek"))
	if isPlayPrepareCancellation(wrapped) {
		t.Fatalf("expected lazy NZB timeout to be treated as recoverable failover error")
	}
}
