package stremio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"testing"

	"streamnzb/pkg/media/unpack"
)

// primeTestStream answers the priming read with a canned outcome and records the
// offset it was asked to start from.
type primeTestStream struct {
	size      int64
	readErr   error
	offset    int64
	seekCalls []int64
	readCalls int
}

func (s *primeTestStream) Read(p []byte) (int, error) {
	s.readCalls++
	if s.readErr != nil {
		return 0, s.readErr
	}
	if s.offset >= s.size {
		return 0, io.EOF
	}
	n := copy(p, make([]byte, 1))
	s.offset += int64(n)
	return n, nil
}

func (s *primeTestStream) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		return 0, errors.New("unexpected whence")
	}
	s.seekCalls = append(s.seekCalls, offset)
	s.offset = offset
	return offset, nil
}

func missingArticleErr() error {
	return fmt.Errorf("segment unavailable: %w", &textproto.Error{Code: 430, Msg: "No Such Article"})
}

func TestPrimeRangeStartSurfacesAnUnservableRange(t *testing.T) {
	stream := &primeTestStream{size: 4096, readErr: missingArticleErr()}

	err := primeRangeStart(stream, "bytes=1024-", stream.size)
	if !isFatalStreamErr(err) {
		t.Fatalf("primeRangeStart err = %v, want a fatal stream error", err)
	}
	if len(stream.seekCalls) == 0 || stream.seekCalls[0] != 1024 {
		t.Fatalf("primed from %v, want the range start 1024", stream.seekCalls)
	}
}

func TestPrimeRangeStartPassesAServableRange(t *testing.T) {
	stream := &primeTestStream{size: 4096}

	if err := primeRangeStart(stream, "bytes=2048-4095", stream.size); err != nil {
		t.Fatalf("primeRangeStart returned error: %v", err)
	}
	if stream.readCalls != 1 {
		t.Fatalf("read %d times, want exactly one primed byte", stream.readCalls)
	}
	// The stream must be handed back rewound: ServeContent sizes it from a seek
	// to the end and then re-seeks itself, so a primed offset must not leak.
	if got := stream.seekCalls[len(stream.seekCalls)-1]; got != 0 {
		t.Fatalf("stream left at offset %d, want it rewound to 0", got)
	}
}

func TestPrimeRangeStartSkipsRangesServeContentAnswersAlone(t *testing.T) {
	for name, rangeHeader := range map[string]string{
		"past EOF":    "bytes=8192-",
		"suffix":      "bytes=-500",
		"multi range": "bytes=0-99,200-299",
	} {
		t.Run(name, func(t *testing.T) {
			stream := &primeTestStream{size: 4096, readErr: missingArticleErr()}
			if err := primeRangeStart(stream, rangeHeader, stream.size); err != nil {
				t.Fatalf("primeRangeStart returned error: %v", err)
			}
			if stream.readCalls != 0 {
				t.Fatalf("read %d times, want none", stream.readCalls)
			}
		})
	}
}

func TestPrimeRangeStartTreatsEOFAsServable(t *testing.T) {
	stream := &primeTestStream{size: 4096, readErr: io.EOF}

	if err := primeRangeStart(stream, "", stream.size); err != nil {
		t.Fatalf("primeRangeStart returned error: %v", err)
	}
}

func TestIsFatalStreamErrSeparatesVerdictsFromBlips(t *testing.T) {
	fatal := map[string]error{
		"missing article": missingArticleErr(),
		"corrupt data":    errors.New("rapidyenc: data corruption"),
		"past zero-fill":  fmt.Errorf("too many failed segments: %w", unpack.ErrTooManyZeroFills),
	}
	for name, err := range fatal {
		if !isFatalStreamErr(err) {
			t.Fatalf("%s: isFatalStreamErr = false, want true", name)
		}
	}

	inconclusive := map[string]error{
		"none":      nil,
		"canceled":  context.Canceled,
		"deadline":  context.DeadlineExceeded,
		"transient": errors.New("connection reset by peer"),
	}
	for name, err := range inconclusive {
		if isFatalStreamErr(err) {
			t.Fatalf("%s: isFatalStreamErr = true, want false", name)
		}
	}
}
