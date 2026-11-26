package ebml

import (
	"io"
	"sync"
	"sync/atomic"

	"streamnzb/pkg/core/logger"
)

// HoleSource is what a playback stream offers when it can say which of its
// bytes were made up. The loader zero-fills a segment no provider will serve
// and records it; the archive layer translates those file offsets into stream
// offsets. Holes appear as playback discovers them, so this is asked on every
// read rather than once at open.
type HoleSource interface {
	// ZeroFilledRanges returns the stream ranges currently known to be
	// zero-filled, merged and in order. Nil means none, or that this stream
	// cannot map them and must be served untouched.
	ZeroFilledRanges() []Range

	// ReadAt reads structure for the repair without disturbing the position
	// the stream is being served from.
	io.ReaderAt
}

// PatchCache holds the repair for each hole of one playback session, so every
// range request over a hole serves the same bytes and the analysis runs once
// no matter how many readers cross it.
type PatchCache struct {
	mu      sync.Mutex
	entries map[int64]*patchEntry
}

type patchEntry struct {
	hole   Range
	once   sync.Once
	result atomic.Pointer[patchResult]
}

// patchResult is published as one value so a reader that has not run the
// analysis still sees a complete answer.
type patchResult struct {
	edits []Edit
}

func NewPatchCache() *PatchCache {
	return &PatchCache{entries: make(map[int64]*patchEntry)}
}

// entryFor returns the cache slot for a hole, keyed by where it starts, and
// creates it when this is the first sight of that hole. A hole that has since
// grown — the run picked up another missing segment — replaces the entry,
// because the shorter hole's repair was computed against a shorter span of
// zeros.
func (c *PatchCache) entryFor(hole Range) *patchEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int64]*patchEntry)
	}
	if e, ok := c.entries[hole.Start]; ok && e.hole == hole {
		return e
	}
	e := &patchEntry{hole: hole}
	c.entries[hole.Start] = e
	return e
}

// HoleFillReader serves a Matroska stream with its zero-filled holes rewritten
// into Void elements. It never changes the length of the stream, and it only
// ever touches bytes inside — or in the few header bytes at the edge of — a hole
// the loader already made up. Serving those edge bytes the same way on every
// read takes a lookahead of up to maxHeaderLen bytes past what the caller asked
// for, which Read explains.
type HoleFillReader struct {
	inner  io.ReadSeekCloser
	source HoleSource
	size   int64
	cache  *PatchCache
	name   string

	container sync.Once
	matroska  atomic.Bool

	// Read and Seek must not overlap on one HoleFillReader: Read snapshots pos and
	// held, drops mu across its reads of inner, and then overwrites both, so a Seek
	// in that window would be undone. mu only keeps the fields consistent for
	// readers of them (a position query, Seek's own update); it is never held
	// across a read of inner. work is Read's alone. Close may be called from
	// another goroutine to cancel a blocked Read.
	mu     sync.Mutex
	pos    int64  // stream offset of the next byte Read serves
	held   []byte // up to maxHeaderLen bytes already read from inner, starting at pos
	behind int64  // bytes before pos a seek has not yet read through, capped at maxHeaderLen
	work   []byte // Read's scratch, reused so a held tail costs a copy and not an allocation
}

var _ io.ReadSeekCloser = (*HoleFillReader)(nil)

// NewHoleFillReader wraps stream so reads crossing a hole come back as valid
// EBML. An intact release never reaches the analysis, and never reads the
// container to decide whether it could; what the wrapper costs it is one copy
// per read and the lookahead Read describes.
//
// stream must be at offset 0, as every stream handed to it is: the reader
// tracks the position it has served itself, and Seek is how a caller moves it.
//
// The read that first reaches a hole pays for the analysis — it has to, since
// the bytes cannot go out before the repair is known. That is bounded by the
// analysis read budget, the reads are marked speculative so they cannot spend
// the zero-fill budget, and it happens once per hole per session; every later
// read over the same hole is a byte overlay.
func NewHoleFillReader(stream io.ReadSeekCloser, source HoleSource, size int64, cache *PatchCache, name string) *HoleFillReader {
	if cache == nil {
		cache = NewPatchCache()
	}
	return &HoleFillReader{inner: stream, source: source, size: size, cache: cache, name: name}
}

