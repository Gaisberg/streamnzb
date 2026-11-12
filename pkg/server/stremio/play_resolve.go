package stremio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/session"
)

// resolvedPlayback is an open, probed stream and everything the serve phase
// needs to go with it. The session it names is the one that will actually be
// served: resolvePlaybackSlot redirects rather than quietly serving a different
// slot than the client asked for.
type resolvedPlayback struct {
	session   *session.Session
	sessionID string
	stream    io.ReadSeekCloser
	name      string
	size      int64
	mode      string

	// ctx is cancelled when either the request ends or the session closes;
	// cancel belongs to the serve phase once this is handed over.
	ctx    context.Context
	cancel context.CancelFunc

	// startupHeld says resolvePlaybackSlot handed over the playback startup it
	// registered on session; the serve phase ends it once the session is
	// validated and playing, so there is no moment between the two in which a
	// background verdict sees an idle session it may close.
	startupHeld bool
}

// resolvePlaybackSlot walks the stream's failover candidates, starting at the
// requested one, until a playback stream opens.
//
// It answers the request itself on every path that does not end in a stream: a
// 404 for a slot that no longer exists and cannot be recovered, a redirect to
// the slot that did open, or the error video once the candidates run out. When
// it returns false the response is already written and the caller must return
// without touching w.
//
// Nothing is written to w while the walk is in progress. That is the whole
// reason resolving is separate from serving: a client that has already been
// given a status line and a Content-Length cannot be failed over to a different
// release.
func (s *Server) resolvePlaybackSlot(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream, requestedSessionID string) (*resolvedPlayback, bool) {
	rt := s.runtime()
	openStart := time.Now()
	sess, sessionID, ok := s.openPlaySession(w, r, streamConfig, requestedSessionID)
	if !ok {
		return nil, false
	}
	openMS := time.Since(openStart).Milliseconds()

	for {
		// Skip slots already known to have failed, so a retry never walks back
		// into one.
		if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
			nextSess, nextID := s.nextFallbackSlot(r.Context(), sess, streamConfig)
			if nextID == "" {
				failPlayback(w, r, sess, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config), nil)
				return nil, false
			}
			logger.Info("Skipping known-failed slot, trying next fallback", "from", sessionID, "to", nextID)
			sess, sessionID = nextSess, nextID
			continue
		}

		ctx, cancel := s.playbackContext(r, sess)

		// Registering the startup is also the last check that the slot still
		// holds this session: a preload verdict can retire its copy after the
		// session was found. Once registered, background verdicts leave it
		// alone; if that already happened, carry on with what the slot holds
		// now instead of starting on a closed session. It comes before the
		// preload row, which would otherwise be left unresolved on a session
		// this request then abandons.
		if !s.sessionManager.BeginSessionStartup(sess) {
			cancel()
			if current, ok := s.sessionAfterSlotMoved(r.Context(), sessionID, streamConfig); ok {
				logger.Debug("Slot moved on before playback started; continuing with the slot", "slot", sessionID)
				sess = current
				continue
			}
			// Given up, or not rebuildable: walk on (or end, with failover off).
			nextSess, nextID := s.nextFallbackSlot(r.Context(), sess, streamConfig)
			if nextID == "" {
				failPlayback(w, r, sess, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config), nil)
				return nil, false
			}
			sess, sessionID = nextSess, nextID
			continue
		}

		// Record preload at most once per session: subsequent HTTP requests
		// (seeks, range retries) for the same session must not insert another
		// "Preload" row that would never be resolved. The flag lives on the
		// session, so a later play of the same slot — a new session behind a
		// reused ID — records its own row.
		preloadStart := time.Now()
		if sess.Once(oncePreloadRecorded) {
			s.recordPreloadAttempt(sess)
		}
		preloadMS := time.Since(preloadStart).Milliseconds()

		prepareStart := time.Now()
		prepared, prepareErr := s.preparePlaybackStream(ctx, sess)
		if prepareErr != nil {
			s.sessionManager.EndSessionStartup(sess)
		}

		// Startup is a chain of independently slow steps (slot recovery, the
		// preload row, NZB download, probe) and a log that only reports the
		// total leaves the slow one to be guessed at.
		logger.Debug("Play resolve timing",
			"session", sessionID,
			"open_session_ms", openMS,
			"preload_row_ms", preloadMS,
			"prepare_ms", time.Since(prepareStart).Milliseconds(),
			"err", prepareErr)

		if prepareErr == nil {
			resolved := &resolvedPlayback{
				session:   sess,
				sessionID: sessionID,
				stream:    prepared.Stream,
				name:      prepared.Spec.Name,
				size:      prepared.Spec.Size,
				mode:      prepared.Mode,
				ctx:       ctx,
				cancel:    cancel,
			}
			if sessionID != requestedSessionID {
				s.sessionManager.EndSessionStartup(sess)
				s.redirectToResolvedSlot(w, r, streamConfig, resolved, requestedSessionID)
				return nil, false
			}
			resolved.startupHeld = true
			return resolved, true
		}

		cancel()
		if isPlayPrepareCancellation(prepareErr) {
			logger.Debug("play prepare canceled", "session", sessionID, "err", prepareErr)
			return nil, false
		}

		// Another indexer's copy of the same release comes before a different
		// release: the user picked this one, and the copy that would not open
		// is not evidence about the copy next to it.
		copySess, retry := s.retrySlotWithNextCopy(r.Context(), sess, sessionID, streamConfig, prepareErr)
		switch retry {
		case copyRetryNext:
			logger.Info("Trying another copy of the same release", "slot", sessionID, "err", prepareErr)
			sess = copySess
			continue
		case copyRetryNone:
			s.recordFailedSlot(sess, sessionID, prepareErr)
		case copyRetryElsewhere:
			// Already handled; recording the slot verdict here could condemn
			// the copy another request moved the slot to.
		}

		nextSess, nextID := s.nextFallbackSlot(r.Context(), sess, streamConfig)
		if nextID == "" {
			logger.Info("No more fallback slots", "last", sessionID, "err", prepareErr)
			failPlayback(w, r, sess, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config), prepareErr)
			return nil, false
		}
		logger.Info("Playback failover advanced", "from", sessionID, "to", nextID, "err", prepareErr)
		sess, sessionID = nextSess, nextID
	}
}

