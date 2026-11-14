package stremio

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/httpx"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/metrics"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

// servePlaybackStream writes an already-resolved stream to the client and
// records what the attempt turned out to be.
//
// By the time this runs the slot is settled: resolvePlaybackSlot redirected
// rather than hand over a session other than the one the client asked for. That
// is why nothing here reconsiders which release to serve — the only failover
// left is the mid-stream kind, which marks the slot and lets the player's
// reconnect pick it up.
func (s *Server) servePlaybackStream(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream, resolved *resolvedPlayback, playStart time.Time) {
	sess := resolved.session
	sessionID := resolved.sessionID
	stream := resolved.stream

	requestedRange := r.Header.Get("Range")
	userAgent := r.Header.Get("User-Agent")

	var closeReason atomic.Value
	var closeStreamOnce sync.Once
	closeStream := func(reason string) {
		closeStreamOnce.Do(func() {
			closeReason.Store(reason)
			if stream != nil {
				logger.Debug("play handler closing stream", "session", sessionID, "reason", reason)
				stream.Close()
				// stream is deliberately left set, not nilled: it is read
				// again below to build the StreamMonitor, from this same
				// closeStreamOnce-guarded callback, which can run
				// concurrently with that read (this codebase notes
				// elsewhere that "Stremio often cancels the initial probe
				// request... immediately before sending a follow-up range
				// request" — i.e. concurrent cancellation here is routine,
				// not an edge case). Nilling it raced that read: an
				// interface value is two words, and reading one while
				// another goroutine writes it is undefined behavior with no
				// guaranteed outcome — in the field it surfaced as
				// StreamMonitor.Seek panicking on a nil interface the
				// instant net/http.ServeContent called it, killing the
				// connection outright. Handing the monitor the same
				// already-closed stream instead is exactly what
				// primeRangeStart(resolved.stream, ...) below already does
				// with the equivalent struct field, and StreamMonitor's own
				// nil guard remains as a backstop for any other way a nil
				// stream could reach it.
			}
		})
	}
	go func(done <-chan struct{}) {
		<-done
		closeStream("playback canceled")
	}(resolved.ctx.Done())

	clientIP := httpx.ClientIP(r)
	// A client that reports its own playback is scrobbled from those reports.
	var rs *reportingServe
	if opts := playServeOptionsFromContext(r.Context()); opts.ClientReportsPlayback {
		rs = &reportingServe{device: opts.ClientDevice, deviceFromURL: opts.ClientDeviceFromURL, address: clientIP, began: playStart}
	}
	s.sessionManager.MarkPlaybackValidated(sess)
	s.sessionManager.StartPlayback(sess, clientIP)
	// Playing now: the play itself holds the session from here on.
	resolved.startup.Release()
	// A shutdown drain waits for the stop this serve may decide as it ends.
	s.scrobbleSends.Add(1)
	defer s.scrobbleSends.Done()
	// Registered before the EndPlayback defer so it runs after it (LIFO) —
	// the stop must see the decremented play count and the folded progress.
	defer s.scrobbleStopIfIdle(streamConfig, sess, rs)
	var endPlaybackOnce sync.Once
	endPlayback := func() { s.sessionManager.EndPlayback(sess, clientIP) }
	defer endPlaybackOnce.Do(endPlayback)
	// When the client cancels (e.g. stop in Stremio) the request context is
	// cancelled; end playback so downloading stops and the session can be evicted.
	go func() {
		<-r.Context().Done()
		endPlaybackOnce.Do(endPlayback)
	}()

	// serveFailureRecorded is set when onReadError records a failure for the
	// session being served. The success defer checks it so it never overwrites a
	// Failure with an OK (the "flip-flop" bug).
	serveFailureRecorded := false
	// Every caller reports about this serve's own session; the id argument is
	// part of StreamMonitor's callback signature.
	onReadError := func(_ string, readErr error) {
		if s.failServedCopy(streamConfig, sess, readErr) {
			serveFailureRecorded = true
		}
	}

	effectiveRange := r.Header.Get("Range")
	if !s.primeRangeOrFailover(w, r, streamConfig, resolved, effectiveRange, onReadError, closeStream) {
		return
	}
	// Past the ranges refused above, before any media goes out: a report
	// while it streams is about this serve. A HEAD streams none.
	if rs != nil && r.Method != http.MethodHead && streamConfig.ScrobbleEnabled() {
		s.clientPlayback.noteServed(streamConfig, sess)
		s.clientPlayback.register(streamConfig, sess.ContentID, rs)
	}

	monitoredStream := &StreamMonitor{
		ReadSeekCloser: stream,
		session:        sess,
		sessionID:      sessionID,
		clientIP:       clientIP,
		manager:        s.sessionManager,
		onReadError:    onReadError,
		lastUpdate:     time.Now(),
	}

	bufW := newMediaResponseWriter(w, resolved.name)
	var ttffOnce sync.Once
	bufW.onFirstWrite = func() {
		ttffOnce.Do(func() {
			s.recordTTFF(sess, sessionID, resolved.mode, playStart)
			// The client is playing; now is the time to spend connections on
			// the release it would fail over to, not before.
			if sess.Once(onceFallbackPrefetched) {
				s.prefetchNextFallbackAfterStart(sessionID, streamConfig)
			}
		})
	}

	serveStartedAt := time.Now()
	monitoredStream.onProgress = func() {
		s.commitGoodAttemptIfQualified(streamConfig, sess, sessionID, serveStartedAt, rs)
	}
	monitoredStream.onServeWindow = serveWindowLogger(sessionID, monitoredStream, bufW, serveStartedAt)

	logger.Debug("play handler serving stream", "session", sessionID, "name", resolved.name, "size", resolved.size)
	logger.Debug("Serving media",
		"session", sessionID,
		"name", resolved.name,
		"size", resolved.size,
		"method", r.Method,
		"requested_range", requestedRange,
		"effective_range", effectiveRange,
		"user_agent", userAgent,
		"client_ip", clientIP,
		"stream_mode", resolved.mode,
	)

	sess.BeginServeProviderTracking()
	defer func() {
		sess.EndServeProviderTracking()
		closeStream("handler exit")
		bufW.Flush()

		responseStats := bufW.Snapshot()
		streamStats := monitoredStream.Snapshot()
		closeReasonText := ""
		if v := closeReason.Load(); v != nil {
			closeReasonText = v.(string)
		}
		probeLikeServe, probeLikeServeReason := classifyProbeLikeServe(r, resolved.size, effectiveRange, responseStats, streamStats, closeReasonText)
		if rs != nil {
			// A refused range writes an error body but reads no media.
			rs.media = !probeLikeServe && streamStats.BytesRead > 0
		}
		if !probeLikeServe {
			// Watched-progress signal for scrobbling: how far into the file
			// this serve proves playback reached. Probe-like serves are
			// excluded — a player sampling the tail for the moov atom would
			// otherwise read as "watched to the end" — and what is left is
			// bounded by the time the serve was open, because a buffering
			// client transfers bytes far faster than it plays them.
			rangeStart, _ := parseRangeStart(effectiveRange)
			sess.NoteServedWindow(session.ServeWindow{
				StartOffset: rangeStart,
				MaxOffset:   streamStats.MaxPos,
				TotalSize:   resolved.size,
				Elapsed:     time.Since(serveStartedAt),
			})
		}

		logger.Debug("Finished serving media",
			"session", sessionID,
			"method", r.Method,
			"requested_range", requestedRange,
			"effective_range", effectiveRange,
			"user_agent", userAgent,
			"response_status", responseStats.StatusCode,
			"response_wrote_header", responseStats.WroteHeader,
			"response_content_range", responseStats.ContentRange,
			"response_content_length", responseStats.ContentLength,
			"response_content_type", responseStats.ContentType,
			"response_accept_ranges", responseStats.AcceptRanges,
			"response_bytes", responseStats.BytesWritten,
			"response_writes", responseStats.WriteCalls,
			"response_flushes", responseStats.FlushCalls,
			"response_flush_error", responseStats.FlushError,
			"stream_bytes", streamStats.BytesRead,
			"stream_reads", streamStats.ReadCalls,
			"stream_eof", streamStats.SawEOF,
			"stream_error", streamStats.LastReadError,
			"stream_read_blocked", streamStats.ReadBlocked.Round(time.Millisecond),
			"response_write_blocked", responseStats.WriteBlocked.Round(time.Millisecond),
			"request_context_err", errorString(r.Context().Err()),
			"serve_context_err", errorString(resolved.ctx.Err()),
			"stream_mode", resolved.mode,
			"probe_like", probeLikeServe,
			"probe_reason", probeLikeServeReason,
			"serve_failure_recorded", serveFailureRecorded,
			"close_reason", closeReasonText,
			"duration", time.Since(serveStartedAt),
		)
	}()

	defer func() {
		s.finishServeBookkeeping(r, streamConfig, resolved, bufW, monitoredStream, effectiveRange, serveStartedAt, &serveFailureRecorded, rs)
	}()

	http.ServeContent(bufW, r, resolved.name, time.Time{}, monitoredStream)
}

