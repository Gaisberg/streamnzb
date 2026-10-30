package stremio

import (
	"sync"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/services/metadata/scrobble"
	"streamnzb/pkg/session"
)

// Client-reported playback. A Jellyfin client says where it is
// (Sessions/Playing), which the HTTP transport cannot see once a player has
// buffered the file: the serve that delivered it ended long before the viewer
// did. So a serve to such a client (PlayServeOptions.ClientReportsPlayback)
// never sends the transport's start, and sends its stop only when the client
// has not reported the title at all — the fallback for clients that do not
// report. Every other serve, Stremio's included, scrobbles from the transport
// as it always has, whatever a Jellyfin client is doing.
//
// Reports are tracked per playback: one device's one play session, not the
// media session, which every device playing the title shares and which
// outlives a playback by hours. A playback starts with the client's first
// report, or a new play session from the same device, and ends with its stop
// — or with that new play session, which stops it at its last confirmed
// position if it never stopped.
// One that reports nothing at all for clientPlaybackIdleTTL is taken as gone —
// an app closed mid-play — and its last confirmed position goes out as the
// stop it never sent. Nothing is ever reported past a position the client
// confirmed, and nothing is reported without one.

// ClientPlaybackEvent is what a client report says happened.
type ClientPlaybackEvent int

const (
	ClientPlaybackStart ClientPlaybackEvent = iota
	ClientPlaybackProgress
	ClientPlaybackStop
)

// ClientPlayback is one client report about one title.
type ClientPlayback struct {
	// ContentID is the addon content id the title plays under.
	ContentID string
	// Device is the reporting client's device. One device plays one thing at
	// a time, so its playbacks of a title replace each other.
	Device string
	// PlaySession is the client's play session, empty when it sent none. A
	// different one from the same device is a new playback.
	PlaySession string
	Event       ClientPlaybackEvent
	// PositionSeconds is where the player is, negative when the report
	// carried no position.
	PositionSeconds float64
}

var (
	// clientPlaybackIdleTTL is how long a playback may report nothing before
	// it counts as gone. Clients report every few seconds while playing and on
	// every pause and resume; the window is long so a paused player is not
	// taken for a closed one.
	clientPlaybackIdleTTL = 30 * time.Minute
	// clientPlaybackStopGrace keeps a stopped playback around so a client
	// repeating its stop does not report twice.
	clientPlaybackStopGrace = 2 * time.Minute
	// clientReportGrace is how long a reporting client's serve waits, once
	// it ends, for the client's first report before taking the client for one
	// that does not report: a player may open and drop a request before it
	// says it is playing.
	clientReportGrace = time.Minute
)

const (
	// maxClientPlaybacks bounds the playbacks, and the titles, tracked at
	// once. Past it a new one is not tracked, and its serves scrobble from the
	// transport.
	maxClientPlaybacks = 1000
	// clientReportLead is how long before a serve began a client report still
	// counts as about that serve: clients report the start as they open the
	// stream, which can land just ahead of the request that serves it.
	clientReportLead = time.Minute
)

type clientAttempt struct {
	stream      *auth.Stream
	playSession string
	sessionID   string
	started     bool    // the client's start has gone out
	stopped     bool    // the client's stop has been handled
	lastPct     float64 // last confirmed position as a percentage, -1 when unknown
	timer       *time.Timer
}

// clientTitle is what is known about one stream's title: the media session
// the transport last served it from (a report names the title, not the
// session, which failover can change mid-play) and when a client last
// reported it.
type clientTitle struct {
	sessionID  string
	servedAt   time.Time
	reportedAt time.Time
}

type clientPlaybacks struct {
	mu       sync.Mutex
	attempts map[string]map[string]*clientAttempt // stream|content → device
	count    int
	titles   map[string]*clientTitle // stream|content
}

func clientPlaybackKey(stream *auth.Stream, contentID string) string {
	return stream.Username + "|" + contentID
}

