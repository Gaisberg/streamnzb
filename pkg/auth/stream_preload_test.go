package auth

import (
	"testing"

	"streamnzb/pkg/core/config"
)

func intPtr(v int) *int { return &v }

func TestEffectivePreloadAttempts(t *testing.T) {
	cfgDefault := &config.Config{}
	cfgLegacyFive := &config.Config{SpeculativePreProbingMaxAttempts: 5}

	cases := []struct {
		name   string
		stream *Stream
		cfg    *config.Config
		want   int
	}{
		// nil per-stream value inherits the deployment default, so configs
		// written before the setting moved to the stream behave unchanged.
		{"nil_stream_nil_cfg", nil, nil, config.DefaultSpeculativePreProbingMaxAttempts},
		{"unset_inherits_legacy_global", &Stream{}, cfgLegacyFive, 5},
		{"explicit_off", &Stream{PreloadAttempts: intPtr(0)}, cfgLegacyFive, 0},
		{"explicit_count", &Stream{PreloadAttempts: intPtr(2)}, cfgDefault, 2},
		{"clamped_high", &Stream{PreloadAttempts: intPtr(9)}, cfgDefault, 5},
		{"clamped_negative", &Stream{PreloadAttempts: intPtr(-3)}, cfgDefault, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stream.EffectivePreloadAttempts(tc.cfg); got != tc.want {
				t.Fatalf("EffectivePreloadAttempts = %d, want %d", got, tc.want)
			}
		})
	}
}

// The Stream <-> config.StreamEntry conversion is positional; a field added to
// one and not the other fails to compile, but a *pointer* field must also
// survive the round trip with its set/unset distinction intact.
func TestPreloadAttemptsRoundTripsThroughEntry(t *testing.T) {
	s := &Stream{Username: "u", PreloadAttempts: intPtr(2)}
	back := streamFromEntry(streamToEntry(s))
	if back.PreloadAttempts == nil || *back.PreloadAttempts != 2 {
		t.Fatalf("PreloadAttempts lost in conversion: %v", back.PreloadAttempts)
	}
	s2 := &Stream{Username: "u"}
	if back2 := streamFromEntry(streamToEntry(s2)); back2.PreloadAttempts != nil {
		t.Fatalf("unset PreloadAttempts became %v", *back2.PreloadAttempts)
	}
}