// Read never serves a byte until the inner stream has delivered maxHeaderLen
// more after it. The repair can rewrite up to one element header before a
// hole, and a hole only becomes known once the loader has read into it.
// Serving those bytes first would send the unrepaired ones on a stream's first
// pass and the repaired ones on every later pass. Holding the tail back covers
// that left edge. The repair can also write a Void header that runs past a
// hole's end, so after a seek Read first consumes up to maxHeaderLen bytes
// before the served position. That read-behind discovers a hole ending just
// before the position before those bytes can go out. Together they make every
// read of a byte the same.
//
// The price is a lookahead of at most maxHeaderLen bytes beyond what was asked
// for. The held tail is flushed at the end of the stream and only there: on any
// other error it stays held, and Seek drops it.
func (h *HoleFillReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	h.mu.Lock()
	pos, held, behind := h.pos, h.held, h.behind
	h.mu.Unlock()

	if behind > 0 {
		start := pos - behind
		if _, err := h.inner.Seek(start, io.SeekStart); err != nil {
			return 0, err
		}
		behindBuf := make([]byte, behind)
		_, behindErr := io.ReadFull(h.inner, behindBuf)
		_, restoreErr := h.inner.Seek(pos, io.SeekStart)
		if restoreErr != nil {
			return 0, restoreErr
		}
		if behindErr != nil {
			return 0, behindErr
		}
		behind = 0
	}

	if need := maxHeaderLen + len(p); cap(h.work) < need {
		h.work = make([]byte, need)
	}
	work := h.work[:maxHeaderLen+len(p)]
	n := copy(work, held)

	var err error
	for err == nil && n <= maxHeaderLen {
		var m int
		m, err = h.inner.Read(work[n:])
		n += m
		if m == 0 && err == nil {
			err = io.ErrNoProgress // like bufio: an inner that returns (0, nil) would spin here
		}
	}

	serve := max(n-maxHeaderLen, 0)
	if err == io.EOF {
		// The stream ended after every byte of it was read, so no hole can still
		// change these. Any other error leaves the tail held: the bytes after it
		// are unread, and a client that resumes from the last byte it received
		// must get the same bytes a read that did not fail would have served.
		serve = n
		if serve > len(p) {
			// More to flush than p holds: serve what fits and let the next call
			// ask the inner stream again for the end it already reported.
			serve, err = len(p), nil
		}
	}
	window := Range{Start: pos, End: pos + int64(serve)}
	if serve > 0 {
		h.repair(work[:serve], window)
		copy(p, work[:serve])
	}

	h.mu.Lock()
	h.pos = window.End
	h.held = append(h.held[:0], work[serve:n]...)
	h.behind = behind
	h.mu.Unlock()
	return serve, err
}

func (h *HoleFillReader) Seek(offset int64, whence int) (int64, error) {
	h.mu.Lock()
	if whence == io.SeekCurrent {
		if offset == 0 {
			// A position query: answer from what was served, and leave the inner
			// stream — and the read-ahead behind it — where it is.
			pos := h.pos
			h.mu.Unlock()
			return pos, nil
		}
		// The inner stream is ahead of what was served by the held tail.
		offset, whence = h.pos+offset, io.SeekStart
	}
	h.mu.Unlock()

	pos, err := h.inner.Seek(offset, whence)
	if err == nil {
		h.mu.Lock()
		h.pos, h.held = pos, h.held[:0]
		h.behind = min(pos, maxHeaderLen)
		h.mu.Unlock()
	}
	return pos, err
}

func (h *HoleFillReader) Close() error { return h.inner.Close() }

// repair overlays the cached edits of every hole this read window touches,
// analyzing the ones seen for the first time.
func (h *HoleFillReader) repair(buf []byte, window Range) {
	holes := MergeRanges(h.source.ZeroFilledRanges())
	if len(holes) == 0 || !h.isMatroska() {
		return
	}
	for _, hole := range holes {
		// The repair can write a Void header at the start of the element whose
		// own header the hole ate, which is up to one header before the hole,
		// and one at the end of the element the hole started inside, which can
		// run past the hole's end by as much. A read that stops or starts in
		// there owes those bytes as much as the read that crosses the hole
		// does — otherwise one response carries half a Void header and the
		// next carries the rest.
		reach := Range{Start: hole.Start - maxHeaderLen, End: hole.End + maxHeaderLen}
		if !reach.Overlaps(window) {
			continue
		}
		edits := h.editsFor(h.cache.entryFor(hole), hole, holes)
		if len(edits) == 0 {
			continue
		}
		if applied := ApplyEdits(buf, window.Start, edits); applied > 0 {
			logger.Trace("Matroska hole fill applied",
				"file", h.name, "hole_start", hole.Start, "hole_bytes", hole.Len(),
				"read_start", window.Start, "bytes", applied)
		}
	}
}

// editsFor runs the analysis for a hole once and remembers the answer,
// including the empty one: a hole the repair cannot make sense of must serve
// the same original bytes on every later read, not be reconsidered per range
// request.
func (h *HoleFillReader) editsFor(entry *patchEntry, hole Range, holes []Range) []Edit {
	entry.once.Do(func() {
		edits, err := Analyze(h.source, h.size, hole, holes)
		if err != nil {
			// Warn, not Debug: the player is about to be handed raw zeros
			// where a repair was the point, and an operator chasing a dead
			// seek should find the reason.
			logger.Warn("Matroska hole fill fell back to the original bytes",
				"file", h.name, "hole_start", hole.Start, "hole_bytes", hole.Len(), "err", err)
			entry.result.Store(&patchResult{})
			return
		}
		entry.result.Store(&patchResult{edits: edits})
		logger.Debug("Matroska hole rewritten as void elements",
			"file", h.name, "hole_start", hole.Start, "hole_bytes", hole.Len(), "edits", len(edits))
	})
	if done := entry.result.Load(); done != nil {
		return done.edits
	}
	return nil
}

// isMatroska checks the container once, and only after a hole has appeared, so
// a healthy stream never pays for a read it did not ask for.
func (h *HoleFillReader) isMatroska() bool {
	h.container.Do(func() {
		ok := IsMatroska(h.source)
		h.matroska.Store(ok)
		if !ok {
			logger.Debug("Stream has holes but is not Matroska; serving the original bytes", "file", h.name)
		}
	})
	return h.matroska.Load()
}