// titleLocked returns the title's entry, making room for a new one when the
// table is full, or nil when it cannot.
func (c *clientPlaybacks) titleLocked(key string, now time.Time) *clientTitle {
	if t := c.titles[key]; t != nil {
		return t
	}
	if c.titles == nil {
		c.titles = make(map[string]*clientTitle)
	}
	if len(c.titles) >= maxClientPlaybacks {
		for k, t := range c.titles {
			if now.Sub(t.servedAt) > clientPlaybackIdleTTL && now.Sub(t.reportedAt) > clientPlaybackIdleTTL {
				delete(c.titles, k)
			}
		}
		if len(c.titles) >= maxClientPlaybacks {
			return nil
		}
	}
	t := &clientTitle{}
	c.titles[key] = t
	return t
}

// noteServed records which media session a stream's title is being served
// from.
func (c *clientPlaybacks) noteServed(stream *auth.Stream, sess *session.Session) {
	if stream == nil || sess == nil || sess.ContentID == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.titleLocked(clientPlaybackKey(stream, sess.ContentID), now); t != nil {
		t.sessionID, t.servedAt = sess.ID, now
	}
}

// reportedSince reports whether a client reported the stream's title at or
// after since.
func (c *clientPlaybacks) reportedSince(stream *auth.Stream, contentID string, since time.Time) bool {
	if stream == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.titles[clientPlaybackKey(stream, contentID)]
	return t != nil && !t.reportedAt.IsZero() && !t.reportedAt.Before(since)
}

func (c *clientPlaybacks) servedSession(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.titles[key]; t != nil {
		return t.sessionID
	}
	return ""
}

// clientScrobble is what one report decided to send.
type clientScrobble struct {
	verb      string
	sessionID string
	pct       float64
}

// apply folds one report into its playback and returns what to send, in
// order. sessionID is the live media session serving the title, empty when
// there is none; pct is the report's position as a percentage, -1 when
// unknown. A report that cannot be tracked is not recorded as reported, so
// the transport still scrobbles that playback.
func (c *clientPlaybacks) apply(stream *auth.Stream, p ClientPlayback, sessionID string, pct float64, expire func(key, device string, a *clientAttempt)) []clientScrobble {
	key := clientPlaybackKey(stream, p.ContentID)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempts == nil {
		c.attempts = make(map[string]map[string]*clientAttempt)
	}
	a := c.attempts[key][p.Device]
	newPlayback := a == nil || p.Event == ClientPlaybackStart ||
		(p.PlaySession != "" && p.PlaySession != a.playSession)
	if !newPlayback && a.stopped {
		// A repeated or late report for a playback already stopped.
		return nil
	}
	title := c.titleLocked(key, now)
	if title == nil || (a == nil && c.count >= maxClientPlaybacks) {
		return nil
	}
	title.reportedAt = now
	var out []clientScrobble
	if newPlayback {
		// The device's earlier playback of the title is over, stopped or not:
		// a device plays one thing at a time. One that never stopped stops
		// now, at the last position it confirmed, ahead of the new one.
		if a != nil {
			c.removeLocked(key, p.Device, a)
			if a.started && !a.stopped && a.lastPct >= 0 {
				out = append(out, clientScrobble{verb: scrobble.VerbStop, sessionID: a.sessionID, pct: a.lastPct})
			}
		}
		a = &clientAttempt{stream: stream, playSession: p.PlaySession, lastPct: -1}
		if c.attempts[key] == nil {
			c.attempts[key] = make(map[string]*clientAttempt)
		}
		c.attempts[key][p.Device] = a
		c.count++
	}
	if sessionID != "" {
		a.sessionID = sessionID
	}
	if pct >= 0 {
		a.lastPct = pct
	}
	wait := clientPlaybackIdleTTL
	switch p.Event {
	case ClientPlaybackStop:
		a.stopped = true
		wait = clientPlaybackStopGrace
		out = append(out, clientScrobble{verb: scrobble.VerbStop, sessionID: a.sessionID, pct: a.lastPct})
	default:
		// The start waits for a live session to address it by, so a client
		// that reports before its stream is open starts on its next report.
		if !a.started && a.sessionID != "" {
			a.started = true
			out = append(out, clientScrobble{verb: scrobble.VerbStart, sessionID: a.sessionID, pct: max(a.lastPct, 0)})
		}
	}
	device := p.Device
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(wait, func() { expire(key, device, a) })
	return out
}

