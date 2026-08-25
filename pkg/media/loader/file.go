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
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/media/nzb"
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

// StatConcurrencyHinter is optional: when implemented, the fetcher decides how
// many STATs CheckFirstSegmentExists may have in flight. The fetcher owns the
// connections, so it is the only thing that can size this sensibly.
type StatConcurrencyHinter interface {
	StatConcurrency() int
}

// SegmentBatchFetcher is optional: when implemented, read-ahead can hand a run
// of segments to one connection that keeps several BODY commands outstanding,
// instead of one connection per segment. Results are aligned with the input
// slice; anything not returned OK stays the caller's to fetch the ordinary way.
type SegmentBatchFetcher interface {
	PipelineDepth() int
	FetchSegmentsPipelined(ctx context.Context, segments []*nzb.Segment, groups []string) []pool.PipelinedResult
}

// FetchConcurrencyHinter is optional: when implemented, the fetcher reports how
// many segment fetches it can keep on the wire at once. Read-ahead needs it to
// tell a spare connection from a contended one — see readAheadBatchSize.
type FetchConcurrencyHinter interface {
	FetchConcurrency() int
}

// defaultStatConcurrency applies when the fetcher offers no hint.
const defaultStatConcurrency = 4

func shouldPersistDownloadedSegment(ctx context.Context) bool {
	return ctx == nil || ctx.Err() == nil
}

func decodeAndCloseBody(body io.ReadCloser, decodeFn func(io.Reader) (*decode.Frame, error)) (*decode.Frame, error) {
	defer body.Close()
	return decodeFn(body)
}

// MaxZeroFills is how many DISTINCT segments of one file may be zero-filled
// before the file is declared unplayable. Counting distinct indices (rather
// than fetch attempts) is what makes the cap mean "this release has N holes":
// seeking back and forth across one damaged segment must not burn the budget.
const MaxZeroFills = 10

// slowSegmentFetchThreshold flags segment downloads that took long enough to
// drain a player's buffer; one Warn per slow segment confirms mid-stream
// stalls in the field without needing Trace-level logs.
const slowSegmentFetchThreshold = 10 * time.Second

// isArticleNotFound reports whether err indicates the article is missing (430 No Such Article).
// Used to fail fast on the first segment instead of zero-filling through many segments.
func isArticleNotFound(err error) bool {
	return nntp.IsArticleNotFound(err)
}

func (f *File) IsFailed() bool {
	return f.ZeroFilledSegments() >= MaxZeroFills
}

// ZeroFilledSegments reports how many distinct segments of this file have been
// zero-filled so far.
func (f *File) ZeroFilledSegments() int {
	f.zeroFillMu.Lock()
	defer f.zeroFillMu.Unlock()
	return len(f.zeroFilled)
}

// isZeroFilled reports whether index was already zero-filled, so repeat reads
// of a known hole return zeros straight away instead of re-fetching an article
// that all providers have already refused.
func (f *File) isZeroFilled(index int) bool {
	f.zeroFillMu.Lock()
	defer f.zeroFillMu.Unlock()
	_, ok := f.zeroFilled[index]
	return ok
}

// zeroSegment builds the filler for a segment index using the segment map as it
// stands now, so a hole discovered before size detection still reads back at the
// mapped length afterwards.
func (f *File) zeroSegment(index int) []byte {
	size := f.segmentDecodedLen(index)
	if size < 0 {
		size = 0
	}
	return make([]byte, size)
}

type Segment struct {
	nzb.Segment
	StartOffset int64
	EndOffset   int64
}

type File struct {
	nzbFile   *nzb.File
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

	zeroFillMu sync.Mutex
	zeroFilled map[int]struct{}

	segmentDetectMu sync.Mutex

	// What produced the segment map, kept so it can be persisted and replayed
	// on a later session instead of re-probing (see SegmentMapSnapshotJSON).
	mapProbes  map[int]int64
	mapKnown   map[int64]int64
	mapSkipGap bool

	firstStatMu      sync.Mutex
	firstStatChecked bool
	firstStatExists  bool
	firstStatErr     error

	// yencName is the "=ybegin name=" seen on the last decoded article of this
	// file. Every article of a file repeats it, so one fetch is enough.
	yencName atomic.Value
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

func NewFile(ctx context.Context, f *nzb.File, estimator *SegmentSizeEstimator, fetcher SegmentFetcher) *File {
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
		fetcher:           fetcher,
		estimator:         estimator,
		segments:          segments,
		totalSize:         offset,
		ctx:               ctx,
		inflightDownloads: make(map[int]*inflightSegmentDownload),
		zeroFilled:        make(map[int]struct{}),
	}
}