// failServedCopy acts on a read error from the stream being served and reports
// whether it recorded a failure.
//
// It triggers slot failover for any permanent mid-stream error:
//   - a missing article the loader could not treat as a fillable hole (the
//     file's first segment, or a hole past MaxZeroFills)
//   - yEnc decode failure (data corruption)
//   - ErrTooManyZeroFills (the file accumulated more holes than allowed)
//
// All of them mean the slot is unrecoverable; isolated missing articles do not
// reach here, because the loader zero-fills those and playback continues.
// SetSlotFailedDuringPlayback marks the slot so resolvePlaybackSlot sends the
// player to the next one on reconnect, without the user having to switch
// manually in Stremio.
//
// The verdict is about served — the session this stream belongs to — and
// nothing else. A read still in flight on a copy the slot has already left
// (another request retired it, and the player may be on the next copy) is
// stale: it is ignored rather than allowed to fail the slot or to blame the
// copy that replaced it.
func (s *Server) failServedCopy(streamConfig *auth.Stream, served *session.Session, readErr error) bool {
	if !isFatalStreamErr(readErr) {
		return false
	}
	// The failure is claimed before the copy can leave the slot, and so before
	// any AvailNZB call: from here no sibling serve can claim success on it.
	// Whichever outcome follows, a won claim is recorded — on this session's
	// own row, so never on a replacement's.
	claimed := served.OnceUnless(onceFailureRecorded, onceSuccessRecorded)
	report := func(nextCopy bool) {
		served.ResetPlaybackStream()
		availOutcome := s.reportBadReleaseOutcome(served, readErr, true, nextCopy)
		if claimed {
			s.recordFailureAttempt(served, readErr, availOutcome)
		}
	}
	recordUnreported := func() {
		if claimed {
			s.recordFailureAttempt(served, readErr, availnzb.ReportOutcome{})
		}
	}
	// Mid-stream the slot can still have an untried copy of the same
	// release. Moving the cursor on instead of marking the slot failed
	// keeps the player on the release it was playing: the session goes, so
	// the reconnect rebuilds this slot against the next NZB rather than
	// being redirected to a different release.
	attempts := 1
	if streamFailoverEnabled(streamConfig) {
		attempts = streamConfig.EffectiveVariantAttempts()
	}
	switch s.sessionManager.RetireSlotCopy(served, attempts, false, func() { report(true) }) {
	case session.SlotCopyRetired:
		logger.Info("Mid-stream failure; the slot moves to another copy of the same release",
			"slot", served.ID, "err", readErr)
		return true
	case session.SlotCopyAlreadyRetired:
		// Another request on this session already recorded the failure and
		// moved the slot on. This serve failed too, which keeps its
		// bookkeeping from counting the condemned copy as good.
		logger.Debug("Read error from a copy the slot has already left", "slot", served.ID, "err", readErr)
		// The retiring request's report may not have claimed yet; this claim
		// then took its place.
		recordUnreported()
		return true
	case session.SlotCopyGone:
		// The session left the slot without a copy verdict (evicted, or the
		// slot was failed): keep the player from coming back to it — unless a
		// replacement now holds the slot, which this stale read says nothing
		// about. The serve itself still failed, so its bookkeeping must not
		// count it good.
		if !s.sessionManager.FailSlotUnlessReplaced(served) {
			logger.Debug("Read error from a session the slot has replaced", "slot", served.ID, "err", readErr)
		}
		recordUnreported()
		return true
	}
	s.sessionManager.SetSlotFailedDuringPlayback(served.ID)
	report(false)
	return true
}

