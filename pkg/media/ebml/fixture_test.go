package ebml

import (
	"fmt"
	"io"
	"testing"
)

// Synthetic Matroska fixtures. The repair reasons about element structure, not
// about video, so a file of countable frames with known offsets tests it more
// precisely than a real release would — and a hole can be punched exactly where
// each case needs it.

type blockPos struct {
	cluster      int
	start        int64 // element header start
	payloadStart int64
	end          int64
}

type fixture struct {
	data     []byte
	clusters []Range
	blocks   []blockPos
}

type ebmlWriter struct {
	buf []byte
}

func (w *ebmlWriter) elem(id uint32, payload []byte) {
	w.buf = append(w.buf, idBytes(id)...)
	w.buf = append(w.buf, sizeVint(int64(len(payload)))...)
	w.buf = append(w.buf, payload...)
}

func (w *ebmlWriter) at() int64 { return int64(len(w.buf)) }

// idBytes writes an element ID as the raw bytes the spec gives it.
func idBytes(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// sizeVint encodes a size in the shortest vint that can hold it.
func sizeVint(n int64) []byte {
	for l := 1; l <= 8; l++ {
		if n < int64(1)<<(7*l)-1 {
			return encodeVint(uint64(n), l)
		}
	}
	panic("size too large")
}

func uintBytes(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var out []byte
	for shift := 56; shift >= 0; shift -= 8 {
		b := byte(v >> shift)
		if len(out) == 0 && b == 0 {
			continue
		}
		out = append(out, b)
	}
	return out
}

func simpleBlockPayload(track byte, timecode int16, frameLen int, fill byte) []byte {
	payload := []byte{0x80 | track, byte(uint16(timecode) >> 8), byte(uint16(timecode)), 0x80}
	for i := 0; i < frameLen; i++ {
		payload = append(payload, fill+byte(i))
	}
	return payload
}

type fixtureOpts struct {
	clusters        int
	blocksPerClustr int
	frameLen        int
	withCRC         bool
	unknownCluster  bool // last cluster declares an unknown size
	withCues        bool // a Cues element trails the clusters, as a real mux writes
	blockGroups     bool // wrap blocks in BlockGroup/Block instead of SimpleBlock
}

// buildFixture assembles a Matroska file and reports where every Cluster and
// block landed, in absolute stream offsets.
func buildFixture(t *testing.T, o fixtureOpts) fixture {
	t.Helper()
	if o.clusters == 0 {
		o.clusters = 3
	}
	if o.blocksPerClustr == 0 {
		o.blocksPerClustr = 4
	}
	if o.frameLen == 0 {
		o.frameLen = 400
	}

	var head ebmlWriter
	var docType ebmlWriter
	docType.elem(0x4282, []byte("matroska"))
	head.elem(IDEBMLHeader, docType.buf)

	// The Segment payload is written first so its offsets are known, then
	// shifted by everything that precedes it.
	var seg ebmlWriter
	var info ebmlWriter
	info.elem(0x2AD7B1, uintBytes(1000000))
	seg.elem(IDInfo, info.buf)
	var track ebmlWriter
	track.elem(0xD7, uintBytes(1))
	track.elem(0x83, uintBytes(1))
	var tracks ebmlWriter
	tracks.elem(IDTrackEntry, track.buf)
	seg.elem(IDTracks, tracks.buf)

	var clusters []Range
	var blocks []blockPos
	for ci := 0; ci < o.clusters; ci++ {
		var body ebmlWriter
		if o.withCRC {
			body.elem(IDCRC32, []byte{0, 0, 0, 0})
		}
		body.elem(IDTimestamp, uintBytes(uint64(ci*1000)))
		// Block offsets are recorded relative to the cluster payload here and
		// shifted once the cluster header length is known.
		type rel struct{ start, payloadStart, end int64 }
		var rels []rel
		for bi := 0; bi < o.blocksPerClustr; bi++ {
			payload := simpleBlockPayload(1, int16(bi*40), o.frameLen, byte(0x10+bi))
			start := body.at()
			if o.blockGroups {
				var group ebmlWriter
				group.elem(IDBlock, payload)
				body.elem(IDBlockGroup, group.buf)
				inner := start + int64(len(idBytes(IDBlockGroup))) + int64(len(sizeVint(int64(len(group.buf)))))
				rels = append(rels, rel{
					start:        inner,
					payloadStart: inner + int64(len(idBytes(IDBlock))) + int64(len(sizeVint(int64(len(payload))))),
					end:          body.at(),
				})
				continue
			}
			body.elem(IDSimpleBlock, payload)
			rels = append(rels, rel{
				start:        start,
				payloadStart: start + int64(len(idBytes(IDSimpleBlock))) + int64(len(sizeVint(int64(len(payload))))),
				end:          body.at(),
			})
		}

		clusterStart := seg.at()
		sizeBytes := sizeVint(int64(len(body.buf)))
		if o.unknownCluster && ci == o.clusters-1 {
			sizeBytes = []byte{0xFF}
		}
		seg.buf = append(seg.buf, idBytes(IDCluster)...)
		seg.buf = append(seg.buf, sizeBytes...)
		payloadStart := seg.at()
		seg.buf = append(seg.buf, body.buf...)
		clusters = append(clusters, Range{Start: clusterStart, End: seg.at()})
		for _, r := range rels {
			blocks = append(blocks, blockPos{
				cluster:      ci,
				start:        payloadStart + r.start,
				payloadStart: payloadStart + r.payloadStart,
				end:          payloadStart + r.end,
			})
		}
	}

	if o.withCues {
		var positions ebmlWriter
		positions.elem(0xF7, uintBytes(1))
		positions.elem(0xF1, uintBytes(uint64(clusters[0].Start)))
		var point ebmlWriter
		point.elem(0xB3, uintBytes(0))
		point.elem(IDCueTrackPositions, positions.buf)
		var cues ebmlWriter
		cues.elem(IDCuePoint, point.buf)
		seg.elem(IDCues, cues.buf)
	}

	prefix := int64(len(head.buf)) + int64(len(idBytes(IDSegment))) + int64(len(sizeVint(int64(len(seg.buf)))))
	var out ebmlWriter
	out.buf = append(out.buf, head.buf...)
	out.elem(IDSegment, seg.buf)

	f := fixture{data: out.buf}
	for _, c := range clusters {
		f.clusters = append(f.clusters, Range{Start: c.Start + prefix, End: c.End + prefix})
	}
	for _, b := range blocks {
		f.blocks = append(f.blocks, blockPos{
			cluster:      b.cluster,
			start:        b.start + prefix,
			payloadStart: b.payloadStart + prefix,
			end:          b.end + prefix,
		})
	}

	if !o.unknownCluster {
		if err := validateStrict(f.data); err != nil {
			t.Fatalf("fixture is not valid Matroska to begin with: %v", err)
		}
	}
	return f
}

// blockIn returns the nth block of cluster ci.
func (f fixture) blockIn(ci, n int) blockPos {
	seen := 0
	for _, b := range f.blocks {
		if b.cluster != ci {
			continue
		}
		if seen == n {
			return b
		}
		seen++
	}
	panic("no such block")
}

// punch returns a copy of data with [start, end) zeroed — what the loader
// serves for a segment no provider will hand over.
func punch(data []byte, hole Range) []byte {
	out := append([]byte(nil), data...)
	for i := hole.Start; i < hole.End; i++ {
		out[i] = 0
	}
	return out
}

// validateStrict walks a Matroska file the way a demuxer that never rescans
// does: every element header has to parse, every size has to fit its parent,
// the children of a master have to tile it exactly, and a block payload has to
// open with a track number. It is the assertion behind every repair test — the
// untransformed fixture must fail it and the transformed one must pass.
func validateStrict(data []byte) error {
	return validateLevel(data, 0, int64(len(data)), 0)
}

func validateLevel(data []byte, start, end int64, depth int) error {
	if depth > 8 {
		return fmt.Errorf("%d: nesting too deep", start)
	}
	for off := start; off < end; {
		if end-off < 2 {
			return fmt.Errorf("%d: %d trailing bytes cannot hold an element", off, end-off)
		}
		idLen, id := ReadID(data[off:])
		if idLen <= 0 {
			return fmt.Errorf("%d: %#02x is not an element id", off, data[off])
		}
		sizeLen, size := ReadVint(data[off+int64(idLen):])
		if sizeLen <= 0 {
			return fmt.Errorf("%d: element %#x has no readable size", off, id)
		}
		if VintIsUnknown(sizeLen, size) {
			return fmt.Errorf("%d: element %#x declares an unknown size", off, id)
		}
		payloadStart := off + int64(idLen) + int64(sizeLen)
		payloadEnd := payloadStart + int64(size)
		if payloadEnd > end {
			return fmt.Errorf("%d: element %#x ends at %d, past its parent at %d", off, id, payloadEnd, end)
		}
		switch {
		case IsMaster(id):
			if err := validateLevel(data, payloadStart, payloadEnd, depth+1); err != nil {
				return err
			}
		case id == IDSimpleBlock || id == IDBlock:
			if size < 4 {
				return fmt.Errorf("%d: block of %d bytes is too short", off, size)
			}
			if l, _ := ReadVint(data[payloadStart:]); l <= 0 {
				return fmt.Errorf("%d: block payload does not open with a track number (%#02x)", payloadStart, data[payloadStart])
			}
		}
		off = payloadEnd
	}
	return nil
}

// repaired applies what Analyze returns for every hole, and checks the one
// invariant that matters everywhere: the length never changes.
func repaired(t *testing.T, data []byte, holes ...Range) []byte {
	t.Helper()
	holes = MergeRanges(holes)
	out := append([]byte(nil), data...)
	for _, hole := range holes {
		edits, err := Analyze(readerAt(data), int64(len(data)), hole, holes)
		if err != nil {
			t.Fatalf("Analyze(%d-%d): %v", hole.Start, hole.End, err)
		}
		ApplyEdits(out, 0, edits)
	}
	if len(out) != len(data) {
		t.Fatalf("repair changed the byte count: %d -> %d", len(data), len(out))
	}
	return out
}

type readerAt []byte

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r)) {
		return 0, io.EOF
	}
	n := copy(p, r[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
