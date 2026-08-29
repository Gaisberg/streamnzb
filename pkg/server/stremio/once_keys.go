package stremio

import "streamnzb/pkg/session"

// Bookkeeping that must happen at most once for a session. Players re-request
// the same URL freely — Stremio retries automatically, and every seek reopens
// the playback path — so without a gate a single play inserts duplicate history
// rows, re-gzips the NZB into the library repeatedly, and repeats diagnostics.
//
// These are claimed on the session (session.Once) rather than in a map keyed by
// session ID. A session ID is a slot path and slot paths are reused, so a map
// out here has to forget each entry before the slot comes back — which is what
// the goroutine parked on Done() beside every one of these used to do. On the
// session, the flags end when the session does, and a later play of the same
// slot starts clean because it is a different session.
const (
	// A "Preload" history row, written when playback starts.
	oncePreloadRecorded session.OnceKey = "preload-recorded"
	// A "Failure" history row. Concurrent retries of the same slot must not
	// each insert one.
	onceFailureRecorded session.OnceKey = "failure-recorded"
	// A successful play, recorded once the good threshold is reached. Also read
	// to decide whether a mid-stream error should still be recorded as a
	// failure, and whether a "next release" cursor may advance.
	onceSuccessRecorded session.OnceKey = "success-recorded"
	// The below-threshold diagnostic, kept to a single line per session. Reset
	// when an attempt does commit, so a later one can report for itself.
	onceThresholdLogged session.OnceKey = "threshold-skip-logged"
	// The pending (pre-verdict) library entry, saved from the serve path.
	oncePendingLibrarySaved session.OnceKey = "pending-library-saved"
	// The next fallback's NZB, warmed once the client has bytes. Every range
	// request reaches the same trigger; the release only needs fetching once.
	onceFallbackPrefetched session.OnceKey = "fallback-prefetched"
)

// countPastEOFRanges tallies GET requests whose Range started at or beyond the
// served size (session.CountEvent, same lifecycle as the once-flags). See
// failSlotOnRepeatedPastEOFRanges for why the count exists.
const countPastEOFRanges session.OnceKey = "past-eof-ranges"

// slotCommitted reports whether the session behind slotID recorded a successful
// play.
//
// A slot whose session is gone counts as not committed: the flag lives on the
// session, so once it is evicted there is nothing left that committed. It peeks
// rather than gets, because this asks a question about the session instead of
// doing work on it — going through GetSession would stamp LastAccess and keep
// alive the very session being asked about.
func (s *Server) slotCommitted(slotID string) bool {
	if s == nil || s.sessionManager == nil {
		return false
	}
	return s.sessionManager.PeekSession(slotID).OnceDone(onceSuccessRecorded)
}