func (f *File) Name() string { return f.nzbFile.Subject }

// ReadFirstSegment returns the decoded bytes of the file's first article.
//
// Segment 0 always starts at offset 0, so this needs no segment map — unlike
// ReadAt and every Open* path, which must resolve an arbitrary offset and so
// probe the whole file first. For a release being identified by its opening
// bytes that difference is one article per file instead of hundreds.
func (f *File) ReadFirstSegment(ctx context.Context) ([]byte, error) {
	if len(f.segments) == 0 {
		return nil, io.EOF
	}
	return f.DownloadSegment(ctx, 0)
}

// YencFileName returns the filename the poster wrote into this file's yEnc
// headers, fetching the first segment when no article has been decoded yet.
// It is the cheapest real name available for a release whose NZB subjects are
// obfuscated: every article repeats the header, and the first segment is
// downloaded by archive scanning anyway. Returns "" when the articles carry no
// name; the fetch error is returned only when nothing could be read at all.
func (f *File) YencFileName(ctx context.Context) (string, error) {
	if name, ok := f.yencName.Load().(string); ok && name != "" {
		return name, nil
	}
	if len(f.segments) == 0 {
		return "", nil
	}
	if _, err := f.DownloadSegment(ctx, 0); err != nil {
		return "", err
	}
	name, _ := f.yencName.Load().(string)
	return name, nil
}

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

// CheckFirstSegmentExists returns whether the required segments (start, middle, end) exist on the server via STAT.
// Used before opening a stream to fail fast when release segments are missing (430).
// statSampleSpread is how many evenly spaced STAT points a file of the given
// segment count gets: one per ~1000 segments (~700 MB at typical article
// sizes), floored at the historical nine and capped so the pre-flight stays
// bounded on very large single-file posts.
func statSampleSpread(segments int) int {
	const (
		base       = 9
		maxSamples = 32
	)
	n := segments / 1000
	if n < base {
		return base
	}
	if n > maxSamples {
		return maxSamples
	}
	return n
}