// openPlaySession finds the session behind the requested slot, recovering it
// when the slot survived but its session did not. It answers the request itself
// when neither is possible.
func (s *Server) openPlaySession(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream, sessionID string) (*session.Session, string, bool) {
	sess, err := s.sessionManager.GetSession(sessionID)
	if err == nil {
		if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
			s.redirectToNextSlotOrFail(w, r, sessionID, streamConfig,
				"Redirecting to next fallback (slot failed during playback)")
			return nil, "", false
		}
		return sess, sessionID, true
	}

	// The session may have been deleted by a concurrent internal failover (e.g.
	// exceeded failure threshold). If the slot was marked as failed, redirect
	// the client to the next working slot rather than 404.
	if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
		s.redirectToNextSlotOrFail(w, r, sessionID, streamConfig,
			"Session deleted (slot failed during playback), redirecting to next")
		return nil, "", false
	}

	recoveredSess, recoveredID, recoverErr := s.recoverPlaySessionAfterEviction(r.Context(), sessionID, streamConfig)
	if recoverErr != nil {
		logger.Debug("Play: session not found", "slot", sessionID, "err", err, "recovery_err", recoverErr)
		http.Error(w, "Session expired or not found", http.StatusNotFound)
		return nil, "", false
	}
	logger.Info("Play: recovered after cache/session eviction", "requested", sessionID, "playing", recoveredID)
	return recoveredSess, recoveredID, true
}