func (c *clientPlaybacks) removeLocked(key, device string, a *clientAttempt) {
	if a.timer != nil {
		a.timer.Stop()
	}
	delete(c.attempts[key], device)
	if len(c.attempts[key]) == 0 {
		delete(c.attempts, key)
	}
	c.count--
}

// release drops a playback whose time ran out, reporting whether it was still
// the current one and, if so, the stop it never sent.
func (c *clientPlaybacks) release(key, device string, a *clientAttempt) (clientScrobble, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempts[key][device] != a {
		return clientScrobble{}, false
	}
	c.removeLocked(key, device, a)
	if a.stopped || !a.started || a.lastPct < 0 {
		return clientScrobble{}, false
	}
	return clientScrobble{verb: scrobble.VerbStop, sessionID: a.sessionID, pct: a.lastPct}, true
}

// ReportClientPlayback takes one client report — Jellyfin's Sessions/Playing
// family — and scrobbles from it. It never blocks on a target: sends go out
// in the background, so a report is answered whatever the targets do.
func (s *Server) ReportClientPlayback(stream *auth.Stream, p ClientPlayback) {
	if s == nil || stream == nil || p.ContentID == "" {
		return
	}
	if len(scrobbleTargets(s.runtime(), stream)) == 0 {
		return
	}
	sessionID, pct := "", -1.0
	if sess := s.liveSession(s.clientPlayback.servedSession(clientPlaybackKey(stream, p.ContentID))); sess != nil {
		sessionID = sess.ID
		if runtime := s.clientRuntimeSeconds(stream, sess); runtime > 0 && p.PositionSeconds >= 0 {
			pct = scrobble.ClampProgress(p.PositionSeconds / runtime * 100)
		}
	}
	for _, out := range s.clientPlayback.apply(stream, p, sessionID, pct, s.expireClientPlayback) {
		s.sendClientScrobble(stream, out)
	}
}

func (s *Server) expireClientPlayback(key, device string, a *clientAttempt) {
	if out, ok := s.clientPlayback.release(key, device, a); ok {
		logger.Debug("Client playback went quiet; reporting its last confirmed position", "content", key, "progress", int(out.pct))
		s.sendClientScrobble(a.stream, out)
	}
}

func (s *Server) liveSession(id string) *session.Session {
	if id == "" || s.sessionManager == nil {
		return nil
	}
	sess, _ := s.sessionManager.GetSession(id)
	return sess
}

// clientRuntimeSeconds is the runtime a client position is measured against:
// the file's own duration, then the metadata runtime the candidate list
// carried.
func (s *Server) clientRuntimeSeconds(stream *auth.Stream, sess *session.Session) float64 {
	if d := sess.MediaDurationSeconds(); d > 0 {
		return d
	}
	if view, ok := s.PlaylistCached(stream, sess.ContentType, sess.ContentID); ok {
		return view.RuntimeSeconds
	}
	return 0
}

func (s *Server) sendClientScrobble(stream *auth.Stream, out clientScrobble) {
	sess := s.liveSession(out.sessionID)
	if sess == nil {
		logger.Debug("Client playback not scrobbled: no playback session to address it by", "stream", streamLogName(stream), "verb", out.verb)
		return
	}
	if out.verb == scrobble.VerbStop && out.pct < 0 {
		// No runtime ever placed a position for this playback. The session's
		// served estimate is no stand-in: it spans every playback the session
		// has served.
		logger.Debug("Client playback stop not scrobbled: no runtime to place its position", "content", sess.ContentID)
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, out.verb)
		return
	}
	item, ok := s.scrobbleItemForSession(sess)
	if !ok {
		logger.Debug("Client playback not scrobbled: no id a target can place", "content", sess.ContentID, "verb", out.verb)
		return
	}
	s.sendScrobble(targets, sess, out.verb, item, out.pct)
}
