package stremio

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/services/metadata/scrobble"
	"streamnzb/pkg/session"
)

const scrobbleRequestTimeout = 15 * time.Second

// scrobbleMinAdvancePercent is how far the watched high-water mark must move
// past the last reported progress before another stop is worth sending. It
// also filters serves too short to mean anything (a play abandoned within the
// first percent).
const scrobbleMinAdvancePercent = 1

// scrobbleTargets is the set of watch-tracking services this stream's playback
// is reported to right now: those the stream has switched on and whose account
// the stream has linked. Both halves are per stream — the account belongs to
// the person the stream belongs to — so one household member's viewing can
// never land in another's history. A stream that has linked nothing, which is
// the default, reports nothing.
func scrobbleTargets(rt serverRuntime, stream *auth.Stream) []scrobble.Target {
	if stream == nil {
		return nil
	}
	var targets []scrobble.Target
	if stream.SimklScrobble {
		if client := rt.simklClients.For(stream.Username); client.Connected() {
			targets = append(targets, client)
		}
	}
	if stream.MDBListScrobble {
		if client := rt.mdblistClients.For(stream.Username); client.Connected() {
			targets = append(targets, client)
		}
	}
	return targets
}

// scrobbleItemForSession maps the session's request context onto the
// addressing the targets need, or reports false when the content can be placed
// nowhere (direct NZB plays with no ids, series requests without an episode,
// anime neither the MAL mapping nor anime-lists' aired ids can name).
func (s *Server) scrobbleItemForSession(sess *session.Session) (scrobble.Item, bool) {
	meta := sess.ReportMeta()
	if meta == nil {
		return scrobble.Item{}, false
	}
	item := scrobble.Item{Title: strings.TrimSpace(sess.ContentTitle)}
	switch {
	case meta.KitsuID != "" || sess.ContentType == "anime":
		item.ContentType = "anime"
		// Both addressings go in, because the targets disagree on how anime is
		// identified: Simkl wants the MAL entry and its entry-local episode,
		// MDBList wants the aired series anime-lists resolved it onto.
		item.IMDbID, item.TMDBID, item.TVDBID = meta.ImdbID, meta.TmdbID, meta.TvdbID
		item.Season, item.Episode = meta.Season, meta.Episode
		if mapping, ok := s.animeLists.LookupKitsu(meta.KitsuID); ok {
			item.AnimeMovie = strings.EqualFold(mapping.Type, "MOVIE")
			if mapping.MALID > 0 {
				item.MALID = strconv.Itoa(mapping.MALID)
				// The entry-local episode number, which MAL shares with Kitsu
				// — not meta.Episode, which anime-lists may have remapped to
				// the aired series numbering.
				parsed := query.ParseContentID(sess.ContentID)
				item.MALEpisode, _ = strconv.Atoi(parsed.KitsuEpisode)
			}
		}
	case sess.ContentType == "movie":
		item.ContentType = "movie"
		item.IMDbID, item.TMDBID = meta.ImdbID, meta.TmdbID
	default:
		item.ContentType = "series"
		item.IMDbID, item.TMDBID, item.TVDBID = meta.ImdbID, meta.TmdbID, meta.TvdbID
		item.Season, item.Episode = meta.Season, meta.Episode
	}
	return item, item.Addressable()
}

// scrobbleStart reports "watching now" once real playback is proven (the
// good-attempt commit). Fire-and-forget: scrobbling must never slow a serve.
// A serve to a client that reports its own playback (rs) leaves the start to
// the client's first report, and sends the transport's only when that client
// has reported nothing clientReportGrace after the serve qualified: a client
// that does not report.
func (s *Server) scrobbleStart(stream *auth.Stream, sess *session.Session, rs *reportingServe) {
	if sess == nil {
		return
	}
	if rs != nil {
		if !rs.startDecided.CompareAndSwap(false, true) || !stream.ScrobbleEnabled() || s.clientPlayback.reportedBy(stream, sess.ContentID, rs) {
			return
		}
		s.afterReportGrace(func() {
			if !s.clientPlayback.reportedBy(stream, sess.ContentID, rs) {
				s.scrobbleStart(stream, sess, nil)
			}
		})
		return
	}
	if !sess.Once(onceTransportStarted) {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, scrobble.VerbStart)
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		logger.Debug("Playback not scrobbled: no id a target can place", "content", sess.ContentID)
		return
	}
	s.sendScrobble(targets, sess.ContentID, scrobble.VerbStart, item, sess.ServedProgressPercent(), nil)
}