func (f *File) CheckFirstSegmentExists(ctx context.Context) (bool, error) {
	if len(f.segments) == 0 {
		return false, nil
	}
	f.firstStatMu.Lock()
	if f.firstStatChecked {
		exists, err := f.firstStatExists, f.firstStatErr
		f.firstStatMu.Unlock()
		return exists, err
	}
	f.firstStatMu.Unlock()

	statter, ok := f.fetcher.(SegmentStatter)
	if !ok {
		return f.recordFirstStat(true, nil)
	}

	// The head segments and the tail are always checked — they catch a
	// truncated post — and the rest of the sample is spread evenly, scaling
	// with the file so scattered damage on a large release is overwhelmingly
	// likely to surface here rather than mid-stream after Content-Length has
	// been promised. Nine fixed points let a post with holes in the low
	// percents through most of the time; the census stays cheap because STATs
	// are batched at the fetcher's concurrency.
	sampleIndices := map[int]bool{
		0:                   true,
		1:                   true,
		2:                   true,
		3:                   true,
		4:                   true,
		len(f.segments) - 1: true,
	}
	spread := statSampleSpread(len(f.segments))
	for i := 0; i < spread; i++ {
		denom := spread - 1
		if denom < 1 {
			denom = 1
		}
		sampleIndices[i*(len(f.segments)-1)/denom] = true
	}

	indices := make([]int, 0, len(sampleIndices))
	for idx := range sampleIndices {
		if idx < 0 || idx >= len(f.segments) {
			continue
		}
		indices = append(indices, idx)
	}
	// Ascending order so the head segments — the ones that catch a truncated
	// post — go out in the first batch.
	sort.Ints(indices)

	msgIDs := make([]string, 0, len(indices))
	for _, idx := range indices {
		msgID := strings.TrimSpace(f.segments[idx].ID)
		if msgID == "" {
			// An article with no message id can never be fetched, so the
			// release is unplayable regardless of what the server holds.
			return f.recordFirstStat(false, nil)
		}
		msgIDs = append(msgIDs, msgID)
	}

	// The sampled segments are independent of each other, so probe them
	// together rather than paying one round trip per segment. The fetcher caps
	// how many run at once, since every file being validated does this at the
	// same time.
	limit := defaultStatConcurrency
	if hinter, ok := f.fetcher.(StatConcurrencyHinter); ok {
		if hinted := hinter.StatConcurrency(); hinted > 0 {
			limit = hinted
		}
	}
	if limit > len(msgIDs) {
		limit = len(msgIDs)
	}

	statCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		missing  bool
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, limit)
	for _, msgID := range msgIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(msgID string) {
			defer wg.Done()
			defer func() { <-sem }()

			exists, err := statter.StatSegment(statCtx, msgID, f.nzbFile.Groups)
			if err == nil && exists {
				return
			}
			mu.Lock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if err == nil && !exists {
				missing = true
			}
			definitive := missing
			mu.Unlock()
			// One article confirmed missing on every provider settles it; stop
			// the siblings. Transient errors do not, so those are left to run:
			// another segment may still return a definitive answer.
			if definitive {
				cancel()
			}
		}(msgID)
	}
	wg.Wait()

	// A definitive miss outranks any error, including the cancellations this
	// function caused in the siblings it stopped. Without this the release
	// would be reported as inconclusive and never marked bad.
	if missing {
		return f.recordFirstStat(false, nil)
	}
	if firstErr != nil {
		return f.recordFirstStat(false, firstErr)
	}
	return f.recordFirstStat(true, nil)
}

// recordFirstStat caches the outcome so repeat checks on the same file are free.
func (f *File) recordFirstStat(exists bool, err error) (bool, error) {
	f.firstStatMu.Lock()
	f.firstStatChecked = true
	f.firstStatExists = exists
	f.firstStatErr = err
	f.firstStatMu.Unlock()
	return exists, err
}