// recordFailedSlot records the verdict on a slot whose stream would not open.
func (s *Server) recordFailedSlot(sess *session.Session, sessionID string, prepareErr error) {
	if errors.Is(prepareErr, ErrPlaybackStartupTimeout) {
		logger.Warn("Playback startup timed out", "session", sessionID, "err", prepareErr)
	}
	// Every failure marks the slot, including one that proves nothing about the
	// release, so the player is moved along now rather than walked back into a
	// slot that just refused to open. Two things keep that from being a verdict:
	// the mark expires with the session TTL, and the durable bad-release verdict
	// is gated separately on conclusiveBadRelease — which already excludes an
	// indexer quota error.
	//
	// This used to be skipped for a throttled indexer. The skip never took
	// effect: reportBadReleaseOutcome below reaches purgeFailedRelease, which
	// marks the slot unconditionally and deliberately. Leaving the condition in
	// place only made it read like a promise the code does not keep.
	s.sessionManager.SetSlotFailedDuringPlayback(sessionID)
	availOutcome := s.reportBadReleaseOutcome(sess, prepareErr, true, false)
	// Concurrent goroutines for the same session — Stremio re-requests a play
	// URL on its own — must not each insert a Failure row. Only the first wins.
	if sess.Once(onceFailureRecorded) {
		s.recordFailureAttempt(sess, prepareErr, availOutcome)
	}
	s.sessionManager.DeleteSession(sessionID)
}

// retrySlotWithNextCopy answers a failed attempt with another copy of the same
// release, when merging kept one and the stream's attempt budget still allows
// it. It returns the session to carry on with, bound to the same slot id.
//
// Nothing about the slot is condemned here: the verdict, the AvailNZB report
// and the persistent bad record all key on the details URL of the NZB that
// failed, so the copies beside it stay playable. Because the slot id does not
// change, the client is never redirected — it asked for this release and it
// still gets this release, out of a different indexer's NZB.
func (s *Server) retrySlotWithNextCopy(ctx context.Context, sess *session.Session, sessionID string, streamConfig *auth.Stream, prepareErr error) (*session.Session, copyRetry) {
	if !streamFailoverEnabled(streamConfig) {
		return nil, copyRetryNone
	}
	if _, _, _, _, ok := parseStreamSlotID(sessionID); !ok {
		return nil, copyRetryNone
	}
	// The session is bound to the NZB that failed, so it leaves the slot before
	// the slot can be rebuilt against the next copy under the same id.
	retirement := s.sessionManager.RetireSlotCopy(sess, streamConfig.EffectiveVariantAttempts(), false, func() {
		availOutcome := s.reportBadReleaseOutcome(sess, prepareErr, true, true)
		if sess.Once(onceFailureRecorded) {
			s.recordFailureAttempt(sess, prepareErr, availOutcome)
		}
	})
	switch retirement {
	case session.SlotCopyLast:
		return nil, copyRetryNone
	case session.SlotCopyRetired:
		if next, ok := s.sessionAfterSlotMoved(ctx, sessionID, streamConfig); ok {
			return next, copyRetryNext
		}
		if ctx.Err() != nil {
			// The client left mid-rebuild, which says nothing about the next
			// copy: the cursor stays on it for the next request, which rebuilds
			// it or gives the slot up on its own evidence.
			return nil, copyRetryElsewhere
		}
		// The next copy cannot be built, so the release gives up: park the
		// cursor past the copies and fail the slot here. The copy's verdict is
		// already recorded, and the failed session is closed — the caller's
		// slot-level path would report it again with nothing to report, and
		// delete whatever a duplicate request has meanwhile built under this id.
		s.sessionManager.ExhaustSlotCopies(sessionID)
		s.sessionManager.SetSlotFailedDuringPlayback(sessionID)
		return nil, copyRetryElsewhere
	}

	// The session left the slot before this report: a duplicate request for
	// the slot reported the same copy first and moved the slot on, or the
	// session went some other way. Whoever did that owns the verdict; this
	// request carries on with whatever the slot holds now, unless the slot
	// was given up.
	logger.Debug("Slot already moved on from this session", "slot", sessionID, "err", prepareErr)
	if current, ok := s.sessionAfterSlotMoved(ctx, sessionID, streamConfig); ok {
		return current, copyRetryNext
	}
	return nil, copyRetryElsewhere
}

