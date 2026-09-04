package playback

import (
	"bytes"
	"io"
	"testing"

	"streamnzb/pkg/media/ebml"
	"streamnzb/pkg/session"
)

// holeReportingStream is a playback stream that can say which of its bytes
// were made up — what the direct and stored-archive paths offer.
type holeReportingStream struct {
	*bytes.Reader
}

func (s holeReportingStream) Close() error { return nil }

func (s holeReportingStream) ZeroFilledRanges() []ebml.Range { return nil }

// plainStream cannot: a compressed or decoder-backed archive stream.
type plainStream struct {
	*bytes.Reader
}

func (s plainStream) Close() error { return nil }

func newPrepared(name string, stream io.ReadSeekCloser, size int64) Prepared {
	return Prepared{
		Spec:   session.PlaybackStreamSpec{Key: "k", Name: name, Size: size},
		Stream: stream,
	}
}

func TestWithHoleFillWrapsOnlyWhatItCanRepair(t *testing.T) {
	data := bytes.Repeat([]byte{1}, 512)
	sess := &session.Session{ID: "s"}

	holed := holeReportingStream{Reader: bytes.NewReader(data)}
	if _, ok := withHoleFill(sess, newPrepared("movie.mkv", holed, int64(len(data)))).(*ebml.HoleFillReader); !ok {
		t.Fatal("a Matroska stream that reports its holes should be served through the repair")
	}

	plain := plainStream{Reader: bytes.NewReader(data)}
	if _, ok := withHoleFill(sess, newPrepared("movie.mkv", plain, int64(len(data)))).(*ebml.HoleFillReader); ok {
		t.Fatal("a stream that cannot report its holes must be served as-is")
	}
	if _, ok := withHoleFill(sess, newPrepared("movie.mp4", holed, int64(len(data)))).(*ebml.HoleFillReader); ok {
		t.Fatal("mp4 has no EBML to repair and must be served as-is")
	}
	if got := withHoleFill(sess, newPrepared("movie.mkv", holed, 0)); got != io.ReadSeekCloser(holed) {
		t.Fatal("a stream of unknown size must be served as-is")
	}
}

// The probe path hands the same stream on wrapped in its own closer; the
// capability check has to see through it or every probed play loses the repair.
func TestWithHoleFillSeesThroughTheProbeWrapper(t *testing.T) {
	data := bytes.Repeat([]byte{1}, 512)
	sess := &session.Session{ID: "s"}
	wrapped := &cancelOnCloseStream{
		ReadSeekCloser: holeReportingStream{Reader: bytes.NewReader(data)},
		cancel:         func() {},
	}
	if _, ok := withHoleFill(sess, newPrepared("movie.mkv", wrapped, int64(len(data)))).(*ebml.HoleFillReader); !ok {
		t.Fatal("the repair must see the hole source under the probe wrapper")
	}
}

// Every request of one session shares one repair, so overlapping ranges over a
// hole cannot disagree.
func TestSessionSharesOnePatchCache(t *testing.T) {
	sess := &session.Session{ID: "s"}
	first := sess.HoleFillPatches()
	if first == nil {
		t.Fatal("a session must hand out a patch cache")
	}
	if second := sess.HoleFillPatches(); second != first {
		t.Fatal("a session must hand out the same patch cache to every request")
	}

	// A new stream means new offsets, so the repair starts over with it.
	sess.ResetPlaybackStream()
	if again := sess.HoleFillPatches(); again == first {
		t.Fatal("resetting the playback stream must drop the repair cache")
	}
}