// StatSegmentAt STATs the article of the segment at index via the fetcher's
// statter. Returns (true, nil) when it exists on at least one provider,
// (false, nil) when definitively missing everywhere (430 on all), and
// (false, err) when inconclusive (no statter available, transient error).
func (f *File) StatSegmentAt(ctx context.Context, index int) (bool, error) {
	if index < 0 || index >= len(f.segments) {
		return false, fmt.Errorf("segment index %d out of range (segments=%d)", index, len(f.segments))
	}
	statter, ok := f.fetcher.(SegmentStatter)
	if !ok {
		return false, errors.New("fetcher does not support STAT")
	}
	msgID := strings.TrimSpace(f.segments[index].ID)
	if msgID == "" {
		return false, nil // article without a message id cannot be fetched
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
	f.segmentDetectMu.Lock()
	defer f.segmentDetectMu.Unlock()

	f.mu.Lock()
	if f.detected {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	return f.detectSegmentSizeLocked(ctx)
}

// PrimeUniformSegmentMapFromEstimator builds a segment map without NNTP probes when
// every segment (including the last) shares the same NZB bytes and the estimator
// already knows the decoded size from an earlier volume in the same release.
func (f *File) PrimeUniformSegmentMapFromEstimator() bool {
	if f == nil || len(f.segments) == 0 || f.estimator == nil {
		return false
	}
	if !hasUniformNZBSegmentBytes(f.segments) {
		return false
	}
	first := f.segments[0]
	decoded, ok := f.estimator.Get(first.Bytes)
	if !ok || decoded <= 0 {
		return false
	}

	knownByNZBBytes := map[int64]int64{first.Bytes: decoded}
	sizes := buildSegmentDecodedSizesFromProbes(f.segments, nil, knownByNZBBytes, true)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detected {
		return true
	}
	f.totalSize = applySegmentDecodedSizes(f.segments, sizes)
	f.detected = true
	f.recordSegmentMapInputsLocked(nil, knownByNZBBytes, true)
	logger.Trace("Primed uniform segment map from estimator",
		"name", f.Name(),
		"size", f.totalSize,
		"segments", len(f.segments),
		"decoded", decoded)
	return true
}

func (f *File) detectSegmentSizeLocked(ctx context.Context) error {
	if ctx == nil {
		ctx = f.ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	f.mu.Lock()
	if f.detected {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	if f.PrimeUniformSegmentMapFromEstimator() {
		return nil
	}

	if len(f.segments) == 0 {
		f.mu.Lock()
		f.totalSize = 0
		f.detected = true
		f.mu.Unlock()
		return nil
	}

	knownByNZBBytes := make(map[int64]int64)
	if f.estimator != nil {
		if decoded, ok := f.estimator.Get(f.segments[0].Bytes); ok {
			knownByNZBBytes[f.segments[0].Bytes] = decoded
			logger.Trace("Using estimated segment size", "name", f.Name(), "size", decoded)
		}
	}

	includeMiddle := shouldProbeMiddleSegment(ctx, f.segments)
	indices := segmentProbeIndices(f.segments, knownByNZBBytes, includeMiddle, IsSkipGapProbingEnabled(ctx))
	logSegmentProbePlan(ctx, f.Name(), f.segments, indices, knownByNZBBytes, includeMiddle)

	probedByIndex, err := f.probeSegmentIndicesParallel(ctx, indices)
	if err != nil {
		return err
	}

	if !IsSkipGapProbingEnabled(ctx) {
		if missing := segmentUnprobedIndices(len(f.segments), indices); len(missing) > 0 {
			logger.Debug("Segment map gap probe",
				"name", f.Name(),
				"indices", missing,
				"count", len(missing))
			gapProbed, gapErr := f.probeSegmentIndicesParallel(ctx, missing)
			if gapErr != nil {
				return gapErr
			}
			for idx, decoded := range gapProbed {
				probedByIndex[idx] = decoded
			}
		}
	}

	lastIdx := len(f.segments) - 1
	for idx, decoded := range probedByIndex {
		if idx < 0 || idx >= len(f.segments) {
			continue
		}
		logger.Debug("Detected segment size",
			"name", f.Name(),
			"index", idx,
			"size", decoded,
			"nzb_size", f.segments[idx].Bytes)
		// Teach the estimator every class this probe measured, so the next
		// volume of the same release can skip re-measuring it.
		//
		// This used to seed only from index 0, which on a non-uniform post is
		// almost never probed — the planner picks whichever segment first
		// represents each size class. An 87-volume release therefore logged
		// known_from_estimator=0 on every single volume and paid a full-class
		// probe for each, having already measured that exact class.
		//
		// The physical last segment is excluded for the same reason
		// buildSegmentDecodedSizesFromProbes excludes it from class matching:
		// it is remainder-sized, and letting it stand for the full-segment
		// class would paint a short size across whole files.
		if f.estimator != nil && decoded > 0 && !(idx == lastIdx && lastIdx > 0) {
			f.estimator.Set(f.segments[idx].Bytes, decoded)
		}
	}

	sizes := buildSegmentDecodedSizesFromProbes(f.segments, probedByIndex, knownByNZBBytes, IsSkipGapProbingEnabled(ctx))
	if includeMiddle {
		mid := middleProbeIndex(len(f.segments))
		if midDecoded, ok := probedByIndex[mid]; ok {
			applyUniformMiddleCalibration(f.segments, sizes, mid, midDecoded)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detected {
		return nil
	}
	f.totalSize = applySegmentDecodedSizes(f.segments, sizes)
	f.detected = true
	f.recordSegmentMapInputsLocked(probedByIndex, knownByNZBBytes, IsSkipGapProbingEnabled(ctx))
	nzbSum := sumNZBSegmentBytes(f.segments)
	logSegmentMapSizeCheck(f.Name(), f.segments, nzbSum, f.totalSize, probedByIndex)
	logger.Debug("Recalculated total decoded size",
		"name", f.Name(),
		"size", f.totalSize,
		"segments", len(f.segments),
		"probed", len(probedByIndex))
	return nil
}

func logSegmentMapSizeCheck(name string, segments []*Segment, nzbSum, decodedSum int64, probedByIndex map[int]int64) {
	attrs := []any{
		"name", name,
		"nzb_bytes_sum", nzbSum,
		"decoded_sum", decodedSum,
		"delta_decoded_minus_nzb", decodedSum - nzbSum,
		"segments", len(segments),
	}
	if len(segments) > 0 && segments[0].Bytes > 0 {
		if dec, ok := probedByIndex[0]; ok && dec > 0 {
			attrs = append(attrs, "seg0_decode_ratio", float64(dec)/float64(segments[0].Bytes))
		}
	}
	if n := len(segments); n > 0 {
		attrs = append(attrs, "last_nzb_bytes", segments[n-1].Bytes)
		if dec, ok := probedByIndex[n-1]; ok {
			attrs = append(attrs, "last_decoded", dec)
		}
	}
	logger.Debug("Segment map size check", attrs...)
}

// SegmentOffsetRange returns decoded byte offsets for a segment index after map detection.
func (f *File) SegmentOffsetRange(index int) (start, end int64, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.segments) {
		return 0, 0, false
	}
	s := f.segments[index]
	return s.StartOffset, s.EndOffset, true
}

// segmentDecodedLen returns the virtual decoded byte length for a segment index.
func (f *File) segmentDecodedLen(idx int) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if idx < 0 || idx >= len(f.segments) {
		return 0
	}
	s := f.segments[idx]
	if f.detected {
		return s.EndOffset - s.StartOffset
	}
	return s.Bytes
}

// verifyMappedSegmentLength refuses a downloaded segment whose decoded length
// disagrees with the segment map. The map can be estimated — gap probing is
// skipped on archive reads, and unprobed segments inherit a class
// representative's size — and serving through a mismatch silently shifts every
// byte after it: the demuxer desyncs and the served range no longer matches
// the release. A loud error turns that silent corruption into a failover, and
// the Warn names the segment so the estimator can be fixed from the field.
// Before the map is detected, mapped lengths are still encoded sizes, so a
// mismatch there is expected and not checked.
func (f *File) verifyMappedSegmentLength(index int, data []byte) error {
	f.mu.Lock()
	detected := f.detected
	f.mu.Unlock()
	if !detected {
		return nil
	}
	mapped := f.segmentDecodedLen(index)
	if int64(len(data)) == mapped {
		return nil
	}
	logger.Warn("Segment decoded length disagrees with the segment map",
		"file", f.Name(), "index", index, "mapped", mapped, "decoded", len(data))
	return fmt.Errorf("segment %d decoded %d bytes but is mapped as %d: refusing to serve shifted bytes", index, len(data), mapped)
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
	// A segment already ruled a hole stays a hole: every provider refused it
	// once, so re-fetching on each pass over the same offset only adds latency
	// and would double-count the same damage against MaxZeroFills.
	if index >= 0 && index < len(f.segments) && f.isZeroFilled(index) {
		return f.zeroSegment(index), nil
	}

	// Callers wait on a shared in-flight fetch keyed by segment index, but they do
	// not own the underlying request lifecycle. That shared fetch runs on the file
	// context so short-lived probe/prefetch/read cancellations do not poison a
	// segment download that another reader may still need moments later.
	req, leader := f.startInflightDownload(index, countFailures)
	logger.Trace("File doDownloadSegment: start", "file", f.Name(), "index", index, "leader", leader, "countFailures", countFailures)
	if leader {
		go f.runInflightDownload(index, req)
	}
	defer f.releaseInflightDownloadWaiter(index, req)

	select {
	case <-ctx.Done():
		// Expected when a probe/playback stream is closed or seeks mid-download; not an error.
		logger.Trace("File doDownloadSegment: ctx cancelled", "file", f.Name(), "index", index, "err", ctx.Err())
		return nil, ctx.Err()
	case <-req.done:
		logger.Trace("File doDownloadSegment: req done", "file", f.Name(), "index", index, "err", req.err)
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
	logger.Trace("File runInflightDownload: start fetch", "file", f.Name(), "index", index, "viaFetcher", f.fetcher != nil)
	start := time.Now()
	var data []byte
	var err error
	if f.fetcher != nil {
		data, err = f.doDownloadSegmentViaFetcher(req.ctx, index)
	}
	if elapsed := time.Since(start); elapsed > slowSegmentFetchThreshold && req.ctx.Err() == nil {
		logger.Warn("Slow segment fetch", "file", f.Name(), "index", index, "duration", elapsed, "err", err)
	}
	logger.Trace("File runInflightDownload: fetch complete", "file", f.Name(), "index", index, "err", err, "dataLen", len(data))

	f.completeInflightDownload(index, req, data, err)
}

// completeInflightDownload publishes a result to everyone waiting on req and
// retires it. A req that is no longer the registered one for index lost a race
// with a reset and is dropped: its waiters have already moved to the new one.
func (f *File) completeInflightDownload(index int, req *inflightSegmentDownload, data []byte, err error) {
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
	_, known := f.zeroFilled[index]
	count := len(f.zeroFilled)
	if !known {
		if count >= MaxZeroFills {
			f.zeroFillMu.Unlock()
			return nil, fmt.Errorf("too many failed segments (%d/%d): %w", count+1, MaxZeroFills, errors.Join(ErrTooManyZeroFills, eligible.cause))
		}
		f.zeroFilled[index] = struct{}{}
		count++
	}
	f.zeroFillMu.Unlock()

	// Warn, not Debug: every zero-filled segment is wrong bytes served to a
	// player, and the operator should be able to find that after the fact.
	logger.Warn("Segment unavailable, zero-filling gap", "file", f.Name(), "index", index, "holes", count, "max", MaxZeroFills, "err", eligible.cause)
	return f.zeroSegment(index), nil
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
		if index == 0 {
			// The first segment carries the container/volume header: nothing
			// downstream can make sense of a zero-filled one, so a miss here
			// stays a fast, definitive verdict about the release.
			if isArticleNotFound(err) {
				return nil, fmt.Errorf("segment unavailable: %w", err)
			}
			return nil, fmt.Errorf("first segment fetch failed: %w", err)
		}
		if isArticleNotFound(err) {
			// An isolated missing article past the header is a hole, not a dead
			// release. Hand it to the zero-fill policy, which decides between a
			// glitch the player rides out and ErrTooManyZeroFills once the file
			// has accumulated more holes than MaxZeroFills allows. The cause is
			// preserved either way, so the fatal error still reads as a 430.
			return nil, &zeroFillEligibleError{cause: fmt.Errorf("segment unavailable: %w", err)}
		}
		if isContextErr(err) || !shouldPersistDownloadedSegment(downloadCtx) {
			if ctxErr := downloadCtx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		// Anything else — a timeout, a connection reset, a decode failure, an
		// exhausted pool — is inconclusive: the bytes may well exist, so
		// substituting zeros would serve provably wrong data for a transient
		// fault. Only a 430 from every provider is evidence of a hole; the
		// rest propagates so the playback layer can retry or fail over.
		return nil, fmt.Errorf("segment fetch failed: %w", err)
	}
	if !shouldPersistDownloadedSegment(downloadCtx) {
		return nil, downloadCtx.Err()
	}
	if name := strings.TrimSpace(data.FileName); name != "" {
		f.yencName.Store(name)
	}
	// Don't cache here when using the pool fetcher: the pool already cached by message ID.
	// Caching again would double memory use (same segment in pool cache + loader segCache) and double-count the budget.
	return data.Body, nil
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
		segLen := f.segmentDecodedLen(i)
		segOff := currentOffset - seg.StartOffset
		if segOff >= segLen {
			continue
		}

		data, err := f.DownloadSegment(f.ctx, i)
		if err != nil {
			return totalRead, err
		}
		if err := f.verifyMappedSegmentLength(i, data); err != nil {
			return totalRead, err
		}

		remain := segLen - segOff
		avail := int64(len(data)) - segOff
		if avail < 0 {
			avail = 0
		}
		toCopy := remain
		if avail < toCopy {
			toCopy = avail
		}
		bufRoom := int64(len(p) - totalRead)
		if bufRoom < toCopy {
			toCopy = bufRoom
		}
		if toCopy <= 0 {
			continue
		}

		copied := copy(p[totalRead:], data[segOff:segOff+toCopy])
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

// OpenPlaybackStreamCtx opens a seekable stream with the playback read-ahead
// window, for direct (non-archive) playback of a whole file.
//
// OpenStreamCtx is shared with the RAR scan, PAR2 repair and archive-probe
// paths, which read small pieces and would only waste bandwidth on a deep
// prefetch — so it keeps DefaultReadAhead. Playback is the opposite case:
// throughput is read-ahead depth times per-connection rate, and at eight
// segments in flight a 4K remux runs out of runway (a 67 GB release needs
// ~7.6 MB/s sustained and was being served 6–8 MB/s, blocked on reads 97% of
// the time). Multi-volume archive playback already opened its reader with the
// deeper window; direct playback, the common case, did not.
func (f *File) OpenPlaybackStreamCtx(ctx context.Context) (io.ReadSeekCloser, error) {
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return nil, err
	}
	return NewSegmentReaderWithReadAhead(ctx, f, 0, f.PlaybackReadAhead()), nil
}

// PlaybackReadAhead is this file's read-ahead window, sized against its own
// article size rather than assumed to be the sub-megabyte common case.
func (f *File) PlaybackReadAhead() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return PlaybackReadAheadFor(f.totalSize, f.averageSegmentSizeLocked())
}

// averageSegmentSizeLocked reports the mean decoded segment size, which is what
// the window has to be measured in. The mean rather than segment zero: a file
// can open with a short article, and one unlucky sample would size the whole
// window wrong. Returns 0 before the map is built, which reads as "unknown".
func (f *File) averageSegmentSizeLocked() int64 {
	if !f.detected || len(f.segments) == 0 || f.totalSize <= 0 {
		return 0
	}
	return f.totalSize / int64(len(f.segments))
}

func (f *File) OpenReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return nil, err
	}
	return NewSegmentReader(ctx, f, offset), nil
}

// OpenPlaybackReaderAt opens a reader with a larger read-ahead window for archive
// playback seeks that cross RAR volume boundaries.
func (f *File) OpenPlaybackReaderAt(ctx context.Context, offset int64) (io.ReadCloser, error) {
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return nil, err
	}
	return NewSegmentReaderWithReadAhead(ctx, f, offset, f.PlaybackReadAhead()), nil
}

// PrefetchPlaybackOffset warms the segment cache ahead of an upcoming volume switch.
func (f *File) PrefetchPlaybackOffset(ctx context.Context, offset int64) {
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return
	}
	idx := f.FindSegmentIndex(offset)
	if idx < 0 {
		return
	}
	end := idx + f.PlaybackReadAhead()
	if end > len(f.segments) {
		end = len(f.segments)
	}
	// Bind prefetch to the lifetime of the session file context (f.ctx) rather than using context.WithoutCancel.
	// This ensures that when the session closes, the prefetches are aborted, but they aren't cancelled by transient HTTP request timeouts.
	bgCtx := f.ctx
	if bgCtx == nil {
		bgCtx = context.Background()
	}
	f.ReadAheadRange(bgCtx, idx, end)
}

// PrefetchPlaybackRange warms only the segments covering [offset, offset+length),
// instead of the full playback window PrefetchPlaybackOffset opens.
//
// The caller that needs this is a tail warm: a player reads the container index
// at the end of the file and then seeks straight back to the start, so pulling a
// whole window in behind it would spend the bandwidth the opening bytes need.
func (f *File) PrefetchPlaybackRange(ctx context.Context, offset, length int64) {
	if length <= 0 {
		return
	}
	if err := f.EnsureSegmentMapCtx(ctx); err != nil {
		return
	}
	if offset < 0 {
		offset = 0
	}
	from := f.FindSegmentIndex(offset)
	if from < 0 {
		return
	}
	to := f.FindSegmentIndex(offset + length - 1)
	if to < from {
		to = len(f.segments) - 1
	}
	// Same binding as PrefetchPlaybackOffset: the session context, so a closed
	// session stops the fetch but a finished HTTP request does not.
	bgCtx := f.ctx
	if bgCtx == nil {
		bgCtx = context.Background()
	}
	f.ReadAheadRange(bgCtx, from, to+1)
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
