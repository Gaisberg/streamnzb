// Package playback owns opening, probing and pre-validating playback sources
// for a session: lazy NZB download, archive-volume STAT verification, media
// stream mapping, ffprobe validation and the disposable-probe / per-request
// open flow. HTTP translation, failover policy and attempt/avail telemetry
// stay in the server layer, which drives this service through narrow
// callbacks.
package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/ffprobe"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/session"
)

// ErrFirstSegmentUnavailable marks a definitive article-missing verdict from
// the pre-open STAT checks; callers classify it as a reportable bad release.
var ErrFirstSegmentUnavailable = errors.New("first segment not found (430)")

// errNZBIncomplete is the missing-from-the-NZB-itself verdict. It classifies
// as ErrFirstSegmentUnavailable — same consequences: definitive, reported bad,
// failed over — without inheriting that sentinel's message, which would claim
// a first segment was not found when the gap is elsewhere in the file.
type errNZBIncomplete struct{ msg string }

func (e *errNZBIncomplete) Error() string { return e.msg }

func (e *errNZBIncomplete) Is(target error) bool { return target == ErrFirstSegmentUnavailable }

// ErrPlaybackStartupTimeout is the sentinel wrapped into every startup-budget
// failure; callers match it with errors.Is to distinguish timeouts from
// cancellations and real media errors.
var ErrPlaybackStartupTimeout = errors.New("playback startup timeout")

// CacheInvalidator is the slice of the validation checker this package needs.
type CacheInvalidator interface {
	InvalidateCache(hash string)
}

// onceTailWarmed keeps the tail warm to one per session: Prepare starts it for
// a session replaying a cached plan, OpenSource for one that just built its
// plan, and both run for a session that had a blueprint all along.
const onceTailWarmed session.OnceKey = "playback-tail-warmed"

// LibraryStatus values mirrored from persistence to avoid a store dependency.
const (
	LibraryStatusPending = "pending"
	LibraryStatusGood    = "good"
)

// Service opens and validates playback streams for sessions. All fields are
// set once at construction; func fields exist because the server hot-swaps
// config on reload and owns library persistence and its once-guards.
type Service struct {
	Sessions  *session.Manager
	Validator CacheInvalidator
	// FFprobePath returns the effective ffprobe binary path ("" = default).
	FFprobePath func() string
	// StartupTimeout returns the per-phase playback startup budget.
	StartupTimeout func() time.Duration
	// PreloadArticleCensus reports whether preloading STATs every article of
	// the selected file rather than a sample. Optional; nil means off.
	PreloadArticleCensus func() bool
	// AllowLargestDirectFallback reports whether stream selection may fall
	// back to the largest direct file for this session (exact-episode and
	// movie sessions only; policy lives with the server's match typing).
	AllowLargestDirectFallback func(sess *session.Session) bool
	// SaveToLibrary persists the session + blueprint synchronously with the
	// given status. Optional.
	SaveToLibrary func(sess *session.Session, bp unpack.Blueprint, name string, size int64, status string)
	// NotePendingLibrarySave records the pending (pre-verdict) library save,
	// once per session — the server owns the once-guard and does the gzip'd
	// save on its own goroutine. Optional.
	NotePendingLibrarySave func(sess *session.Session, bp unpack.Blueprint, name string, size int64)
}

// Prepared bundles a validated per-request body stream with the cached
// startup metadata used by http.ServeContent.
type Prepared struct {
	Spec           session.PlaybackStreamSpec
	StartupInfo    seek.StreamStartInfo
	HasStartupInfo bool
	Stream         io.ReadSeekCloser
	Mode           string
}

type sourceOpenResult struct {
	stream io.ReadSeekCloser
	err    error
}

func (p *Service) startupTimeout() time.Duration {
	if p.StartupTimeout != nil {
		if d := p.StartupTimeout(); d > 0 {
			return d
		}
	}
	return 45 * time.Second
}

func (p *Service) preloadArticleCensus() bool {
	return p.PreloadArticleCensus != nil && p.PreloadArticleCensus()
}

func (p *Service) ffprobePath() string {
	if p.FFprobePath != nil {
		return p.FFprobePath()
	}
	return ""
}