// primeRangeOrFailover proves the requested range can actually be delivered
// before any of it is promised, and reports whether serving may continue.
//
// Everything after this belongs to ServeContent, which commits the status line
// and Content-Length before its first read — so a failure past that point can
// only truncate a response the client already believes. HEAD carries no body, so
// there is nothing to promise and nothing to prove.
func (s *Server) primeRangeOrFailover(
	w http.ResponseWriter,
	r *http.Request,
	streamConfig *auth.Stream,
	resolved *resolvedPlayback,
	effectiveRange string,
	onReadError func(string, error),
	closeStream func(string),
) bool {
	rt := s.runtime()
	if r.Method == http.MethodHead {
		return true
	}

	if s.failSlotOnRepeatedPastEOFRanges(w, r, streamConfig, resolved, effectiveRange, closeStream) {
		return false
	}

	primeErr := primeRangeStart(resolved.stream, effectiveRange, resolved.size)
	switch {
	case isFatalStreamErr(primeErr):
		logger.Warn("Refusing to advertise a range this release cannot deliver",
			"session", resolved.sessionID, "range", effectiveRange, "size", resolved.size, "err", primeErr)
		onReadError(resolved.sessionID, primeErr)
		closeStream("range unavailable")
		if streamFailoverEnabled(streamConfig) {
			s.redirectToNextSlotOrFail(w, r, resolved.sessionID, streamConfig,
				"Redirecting to next fallback (requested range unavailable)")
		} else {
			failPlayback(w, r, resolved.session, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config), primeErr)
		}
		return false
	case primeErr != nil:
		// Timeouts, cancellations and other inconclusive failures are not a
		// verdict about the release; serve and let the read surface a real one.
		logger.Debug("Range prime read inconclusive, serving anyway",
			"session", resolved.sessionID, "range", effectiveRange, "err", primeErr)
	}
	return true
}

