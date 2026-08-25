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
		s.sessionManager.BeginPlaybackStartup(sessionID)
		prepared, prepareErr := s.preparePlaybackStream(ctx, sess)
		s.sessionManager.EndPlaybackStartup(sessionID)

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
				s.redirectToResolvedSlot(w, r, streamConfig, resolved, requestedSessionID)
				return nil, false
			}
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
		if nextSess, ok := s.retrySlotWithNextCopy(r.Context(), sess, sessionID, streamConfig, prepareErr); ok {
			logger.Info("Trying another copy of the same release", "slot", sessionID, "err", prepareErr)
			sess = nextSess
			continue
		}

		s.recordFailedSlot(sess, sessionID, prepareErr)

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
func (s *Server) retrySlotWithNextCopy(ctx context.Context, sess *session.Session, sessionID string, streamConfig *auth.Stream, prepareErr error) (*session.Session, bool) {
	if !streamFailoverEnabled(streamConfig) {
		return nil, false
	}
	streamId, contentType, id, index, ok := parseStreamSlotID(sessionID)
	if !ok {
		return nil, false
	}
	if !s.sessionManager.AdvanceSlotCopy(sessionID, streamConfig.EffectiveVariantAttempts()) {
		return nil, false
	}
	availOutcome := s.reportBadReleaseOutcome(sess, prepareErr, true, true)
	if sess.Once(onceFailureRecorded) {
		s.recordFailureAttempt(sess, prepareErr, availOutcome)
	}
	// The session is bound to the NZB that failed, so it has to go before the
	// slot can be rebuilt against the next copy under the same id.
	s.sessionManager.DeleteSession(sessionID)

	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
	nextSess, err := s.resolveStreamSlot(ctx, key, index, streamConfig)
	if err != nil {
		// Park the cursor past the copies so the normal failure path treats
		// this as the release giving up rather than as an untried copy.
		logger.Debug("Could not resolve the next copy of the release", "slot", sessionID, "err", err)
		s.sessionManager.ExhaustSlotCopies(sessionID)
		return nil, false
	}
	return nextSess, true
}

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
	nextURL := s.baseURLWithToken(streamConfig) + "/play/" + resolved.sessionID
	if r.URL.RawQuery != "" {
		nextURL += "?" + r.URL.RawQuery
	}
	logger.Info("Redirecting client to resolved/failover slot", "from", requestedSessionID, "to", resolved.sessionID)
	w.Header().Set("Location", nextURL)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusTemporaryRedirect)
}