func (p *Service) invalidateValidatorCache(sess *session.Session) {
	if p.Validator == nil {
		return
	}
	if nzbData := sess.NZB(); nzbData != nil {
		p.Validator.InvalidateCache(nzbData.Hash())
	}
}

func (p *Service) allowLargestDirectFallback(sess *session.Session) bool {
	if p.AllowLargestDirectFallback == nil {
		return false
	}
	return p.AllowLargestDirectFallback(sess)
}

// ClassifyStartupErr maps a phase error onto the startup-timeout sentinel
// when the phase deadline elapsed; other errors pass through unchanged.
//
// A proven-missing article outranks the deadline: ErrFirstSegmentUnavailable is
// only ever built from a real 430, never from an unanswered probe, so a verdict
// that lands after the budget expired is still a verdict. Rebranding it as a
// timeout downgraded it to "may be temporary" — no durable bad record, no
// AvailNZB report — and the dead release burned the full startup budget again
// on every future play.
func ClassifyStartupErr(phase string, startupTimeout time.Duration, startupCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrFirstSegmentUnavailable) {
		return err
	}
	// context.Cause covers both shapes of an expired budget: a WithTimeout
	// deadline (Cause == DeadlineExceeded) and a WithCancelCause cancelled with
	// DeadlineExceeded, which is how Prepare expires a probe whose stream must
	// outlive the phase (a plain WithTimeout would kill the reused stream
	// mid-serve once the deadline fired).
	if errors.Is(context.Cause(startupCtx), context.DeadlineExceeded) {
		return StartupTimeoutErr(startupTimeout, phase, err)
	}
	return err
}

// StartupTimeoutErr wraps ErrPlaybackStartupTimeout with phase context; the
// cause is flattened (%v) so the sentinel stays the only matchable error.
func StartupTimeoutErr(startupTimeout time.Duration, phase string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%w during %s after %s: %v", ErrPlaybackStartupTimeout, phase, startupTimeout, cause)
	}
	return fmt.Errorf("%w during %s after %s", ErrPlaybackStartupTimeout, phase, startupTimeout)
}

// Prepare probes with a temporary reader when startup metadata is missing,
// then opens a fresh per-request body stream matching the cached spec.
func (p *Service) Prepare(ctx context.Context, sess *session.Session) (Prepared, error) {
	preparedStream := Prepared{}

	snapshot, haveSnapshot := sess.PlaybackStreamSnapshot()
	if haveSnapshot {
		preparedStream.Spec = snapshot.Spec
		preparedStream.StartupInfo = snapshot.StartupInfo
		preparedStream.HasStartupInfo = snapshot.HasStartupInfo
	}

	needProbe := !haveSnapshot || !snapshot.HasStartupInfo
	probeMS := int64(0)
	if needProbe {
		// The container tail is warmed alongside the probe, not after it: the
		// probe reads the head for the better part of a second, and the player
		// asks for the tail the instant it has a header. A session replaying a
		// cached plan can start here; one still being mapped starts as soon as
		// OpenSource knows its blueprint, which is still inside this probe.
		p.warmPlaybackTail(sess)
		startupTimeout := p.startupTimeout()
		probeStart := time.Now()
		// Not WithTimeout: the probe's stream becomes the body stream below,
		// and a deadline that outlives the probe would kill it mid-serve. The
		// timer delivers the same budget as a DeadlineExceeded cause, which
		// ClassifyStartupErr reads back; success stops the timer and the
		// stream's Close retires the context.
		probeCtx, cancelProbe := context.WithCancelCause(ctx)
		timer := time.AfterFunc(startupTimeout, func() { cancelProbe(context.DeadlineExceeded) })
		spec, startupInfo, stream, err := p.probeKeepingStream(probeCtx, sess)
		timer.Stop()
		err = ClassifyStartupErr("probe", startupTimeout, probeCtx, err)
		probeMS = time.Since(probeStart).Milliseconds()
		if err != nil {
			cancelProbe(context.Canceled)
			return Prepared{}, err
		}
		preparedStream.Spec = spec
		preparedStream.StartupInfo = startupInfo
		preparedStream.HasStartupInfo = true
		preparedStream.Stream = &cancelOnCloseStream{
			ReadSeekCloser: stream,
			cancel:         func() { cancelProbe(context.Canceled) },
		}
	}

	if preparedStream.Spec.Key == "" {
		if preparedStream.Stream != nil {
			_ = preparedStream.Stream.Close()
		}
		return Prepared{}, fmt.Errorf("playback stream spec missing for session %s", sess.ID)
	}

	openMS := int64(0)
	if preparedStream.Stream == nil {
		openStart := time.Now()
		stream, err := p.openExpectedSourceWithStartupTimeout(ctx, sess, preparedStream.Spec, p.startupTimeout())
		if err != nil {
			sess.ResetPlaybackStream()
			return Prepared{}, err
		}
		preparedStream.Stream = stream
		openMS = time.Since(openStart).Milliseconds()
	}
	logger.Debug("Playback prepare timing",
		"session", sess.ID,
		"probed", needProbe,
		"reused_probe_stream", needProbe,
		"probe_ms", probeMS,
		"open_ms", openMS)

	preparedStream.Mode = "per_request"
	sess.CachePlaybackStreamSnapshot(preparedStream.Spec, preparedStream.StartupInfo, preparedStream.HasStartupInfo)
	return preparedStream, nil
}