// maxPastEOFRangesBeforeFailover is how many GET requests may start at or
// beyond the served size before the pattern reads as a player looping rather
// than a one-off quirk. A stale cached size after a failover can produce a
// stray request or two; a player stuck on a size mismatch produces them every
// few hundred milliseconds, forever.
const maxPastEOFRangesBeforeFailover = 3

// failSlotOnRepeatedPastEOFRanges turns repeated ranges past the served size
// into a failover, reporting whether it answered the request.
//
// A virtual file smaller than what its own container header declares — a
// wrongly estimated segment map, or any truncation the pre-flights missed —
// sends the player to a tail offset this slot cannot have. ServeContent
// answers that from the size alone with a 416: the stream is never read, so
// onReadError cannot fire, primeRangeStart deliberately has no verdict for a
// past-EOF start, and every serve ends below the good threshold, which is
// neither success nor failure. Nothing marks the slot, the player retries the
// same URL, and the loop runs until the user gives up.
//
// Three strikes on a session that has never committed a good play is taken as
// that loop: the slot is marked failed so resolvePlaybackSlot and the
// next-release cursor step over it, and this request is redirected the way an
// undeliverable range already is. A committed slot is never failed here — it
// is delivering real bytes, so a stray stale-metadata request must not kill
// it. No release verdict is recorded either: the articles may all exist and
// merely be mapped wrong, so AvailNZB and History stay out of it.
func (s *Server) failSlotOnRepeatedPastEOFRanges(
	w http.ResponseWriter,
	r *http.Request,
	streamConfig *auth.Stream,
	resolved *resolvedPlayback,
	effectiveRange string,
	closeStream func(string),
) bool {
	if resolved.size <= 0 {
		return false
	}
	start, ok := parseRangeStart(effectiveRange)
	if !ok || start < resolved.size {
		return false
	}
	sess := resolved.session
	if sess.OnceDone(onceSuccessRecorded) {
		return false
	}
	count := sess.CountEvent(countPastEOFRanges)
	if count < maxPastEOFRangesBeforeFailover {
		logger.Debug("Range starts past the served size",
			"session", resolved.sessionID, "range", effectiveRange, "size", resolved.size, "count", count)
		return false
	}
	logger.Warn("Player keeps requesting ranges past the served size; failing the slot over",
		"session", resolved.sessionID, "range", effectiveRange, "size", resolved.size, "count", count)
	s.sessionManager.SetSlotFailedDuringPlayback(resolved.sessionID)
	closeStream("range past served size")
	if streamFailoverEnabled(streamConfig) {
		s.redirectToNextSlotOrFail(w, r, resolved.sessionID, streamConfig,
			"Redirecting to next fallback (player loops past served size)")
	} else {
		rt := s.runtime()
		failPlayback(w, r, sess, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config),
			fmt.Errorf("release serves %d bytes but the player keeps asking for offset %d", resolved.size, start))
	}
	return true
}

// recordTTFF records time-to-first-frame once the first byte reaches the client.
//
// ProviderName is the Usenet provider that served those bytes, resolved the same
// way as everywhere else. It used to hold the *indexer* instead, because the
// variable feeding it was declared and never assigned, so the fallback was the
// only branch that ever ran — which put an indexer name in a field sitting next
// to NNTPConnectDuration, pointing anyone reading a slow start-up at the wrong
// subsystem. Empty when nothing has served yet, which is the honest answer.
func (s *Server) recordTTFF(sess *session.Session, sessionID, streamMode string, playStart time.Time) {
	name := providerNameFromHosts(providerHostsForOutcome(sess, true))
	ttff := time.Since(playStart)
	metrics.Default().RecordPlaybackTTFF(metrics.PlaybackTTFFSample{
		Timestamp:    time.Now(),
		SessionID:    sessionID,
		ProviderName: name,
		TTFF:         ttff,
		IsCacheHit:   streamMode != "",
	})
	if s.attemptRecorder != nil && sess != nil {
		p := s.recordAttemptParamsForOutcome(sess, true)
		p.ID, p.Gen = s.attemptRow(sess)
		p.TTFFMS = ttff.Milliseconds()
		s.attemptRecorder.UpdatePendingAttempt(p)
	}
	logger.Debug("TTFF recorded", "session", sessionID, "ttff_ms", ttff.Milliseconds(), "provider", name)
}

