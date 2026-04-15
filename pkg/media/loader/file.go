package loader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
)

type SegmentFetcher interface {
	FetchSegment(ctx context.Context, segment *nzb.Segment, groups []string) (pool.SegmentData, error)
}

// SegmentFirstFetcher is optional: when implemented, the loader uses it for segment index 0
// to try all providers in parallel and reduce latency when the article is missing everywhere.
type SegmentFirstFetcher interface {
	FetchSegmentFirst(ctx context.Context, segment *nzb.Segment, groups []string) (pool.SegmentData, error)
}

// SegmentStatter is optional: when implemented by the fetcher, CheckFirstSegmentExists uses
// STAT to verify the first segment exists before opening a stream
type SegmentStatter interface {
	StatSegment(ctx context.Context, messageID string, groups []string) (exists bool, err error)
}

func shouldPersistDownloadedSegment(ctx context.Context) bool {
	return ctx == nil || ctx.Err() == nil
}

func decodeAndCloseBody(body io.ReadCloser, decodeFn func(io.Reader) (*decode.Frame, error)) (*decode.Frame, error) {
	defer body.Close()
	return decodeFn(body)
}

const MaxZeroFills = 10

// isArticleNotFound reports whether err indicates the article is missing (430 No Such Article).
// Used to fail fast on the first segment instead of zero-filling through many segments.
func isArticleNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "430") || strings.Contains(s, "no such article")
}

func (f *File) IsFailed() bool {
	f.zeroFillMu.Lock()
	defer f.zeroFillMu.Unlock()
	return f.zeroFillCount >= MaxZeroFills
}

type Segment struct {
	nzb.Segment
	StartOffset int64
	EndOffset   int64
}

type File struct {
	nzbFile   *nzb.File
	pools     []*nntp.ClientPool
	fetcher   SegmentFetcher
	estimator *SegmentSizeEstimator
	segments  []*Segment
	totalSize int64
	detected  bool
	ctx       context.Context
	ownerID   string
	mu        sync.Mutex

	downloadMu        sync.Mutex
	inflightDownloads map[int]*inflightSegmentDownload

	zeroFillMu    sync.Mutex
	zeroFillCount int
}

type inflightSegmentDownload struct {
	done          chan struct{}
	countFailures bool
	data          []byte
	err           error
	ctx           context.Context
	cancel        context.CancelFunc
	waiters       int
}

type zeroFillEligibleError struct {
	cause error
}

func (e *zeroFillEligibleError) Error() string { return e.cause.Error() }

func (e *zeroFillEligibleError) Unwrap() error { return e.cause }

func NewFile(ctx context.Context, f *nzb.File, pools []*nntp.ClientPool, estimator *SegmentSizeEstimator, fetcher SegmentFetcher) *File {
	segments := make([]*Segment, len(f.Segments))
	var offset int64
	for i, s := range f.Segments {
		segments[i] = &Segment{
			Segment:     s,
			StartOffset: offset,
			EndOffset:   offset + s.Bytes,
		}
		offset += s.Bytes
	}
	return &File{
		nzbFile:           f,
		pools:             pools,
		fetcher:           fetcher,
		estimator:         estimator,
		segments:          segments,
		totalSize:         offset,
		ctx:               ctx,
		inflightDownloads: make(map[int]*inflightSegmentDownload),
	}
}

func (f *File) Name() string { return f.nzbFile.Subject }

func (f *File) SetOwnerSessionID(sessionID string) {
	f.mu.Lock()
	f.ownerID = sessionID
	f.mu.Unlock()
}

func (f *File) OwnerSessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownerID
}

func (f *File) Size() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totalSize
}

func (f *File) Segments() []*Segment { return f.segments }

func (f *File) SegmentCount() int { return len(f.segments) }

// CheckFirstSegmentExists returns whether the first segment exists on the server (STAT only).
// If the fetcher implements SegmentStatter, it runs STAT on the first segment; otherwise returns true.
// Used before opening a stream to fail fast when the release is unavailable (430).
func (f *File) CheckFirstSegmentExists(ctx context.Context) (bool, error) {
	if len(f.segments) == 0 {
		return false, nil
	}
	statter, ok := f.fetcher.(SegmentStatter)
	if !ok {
		return true, nil
	}
	msgID := strings.TrimSpace(f.segments[0].ID)
	if msgID == "" {
		return false, nil
	}
	return statter.StatSegment(ctx, msgID, f.nzbFile.Groups)
}

func (f *File) SegmentMapDetected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.detected
}