// warmPlaybackTail prepares the end of the media on its own goroutine, bound to
// the session context so it survives the probe request that started it.
// Best-effort throughout: it only ever saves the player's first seek from
// paying what it would otherwise pay.
func (p *Service) warmPlaybackTail(sess *session.Session) bool {
	bp := sess.Blueprint()
	if bp == nil {
		// Nothing to warm against yet. The claim is deliberately not taken, so
		// the caller that does have a blueprint still gets its turn.
		return false
	}
	if !sess.Once(onceTailWarmed) {
		return false
	}
	files := sess.Files()
	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for i := range files {
		unpackFiles[i] = files[i]
	}
	sessCtx := sess.Context()
	if sessCtx == nil {
		sessCtx = context.Background()
	}
	timeout := p.startupTimeout()
	go func() {
		ctx, cancel := context.WithTimeout(sessCtx, timeout)
		defer cancel()
		started := time.Now()
		if unpack.WarmPlaybackTail(ctx, bp, unpackFiles) {
			logger.Debug("Warmed playback tail during startup",
				"session", sess.ID, "kind", bp.Kind(), "took_ms", time.Since(started).Milliseconds())
		}
	}()
	return true
}

func (p *Service) openExpectedSourceWithStartupTimeout(ctx context.Context, sess *session.Session, spec session.PlaybackStreamSpec, startupTimeout time.Duration) (io.ReadSeekCloser, error) {
	openCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan sourceOpenResult, 1)
	done := make(chan struct{})
	go func() {
		stream, err := p.openExpectedSource(openCtx, sess, spec)
		select {
		case resultCh <- sourceOpenResult{stream: stream, err: err}:
		case <-done:
			if stream != nil {
				_ = stream.Close()
			}
		}
	}()

	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()

	cleanup := func() {
		close(done)
		cancel()
		select {
		case res := <-resultCh:
			if res.stream != nil {
				_ = res.stream.Close()
			}
		default:
		}
	}

	select {
	case res := <-resultCh:
		close(done)
		if res.err != nil {
			cancel()
			return nil, res.err
		}
		return res.stream, nil
	case <-timer.C:
		cleanup()
		return nil, StartupTimeoutErr(startupTimeout, "open", nil)
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	}
}

func (p *Service) openExpectedSource(ctx context.Context, sess *session.Session, spec session.PlaybackStreamSpec) (io.ReadSeekCloser, error) {
	bodyStream, bodyName, bodySize, err := p.OpenSource(ctx, sess)
	if err != nil {
		return nil, err
	}
	if bodyName != spec.Name || bodySize != spec.Size {
		_ = bodyStream.Close()
		return nil, fmt.Errorf("playback stream changed during open: expected %q (%d), got %q (%d)", spec.Name, spec.Size, bodyName, bodySize)
	}
	return bodyStream, nil
}

// Probe validates the selected media and gathers startup metadata using a
// disposable probe reader so that small scans never disturb the session body
// stream.
func (p *Service) Probe(ctx context.Context, sess *session.Session) (session.PlaybackStreamSpec, seek.StreamStartInfo, error) {
	spec, info, stream, err := p.probeKeepingStream(ctx, sess)
	if stream != nil {
		_ = stream.Close()
	}
	return spec, info, err
}

