package decode

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

// The size error is the one the tolerance rule is built on, so the numbers
// have to come out of it exactly — a misparse silently turns a normal short
// final segment into a corrupt release.
func TestClassifyReadsTheSizeMismatch(t *testing.T) {
	err := classify(errors.New("expected size 768000 but got 767994"))

	var mismatch *SizeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("classify returned %T, want *SizeMismatchError", err)
	}
	if mismatch.Expected != 768000 || mismatch.Got != 767994 {
		t.Errorf("parsed %d/%d, want 768000/767994", mismatch.Expected, mismatch.Got)
	}
	if mismatch.Shortfall() != 6 {
		t.Errorf("Shortfall() = %d, want 6", mismatch.Shortfall())
	}
	// The original wording has to survive into a log line.
	if !errors.Is(err, mismatch.Err) || mismatch.Err == nil {
		t.Error("the underlying decoder error should stay reachable")
	}
}

// A size mismatch is not damage. Treating it as such would report the release
// bad over a rounded number the poster wrote.
func TestSizeMismatchIsNotCorruption(t *testing.T) {
	if IsCorrupt(classify(errors.New("expected size 768000 but got 767994"))) {
		t.Error("a size mismatch must not classify as corruption")
	}
}

func TestClassifyRecognisesTheDecodersDamageWordings(t *testing.T) {
	for _, msg := range []string{
		"rapidyenc: data corruption detected",
		"[rapidyenc] data corruption detected",
		`reached end of input without finding "=yend" trailer`,
		"reached end of input without finding '=yend' trailer",
		"DATA CORRUPTION in segment",
	} {
		if err := classify(errors.New(msg)); !errors.Is(err, ErrCorrupt) {
			t.Errorf("classify(%q) = %v, want it to be ErrCorrupt", msg, err)
		}
	}
}

// The safety property. Corruption fails the slot over and reports the release
// to a community database; a read that timed out or was cancelled means
// nothing about the release, and must never be mistaken for damage.
func TestClassifyLeavesEverythingElseAlone(t *testing.T) {
	for _, err := range []error{
		io.ErrUnexpectedEOF,
		errors.New("read tcp 10.0.0.1:119: i/o timeout"),
		errors.New("430 no such article"),
		errors.New("context canceled"),
		errors.New("use of closed network connection"),
	} {
		got := classify(err)
		if !errors.Is(got, err) {
			t.Errorf("classify(%v) replaced the error with %v", err, got)
		}
		if IsCorrupt(got) {
			t.Errorf("%v must not classify as corruption", err)
		}
	}
}

func TestIsCorrupt(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if IsCorrupt(nil) {
			t.Error("nil is not corruption")
		}
	})

	// The classified error travels up through several layers of wrapping
	// before anything asks about it.
	t.Run("through wrapping", func(t *testing.T) {
		err := classify(errors.New("rapidyenc: data corruption detected"))
		wrapped := fmt.Errorf("segment 3: %w", fmt.Errorf("decode: %w", err))
		if !IsCorrupt(wrapped) {
			t.Error("wrapping should not hide corruption")
		}
	})

	// An archive reader sitting between the decoder and the caller may flatten
	// the error into its own message. Until every such path wraps, the text
	// fallback is what keeps a damaged article counted as evidence.
	t.Run("through a flattened message", func(t *testing.T) {
		flattened := errors.New("rardecode: invalid file block: rapidyenc: data corruption detected")
		if !IsCorrupt(flattened) {
			t.Error("a flattened decoder message should still read as corruption")
		}
	})
}

// The tolerance rule is the reason classification exists, so it is worth
// asserting at the boundary rather than only through the decoder.
func TestSizeMismatchShortfallBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expected  int64
		got       int64
		shortfall int64
	}{
		{"nominal rounding", 768000, 767994, 6},
		{"at the tolerance", 768000, 768000 - maxDecodeSizeTolerance, maxDecodeSizeTolerance},
		{"past the tolerance", 768000, 768000 - maxDecodeSizeTolerance - 1, maxDecodeSizeTolerance + 1},
		{"overshoot is negative", 768000, 768010, -10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &SizeMismatchError{Expected: tc.expected, Got: tc.got}
			if e.Shortfall() != tc.shortfall {
				t.Errorf("Shortfall() = %d, want %d", e.Shortfall(), tc.shortfall)
			}
		})
	}
}