func (f *File) EnsureSegmentMap() error {
	return f.EnsureSegmentMapCtx(f.ctx)
}

func (f *File) EnsureSegmentMapCtx(ctx context.Context) error {
	f.mu.Lock()
	if f.detected {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	return f.detectSegmentSize(ctx)
}

func (f *File) detectSegmentSize(ctx context.Context) error {
	f.mu.Lock()
	if f.detected {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	if ctx == nil {
		ctx = f.ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(f.segments) == 0 {
		f.mu.Lock()
		f.totalSize = 0
		f.detected = true
		f.mu.Unlock()
		return nil
	}

	firstEncoded := f.segments[0].Bytes
	segSize := int64(0)
	usedEstimator := false
	if f.estimator != nil {
		if decoded, ok := f.estimator.Get(firstEncoded); ok {
			segSize = decoded
			usedEstimator = true
		}
	}

	if !usedEstimator {
		data, err := f.DownloadSegment(ctx, 0)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return errors.New("empty first segment")
		}
		segSize = int64(len(data))
	}

	lastSegSize := segSize
	lastIdx := len(f.segments) - 1
	if lastIdx > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := f.DownloadSegment(ctx, lastIdx)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return errors.New("empty last segment")
		}
		lastSegSize = int64(len(data))
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detected {
		return nil
	}
	if usedEstimator {
		logger.Trace("Using estimated segment size", "name", f.Name(), "size", segSize)
	} else {
		logger.Debug("Detected segment size", "name", f.Name(), "size", segSize, "nzb_size", f.segments[0].Bytes)
		if f.estimator != nil {
			f.estimator.Set(f.segments[0].Bytes, segSize)
		}
	}
	if lastIdx > 0 {
		logger.Debug("Detected last segment size", "name", f.Name(), "size", lastSegSize, "nzb_size", f.segments[lastIdx].Bytes)
	}
	f.applySegmentSize(segSize, lastSegSize)
	return nil
}

func (f *File) applySegmentSize(segSize, lastSegSize int64) {
	if lastSegSize <= 0 {
		lastSegSize = segSize
	}
	var offset int64
	for i := range f.segments {
		f.segments[i].StartOffset = offset
		currentSize := segSize
		if i == len(f.segments)-1 {
			currentSize = lastSegSize
		}
		f.segments[i].EndOffset = offset + currentSize
		offset += currentSize
	}
	f.totalSize = offset
	f.detected = true
	logger.Trace("Recalculated total decoded size", "size", f.totalSize)
}

func (f *File) FindSegmentIndex(offset int64) int {
	idx := sort.Search(len(f.segments), func(i int) bool {
		return f.segments[i].EndOffset > offset
	})
	if idx < len(f.segments) && offset >= f.segments[idx].StartOffset {
		return idx
	}
	return -1
}

// DownloadSegment fetches segment index on demand.
// On all-provider failure, it zero-fills the segment and counts it toward IsFailed().
func (f *File) DownloadSegment(ctx context.Context, index int) ([]byte, error) {
	return f.doDownloadSegment(ctx, index, true)
}

// ReadAheadSegment downloads a segment in the background without counting failures
// toward IsFailed(). Used by SegmentReader to warm the pool cache ahead of the read
// pointer so subsequent Read calls don't block on network I/O.
func (f *File) ReadAheadSegment(ctx context.Context, index int) {
	if index < 0 || index >= len(f.segments) {
		return
	}
	go func() {
		_, _ = f.doDownloadSegment(ctx, index, false)
	}()
}

func (f *File) doDownloadSegment(ctx context.Context, index int, countFailures bool) ([]byte, error) {

	// Callers wait on a shared in-flight fetch keyed by segment index, but they do
	// not own the underlying request lifecycle. That shared fetch runs on the file
	// context so short-lived probe/prefetch/read cancellations do not poison a
	// segment download that another reader may still need moments later.
	req, leader := f.startInflightDownload(index, countFailures)
	if leader {
		go f.runInflightDownload(index, req)
	}
	defer f.releaseInflightDownloadWaiter(index, req)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-req.done:
		return req.data, req.err
	}
}

func (f *File) startInflightDownload(index int, countFailures bool) (*inflightSegmentDownload, bool) {
	f.downloadMu.Lock()
	defer f.downloadMu.Unlock()

	if req, ok := f.inflightDownloads[index]; ok {
		req.waiters++
		if countFailures {
			req.countFailures = true
		}
		return req, false
	}

	baseCtx := f.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	sharedCtx, cancel := context.WithCancel(baseCtx)

	req := &inflightSegmentDownload{
		done:          make(chan struct{}),
		countFailures: countFailures,
		ctx:           sharedCtx,
		cancel:        cancel,
		waiters:       1,
	}
	f.inflightDownloads[index] = req
	return req, true
}

