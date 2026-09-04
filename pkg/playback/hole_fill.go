package playback

import (
	"io"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/ebml"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/session"
)

// withHoleFill wraps a Matroska stream so the zero-filled holes playback
// serves for missing articles read back as Void elements a demuxer skips,
// instead of as invalid element headers a strict one dies on.
//
// It applies to every play that can report its holes: the direct path, and the
// stored-archive path where the volume map translates a volume's holes into
// stream offsets. Compressed, encrypted and decoder-backed streams cannot say
// which bytes were made up, so they are served exactly as before. So is
// anything that is not Matroska.
//
// The wrapper costs nothing until a hole appears: an intact release never
// reads the container through it.
func withHoleFill(sess *session.Session, prepared Prepared) io.ReadSeekCloser {
	if prepared.Stream == nil || prepared.Spec.Size <= 0 {
		return prepared.Stream
	}
	if !seek.IsMatroskaFilename(prepared.Spec.Name) {
		return prepared.Stream
	}
	source := holeSource(prepared.Stream)
	if source == nil {
		return prepared.Stream
	}
	logger.Debug("Serving with Matroska hole fill", "session", sess.ID, "file", prepared.Spec.Name)
	return ebml.NewHoleFillReader(prepared.Stream, source, prepared.Spec.Size, sess.HoleFillPatches(), prepared.Spec.Name)
}

// holeSource digs the hole-reporting stream out from under the wrappers
// Prepare puts on it.
func holeSource(stream io.ReadSeekCloser) ebml.HoleSource {
	for i := 0; stream != nil && i < 8; i++ {
		if source, ok := stream.(ebml.HoleSource); ok {
			return source
		}
		unwrapper, ok := stream.(interface{ Unwrap() io.ReadSeekCloser })
		if !ok {
			return nil
		}
		stream = unwrapper.Unwrap()
	}
	return nil
}