// probeKeepingStream is Probe, except a successful probe hands its opened,
// rewound stream back instead of closing it. Prepare serves the first request
// straight off it — the probe already paid for the NZB, the STAT sweep, the
// archive mapping and the validation, and reopening the same source right after
// repeated the parts of that the caches cannot answer.
func (p *Service) probeKeepingStream(ctx context.Context, sess *session.Session) (session.PlaybackStreamSpec, seek.StreamStartInfo, io.ReadSeekCloser, error) {
	probeStream, probeName, probeSize, err := p.OpenSource(ctx, sess)
	if err != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, nil, err
	}

	startInfo, inspectErr := seek.InspectStreamStart(probeStream, probeSize, probeName, unpack.ProbeSize)
	if inspectErr != nil {
		_ = probeStream.Close()
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, nil, fmt.Errorf("probe inspect: %w", inspectErr)
	}
	if !startInfo.HeaderValid {
		if peek, err := readProbePrefix(probeStream, 16); err == nil && len(peek) > 0 {
			logger.Debug("Probe rejected container header",
				"session", sess.ID,
				"name", probeName,
				"size", probeSize,
				"prefix_hex", fmt.Sprintf("% x", peek),
				"encrypted_blueprint", blueprintAnyEncrypted(sess),
				"password_len", nzbPasswordLen(sess))
		}
		_ = probeStream.Close()
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, nil, fmt.Errorf("probe: invalid container header for %s", probeName)
	}

	return NewStreamSpec(sess.ID, probeName, probeSize), startInfo, probeStream, nil
}

// cancelOnCloseStream retires the probe-phase context when the reused stream
// is closed, so the context neither leaks nor outlives the request body.
type cancelOnCloseStream struct {
	io.ReadSeekCloser
	cancel func()
}

func (s *cancelOnCloseStream) Close() error {
	err := s.ReadSeekCloser.Close()
	s.cancel()
	return err
}

// NewStreamSpec creates the stable session/file key used to cache validated
// playback metadata and verify that fresh per-request readers still target
// the same file.
func NewStreamSpec(sessionID, name string, size int64) session.PlaybackStreamSpec {
	return session.PlaybackStreamSpec{
		Key:  fmt.Sprintf("%s|%s|%d", sessionID, name, size),
		Name: name,
		Size: size,
	}
}

// CacheReturnedBlueprint stores a blueprint returned from stream mapping onto
// the session so later opens skip the archive rescan.
func CacheReturnedBlueprint(sess *session.Session, bp unpack.Blueprint) {
	if sess == nil || bp == nil {
		return
	}
	sess.SetBlueprint(bp)
}

func readProbePrefix(stream io.ReadSeeker, n int) ([]byte, error) {
	if _, err := stream.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(stream, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && err != io.EOF {
		return buf[:got], err
	}
	if _, err := stream.Seek(0, io.SeekStart); err != nil {
		return buf[:got], err
	}
	return buf[:got], nil
}

func blueprintAnyEncrypted(sess *session.Session) bool {
	if bp, ok := sess.Blueprint().(*unpack.ArchiveBlueprint); ok {
		return bp.AnyEncrypted
	}
	return false
}

func nzbPasswordLen(sess *session.Session) int {
	if nzbData := sess.NZB(); nzbData != nil {
		return len(nzbData.Password())
	}
	return 0
}

// statSampleBudget bounds the pre-open STAT sampling. The check is a fail-fast
// optimization rather than a correctness gate, so it must not swallow the whole
// startup phase it runs inside: it gets at most this long, and at most half of
// whatever the caller's deadline has left, then reports what it has.
const statSampleBudget = 10 * time.Second

// statSampleCtx derives the sampling deadline from the caller's remaining
// budget. Taking the phase deadline verbatim made the sampling outlive the
// phase that owned it, so every probe was still in flight when the phase
// expired and the release took the blame for it.
func statSampleCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := statSampleBudget
	if deadline, ok := ctx.Deadline(); ok {
		if half := time.Until(deadline) / 2; half < budget {
			budget = half
		}
	}
	return context.WithTimeout(ctx, budget)
}

