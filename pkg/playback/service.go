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

// ErrPlaybackStartupTimeout is the sentinel wrapped into every startup-budget
// failure; callers match it with errors.Is to distinguish timeouts from
// cancellations and real media errors.
var ErrPlaybackStartupTimeout = errors.New("playback startup timeout")

// CacheInvalidator is the slice of the validation checker this package needs.
type CacheInvalidator interface {
	InvalidateCache(hash string)
}

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
func ClassifyStartupErr(phase string, startupTimeout time.Duration, startupCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(startupCtx.Err(), context.DeadlineExceeded) {
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
	if needProbe {
		startupTimeout := p.startupTimeout()
		probeCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		spec, startupInfo, err := p.Probe(probeCtx, sess)
		err = ClassifyStartupErr("probe", startupTimeout, probeCtx, err)
		cancel()
		if err != nil {
			return Prepared{}, err
		}
		preparedStream.Spec = spec
		preparedStream.StartupInfo = startupInfo
		preparedStream.HasStartupInfo = true
	}

	if preparedStream.Spec.Key == "" {
		return Prepared{}, fmt.Errorf("playback stream spec missing for session %s", sess.ID)
	}

	stream, err := p.openExpectedSourceWithStartupTimeout(ctx, sess, preparedStream.Spec, p.startupTimeout())
	if err != nil {
		sess.ResetPlaybackStream()
		return Prepared{}, err
	}

	preparedStream.Stream = stream
	preparedStream.Mode = "per_request"
	sess.CachePlaybackStreamSnapshot(preparedStream.Spec, preparedStream.StartupInfo, preparedStream.HasStartupInfo)
	return preparedStream, nil
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
	probeStream, probeName, probeSize, err := p.OpenSource(ctx, sess)
	if err != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, err
	}
	defer probeStream.Close()

	startInfo, inspectErr := seek.InspectStreamStart(probeStream, probeSize, probeName, unpack.ProbeSize)
	if inspectErr != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, fmt.Errorf("probe inspect: %w", inspectErr)
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
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, fmt.Errorf("probe: invalid container header for %s", probeName)
	}

	return NewStreamSpec(sess.ID, probeName, probeSize), startInfo, nil
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
	if len(files) == 1 {
		return files[0].CheckFirstSegmentExists(ctx)
	}

	n := len(files)
	sampleIndicesMap := make(map[int]bool)
	sampleIndicesMap[0] = true
	sampleIndicesMap[n-1] = true

	// Sample up to 11 points across the RAR volume list (decile sampling)
	step := float64(n-1) / 10.0
	for i := 0; i <= 10; i++ {
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
	if _, err := sess.GetOrDownloadNZBWithContext(ctx, p.Sessions); err != nil {
		logger.Error("Failed to lazy load NZB", "id", sessionID, "err", err)
		return nil, "", 0, err
	}

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
		exists, statErr := VerifyRequiredArchivesExist(ctx, files)
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
	stream, name, size, bp, err := unpack.GetMediaStreamForEpisodeWithHints(ctx, unpackFiles, sess.Blueprint(), password, target, hints)
	CacheReturnedBlueprint(sess, bp)
	if err != nil {
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)
		p.invalidateValidatorCache(sess)
		return nil, "", 0, err
	}
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
		if err := unpack.ValidateMediaStreamHasVideoWithFFprobe(stream, name, p.ffprobePath()); err != nil {
			logger.Warn("Media stream container track validation failed", "id", sessionID, "name", name, "err", err)
			stream.Close()
			return nil, "", 0, fmt.Errorf("container track validation failed: %w", err)
		}
		p.Sessions.MarkPlaybackValidated(sessionID)
	} else {
		logger.Debug("Skipping redundant FFprobe validation (session already validated)", "id", sessionID)
	}
	sess.SetSelectedPlaybackFile(name)
	return stream, name, size, nil
}

// PreProbe speculatively downloads, maps and strictly validates a session's
// media before any client asks for it: NZB fetch, volume STAT sampling, dense
// article verification of the selected file, and a forced-decode ffprobe run.
func (p *Service) PreProbe(ctx context.Context, sess *session.Session) (bool, error) {
	if sess == nil {
		return false, errors.New("nil session")
	}

	if sess.IsWarm() {
		return true, nil
	}

	releaseTitle := sess.ReportReleaseName()
	logger.Debug("Speculative pre-probing starting for session", "slot", sess.ID, "title", releaseTitle)

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

	// Dense article check of the SELECTED file: every startup-window article plus
	// spread samples across all its volumes. Catches holes past the sparse
	// first-segment sampling (e.g. 121MB into an 8.7GB release) before the
	// release is ever offered, and before the forced decode downloads anything.
	if err := VerifySelectedFileArticlesDense(ctx, bp); err != nil {
		logger.Warn("Speculative pre-probing rejected release via dense article STAT", "slot", sess.ID, "file", name, "err", err)
		if streamReader != nil {
			streamReader.Close()
		}
		return false, fmt.Errorf("speculative pre-probing rejected stream: %w", err)
	}

	logger.Info("Speculative pre-probing executing FFprobe validation", "slot", sess.ID, "file", name)
	probeRes, err := unpack.ValidateMediaStreamWithOptions(ctx, streamReader, name, p.ffprobePath(), unpack.ValidateOptions{
		ForceDecode:   true,
		StrictFFprobe: true,
	})
	if err != nil {
		logger.Warn("Speculative pre-probing rejected audio-only or unplayable stream via FFprobe", "slot", sess.ID, "name", name, "err", err)
		if streamReader != nil {
			streamReader.Close()
		}
		return false, fmt.Errorf("speculative pre-probing rejected stream: %w", err)
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

	logger.Info("Speculative pre-probing successfully completed", "slot", sess.ID, "title", releaseTitle, "file", name, "size", size)
	return true, nil
}

// MediaCapabilitiesFromProbe converts an ffprobe result into the
// session-level capability record surfaced to clients.
func MediaCapabilitiesFromProbe(res *ffprobe.FFprobeResult) *session.MediaCapabilities {
	if res == nil {
		return nil
	}
	return &session.MediaCapabilities{
		VideoCodec:    res.VideoCodec,
		AudioCodec:    res.AudioCodec,
		Width:         res.Width,
		Height:        res.Height,
		Profile:       res.Profile,
		PixFmt:        res.PixFmt,
		BitDepth:      res.BitDepth,
		HDR:           res.HDR,
		DolbyVision:   res.DolbyVision,
		ColorTransfer: res.ColorTransfer,
		CodecTag:      res.CodecTag,
	}
}
