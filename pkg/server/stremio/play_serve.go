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
				stream = nil
			}
		})
	}
	go func(done <-chan struct{}) {
		<-done
		closeStream("playback canceled")
	}(resolved.ctx.Done())

	clientIP := httpx.ClientIP(r)
	s.sessionManager.MarkPlaybackValidated(sessionID)
	s.sessionManager.StartPlayback(sessionID, clientIP)
	var endPlaybackOnce sync.Once
	endPlayback := func() { s.sessionManager.EndPlayback(sessionID, clientIP) }
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
	onReadError := func(playbackSessionID string, readErr error) {
		// Trigger slot failover for any permanent mid-stream error:
		//   - a missing article the loader could not treat as a fillable hole
		//     (the file's first segment, or a hole past MaxZeroFills)
		//   - yEnc decode failure (data corruption)
		//   - ErrTooManyZeroFills (the file accumulated more holes than allowed)
		// All of them mean the slot is unrecoverable; isolated missing articles do
		// not reach here, because the loader zero-fills those and playback continues.
		// SetSlotFailedDuringPlayback marks the slot so resolvePlaybackSlot sends
		// the player to the next one on reconnect, without the user having to
		// switch manually in Stremio.
		if !isFatalStreamErr(readErr) {
			return
		}
		s.sessionManager.SetSlotFailedDuringPlayback(playbackSessionID)
		errSess, _ := s.sessionManager.GetSession(playbackSessionID)
		if errSess == nil {
			return
		}
		errSess.ResetPlaybackStream()
		availOutcome := s.reportBadReleaseOutcome(errSess, readErr, true)
		if !errSess.OnceDone(onceSuccessRecorded) && errSess.Once(onceFailureRecorded) {
			s.recordFailureAttempt(errSess, readErr, availOutcome)
		}
		if playbackSessionID == sessionID {
			serveFailureRecorded = true
		}
	}

	effectiveRange := r.Header.Get("Range")
	if !s.primeRangeOrFailover(w, r, streamConfig, resolved, effectiveRange, onReadError, closeStream) {
		return
	}

	monitoredStream := &StreamMonitor{
		ReadSeekCloser: stream,
		sessionID:      sessionID,
		clientIP:       clientIP,
		manager:        s.sessionManager,
		onReadError:    onReadError,
		lastUpdate:     time.Now(),
	}

	bufW := newMediaResponseWriter(w, resolved.name)
	var ttffOnce sync.Once
	bufW.onFirstWrite = func() {
		ttffOnce.Do(func() { s.recordTTFF(sess, sessionID, resolved.mode, playStart) })
	}

	serveStartedAt := time.Now()
	monitoredStream.onProgress = func() {
		s.commitGoodAttemptIfQualified(sess, sessionID, serveStartedAt)
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
		s.finishServeBookkeeping(r, resolved, bufW, monitoredStream, effectiveRange, serveStartedAt, &serveFailureRecorded)
	}()

	http.ServeContent(bufW, r, resolved.name, time.Time{}, monitoredStream)
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
			forceDisconnect(w, r, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config))
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
	resolved *resolvedPlayback,
	bufW *bufferedResponseWriter,
	monitored *StreamMonitor,
	effectiveRange string,
	serveStartedAt time.Time,
	serveFailureRecorded *bool,
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

	if s.commitGoodAttemptIfQualified(sess, sessionID, serveStartedAt) {
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
	s.commitGoodAttemptIfQualified(sess, sessionID, serveStartedAt)
}
