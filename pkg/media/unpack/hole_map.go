package unpack

import (
	"io"

	"streamnzb/pkg/media/ebml"
	"streamnzb/pkg/media/loader"
)

// zeroFilledRanger is the optional half of an UnpackableFile: a loader file can
// say which of its bytes it made up after every provider refused the article.
type zeroFilledRanger interface {
	ZeroFilledRanges() []ebml.Range
}

// ZeroFilledRanges translates each volume's holes into offsets of the stream
// being played. A hole is a range of one volume's bytes; the same bytes are a
// slice of the movie, and only this layer knows which slice — which is why the
// container repair asks the stream rather than the loader.
//
// A stream with encrypted parts reports none: its plaintext is produced by
// decrypting, so the bytes a hole contributes are not the zeros the loader
// wrote and there is no structure to reason about.
func (s *VirtualStream) ZeroFilledRanges() []ebml.Range {
	var out []ebml.Range
	for i := range s.parts {
		part := &s.parts[i]
		if len(part.AesKey) > 0 {
			return nil
		}
		ranger, ok := part.VolFile.(zeroFilledRanger)
		if !ok {
			continue
		}
		window := ebml.Range{Start: part.VolOffset, End: part.VolOffset + (part.VirtualEnd - part.VirtualStart)}
		for _, hole := range ranger.ZeroFilledRanges() {
			if !hole.Overlaps(window) {
				continue
			}
			start, end := hole.Start, hole.End
			if start < window.Start {
				start = window.Start
			}
			if end > window.End {
				end = window.End
			}
			out = append(out, ebml.Range{
				Start: part.VirtualStart + (start - part.VolOffset),
				End:   part.VirtualStart + (end - part.VolOffset),
			})
		}
	}
	// A hole that straddles a volume boundary arrives as two ranges and is one
	// range of the stream; merging is what makes the repair see it that way.
	return ebml.MergeRanges(out)
}

// ReadAt reads through the volume map without touching the position the stream
// is being served from, so the container repair can look at the structure
// around a hole while playback continues.
//
// It reads the volumes directly rather than through a second stream: a stream
// would open a playback reader and drag its read-ahead window along with it,
// and this only ever wants the few kilobytes an element header sits in. The
// reads are marked speculative, so an article missing from a volume the player
// never reached does not spend the zero-fill budget.
func (s *VirtualStream) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, io.EOF
	}
	ctx := loader.WithSpeculativeRead(s.ctx, true)
	total := 0
	for total < len(p) {
		at := off + int64(total)
		if at >= s.totalSize {
			return total, io.EOF
		}
		part, _ := s.findPart(at)
		if part == nil {
			return total, io.EOF
		}
		want := int64(len(p) - total)
		if remaining := part.VirtualEnd - at; want > remaining {
			want = remaining
		}
		n, err := readAtCtx(ctx, part.VolFile, p[total:total+int(want)], part.VolOffset+(at-part.VirtualStart))
		total += n
		if err != nil && err != io.EOF {
			return total, err
		}
		if n == 0 {
			return total, io.EOF
		}
	}
	return total, nil
}