// serveWindowLogger returns the per-window telemetry callback: it splits each
// ~10s window into time blocked reading (usenet side) versus writing (client
// side), so a buffering report can be attributed to the right leg of the
// pipeline. It is called only from the serving goroutine, so plain closure state
// is safe.
func serveWindowLogger(sessionID string, monitored *StreamMonitor, bufW *bufferedResponseWriter, start time.Time) func() {
	windowStart := start
	var windowBytes int64
	var windowReadBlocked, windowWriteBlocked time.Duration

	return func() {
		now := time.Now()
		streamStats := monitored.Snapshot()
		writeBlocked := time.Duration(bufW.writeBlocked.Load())
		logger.Debug("Serve window",
			"session", sessionID,
			"window", now.Sub(windowStart).Round(time.Millisecond),
			"bytes", streamStats.BytesRead-windowBytes,
			"read_blocked", (streamStats.ReadBlocked - windowReadBlocked).Round(time.Millisecond),
			"write_blocked", (writeBlocked - windowWriteBlocked).Round(time.Millisecond),
			"served_total", streamStats.BytesRead,
		)
		windowStart = now
		windowBytes = streamStats.BytesRead
		windowReadBlocked = streamStats.ReadBlocked
		windowWriteBlocked = writeBlocked
	}
}

// finishServeBookkeeping decides what the finished attempt was and records it.
//
// Reporting good only happens here, after serving, because the bytes-read
// threshold cannot be met before then. It runs at most once per session — several
// HTTP requests (a seek, a range retry) serve the same stream, and each would
// otherwise write its own "OK" row. If onReadError already recorded a failure,
// nothing here may flip it back to OK.
func (s *Server) finishServeBookkeeping(
	r *http.Request,
	streamConfig *auth.Stream,
	resolved *resolvedPlayback,
	bufW *bufferedResponseWriter,
	monitored *StreamMonitor,
	effectiveRange string,
	serveStartedAt time.Time,
	serveFailureRecorded *bool,
	rs *reportingServe,
) {
	rt := s.runtime()
	if *serveFailureRecorded {
		return
	}
	sess := resolved.session
	sessionID := resolved.sessionID

	probeLike, probeReason := classifyProbeLikeServe(
		r,
		resolved.size,
		effectiveRange,
		bufW.Snapshot(),
		monitored.Snapshot(),
		errorString(r.Context().Err()),
	)
	if probeLike {
		logger.Debug("Skipping success bookkeeping for probe-like play request",
			"session", sessionID,
			"effective_range", effectiveRange,
			"stream_mode", resolved.mode,
			"reason", probeReason,
		)
		return
	}

	if s.commitGoodAttemptIfQualified(streamConfig, sess, sessionID, serveStartedAt, rs) {
		return
	}

	serveDuration := time.Since(serveStartedAt)
	minBytes := availnzb.DefaultMinBytesToReportGood
	minDuration := availnzb.DefaultMinDurationToReportGood
	if rt.availReporter != nil {
		minBytes = rt.availReporter.MinBytesToReportGood
		minDuration = rt.availReporter.MinDurationToReportGood
	}
	if !availnzb.QualifiesGood(sess, serveDuration, minBytes, minDuration) {
		reason := fmt.Sprintf(
			"Playback ended too early to classify this release as good. Threshold not reached (%d/%d bytes, %ds/%ds).",
			sess.BytesRead(),
			minBytes,
			int(serveDuration/time.Second),
			int(minDuration/time.Second),
		)
		s.logBelowGoodThresholdOnce(sess, sessionID, serveDuration, minBytes, minDuration, reason)
		s.recordPendingAttempt(sess, reason, availnzb.SkippedOutcome("Playback ended before the good threshold was reached."))
		return
	}
	// Safety fallback: this path should normally already have returned via
	// commitGoodAttemptIfQualified above.
	s.commitGoodAttemptIfQualified(streamConfig, sess, sessionID, serveStartedAt, rs)
}