func (f *File) releaseInflightDownloadWaiter(index int, req *inflightSegmentDownload) {
	f.downloadMu.Lock()
	defer f.downloadMu.Unlock()

	current, ok := f.inflightDownloads[index]
	if !ok || current != req {
		return
	}
	if req.waiters > 0 {
		req.waiters--
	}
}

func (f *File) runInflightDownload(index int, req *inflightSegmentDownload) {
	var data []byte
	var err error
	if f.fetcher != nil {
		data, err = f.doDownloadSegmentViaFetcher(req.ctx, index)
	} else {
		data, err = f.doDownloadSegmentViaPools(req.ctx, index)
	}

	f.downloadMu.Lock()
	defer f.downloadMu.Unlock()

	current, ok := f.inflightDownloads[index]
	if !ok || current != req {
		return
	}
	req.data, req.err = f.finalizeSegmentDownload(index, data, err, req.countFailures)
	delete(f.inflightDownloads, index)
	req.cancel()
	close(req.done)
}

func (f *File) finalizeSegmentDownload(index int, data []byte, err error, countFailures bool) ([]byte, error) {
	if err == nil {
		return data, nil
	}

	var eligible *zeroFillEligibleError
	if !errors.As(err, &eligible) {
		return nil, err
	}

	if !countFailures {
		return nil, fmt.Errorf("prefetch segment download failed (not counted): %w", eligible.cause)
	}

	f.zeroFillMu.Lock()
	count := f.zeroFillCount
	if count >= MaxZeroFills {
		f.zeroFillMu.Unlock()
		return nil, fmt.Errorf("too many failed segments (%d/%d): %w", count+1, MaxZeroFills, errors.Join(unpack.ErrTooManyZeroFills, eligible.cause))
	}
	f.zeroFillCount++
	f.zeroFillMu.Unlock()

	seg := f.segments[index]
	size := int(seg.EndOffset - seg.StartOffset)
	if size < 0 {
		size = 0
	}
	zeroData := make([]byte, size)
	logger.Trace("Segment download failed, zero-filling", "index", index, "count", count+1, "max", MaxZeroFills, "err", eligible.cause)
	return zeroData, nil
}

