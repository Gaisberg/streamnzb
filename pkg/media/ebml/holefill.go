package ebml

import (
	"errors"
	"fmt"
	"io"
	"sort"
)

// Range is a half-open byte range [Start, End) of a playback stream.
type Range struct {
	Start int64
	End   int64
}

func (r Range) Len() int64 { return r.End - r.Start }

// Overlaps reports whether the two ranges share at least one byte.
func (r Range) Overlaps(o Range) bool { return r.Start < o.End && o.Start < r.End }

func (r Range) contains(off int64) bool { return off >= r.Start && off < r.End }

// Edit is a byte-for-byte replacement at an absolute stream offset. Applying
// an edit never changes the length of anything.
type Edit struct {
	Offset int64
	Bytes  []byte
}

// MergeRanges returns the ranges sorted and coalesced, dropping empty ones. A
// run of consecutive missing segments is one hole, not several: the repair
// reasons about the whole run at once, because that is what the container sees.
func MergeRanges(in []Range) []Range {
	if len(in) == 0 {
		return nil
	}
	out := make([]Range, 0, len(in))
	for _, r := range in {
		if r.Len() > 0 {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:1]
	for _, r := range out[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

const (
	// anchorWindow is how far back Analyze looks for the Cluster start it
	// parses forward from. Clusters run a few megabytes at most, so this
	// reaches one comfortably — and no further, because these reads go through
	// the loader: bytes the reader has already streamed cost nothing, and
	// bytes it has not can cost an article fetch.
	anchorWindow = 8 << 20

	// clusterLookahead is how far past a hole the search for the next intact
	// Cluster reaches before giving up and serving the original bytes. Bounded
	// for the same reason, and against the same scale: the resume point is the
	// next Cluster after the damage, not an arbitrary distance away.
	clusterLookahead = 8 << 20

	// analysisChunk is the read size the cursor works in. Large enough that
	// walking a Cluster costs a couple of reads, small enough that a repair
	// never holds much.
	analysisChunk = 512 << 10

	// maxAnalysisReads bounds one repair's cost in source reads, so a
	// pathological file cannot turn a hole into a download. It covers both
	// windows above with room for the walk between them.
	maxAnalysisReads = 48
)

var (
	errNoAnchor       = errors.New("no intact cluster to parse from")
	errUnknownSize    = errors.New("element declares an unknown size")
	errNoNextCluster  = errors.New("no intact cluster after the hole")
	errNoRoomForVoid  = errors.New("not enough room for a void header")
	errBudgetExceeded = errors.New("analysis read budget exceeded")
	errNotMatroska    = errors.New("stream does not start with an EBML header")
)

// IsMatroska reports whether the stream begins with an EBML header. MP4 and
// everything else is passed through untouched.
func IsMatroska(src io.ReaderAt) bool {
	var head [4]byte
	if _, err := src.ReadAt(head[:], 0); err != nil && err != io.EOF {
		return false
	}
	_, id := ReadID(head[:])
	return id == IDEBMLHeader
}

// element is one parsed element header. End is the offset one past the
// payload, or -1 when the size is the unknown-size marker.
type element struct {
	ID           uint32
	Start        int64
	PayloadStart int64
	End          int64
}

// cursor reads the source in chunks, with a budget, so a repair's cost is
// bounded no matter what the bytes look like.
type cursor struct {
	src   io.ReaderAt
	size  int64
	buf   []byte
	off   int64
	reads int
}

// bytes returns a view of [off, off+n) of the source, reading a chunk when the
// current buffer does not already cover it. The returned slice may be shorter
// than n at end of file.
func (c *cursor) bytes(off int64, n int) ([]byte, error) {
	if off < 0 || n <= 0 || off >= c.size {
		return nil, io.EOF
	}
	if off >= c.off && off+int64(n) <= c.off+int64(len(c.buf)) {
		return c.buf[off-c.off : off-c.off+int64(n)], nil
	}
	want := int64(n)
	if want < analysisChunk {
		want = analysisChunk
	}
	if off+want > c.size {
		want = c.size - off
	}
	if c.reads >= maxAnalysisReads {
		return nil, errBudgetExceeded
	}
	c.reads++
	buf := make([]byte, want)
	read, err := c.src.ReadAt(buf, off)
	if read <= 0 {
		if err == nil {
			err = io.EOF
		}
		return nil, err
	}
	c.buf, c.off = buf[:read], off
	if read < n {
		return c.buf, io.EOF
	}
	return c.buf[:n], nil
}

// element parses the element header at off, refusing anything that cannot be a
// real one: a header that runs past the file, a size that does so, or an ID
// longer than four bytes.
func (c *cursor) element(off int64) (element, error) {
	data, err := c.bytes(off, maxHeaderLen)
	if err != nil && len(data) == 0 {
		return element{}, err
	}
	idLen, id := ReadID(data)
	if idLen <= 0 {
		return element{}, fmt.Errorf("bad element id at %d", off)
	}
	sizeLen, size := ReadVint(data[idLen:])
	if sizeLen <= 0 {
		return element{}, fmt.Errorf("bad element size at %d", off)
	}
	el := element{ID: id, Start: off, PayloadStart: off + int64(idLen) + int64(sizeLen)}
	if VintIsUnknown(sizeLen, size) {
		el.End = -1
		return el, nil
	}
	if size > uint64(c.size) {
		return element{}, fmt.Errorf("element at %d declares %d bytes past a %d byte stream", off, size, c.size)
	}
	el.End = el.PayloadStart + int64(size)
	if el.End > c.size {
		return element{}, fmt.Errorf("element at %d ends past the stream", off)
	}
	return el, nil
}

// walkResult is where the walk stopped: the chain of master elements
// enclosing the hole (outermost first), the element the hole starts inside
// when that element's header survived intact, and the last boundary the file
// still declares at or before the hole.
type walkResult struct {
	masters  []element
	leaf     *element
	boundary int64
}

// walk descends from a sibling boundary towards the hole.
//
// An element whose own header the hole ate declares a length read out of
// zeros, so it is never returned as the leaf: the walk reports the boundary
// where that element starts instead, and the repair voids from there. The same
// goes for a header the hole left unparseable — at the edge of a hole that is
// the hole's doing, further out it is corruption the repair must not touch.
func (c *cursor) walk(start int64, masters []element, hole Range) (walkResult, error) {
	cur := start
	for {
		if cur == hole.Start {
			return walkResult{masters: masters, boundary: cur}, nil
		}
		if cur > hole.Start {
			return walkResult{}, fmt.Errorf("walked past %d to %d", hole.Start, cur)
		}
		el, err := c.element(cur)
		if err != nil {
			if cur+maxHeaderLen > hole.Start {
				return walkResult{masters: masters, boundary: cur}, nil
			}
			return walkResult{}, err
		}
		if el.End < 0 {
			return walkResult{}, errUnknownSize
		}
		if hole.Start < el.PayloadStart {
			return walkResult{masters: masters, boundary: el.Start}, nil
		}
		if len(masters) > 0 {
			if parent := masters[len(masters)-1]; parent.End >= 0 && el.End > parent.End {
				return walkResult{}, fmt.Errorf("element at %d runs past its parent", cur)
			}
		}
		if hole.Start < el.End {
			if IsMaster(el.ID) {
				masters = append(masters, el)
				cur = el.PayloadStart
				continue
			}
			return walkResult{masters: masters, leaf: &el, boundary: el.End}, nil
		}
		cur = el.End
	}
}

// looksLikeCluster reports whether off holds an intact Cluster header. A bare
// ID match is not enough — the four ID bytes turn up inside frame data — so the
// size has to be declared, has to fit the stream, and the first child has to be
// one of the handful a Cluster opens with.
func (c *cursor) looksLikeCluster(off int64) bool {
	el, err := c.element(off)
	if err != nil || el.ID != IDCluster || el.End < 0 || el.End <= el.PayloadStart {
		return false
	}
	child, err := c.element(el.PayloadStart)
	if err != nil || child.End < 0 || child.End > el.End {
		return false
	}
	switch child.ID {
	case IDTimestamp, IDCRC32, IDPosition, IDPrevSize:
		return true
	}
	return false
}

// clusterIDBytes is the Cluster ID as it appears in the file, for scanning.
var clusterIDBytes = [4]byte{0x1F, 0x43, 0xB6, 0x75}

// anchor finds the boundary Analyze parses forward from: the last intact
// Cluster start at or before the hole, searched no further back than floor.
// When the hole is in the header region — before any Cluster — the walk starts
// at the Segment's first child instead, which is cheap because the header is
// at the front of the file.
func (c *cursor) anchor(hole Range, floor int64) (int64, []element, error) {
	if floor < 0 {
		floor = 0
	}
	for chunkEnd := hole.Start; chunkEnd > floor; {
		chunkStart := chunkEnd - analysisChunk
		if chunkStart < floor {
			chunkStart = floor
		}
		data, err := c.bytes(chunkStart, int(chunkEnd-chunkStart))
		if err != nil && len(data) == 0 {
			return 0, nil, err
		}
		for i := len(data) - 4; i >= 0; i-- {
			if data[i] != clusterIDBytes[0] || data[i+1] != clusterIDBytes[1] ||
				data[i+2] != clusterIDBytes[2] || data[i+3] != clusterIDBytes[3] {
				continue
			}
			at := chunkStart + int64(i)
			if c.looksLikeCluster(at) {
				// The Cluster is a child of Segment; its end is all the walk
				// needs, so Segment stands in with an unknown one.
				return at, []element{{ID: IDSegment, Start: -1, PayloadStart: -1, End: -1}}, nil
			}
		}
		if chunkStart <= floor {
			break
		}
		// Overlap by three bytes so an ID split across the chunk edge is seen.
		chunkEnd = chunkStart + 3
	}

	if floor > 0 {
		return 0, nil, errNoAnchor
	}
	header, err := c.element(0)
	if err != nil || header.ID != IDEBMLHeader || header.End < 0 {
		return 0, nil, errNotMatroska
	}
	segment, err := c.element(header.End)
	if err != nil || segment.ID != IDSegment {
		return 0, nil, errNotMatroska
	}
	return segment.PayloadStart, []element{segment}, nil
}

// nextCluster finds the first intact Cluster start at or after from, skipping
// any candidate that falls inside a hole — a Cluster header read out of zeros
// is not a place to resume.
func (c *cursor) nextCluster(from int64, holes []Range) (int64, error) {
	limit := from + clusterLookahead
	if limit > c.size {
		limit = c.size
	}
	for chunkStart := from; chunkStart < limit; {
		chunkEnd := chunkStart + analysisChunk
		if chunkEnd > limit {
			chunkEnd = limit
		}
		data, err := c.bytes(chunkStart, int(chunkEnd-chunkStart))
		if err != nil && len(data) == 0 {
			return -1, err
		}
		for i := 0; i+4 <= len(data); i++ {
			if data[i] != clusterIDBytes[0] || data[i+1] != clusterIDBytes[1] ||
				data[i+2] != clusterIDBytes[2] || data[i+3] != clusterIDBytes[3] {
				continue
			}
			at := chunkStart + int64(i)
			if !inAnyHole(holes, at) && c.looksLikeCluster(at) {
				return at, nil
			}
		}
		// Overlap by three bytes so an ID split across the chunk edge is seen.
		chunkStart = chunkEnd - 3
		if chunkEnd >= limit {
			break
		}
	}
	return -1, errNoNextCluster
}

func inAnyHole(holes []Range, off int64) bool {
	for _, h := range holes {
		if h.contains(off) {
			return true
		}
	}
	return false
}

// Analyze returns the edits that turn one zero-filled hole into structure a
// demuxer skips. holes is the whole merged hole set, so the repair never
// anchors on, or resumes at, bytes another hole made up.
//
// Every failure returns an error and no edits, and the caller then serves the
// original bytes: the repair is only ever allowed to help.
//
// Every edit lands inside the hole, or within one element header of its start.
// That bound is what lets a serve apply them without having read the whole
// file: a read window near the hole owes at most those few bytes. It also
// rules out repairing a Cluster's CRC-32, which sits at the front of the
// Cluster and is long gone by the time the hole is reached — a Cluster that
// carries one fails its checksum here and ffmpeg drops it, exactly as it would
// have without the repair.
func Analyze(src io.ReaderAt, size int64, hole Range, holes []Range) ([]Edit, error) {
	if hole.Len() <= 0 || hole.Start < 0 || hole.End > size {
		return nil, fmt.Errorf("hole %d-%d outside a %d byte stream", hole.Start, hole.End, size)
	}
	c := &cursor{src: src, size: size}

	floor := hole.Start - anchorWindow
	if prev := previousHoleEnd(holes, hole.Start); prev > floor {
		floor = prev
	}
	anchorOff, masters, err := c.anchor(hole, floor)
	if err != nil {
		return nil, err
	}
	res, err := c.walk(anchorOff, masters, hole)
	if err != nil {
		return nil, err
	}
	masters, leaf := res.masters, res.leaf

	var edits []Edit
	// A hole that swallows a Block's payload start takes the track number with
	// it, and 0x00 is not a vint: a strict demuxer throws there even though the
	// enclosing element's size told it how far to skip. One byte puts a valid
	// track number back.
	if leaf != nil && (leaf.ID == IDSimpleBlock || leaf.ID == IDBlock) && hole.contains(leaf.PayloadStart) {
		edits = append(edits, Edit{Offset: leaf.PayloadStart, Bytes: []byte{0x81}})
	}

	// The hole stays inside one leaf's payload: the element's own size already
	// tells the demuxer how far to skip, so the zeros can stay.
	if leaf != nil && leaf.End >= hole.End {
		return edits, nil
	}

	// Otherwise the hole outlives the element it started in, and the bytes at
	// that element's end — where the next header should be — are zeros. Void
	// from the first boundary inside the hole to the end of each enclosing
	// master in turn, popping outward until one of them ends past the hole.
	boundary := res.boundary

	for i := len(masters) - 1; i >= 0; i-- {
		m := masters[i]
		if m.ID == IDSegment {
			break
		}
		if m.End < 0 {
			return nil, errUnknownSize
		}
		edit, err := voidEdit(boundary, m.End)
		if err != nil {
			return nil, err
		}
		if edit != nil {
			edits = append(edits, *edit)
		}
		if m.End >= hole.End {
			return edits, nil
		}
		boundary = m.End
	}

	// The hole outlived its Cluster: from the Cluster's end — a Segment child
	// boundary, and the last one the file still declares — one Void spans to
	// the next Cluster that survived intact.
	next, err := c.nextCluster(max(boundary, hole.End), holes)
	if err != nil {
		return nil, err
	}
	edit, err := voidEdit(boundary, next)
	if err != nil {
		return nil, err
	}
	if edit != nil {
		edits = append(edits, *edit)
	}
	return edits, nil
}

// voidEdit builds the Void header that makes [from, to) one skipped element.
// An empty span needs nothing; a single byte cannot hold a header at all, and
// the repair gives up rather than emit something a demuxer would read as an
// element.
func voidEdit(from, to int64) (*Edit, error) {
	span := to - from
	if span == 0 {
		return nil, nil
	}
	if span < 0 {
		return nil, fmt.Errorf("void span %d-%d runs backwards", from, to)
	}
	header := VoidHeader(span)
	if header == nil {
		return nil, errNoRoomForVoid
	}
	return &Edit{Offset: from, Bytes: header}, nil
}

// previousHoleEnd returns where the closest hole before start ends, so neither
// the anchor search nor the walk crosses bytes another hole made up.
func previousHoleEnd(holes []Range, start int64) int64 {
	var end int64
	for _, h := range holes {
		if h.End <= start && h.End > end {
			end = h.End
		}
	}
	return end
}

// ApplyEdits overlays edits onto buf, which holds the stream bytes starting at
// bufOffset. Edits outside the buffer, and the parts of one that reach past
// either end, are skipped — the rest of that edit lands with the read that
// covers it.
func ApplyEdits(buf []byte, bufOffset int64, edits []Edit) int {
	applied := 0
	for _, e := range edits {
		for i, b := range e.Bytes {
			at := e.Offset + int64(i) - bufOffset
			if at < 0 || at >= int64(len(buf)) {
				continue
			}
			buf[at] = b
			applied++
		}
	}
	return applied
}
