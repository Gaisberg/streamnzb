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
	if rs != nil {
		if !rs.startDecided.CompareAndSwap(false, true) || !stream.ScrobbleEnabled() || rs.reported.Load() {
			return
		}
		// The item is taken now, while the serve has the session: it may be
		// evicted before the grace is out.
		item := takenItem(s.scrobbleItemForSession(sess))
		s.clientPlayback.mu.Lock()
		rs.start = func() {
			if !rs.reported.Load() {
				s.transportStart(stream, sess, item)
			}
		}
		s.clientPlayback.mu.Unlock()
		s.afterReportGrace(rs, func() { s.runFallbackStart(rs) })
		return
	}
	s.transportStart(stream, sess, s.liveItem(sess))
}

// takenItem is what a title is scrobbled as, taken while its session was
// live; liveItem takes it from the session only once a send needs it.
func takenItem(item scrobble.Item, ok bool) func() (scrobble.Item, bool) {
	return func() (scrobble.Item, bool) { return item, ok }
}

func (s *Server) liveItem(sess *session.Session) func() (scrobble.Item, bool) {
	return func() (scrobble.Item, bool) { return s.scrobbleItemForSession(sess) }
}

// transportStart sends the transport's start for the session, once, as item.
func (s *Server) transportStart(stream *auth.Stream, sess *session.Session, itemFor func() (scrobble.Item, bool)) {
	if !sess.Once(onceTransportStarted) {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, scrobble.VerbStart)
		return
	}
	item, ok := itemFor()
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
// title decides nothing for it. A serve that streamed no media (rs.media: a
// HEAD, a range refused, a probe) owes nothing and waits for nothing.
func (s *Server) scrobbleStopIfIdle(stream *auth.Stream, sess *session.Session, rs *reportingServe) {
	if sess == nil {
		return
	}
	switch {
	case rs == nil:
		sess.Once(onceTransportStopDue)
	case rs.media && stream.ScrobbleEnabled() && !rs.reported.Load():
		// Taken now for the same reason as the start's: the session may be
		// gone by the time a fallback stop is decided.
		item := takenItem(s.scrobbleItemForSession(sess))
		s.afterReportGrace(rs, func() {
			s.runFallbackStart(rs)
			if !rs.reported.Load() {
				sess.Once(onceTransportStopDue)
				s.transportStopIfIdle(stream, sess, item)
			}
			s.endReportingServe(stream, sess.ContentID, rs)
		})
	default:
		s.endReportingServe(stream, sess.ContentID, rs)
	}
	s.transportStopIfIdle(stream, sess, s.liveItem(sess))
}

// transportStopIfIdle sends the transport's stop, as item, when the session's
// last serve has ended and one of its serves owes it. Claiming the debt is one
// atomic step, so a serve ending and a fallback timer firing together send it
// once.
func (s *Server) transportStopIfIdle(stream *auth.Stream, sess *session.Session, itemFor func() (scrobble.Item, bool)) {
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
	item, ok := itemFor()
	if !ok {
		logger.Debug("Playback not scrobbled: no id a target can place", "content", sess.ContentID)
		return
	}
	sess.SetLastReportedProgress(progress)
	s.sendScrobble(targets, clientPlaybackKey(stream, sess.ContentID), sess.ContentID, scrobble.VerbStop, item, progress)
}

// maxQueuedScrobbles bounds the sends one lane holds behind the one in
// flight. Each takes at most scrobbleRequestTimeout and a playback makes a
// handful, so reaching it means the target is answering slower than a client
// stops and resumes.
const maxQueuedScrobbles = 16

// scrobbleWatchedPercent is where both targets take a stop as watched and
// add it to history; below it a stop only leaves a resume position.
const scrobbleWatchedPercent = 80

// scrobbleLanes sends one target's scrobbles of one stream's title one at a
// time, in the order they were made, so a slow request is never overtaken by
// a later one: a resume's start by the stop before it, a fallback's stop by
// its start. Each lane is a target, a stream and a title, so a target that
// hangs holds back no other target, title or stream, and a lane's goroutine
// lives only while it has sends.
//
// A lane is one account's one item at one target, and a full lane makes room
// by dropping the oldest waiting send a later one makes moot, never the new
// one: a start, which any later send restates or ends; else a stop below the
// watched mark, whose resume position a later stop replaces; else a watched
// stop, when a later one is watched too. A stop is never dropped for a later
// start, which leaves its resume position standing. Of 17 sends at most three
// are not moot, so the latest state, the latest resume position and the
// latest watched stop always go out — as far as a target that fails or times
// out takes them.
type scrobbleLanes struct {
	mu     sync.Mutex
	lanes  map[string][]laneSend // lane → sends waiting; present while its goroutine runs
	closed bool                  // shut down: nothing more is taken or sent
	// ctx parents every send and is cancelled by close; nil until the first.
	ctx    context.Context
	cancel context.CancelFunc
}

type laneSend struct {
	verb     string
	progress float64
	// send makes the request under ctx, or only accounts for it when ctx is
	// nil (dropped to make room) or done (shut down).
	send func(ctx context.Context)
}

// laneMoot are the ways a later send on a lane makes an earlier one moot, in
// the order a full lane would rather drop them.
var laneMoot = []func(earlier, later laneSend) bool{
	func(e, _ laneSend) bool { return e.verb == scrobble.VerbStart },
	func(e, l laneSend) bool {
		return e.verb == scrobble.VerbStop && e.progress < scrobbleWatchedPercent && l.verb == scrobble.VerbStop
	},
	func(e, l laneSend) bool {
		return e.verb == scrobble.VerbStop && l.verb == scrobble.VerbStop && l.progress >= scrobbleWatchedPercent
	},
}

