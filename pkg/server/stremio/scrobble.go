package stremio

import (
	"context"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/simkl"
	"streamnzb/pkg/session"
)

const scrobbleRequestTimeout = 15 * time.Second

// scrobbleMinAdvancePercent is how far the watched high-water mark must move
// past the last reported progress before another stop is worth sending. It
// also filters serves too short to mean anything (a play abandoned within the
// first percent).
const scrobbleMinAdvancePercent = 1

// simklScrobblingOn reports whether playback should be reported to Simkl at
// all: the toggle is on and an account is linked.
func simklScrobblingOn(rt serverRuntime) bool {
	return rt.config != nil && rt.config.SimklScrobble && rt.simklClient.Connected()
}

// scrobbleItemForSession maps the session's request context onto Simkl's
// addressing, or reports false when the content cannot be addressed (direct
// NZB plays with no ids, anime the MAL mapping does not know yet, series
// requests without an episode).
func (s *Server) scrobbleItemForSession(sess *session.Session) (simkl.ScrobbleItem, bool) {
	meta := sess.ContentIDs
	if meta == nil {
		return simkl.ScrobbleItem{}, false
	}
	item := simkl.ScrobbleItem{Title: strings.TrimSpace(sess.ContentTitle)}
	switch {
	case meta.KitsuID != "" || sess.ContentType == "anime":
		mapping, ok := s.animeLists.LookupKitsu(meta.KitsuID)
		if !ok || mapping.MALID <= 0 {
			return item, false
		}
		item.ContentType = "anime"
		item.MALID = strconv.Itoa(mapping.MALID)
		// The entry-local episode number, which MAL shares with Kitsu — not
		// meta.Episode, which anime-lists may have remapped to the aired
		// series numbering.
		parsed := query.ParseContentID(sess.ContentID)
		item.Episode, _ = strconv.Atoi(parsed.KitsuEpisode)
	case sess.ContentType == "movie":
		item.ContentType = "movie"
		item.IMDbID, item.TMDBID = meta.ImdbID, meta.TmdbID
		if item.IMDbID == "" && item.TMDBID == "" {
			return item, false
		}
	default:
		item.ContentType = "series"
		item.IMDbID, item.TMDBID, item.TVDBID = meta.ImdbID, meta.TmdbID, meta.TvdbID
		item.Season, item.Episode = meta.Season, meta.Episode
		if (item.IMDbID == "" && item.TMDBID == "" && item.TVDBID == "") || item.Season < 1 || item.Episode < 1 {
			return item, false
		}
	}
	return item, true
}

// scrobbleSimklStart reports "watching now" once real playback is proven (the
// good-attempt commit). Fire-and-forget: scrobbling must never slow a serve.
func (s *Server) scrobbleSimklStart(sess *session.Session) {
	rt := s.runtime()
	if sess == nil || !simklScrobblingOn(rt) {
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		return
	}
	s.sendScrobble(rt, sess, "start", item, sess.ServedProgressPercent())
}

// scrobbleSimklStopIfIdle runs as a serve unwinds, after playback bookkeeping:
// when the last open play on the session has ended, the current watched
// progress goes out as a scrobble stop. Simkl marks the item watched at ≥80%
// and stores a resumable playback below that, so a pause/stop mid-film shows
// up on Simkl at the right position and a finished film lands in history.
func (s *Server) scrobbleSimklStopIfIdle(sess *session.Session) {
	rt := s.runtime()
	if sess == nil || !simklScrobblingOn(rt) || sess.CurrentActivePlays() != 0 {
		return
	}
	progress := sess.ServedProgressPercent()
	// A stop is only news when the mark moved: seeks reopen the request every
	// few minutes, and each brief zero-plays gap between requests lands here.
	if progress <= sess.LastReportedProgress()+scrobbleMinAdvancePercent {
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		return
	}
	sess.SetLastReportedProgress(progress)
	s.sendScrobble(rt, sess, "stop", item, progress)
}

func (s *Server) sendScrobble(rt serverRuntime, sess *session.Session, verb string, item simkl.ScrobbleItem, progress float64) {
	contentID := sess.ContentID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scrobbleRequestTimeout)
		defer cancel()
		if err := rt.simklClient.Scrobble(ctx, verb, item, progress); err != nil {
			logger.Debug("Simkl scrobble failed", "verb", verb, "content", contentID, "err", err)
			return
		}
		logger.Debug("Simkl scrobble sent", "verb", verb, "content", contentID, "progress", int(progress))
	}()
}