// sessionAfterSlotMoved returns the session to carry on with in a slot that
// moved on from the one a request was holding: the slot's current session, or
// one built from the play list when there is none yet. It reports false when
// the slot was given up or cannot be rebuilt.
func (s *Server) sessionAfterSlotMoved(ctx context.Context, sessionID string, streamConfig *auth.Stream) (*session.Session, bool) {
	if s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
		return nil, false
	}
	if current := s.sessionManager.PeekSession(sessionID); current != nil {
		return current, true
	}
	streamId, contentType, id, index, ok := parseStreamSlotID(sessionID)
	if !ok {
		return nil, false
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
	nextSess, err := s.resolveStreamSlot(ctx, key, index, streamConfig)
	if err != nil {
		logger.Debug("Could not rebuild the slot after it moved on", "slot", sessionID, "err", err)
		return nil, false
	}
	return nextSess, true
}

// copyRetry is what retrySlotWithNextCopy did with a failed attempt.
type copyRetry int

const (
	// copyRetryNone: no copy to move to; the slot-level verdict is the caller's.
	copyRetryNone copyRetry = iota
	// copyRetryNext: the slot was rebuilt against another copy of the release.
	copyRetryNext
	// copyRetryElsewhere: the failure is already fully handled — by another
	// request for the slot, or by giving the slot up here — so the caller
	// records nothing more and walks on.
	copyRetryElsewhere
)

// nextFallbackSlot returns the next candidate after sess, or ("" ) when
// failover is off for this stream or nothing is left to try.
//
// A throttled indexer does not end the walk: the remaining candidates usually
// come from other indexers, and stopping would hand the client nothing over a
// failure that says only "not from this indexer, not right now". The slot is
// left unpoisoned by recordFailedSlot so it can be retried once the limit
// clears, and the indexer's own cooldown keeps the walk past its other
// candidates cheap.
func (s *Server) nextFallbackSlot(ctx context.Context, sess *session.Session, streamConfig *auth.Stream) (*session.Session, string) {
	if !streamFailoverEnabled(streamConfig) {
		return nil, ""
	}
	nextSess, nextID, err := s.switchToNextFallback(ctx, sess, streamConfig)
	if err != nil || nextID == "" {
		return nil, ""
	}
	return nextSess, nextID
}

// playbackContext returns a context that ends when either the request ends or
// the session is closed, so closing a session from the dashboard aborts
// playback and stops downloading immediately.
func (s *Server) playbackContext(r *http.Request, sess *session.Session) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	go func(done <-chan struct{}) {
		select {
		case <-done:
			return
		case <-sess.Done():
			logger.Debug("playback aborted: session closed", "session", sess.ID)
			cancel()
		}
	}(ctx.Done())
	return ctx, cancel
}

// redirectToResolvedSlot sends the client to the slot that actually opened.
//
// Redirecting rather than serving the other release over the URL the client
// asked for is what lets its Range header be re-evaluated against the file it
// is about to receive, instead of being applied to a file of a different length.
func (s *Server) redirectToResolvedSlot(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream, resolved *resolvedPlayback, requestedSessionID string) {
	resolved.cancel()
	if resolved.stream != nil {
		resolved.stream.Close()
	}
	logger.Info("Redirecting client to resolved/failover slot", "from", requestedSessionID, "to", resolved.sessionID)
	w.Header().Set("Location", s.slotLocation(r, streamConfig, resolved.sessionID))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusTemporaryRedirect)
}