// overflow picks the waiting send a full lane drops to take next.
func overflow(waiting []laneSend, next laneSend) int {
	all := append(waiting[:len(waiting):len(waiting)], next)
	for _, moot := range laneMoot {
		for i, e := range waiting {
			if slices.ContainsFunc(all[i+1:], func(l laneSend) bool { return moot(e, l) }) {
				return i
			}
		}
	}
	return 0 // unreachable past three waiting sends
}

// run queues next on lane, after the lane's earlier sends, reporting false
// once the lanes are closed.
func (l *scrobbleLanes) run(lane string, next laneSend) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	if l.ctx == nil {
		l.ctx, l.cancel = context.WithCancel(context.Background())
	}
	if waiting, busy := l.lanes[lane]; busy {
		if len(waiting) >= maxQueuedScrobbles {
			i := overflow(waiting, next)
			waiting[i].send(nil)
			waiting = slices.Delete(waiting, i, i+1)
		}
		l.lanes[lane] = append(waiting, next)
		return true
	}
	if l.lanes == nil {
		l.lanes = make(map[string][]laneSend)
	}
	l.lanes[lane] = nil
	ctx := l.ctx
	go func() {
		for {
			next.send(ctx)
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
	return true
}

// close takes no more sends and cancels those in flight; the ones still
// waiting go nowhere as their lanes reach them.
func (l *scrobbleLanes) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}
}

// sendScrobble sends one scrobble to each target in the background, on that
// target's lane for the stream and title (lane, clientPlaybackKey): after the
// lane's earlier sends, and before its later ones. Nothing goes out once the
// shutdown drain is over.
func (s *Server) sendScrobble(targets []scrobble.Target, lane, contentID, verb string, item scrobble.Item, progress float64) {
	for _, target := range targets {
		// A target that cannot address this item is skipped: anime only
		// anime-lists' aired mapping names has no Simkl address, and anime only
		// MAL names has no MDBList one. Neither is a failure.
		if !target.Supports(item) {
			logger.Debug("Scrobble skipped: the target cannot place this item", "target", target.Name(), "content", contentID)
			continue
		}
		s.scrobbleSends.Add(1)
		queued := s.scrobbleLanes.run(target.Name()+"|"+lane, laneSend{verb: verb, progress: progress, send: func(ctx context.Context) {
			defer s.scrobbleSends.Done()
			switch {
			case ctx == nil:
				logger.Debug("Scrobble superseded: a later send for the title makes it moot", "target", target.Name(), "verb", verb, "content", contentID, "progress", int(progress))
				return
			case ctx.Err() != nil:
				logger.Debug("Scrobble dropped: the server shut down before it went out", "target", target.Name(), "verb", verb, "content", contentID)
				return
			}
			ctx, cancel := context.WithTimeout(ctx, scrobbleRequestTimeout)
			defer cancel()
			if err := target.Scrobble(ctx, verb, item, progress); err != nil {
				logger.Debug("Scrobble failed", "target", target.Name(), "verb", verb, "content", contentID, "err", err)
				return
			}
			logger.Debug("Scrobble sent", "target", target.Name(), "verb", verb, "content", contentID, "progress", int(progress))
		}})
		if !queued {
			s.scrobbleSends.Done()
			logger.Debug("Scrobble dropped: the server is shut down", "target", target.Name(), "verb", verb, "content", contentID)
		}
	}
}

// pendingScrobbles counts the scrobble sends queued or in flight, the
// fallback timers that may make one, and the serves that may decide one as
// they end. Unlike a WaitGroup it may be waited on while more is still being
// added, as the shutdown drain is while cut connections unwind.
type pendingScrobbles struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n falls to 0; nil while it is 0
}

func (p *pendingScrobbles) Add(delta int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n += delta
	switch {
	case p.n > 0 && p.idle == nil:
		p.idle = make(chan struct{})
	case p.n == 0 && p.idle != nil:
		close(p.idle)
		p.idle = nil
	}
}

func (p *pendingScrobbles) Done() { p.Add(-1) }

// Wait waits until nothing is pending.
func (p *pendingScrobbles) Wait() { p.waitFor(-1) }

// waitFor waits until nothing is pending, or for d when d is not negative,
// reporting whether nothing is.
func (p *pendingScrobbles) waitFor(d time.Duration) bool {
	p.mu.Lock()
	idle := p.idle
	p.mu.Unlock()
	if idle == nil {
		return true
	}
	if d < 0 {
		<-idle
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}

// drainScrobbles is Shutdown's part in scrobbling: serves' pending fallbacks
// are decided now, and what has been decided — those, and every send already
// queued or made by a serve the shutdown cut — gets the drain to go out.
// Then nothing more is taken, what is in flight is cancelled, and what still
// waits is dropped.
func (s *Server) drainScrobbles() {
	s.shutdownClientPlayback()
	if !s.scrobbleSends.waitFor(s.clientPlayback.timing().shutdownDrain) {
		logger.Info("Scrobbles still unsent at shutdown were dropped", "drain", s.clientPlayback.timing().shutdownDrain)
	}
	s.scrobbleLanes.close()
}

// logUnlinkedScrobble notes a stream that has scrobbling switched on but no
// linked account to take it — otherwise indistinguishable from scrobbling off.
func logUnlinkedScrobble(stream *auth.Stream, verb string) {
	if stream.ScrobbleEnabled() {
		logger.Debug("Scrobbling is on but no linked account can take it", "stream", streamLogName(stream), "verb", verb)
	}
}