// scrobbleStopIfIdle runs as a serve unwinds, after playback bookkeeping: when
// the last open play on the session has ended, the current watched progress
// goes out as a scrobble stop. Both services mark an item watched at ≥80% and
// store a resumable playback below that, so a pause/stop mid-film shows up at
// the right position and a finished film lands in history.
//
// A serve to a client that reports its own playback (rs) leaves the stop to
// those reports. Every other serve — Stremio's, and one to a Jellyfin client
// that reports nothing — owes it, so a reporting client sharing the session
// never silences them. A reporting client's serve that ends before its client
// has reported anything (a request opened and dropped before it says it
// plays) waits clientReportGrace before deciding which kind it was. Reports
// are matched to the serve's own client, so another device reporting the same
// title decides nothing for it.
func (s *Server) scrobbleStopIfIdle(stream *auth.Stream, sess *session.Session, rs *reportingServe) {
	if sess == nil {
		return
	}
	switch {
	case rs == nil:
		sess.Once(onceTransportStopDue)
	case stream.ScrobbleEnabled() && !s.clientPlayback.reportedBy(stream, sess.ContentID, rs):
		s.afterReportGrace(func() {
			if !s.clientPlayback.reportedBy(stream, sess.ContentID, rs) {
				sess.Once(onceTransportStopDue)
				s.transportStopIfIdle(stream, sess)
			}
		})
	}
	s.transportStopIfIdle(stream, sess)
}

// afterReportGrace runs fn once clientReportGrace has passed, counted with
// the sends it may make, so whatever waits on those waits on it too.
func (s *Server) afterReportGrace(fn func()) {
	s.scrobbleSends.Add(1)
	time.AfterFunc(clientReportGrace, func() {
		defer s.scrobbleSends.Done()
		fn()
	})
}

// transportStopIfIdle sends the transport's stop when the session's last
// serve has ended and one of its serves owes it. Claiming the debt is one
// atomic step, so a serve ending and a fallback timer firing together send it
// once.
func (s *Server) transportStopIfIdle(stream *auth.Stream, sess *session.Session) {
	if sess.CurrentActivePlays() != 0 || !sess.TakeOnce(onceTransportStopDue) {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, scrobble.VerbStop)
		return
	}
	progress := sess.ServedProgressPercent()
	// A stop is only news when the mark moved: seeks reopen the request every
	// few minutes, and each brief zero-plays gap between requests lands here.
	if progress <= sess.LastReportedProgress()+scrobbleMinAdvancePercent {
		logger.Debug("Scrobble stop skipped: progress has not moved since the last report", "content", sess.ContentID, "progress", int(progress))
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		logger.Debug("Playback not scrobbled: no id a target can place", "content", sess.ContentID)
		return
	}
	sess.SetLastReportedProgress(progress)
	s.sendScrobble(targets, sess.ContentID, scrobble.VerbStop, item, progress, nil)
}

// sendScrobble sends one scrobble to each target in the background, each
// once the sends in after (nil for none) are done, and returns what a send
// that must follow this one waits on. Nothing goes out once Shutdown has run:
// the grace and idle timers can outlive the server, and must not start
// network sends on a stopped one.
func (s *Server) sendScrobble(targets []scrobble.Target, contentID, verb string, item scrobble.Item, progress float64, after *sync.WaitGroup) *sync.WaitGroup {
	var sent sync.WaitGroup
	select {
	case <-s.stopCh:
		logger.Debug("Scrobble dropped: the server is shutting down", "verb", verb, "content", contentID)
		return &sent
	default:
	}
	for _, target := range targets {
		// A target that cannot address this item is skipped: anime only
		// anime-lists' aired mapping names has no Simkl address, and anime only
		// MAL names has no MDBList one. Neither is a failure.
		if !target.Supports(item) {
			logger.Debug("Scrobble skipped: the target cannot place this item", "target", target.Name(), "content", contentID)
			continue
		}
		s.scrobbleSends.Add(1)
		sent.Add(1)
		go func(target scrobble.Target) {
			defer s.scrobbleSends.Done()
			defer sent.Done()
			if after != nil {
				after.Wait()
			}
			ctx, cancel := context.WithTimeout(context.Background(), scrobbleRequestTimeout)
			defer cancel()
			if err := target.Scrobble(ctx, verb, item, progress); err != nil {
				logger.Debug("Scrobble failed", "target", target.Name(), "verb", verb, "content", contentID, "err", err)
				return
			}
			logger.Debug("Scrobble sent", "target", target.Name(), "verb", verb, "content", contentID, "progress", int(progress))
		}(target)
	}
	return &sent
}

// logUnlinkedScrobble notes a stream that has scrobbling switched on but no
// linked account to take it — otherwise indistinguishable from scrobbling off.
func logUnlinkedScrobble(stream *auth.Stream, verb string) {
	if stream.ScrobbleEnabled() {
		logger.Debug("Scrobbling is on but no linked account can take it", "stream", streamLogName(stream), "verb", verb)
	}
}
