package stremio

import (
	"context"
	"slices"
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
// has reported nothing the report grace after the serve qualified: a client
// that does not report.
func (s *Server) scrobbleStart(stream *auth.Stream, sess *session.Session, rs *reportingServe) {
	if sess == nil {
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if rs != nil {
		if !rs.startDecided.CompareAndSwap(false, true) || !stream.ScrobbleEnabled() || rs.reported.Load() {
			return
		}
		// The item is taken now, while the serve has the session: it may be
		// evicted before the grace is out.
		s.clientPlayback.mu.Lock()
		rs.start = func() {
			if !rs.reported.Load() {
				s.transportStart(stream, sess, item, ok)
			}
		}
		s.clientPlayback.mu.Unlock()
		s.afterReportGrace(rs, func() { s.runFallbackStart(rs) })
		return
	}
	s.transportStart(stream, sess, item, ok)
}

// transportStart sends the transport's start for the session, once. item is
// what the title is scrobbled as, taken while the session was live.
func (s *Server) transportStart(stream *auth.Stream, sess *session.Session, item scrobble.Item, ok bool) {
	if !sess.Once(onceTransportStarted) {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, scrobble.VerbStart)
		return
	}
	if !ok {
		logger.Debug("Playback not scrobbled: no id a target can place", "content", sess.ContentID)
		return
	}
	s.sendScrobble(targets, clientPlaybackKey(stream, sess.ContentID), sess.ContentID, scrobble.VerbStart, item, sess.ServedProgressPercent())
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
// plays) waits the report grace before deciding which kind it was, and then
// sends its fallback start, if still due, ahead of its stop. Reports are
// matched to the serve's own client, so another device reporting the same
// title decides nothing for it.
func (s *Server) scrobbleStopIfIdle(stream *auth.Stream, sess *session.Session, rs *reportingServe) {
	if sess == nil {
		return
	}
	// Taken now for the same reason as the start's: the session may be gone
	// by the time a fallback stop is decided.
	item, ok := s.scrobbleItemForSession(sess)
	switch {
	case rs == nil:
		sess.Once(onceTransportStopDue)
	case stream.ScrobbleEnabled() && !rs.reported.Load():
		s.afterReportGrace(rs, func() {
			s.runFallbackStart(rs)
			if !rs.reported.Load() {
				sess.Once(onceTransportStopDue)
				s.transportStopIfIdle(stream, sess, item, ok)
			}
			s.endReportingServe(stream, sess.ContentID, rs)
		})
	default:
		s.endReportingServe(stream, sess.ContentID, rs)
	}
	s.transportStopIfIdle(stream, sess, item, ok)
}

// transportStopIfIdle sends the transport's stop when the session's last
// serve has ended and one of its serves owes it. Claiming the debt is one
// atomic step, so a serve ending and a fallback timer firing together send it
// once. item is what the title is scrobbled as, taken while the session was
// live.
func (s *Server) transportStopIfIdle(stream *auth.Stream, sess *session.Session, item scrobble.Item, ok bool) {
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
	if !ok {
		logger.Debug("Playback not scrobbled: no id a target can place", "content", sess.ContentID)
		return
	}
	sess.SetLastReportedProgress(progress)
	s.sendScrobble(targets, clientPlaybackKey(stream, sess.ContentID), sess.ContentID, scrobble.VerbStop, item, progress)
}

// maxQueuedScrobbles bounds the sends one lane holds behind the one in
// flight. Each takes at most scrobbleRequestTimeout and a playback makes a
// handful, so reaching it means the targets are answering slower than a
// client stops and resumes.
const maxQueuedScrobbles = 16

// scrobbleWatchedPercent is where both targets take a stop as watched and
// add it to history; below it a stop only leaves a resume position.
const scrobbleWatchedPercent = 80

// scrobbleLanes sends a stream's scrobbles of one title one at a time, in the
// order they were made, so a slow request is never overtaken by a later one:
// a resume's start by the stop before it, a fallback's stop by its start.
// Each lane is a stream and a title, so other titles and other streams go out
// alongside, and a lane's goroutine lives only while it has sends.
//
// A lane is one account's one item, and a target keeps one state per item: a
// start, or a stop below the watched mark, is wholly overwritten by any later
// send on its lane. So a full lane makes room by dropping the oldest waiting
// send a later one overwrites, never the new one — the latest state always
// lands. A watched stop also lands in history and is dropped only when every
// waiting send is one, and then the oldest: the item stays watched by the
// ones after it.
type scrobbleLanes struct {
	mu    sync.Mutex
	lanes map[string][]laneSend // lane → sends waiting; present while its goroutine runs
}

type laneSend struct {
	send        func(deliver bool) // deliver false: overwritten before it went out
	overwritten bool               // a later send on the lane overwrites it
}

// run queues send on lane, after the lane's earlier sends.
func (l *scrobbleLanes) run(lane string, next laneSend) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if waiting, busy := l.lanes[lane]; busy {
		if len(waiting) >= maxQueuedScrobbles {
			i := max(0, slices.IndexFunc(waiting, func(w laneSend) bool { return w.overwritten }))
			waiting[i].send(false)
			waiting = slices.Delete(waiting, i, i+1)
		}
		l.lanes[lane] = append(waiting, next)
		return
	}
	if l.lanes == nil {
		l.lanes = make(map[string][]laneSend)
	}
	l.lanes[lane] = nil
	go func() {
		for {
			next.send(true)
			l.mu.Lock()
			waiting := l.lanes[lane]
			if len(waiting) == 0 {
				delete(l.lanes, lane)
				l.mu.Unlock()
				return
			}
			next, l.lanes[lane] = waiting[0], waiting[1:]
			l.mu.Unlock()
		}
	}()
}

// sendScrobble sends one scrobble to each target in the background, on the
// lane (a stream and title, clientPlaybackKey) it belongs to: after the
// lane's earlier sends, and before its later ones. Nothing goes out once
// Shutdown has run.
func (s *Server) sendScrobble(targets []scrobble.Target, lane, contentID, verb string, item scrobble.Item, progress float64) {
	if s.shuttingDown() {
		logger.Debug("Scrobble dropped: the server is shutting down", "verb", verb, "content", contentID)
		return
	}
	var placed []scrobble.Target
	for _, target := range targets {
		// A target that cannot address this item is skipped: anime only
		// anime-lists' aired mapping names has no Simkl address, and anime only
		// MAL names has no MDBList one. Neither is a failure.
		if !target.Supports(item) {
			logger.Debug("Scrobble skipped: the target cannot place this item", "target", target.Name(), "content", contentID)
			continue
		}
		placed = append(placed, target)
	}
	if len(placed) == 0 {
		return
	}
	s.scrobbleSends.Add(1)
	s.scrobbleLanes.run(lane, laneSend{overwritten: verb == scrobble.VerbStart || progress < scrobbleWatchedPercent, send: func(deliver bool) {
		defer s.scrobbleSends.Done()
		if !deliver {
			logger.Debug("Scrobble superseded: a later send for the title overwrites it before it went out", "verb", verb, "content", contentID, "progress", int(progress))
			return
		}
		if s.shuttingDown() {
			logger.Debug("Scrobble dropped: the server is shutting down", "verb", verb, "content", contentID)
			return
		}
		var sent sync.WaitGroup
		for _, target := range placed {
			sent.Go(func() {
				ctx, cancel := context.WithTimeout(context.Background(), scrobbleRequestTimeout)
				defer cancel()
				if err := target.Scrobble(ctx, verb, item, progress); err != nil {
					logger.Debug("Scrobble failed", "target", target.Name(), "verb", verb, "content", contentID, "err", err)
					return
				}
				logger.Debug("Scrobble sent", "target", target.Name(), "verb", verb, "content", contentID, "progress", int(progress))
			})
		}
		sent.Wait()
	}})
}

func (s *Server) shuttingDown() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

// logUnlinkedScrobble notes a stream that has scrobbling switched on but no
// linked account to take it — otherwise indistinguishable from scrobbling off.
func logUnlinkedScrobble(stream *auth.Stream, verb string) {
	if stream.ScrobbleEnabled() {
		logger.Debug("Scrobbling is on but no linked account can take it", "stream", streamLogName(stream), "verb", verb)
	}
}
