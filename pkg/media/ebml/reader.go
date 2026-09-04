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
// into Void elements. It never changes how many bytes a read returns, and it
// only ever touches bytes inside — or in the few header bytes at the edge of —
// a hole the loader already made up.
type HoleFillReader struct {
	inner  io.ReadSeekCloser
	source HoleSource
	size   int64
	cache  *PatchCache
	name   string

	container sync.Once
	matroska  atomic.Bool

	mu  sync.Mutex
	pos int64
}

var _ io.ReadSeekCloser = (*HoleFillReader)(nil)

// NewHoleFillReader wraps stream so reads crossing a hole come back as valid
// EBML. The wrapper costs nothing until a hole shows up: an intact release
// never reaches the analysis, and never reads the container to decide whether
// it could.
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

func (h *HoleFillReader) Read(p []byte) (int, error) {
	n, err := h.inner.Read(p)

	h.mu.Lock()
	window := Range{Start: h.pos, End: h.pos + int64(n)}
	h.pos = window.End
	h.mu.Unlock()

	if n > 0 {
		h.repair(p[:n], window)
	}
	return n, err
}

func (h *HoleFillReader) Seek(offset int64, whence int) (int64, error) {
	pos, err := h.inner.Seek(offset, whence)
	if err == nil {
		h.mu.Lock()
		h.pos = pos
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
		// own header the hole ate, which is up to one header before the hole.
		// A read that stops in there owes those bytes as much as the read that
		// crosses the hole does — otherwise one response carries half a Void
		// header and the next carries the rest.
		reach := Range{Start: hole.Start - maxHeaderLen, End: hole.End}
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