// VerifyRequiredArchivesExist STAT-samples the first segment of the archive
// volumes (decile sampling across the set) so playback fails fast on 430
// instead of stalling mid-stream.
//
// The three outcomes are kept distinct, because only one of them says anything
// about the release:
//
//	(true, nil)                          every sampled volume is present
//	(false, wrapped ErrFirstSegment…)    a volume is missing on every provider
//	(false, err)                         inconclusive — no verdict was reached
func VerifyRequiredArchivesExist(ctx context.Context, files []*loader.File) (bool, error) {
	if len(files) == 0 {
		return false, errors.New("no files in release")
	}
	// A gap in the NZB's own segment numbering is a verdict no STAT can
	// overturn: the missing articles were never indexed, so no provider can
	// serve them. Past the zero-fill budget such a release cannot stream, so it
	// fails here, before a Content-Length is promised. It used to serve a
	// truncated, byte-shifted file whose container header pointed players past
	// EOF — every reload answered 416 from the size alone, so nothing ever
	// marked the slot failed and the player looped instead of failing over.
	// Checked on every volume, not just the STAT-sampled ones, because it is
	// free and a sampled sweep can miss the gappy volume.
	for _, f := range files {
		if f == nil {
			continue
		}
		if missing := f.MissingFromNZB(); missing > loader.MaxZeroFills {
			return false, &errNZBIncomplete{msg: fmt.Sprintf(
				"archive volume %s is missing %d articles from the NZB itself", f.Name(), missing)}
		}
		// An NZB gap is one contiguous span, so a gap within the count cap can
		// still be a block of zeros no player survives: the same run cap the
		// loader applies at read time is applied here, before any promise.
		if run := f.MissingRunFromNZB(); run > loader.MaxZeroFillRun {
			return false, &errNZBIncomplete{msg: fmt.Sprintf(
				"archive volume %s is missing a run of %d consecutive articles from the NZB itself", f.Name(), run)}
		}
	}
	if len(files) == 1 {
		return files[0].CheckFirstSegmentExists(ctx)
	}

	n := len(files)
	sampleIndicesMap := make(map[int]bool)
	sampleIndicesMap[0] = true
	sampleIndicesMap[n-1] = true

	// Sample volumes evenly, scaling with the set: a fixed 11 points let a
	// many-volume release with damage clustered in unsampled volumes stream
	// until it broke mid-body. One in eight volumes, floored at the historical
	// 11 and capped to keep the pre-flight bounded; each sampled volume STATs
	// its own spread of segments (see CheckFirstSegmentExists).
	samples := n / 8
	if samples < 11 {
		samples = 11
	}
	if samples > 24 {
		samples = 24
	}
	if samples > n {
		samples = n
	}
	step := float64(n-1) / float64(samples-1)
	for i := 0; i < samples; i++ {
		idx := int(float64(i) * step)
		if idx >= 0 && idx < n {
			sampleIndicesMap[idx] = true
		}
	}

	var indices []int
	for idx := range sampleIndicesMap {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	ctx, cancel := statSampleCtx(ctx)
	defer cancel()

	type statResult struct {
		file *loader.File
		ok   bool
		err  error
	}

	ch := make(chan statResult, len(indices))
	for _, idx := range indices {
		f := files[idx]
		go func(targetFile *loader.File) {
			ok, err := targetFile.CheckFirstSegmentExists(ctx)
			ch <- statResult{file: targetFile, ok: ok, err: err}
		}(f)
	}

	// A missing article and a STAT that never got an answer both arrive as
	// ok=false; only the first proves anything about the release. Collapsing
	// the two is what turned provider blips and expired deadlines into
	// permanent bad verdicts, so they stay apart here: a proven-missing volume
	// wins immediately, and inconclusive probes are held back to surface only
	// when nothing was proven missing.
	var firstErr error
	for range indices {
		res := <-ch
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		if !res.ok {
			name := "unknown"
			if res.file != nil {
				name = res.file.Name()
			}
			return false, fmt.Errorf("archive volume %s segment unavailable: %w", name, ErrFirstSegmentUnavailable)
		}
	}
	if firstErr != nil {
		return false, firstErr
	}
	return true, nil
}

// OpenSource opens a fresh reader for the currently selected playback source.
// Callers decide whether that reader is used as a disposable probe stream or
// as the request-local body stream for a single /play response.
func (p *Service) OpenSource(ctx context.Context, sess *session.Session) (io.ReadSeekCloser, string, int64, error) {
	sessionID := sess.ID
	// Phase timings: opening a source is an NZB download, a STAT sweep over the
	// volumes, the archive mapping and a header validation, each of which can
	// own a second of a cold start on its own. Without them the whole stretch
	// between "loader files created" and "serving stream" is one unexplained
	// gap in the log.
	nzbStart := time.Now()
	if _, err := sess.GetOrDownloadNZBWithContext(ctx, p.Sessions); err != nil {
		logger.Error("Failed to lazy load NZB", "id", sessionID, "err", err)
		return nil, "", 0, err
	}
	nzbMS := time.Since(nzbStart).Milliseconds()
	statMS, mapMS, validateMS := int64(0), int64(0), int64(0)

	files := sess.Files()
	sessNZB := sess.NZB()
	if len(files) == 0 {
		if single := sess.File(); single != nil {
			files = []*loader.File{single}
		} else {
			logger.Error("No files in session", "id", sessionID)
			p.invalidateValidatorCache(sess)
			return nil, "", 0, fmt.Errorf("no files in session %s", sessionID)
		}
	}

	// STAT-sample the archive volumes before opening so a release with a hole
	// fails now instead of stalling mid-stream. Only a proven-missing article
	// stops the open: when the probes cannot reach a verdict in their budget we
	// continue and let the read surface a real 430, because a slow or flaky
	// provider is not evidence about the release.
	if len(files) > 0 {
		statStart := time.Now()
		exists, statErr := VerifyRequiredArchivesExist(ctx, files)
		statMS = time.Since(statStart).Milliseconds()
		switch {
		case errors.Is(statErr, ErrFirstSegmentUnavailable):
			logger.Debug("Stat archive volume segment missing", "id", sessionID, "err", statErr)
			return nil, "", 0, statErr
		case statErr != nil:
			logger.Debug("Stat archive volume sampling inconclusive, opening anyway", "id", sessionID, "err", statErr)
		case !exists:
			return nil, "", 0, fmt.Errorf("archive volume segment unavailable: %w", ErrFirstSegmentUnavailable)
		}
	}

	// Skip IsFailed() when the session is actively serving OR has already validated playback.
	// Stremio often cancels the initial probe request (dropping ActivePlays back to 0) immediately
	// before sending a follow-up range request. During that brief gap IsActivelyServing() is false,
	// but HasPreviouslyServed() tells us the file was already validated.
	// If the file is truly bad during streaming, onReadError will catch it.
	if !sess.IsActivelyServing() && !sess.HasPreviouslyServed() {
		for _, f := range files {
			if f.IsFailed() {
				logger.Error("Session file has too many failures", "session", sessionID, "file", f.Name())
				p.invalidateValidatorCache(sess)
				return nil, "", 0, fmt.Errorf("file %s exceeded failure threshold", f.Name())
			}
		}
	}

	password := ""
	if sessNZB != nil {
		password = sessNZB.Password()
	}
	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for i := range files {
		unpackFiles[i] = files[i]
	}
	target := unpack.EpisodeTarget{}
	if sess.ContentIDs != nil {
		target = unpack.EpisodeTarget{Season: sess.ContentIDs.Season, Episode: sess.ContentIDs.Episode, Absolute: sess.ContentIDs.AbsoluteEpisode}
	}
	hints := unpack.StreamSelectionHints{
		AllowLargestDirectFallback: p.allowLargestDirectFallback(sess),
	}
	// Serve path: content probes inside the scan run bounded (quick), and with
	// the operator's configured ffprobe binary rather than the default lookup.
	ctx = unpack.WithProbeConfig(ctx, p.ffprobePath(), true)
	mapStart := time.Now()
	stream, name, size, bp, err := unpack.GetMediaStreamForEpisodeWithHints(ctx, unpackFiles, sess.Blueprint(), password, target, hints)
	mapMS = time.Since(mapStart).Milliseconds()
	CacheReturnedBlueprint(sess, bp)
	if err != nil {
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)
		p.invalidateValidatorCache(sess)
		return nil, "", 0, err
	}
	// The plan exists now, and the ffprobe validation below spends seconds at
	// the head of the file. That is the window the tail warm wants.
	p.warmPlaybackTail(sess)
	// Persist NZB + blueprint immediately (status pending) so the mapping work is
	// kept even when playback later fails or is abandoned; the good/bad verdict
	// is applied once known. Library-sourced sessions already have their entry.
	// Once per session — every HTTP range request runs Prepare, and each save
	// gzips the full NZB, so an unguarded save here would re-upsert on every
	// seek/reconnect. The server owns the once-guard.
	if rel := sess.Release(); rel != nil && !rel.IsLibraryResult() && p.NotePendingLibrarySave != nil {
		p.NotePendingLibrarySave(sess, bp, name, size)
	}
	if !sess.IsPlaybackValidated() {
		// QuickHeader: this validation sits on time-to-first-byte, and the
		// full-width probe pulled up to 50 MB through the segment pool before
		// the first byte could be served — most of our cold-open latency in
		// the field. The bounded window still catches audio-only and garbage;
		// what it cannot settle degrades to the permissive header heuristic.
		validateStart := time.Now()
		if _, err := unpack.ValidateMediaStreamWithOptions(ctx, stream, name, p.ffprobePath(), unpack.ValidateOptions{QuickHeader: true}); err != nil {
			logger.Warn("Media stream container track validation failed", "id", sessionID, "name", name, "err", err)
			stream.Close()
			return nil, "", 0, fmt.Errorf("container track validation failed: %w", err)
		}
		validateMS = time.Since(validateStart).Milliseconds()
		p.Sessions.MarkPlaybackValidated(sessionID)
	} else {
		logger.Debug("Skipping redundant FFprobe validation (session already validated)", "id", sessionID)
	}
	sess.SetSelectedPlaybackFile(name)
	logger.Debug("Playback open source timing",
		"session", sessionID,
		"nzb_ms", nzbMS,
		"volume_stat_ms", statMS,
		"stream_map_ms", mapMS,
		"validate_ms", validateMS,
		"files", len(files))
	return stream, name, size, nil
}