func (f *File) doDownloadSegmentViaFetcher(ctx context.Context, index int) ([]byte, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	seg := f.segments[index]
	var data pool.SegmentData
	var err error
	if index == 0 {
		if firstFetcher, ok := f.fetcher.(SegmentFirstFetcher); ok {
			data, err = firstFetcher.FetchSegmentFirst(downloadCtx, &seg.Segment, f.nzbFile.Groups)
		} else {
			data, err = f.fetcher.FetchSegment(downloadCtx, &seg.Segment, f.nzbFile.Groups)
		}
	} else {
		data, err = f.fetcher.FetchSegment(downloadCtx, &seg.Segment, f.nzbFile.Groups)
	}
	if err != nil {
		if isArticleNotFound(err) {
			return nil, fmt.Errorf("segment unavailable: %w", err)
		}
		if index == 0 {
			return nil, fmt.Errorf("first segment fetch failed: %w", err)
		}
		if isContextErr(err) || !shouldPersistDownloadedSegment(downloadCtx) {
			if ctxErr := downloadCtx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		return nil, &zeroFillEligibleError{cause: err}
	}
	if !shouldPersistDownloadedSegment(downloadCtx) {
		return nil, downloadCtx.Err()
	}
	// Don't cache here when using the pool fetcher: the pool already cached by message ID.
	// Caching again would double memory use (same segment in pool cache + loader segCache) and double-count the budget.
	return data.Body, nil
}

func (f *File) doDownloadSegmentViaPools(ctx context.Context, index int) ([]byte, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	seg := f.segments[index]
	tried := make([]bool, len(f.pools))
	var lastErr error

	for attempt := 0; attempt < len(f.pools); attempt++ {
		select {
		case <-downloadCtx.Done():
			return nil, downloadCtx.Err()
		default:
		}

		var client *nntp.Client
		var clientPool *nntp.ClientPool
		var poolIdx int = -1

		for i, p := range f.pools {
			if !tried[i] {
				if c, ok := p.TryGet(downloadCtx); ok {
					client = c
					clientPool = p
					poolIdx = i
					break
				}
			}
		}

		if client == nil {
			for i, p := range f.pools {
				if !tried[i] {
					var err error
					client, err = p.Get(downloadCtx)
					if err != nil {
						tried[i] = true
						lastErr = err
						if errors.Is(err, context.Canceled) {
							return nil, err
						}
						continue
					}
					clientPool = p
					poolIdx = i
					break
				}
			}
		}

		if client == nil {
			break
		}

		if len(f.nzbFile.Groups) > 0 {
			client.Group(f.nzbFile.Groups[0])
		}

		r, err := client.Body(seg.ID)
		if err != nil {
			clientPool.Put(client)
			tried[poolIdx] = true
			lastErr = err
			continue
		}

		type decodeResult struct {
			frame *decode.Frame
			err   error
		}
		done := make(chan decodeResult, 1)
		go func(body io.ReadCloser) {
			frame, err := decodeAndCloseBody(body, decode.DecodeToBytes)
			done <- decodeResult{frame, err}
		}(r)

		select {
		case <-downloadCtx.Done():
			clientPool.Discard(client)
			return nil, downloadCtx.Err()
		case res := <-done:
			if res.err != nil {
				clientPool.Put(client)
				tried[poolIdx] = true
				lastErr = res.err
				continue
			}
			clientPool.Put(client)
			if !shouldPersistDownloadedSegment(downloadCtx) {
				return nil, downloadCtx.Err()
			}
			return res.frame.Data, nil
		}
	}

	if lastErr != nil && isArticleNotFound(lastErr) {
		return nil, fmt.Errorf("segment unavailable: %w", lastErr)
	}

	if index == 0 {
		return nil, fmt.Errorf("first segment failed on all providers: %w", lastErr)
	}

	if isContextErr(lastErr) || !shouldPersistDownloadedSegment(downloadCtx) {
		if ctxErr := downloadCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, lastErr
	}

	return nil, &zeroFillEligibleError{cause: lastErr}
}

func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	if err := f.EnsureSegmentMap(); err != nil {
		return 0, err
	}
	if off >= f.totalSize {
		return 0, io.EOF
	}

	startIdx := f.FindSegmentIndex(off)
	if startIdx == -1 {
		return 0, io.EOF
	}

	currentOffset := off
	totalRead := 0
	for i := startIdx; i < len(f.segments) && totalRead < len(p); i++ {
		seg := f.segments[i]
		segOff := currentOffset - seg.StartOffset

		data, err := f.DownloadSegment(f.ctx, i)
		if err != nil {
			return totalRead, err
		}
		if segOff >= int64(len(data)) {
			continue
		}

		copied := copy(p[totalRead:], data[segOff:])
		totalRead += copied
		currentOffset += int64(copied)
	}

	if totalRead < len(p) && currentOffset >= f.totalSize {
		return totalRead, io.EOF
	}
	return totalRead, nil
}

func (f *File) OpenStream() (io.ReadSeekCloser, error) {
	return f.OpenStreamCtx(f.ctx)
}

func (f *File) OpenStreamCtx(ctx context.Context) (io.ReadSeekCloser, error) {
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return nil, err
	}
	return NewSegmentReader(ctx, f, 0), nil
}

func (f *File) OpenReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return nil, err
	}
	return NewSegmentReader(ctx, f, offset), nil
}

// MaxSegmentSizeEstimatorEntries caps the number of size entries to prevent unbounded growth.
const MaxSegmentSizeEstimatorEntries = 128

type SegmentSizeEstimator struct {
	entries []sizeEntry
	mu      sync.RWMutex
}

type sizeEntry struct {
	encoded int64
	decoded int64
}

func NewSegmentSizeEstimator() *SegmentSizeEstimator {
	return &SegmentSizeEstimator{entries: make([]sizeEntry, 0, 4)}
}

func (e *SegmentSizeEstimator) Get(encodedSize int64) (int64, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, entry := range e.entries {
		diff := entry.encoded - encodedSize
		if diff < 0 {
			diff = -diff
		}
		if diff < 4096 {
			return entry.decoded, true
		}
	}
	return 0, false
}

func (e *SegmentSizeEstimator) Set(encodedSize, decodedSize int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, entry := range e.entries {
		diff := entry.encoded - encodedSize
		if diff < 0 {
			diff = -diff
		}
		if diff < 4096 {
			return
		}
	}
	if len(e.entries) >= MaxSegmentSizeEstimatorEntries {
		e.entries = e.entries[1:]
	}
	e.entries = append(e.entries, sizeEntry{encoded: encodedSize, decoded: decodedSize})
}
