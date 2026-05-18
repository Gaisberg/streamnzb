package loader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"streamnzb/pkg/core/logger"
)

// DefaultReadAhead is how many segments ahead of the current read position
// the reader will pre-download using parallel connections. This warms the
// pool-level segment cache so subsequent Read calls don't block on I/O.
const DefaultReadAhead = 8

type SegmentReader struct {
	file    *File
	ctx     context.Context
	cancel  context.CancelFunc
	traceID uint64

	mu               sync.Mutex
	segIdx           int
	segOff           int64
	offset           int64
	closed           bool
	readAheadSize    int
	readAheadCtx     context.Context
	readAheadCancel  context.CancelFunc
	lastReadAheadSeg int // last segment index we triggered read-ahead from (-1 = none)
}

var liveSegmentReaders atomic.Int64
var nextSegmentReaderID atomic.Uint64
var liveSegmentReaderRegistry sync.Map

func LiveSegmentReaders() int64 {
	return liveSegmentReaders.Load()
}

func LiveSegmentReaderDetails() []string {
	details := make([]string, 0)
	liveSegmentReaderRegistry.Range(func(_, value any) bool {
		r, ok := value.(*SegmentReader)
		if !ok || r == nil {
			return true
		}
		details = append(details, r.traceDetail())
		return true
	})
	sort.Strings(details)
	return details
}

func NewSegmentReader(parent context.Context, f *File, startOffset int64) *SegmentReader {
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	raCtx, raCancel := context.WithCancel(ctx)
	sr := &SegmentReader{
		file:             f,
		ctx:              ctx,
		cancel:           cancel,
		traceID:          nextSegmentReaderID.Add(1),
		offset:           startOffset,
		readAheadSize:    DefaultReadAhead,
		readAheadCtx:     raCtx,
		readAheadCancel:  raCancel,
		lastReadAheadSeg: -1,
	}

	idx := f.FindSegmentIndex(startOffset)
	if idx == -1 {
		if startOffset >= f.Size() {
			sr.segIdx = len(f.segments)
		} else {
			sr.segIdx = 0
		}
	} else {
		sr.segIdx = idx
		sr.segOff = startOffset - f.segments[idx].StartOffset
	}

	liveSegmentReaders.Add(1)
	liveSegmentReaderRegistry.Store(sr.traceID, sr)

	return sr
}

func (r *SegmentReader) traceDetail() string {
	r.mu.Lock()
	id := r.traceID
	segIdx := r.segIdx
	segOff := r.segOff
	offset := r.offset
	closed := r.closed
	r.mu.Unlock()

	sessionID := "unknown"
	fileName := ""
	if r.file != nil {
		fileName = r.file.Name()
		if ownerID := r.file.OwnerSessionID(); ownerID != "" {
			sessionID = ownerID
		}
	}

	return fmt.Sprintf("id=%d session=%s file=%q offset=%d seg=%d seg_off=%d closed=%t", id, sessionID, fileName, offset, segIdx, segOff, closed)
}

func (r *SegmentReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if r.segIdx >= len(r.file.segments) {
		r.mu.Unlock()
		return 0, io.EOF
	}
	segIdx := r.segIdx
	segOff := r.segOff
	r.mu.Unlock()

	data, err := r.file.DownloadSegment(r.ctx, segIdx)
	if err != nil {
		return 0, err
	}

	if segOff >= int64(len(data)) {
		r.mu.Lock()
		r.segIdx++
		r.segOff = 0
		nextSegIdx := r.segIdx
		r.mu.Unlock()
		if nextSegIdx >= len(r.file.segments) {
			return 0, io.EOF
		}
		return r.Read(p)
	}

	n := copy(p, data[segOff:])

	r.mu.Lock()
	r.segOff += int64(n)
	r.offset += int64(n)
	if r.segOff >= int64(len(data)) {
		r.segIdx++
		r.segOff = 0
	}
	currentSeg := r.segIdx
	r.mu.Unlock()

	r.triggerReadAhead(currentSeg)

	return n, nil
}

// triggerReadAhead fires off background downloads for the next readAheadSize
// segments starting from fromSeg. It is idempotent: calling it repeatedly with
// the same fromSeg is a no-op.
func (r *SegmentReader) triggerReadAhead(fromSeg int) {
	if r.readAheadSize <= 0 {
		return
	}

	r.mu.Lock()
	if r.closed || fromSeg == r.lastReadAheadSeg {
		r.mu.Unlock()
		return
	}
	r.lastReadAheadSeg = fromSeg
	raCtx := r.readAheadCtx
	r.mu.Unlock()

	totalSegs := len(r.file.segments)
	end := fromSeg + r.readAheadSize
	if end > totalSegs {
		end = totalSegs
	}

	for i := fromSeg; i < end; i++ {
		r.file.ReadAheadSegment(raCtx, i)
	}
}

func (r *SegmentReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = r.offset + offset
	case io.SeekEnd:
		target = r.file.Size() + offset
	default:
		r.mu.Unlock()
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > r.file.Size() {
		r.mu.Unlock()
		return 0, errors.New("seek out of bounds")
	}

	if target == r.offset {
		r.mu.Unlock()
		return target, nil
	}

	r.offset = target
	if target >= r.file.Size() {
		r.segIdx = len(r.file.segments)
		r.segOff = 0
	} else {
		idx := r.file.FindSegmentIndex(target)
		if idx == -1 {
			r.segIdx = len(r.file.segments)
			r.segOff = 0
		} else {
			r.segIdx = idx
			r.segOff = target - r.file.segments[idx].StartOffset
		}
	}

	// Cancel pending read-ahead goroutines and create a fresh context so
	// new read-ahead starts from the seek target.
	r.readAheadCancel()
	raCtx, raCancel := context.WithCancel(r.ctx)
	r.readAheadCtx = raCtx
	r.readAheadCancel = raCancel
	r.lastReadAheadSeg = -1

	r.mu.Unlock()

	return target, nil
}

func (r *SegmentReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.readAheadCancel()
	r.mu.Unlock()

	logger.Debug("loader SegmentReader Close", "file", r.file.Name())
	r.cancel()
	liveSegmentReaderRegistry.Delete(r.traceID)
	liveSegmentReaders.Add(-1)
	return nil
}

func isContextErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "canceled") || strings.Contains(s, "cancelled")
}