// Preload speculatively downloads, maps and strictly validates a session's
// media before any client asks for it: NZB fetch, volume STAT sampling, an
// article check of the selected file (sampled, or a full census when opted
// in), and a forced-decode ffprobe run.
// Users see this as "preloading" the top search results.
func (p *Service) Preload(ctx context.Context, sess *session.Session) (bool, error) {
	if sess == nil {
		return false, errors.New("nil session")
	}

	if sess.IsWarm() {
		return true, nil
	}

	releaseTitle := sess.ReportReleaseName()
	logger.Debug("Preloading starting for session", "slot", sess.ID, "title", releaseTitle)

	// 1. Fetch the NZB and construct loader files
	if _, err := sess.GetOrDownloadNZBWithContext(ctx, p.Sessions); err != nil {
		return false, fmt.Errorf("download NZB: %w", err)
	}

	// 2. Resolve the obfuscated names and verify archive volume availability
	files := sess.Files()
	if len(files) > 0 {
		exists, statErr := VerifyRequiredArchivesExist(ctx, files)
		switch {
		case errors.Is(statErr, ErrFirstSegmentUnavailable):
			return false, fmt.Errorf("stat check: %w", statErr)
		case statErr != nil:
			// The pre-probe still fails — it cannot vouch for this release —
			// but the wording carries no bad-release verdict for the caller's
			// classifier to act on.
			return false, fmt.Errorf("stat check inconclusive: %w", statErr)
		case !exists:
			return false, errors.New("archive volume segment unavailable")
		}
	}

	// 3. Trigger direct media stream hints parsing (name overrides)
	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for idx := range files {
		unpackFiles[idx] = files[idx]
	}
	password := ""
	if sessNZB := sess.NZB(); sessNZB != nil {
		password = sessNZB.Password()
	}
	target := unpack.EpisodeTarget{}
	if sess.ContentIDs != nil {
		target = unpack.EpisodeTarget{Season: sess.ContentIDs.Season, Episode: sess.ContentIDs.Episode, Absolute: sess.ContentIDs.AbsoluteEpisode}
	}
	hints := unpack.StreamSelectionHints{
		AllowLargestDirectFallback: p.allowLargestDirectFallback(sess),
	}

	// Pre-probe runs off the serve path, so its content probes keep the
	// thorough window; the configured binary still applies.
	ctx = unpack.WithProbeConfig(ctx, p.ffprobePath(), false)
	streamReader, name, size, bp, err := unpack.GetMediaStreamForEpisodeWithHints(ctx, unpackFiles, sess.Blueprint(), password, target, hints)
	if err != nil {
		return false, fmt.Errorf("media stream mapping: %w", err)
	}

	// Store the NZB + blueprint immediately (status pending) so the expensive
	// download/mapping work survives even if validation below is interrupted;
	// the verdict is applied once known.
	if p.SaveToLibrary != nil {
		p.SaveToLibrary(sess, bp, name, size, LibraryStatusPending)
	}

	// Article check of the SELECTED file, before the release is ever offered
	// and before the forced decode downloads anything. Sampled by default —
	// every startup-window article plus spread samples across the volumes —
	// or, when the operator opted in, a census that asks about every article
	// in an order that keeps any prefix a uniform sample of the file.
	if p.preloadArticleCensus() {
		censusCtx, cancelCensus := censusPreloadCtx(ctx)
		err := VerifySelectedFileArticles(censusCtx, bp)
		cancelCensus()
		if err != nil {
			logger.Warn("Preloading rejected release via article census", "slot", sess.ID, "file", name, "err", err)
			if streamReader != nil {
				streamReader.Close()
			}
			return false, fmt.Errorf("preloading rejected stream: %w", err)
		}
	} else if err := VerifySelectedFileArticlesDense(ctx, bp); err != nil {
		logger.Warn("Preloading rejected release via dense article STAT", "slot", sess.ID, "file", name, "err", err)
		if streamReader != nil {
			streamReader.Close()
		}
		return false, fmt.Errorf("preloading rejected stream: %w", err)
	}

	logger.Info("Preloading executing FFprobe validation", "slot", sess.ID, "file", name)
	probeRes, err := unpack.ValidateMediaStreamWithOptions(ctx, streamReader, name, p.ffprobePath(), unpack.ValidateOptions{
		ForceDecode:   true,
		StrictFFprobe: true,
	})
	if err != nil {
		logger.Warn("Preloading rejected audio-only or unplayable stream via FFprobe", "slot", sess.ID, "name", name, "err", err)
		if streamReader != nil {
			streamReader.Close()
		}
		return false, fmt.Errorf("preloading rejected stream: %w", err)
	}
	if probeRes != nil {
		sess.SetMediaCapabilities(MediaCapabilitiesFromProbe(probeRes))
	}

	CacheReturnedBlueprint(sess, bp)
	sess.SetSelectedPlaybackFile(name)
	if p.SaveToLibrary != nil {
		p.SaveToLibrary(sess, bp, name, size, LibraryStatusGood)
	}
	if streamReader != nil {
		streamReader.Close()
	}

	logger.Info("Preloading successfully completed", "slot", sess.ID, "title", releaseTitle, "file", name, "size", size)
	return true, nil
}

// MediaCapabilitiesFromProbe converts an ffprobe result into the
// session-level capability record surfaced to clients.
func MediaCapabilitiesFromProbe(res *ffprobe.FFprobeResult) *session.MediaCapabilities {
	if res == nil {
		return nil
	}
	return &session.MediaCapabilities{
		VideoCodec:      res.VideoCodec,
		AudioCodec:      res.AudioCodec,
		Width:           res.Width,
		Height:          res.Height,
		Profile:         res.Profile,
		PixFmt:          res.PixFmt,
		BitDepth:        res.BitDepth,
		HDR:             res.HDR,
		DolbyVision:     res.DolbyVision,
		ColorTransfer:   res.ColorTransfer,
		CodecTag:        res.CodecTag,
		DurationSeconds: res.DurationSeconds,

		TracksProbed:      true,
		AudioLanguages:    res.AudioLanguages,
		SubtitleLanguages: res.SubtitleLanguages,
		AudioStreams:      res.AudioStreams,
		SubtitleStreams:   res.SubtitleStreams,
	}
}
