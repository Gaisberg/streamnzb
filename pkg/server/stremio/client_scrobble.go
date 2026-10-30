package stremio

import (
	"sync"
	"sync/atomic"
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
// leaves its start and stop to the client's reports, and falls back to the
// transport's own when that client has reported nothing within
// clientReportGrace — the case of a client that does not report. Every other
// serve, Stremio's included, scrobbles from the transport as it always has,
// whatever a Jellyfin client is doing.
//
// Reports are tracked per playback: one device's one play session, not the
// media session, which every device playing the title shares and which
// outlives a playback by hours. A playback starts with the client's first
// report, and ends with its stop — or with a new play session from the same
// device, which stops it at its last confirmed position if it never stopped.
// A start repeated within the play session (after a seek, or coming back from
// the background) is the same playback. Reports after a stop are a resume, a
// new playback, except a repeated stop and progress that trails the stop by
// less than clientStopSettle, which is the stopped playback's own late report.
// One that reports nothing at all for clientPlaybackIdleTTL is taken as gone —
// an app closed mid-play — and its last confirmed position goes out as the
// stop it never sent. Nothing is ever reported past a position the client
// confirmed, and nothing is reported without one.
//
// What a title is scrobbled as — its ids and runtime — is taken from the media
// session while it is live and kept with the title, so a player still
// reporting from its buffer after the session was evicted is still scrobbled.

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
	// Device is the client's DeviceId, empty when it sent none. One device
	// plays one thing at a time, so its playbacks of a title replace each
	// other.
	Device string
	// Client names the app and device, and Address is where the report came
	// from. Together they stand in for a missing Device.
	Client, Address string
	// PlaySession is the client's play session, empty when it sent none. A
	// different one from the same device is a new playback.
	PlaySession string
	Event       ClientPlaybackEvent
	// PositionSeconds is where the player is, negative when the report
	// carried no position.
	PositionSeconds float64
}

// playbackKey is the device a report tracks a playback under.
func (p ClientPlayback) playbackKey() string {
	if p.Device != "" {
		return "id:" + p.Device
	}
	// shortcut: two clients with no DeviceId, the same app and device name, and
	// one address (a reverse proxy) still share a playback; none has been seen.
	return "anon:" + p.Client + "|" + p.Address
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
	// clientStopSettle is how long after a stop a progress report is still
	// taken for the stopped playback's own, sent before the stop and delivered
	// after it, rather than a resume.
	clientStopSettle = 10 * time.Second
	// clientReportGrace is how long a reporting client's serve waits, once it
	// qualifies or ends, for the client's first report before taking the
	// client for one that does not report: a player may open and drop a
	// request before it says it is playing.
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
	started     bool      // the client's start has gone out
	stopped     bool      // the client's stop has been handled
	stoppedAt   time.Time // when it was
	lastPct     float64   // last confirmed position as a percentage, -1 when unknown
	// replaced is the play session this playback replaced: that one has
	// stopped, and a report it sends late is ignored.
	replaced string
	timer    *time.Timer
	timerGen int // counts the timer's arming; a callback from an earlier one is stale
}

// clientItem is what a title is scrobbled as, taken from a live media session.
// It holds ids and a runtime: nothing of the session, nor of the account.
type clientItem struct {
	item        scrobble.Item
	addressable bool
	runtime     float64 // seconds, 0 when unknown
}

// clientTitle is what is known about one stream's title: the media session
// the transport last served it from (a report names the title, not the
// session, which failover can change mid-play), what it is scrobbled as, and
// when each device and address last reported it.
type clientTitle struct {
	sessionID  string
	servedAt   time.Time
	reportedAt time.Time
	byDevice   map[string]time.Time
	byAddress  map[string]time.Time
	item       *clientItem
}

type clientPlaybacks struct {
	mu       sync.Mutex
	attempts map[string]map[string]*clientAttempt // stream|content → device
	count    int
	titles   map[string]*clientTitle // stream|content
}

// reportingServe is one serve to a client that reports its own playback, and
// what tells its client's reports apart from another's: the DeviceId the
// request carried, or failing that its address.
type reportingServe struct {
	device, address string
	began           time.Time
	// startDecided is set once the serve has qualified and settled whether
	// its client or the transport sends its start.
	startDecided atomic.Bool
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
	t := &clientTitle{byDevice: map[string]time.Time{}, byAddress: map[string]time.Time{}}
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

// reportedBy reports whether the client a serve went to has reported the
// stream's title since shortly before the serve began.
func (c *clientPlaybacks) reportedBy(stream *auth.Stream, contentID string, rs *reportingServe) bool {
	if stream == nil || rs == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.titles[clientPlaybackKey(stream, contentID)]
	if t == nil {
		return false
	}
	at := t.byAddress[rs.address]
	if rs.device != "" {
		at = t.byDevice[rs.device]
	}
	return !at.IsZero() && !at.Before(rs.began.Add(-clientReportLead))
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
	verb string
	item *clientItem // nil when no live session ever placed the title
	pct  float64
}

// apply folds one report into its playback and returns what to send, in
// order. live is what the title's live media session says it is, nil when
// there is none. A report that cannot be tracked is not recorded as reported,
// so the transport still scrobbles that playback.
func (c *clientPlaybacks) apply(stream *auth.Stream, p ClientPlayback, live *clientItem, expire func(key, device string, a *clientAttempt, gen int)) []clientScrobble {
	key := clientPlaybackKey(stream, p.ContentID)
	device := p.playbackKey()
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempts == nil {
		c.attempts = make(map[string]map[string]*clientAttempt)
	}
	a := c.attempts[key][device]
	if a != nil && p.PlaySession != "" && p.PlaySession == a.replaced {
		// shortcut: only the last replaced play session is remembered; a client
		// switching sessions twice before the first one's late report lands
		// would still have it taken for a new playback.
		return nil
	}
	// Only a play session different from the one the playback has is a new
	// playback: a client that names it on some reports and not others is
	// still in the one it named.
	newPlayback := a == nil || (p.PlaySession != "" && a.playSession != "" && p.PlaySession != a.playSession)
	if !newPlayback && a.stopped {
		if p.Event == ClientPlaybackStop || (p.Event == ClientPlaybackProgress && now.Sub(a.stoppedAt) < clientStopSettle) {
			// A repeated stop, or the stopped playback's late progress.
			return nil
		}
		newPlayback = true // resumed
	}
	title := c.titleLocked(key, now)
	if title == nil || (a == nil && c.count >= maxClientPlaybacks) {
		return nil
	}
	title.reportedAt = now
	if p.Device != "" {
		title.byDevice[p.Device] = now
	}
	if p.Address != "" {
		title.byAddress[p.Address] = now
	}
	if live != nil {
		if live.runtime <= 0 && title.item != nil {
			live.runtime = title.item.runtime
		}
		title.item = live
	}
	pct := -1.0
	if title.item != nil && title.item.runtime > 0 && p.PositionSeconds >= 0 {
		pct = scrobble.ClampProgress(p.PositionSeconds / title.item.runtime * 100)
	}
	var out []clientScrobble
	if newPlayback {
		// The device's earlier playback of the title is over, stopped or not:
		// a device plays one thing at a time. One that never stopped stops
		// now, at the last position it confirmed, ahead of the new one.
		replaced := ""
		if a != nil {
			c.removeLocked(key, device, a)
			if a.started && !a.stopped && a.lastPct >= 0 {
				out = append(out, clientScrobble{verb: scrobble.VerbStop, item: title.item, pct: a.lastPct})
			}
			replaced = a.replaced
			// Only a named play session switching to another named one is
			// replaced; a report naming none is still in the one it had.
			if p.PlaySession != "" && a.playSession != "" && a.playSession != p.PlaySession {
				replaced = a.playSession
			}
		}
		a = &clientAttempt{stream: stream, playSession: p.PlaySession, lastPct: -1, replaced: replaced}
		if c.attempts[key] == nil {
			c.attempts[key] = make(map[string]*clientAttempt)
		}
		c.attempts[key][device] = a
		c.count++
	}
	if a.playSession == "" {
		a.playSession = p.PlaySession
	}
	if pct >= 0 {
		a.lastPct = pct
	}
	wait := clientPlaybackIdleTTL
	switch p.Event {
	case ClientPlaybackStop:
		a.stopped, a.stoppedAt = true, now
		wait = clientPlaybackStopGrace
		out = append(out, clientScrobble{verb: scrobble.VerbStop, item: title.item, pct: a.lastPct})
	default:
		// The start waits for a live session to place the title, so a client
		// that reports before its stream is open starts on its next report.
		if !a.started && title.item != nil {
			a.started = true
			out = append(out, clientScrobble{verb: scrobble.VerbStart, item: title.item, pct: max(a.lastPct, 0)})
		}
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timerGen++
	gen := a.timerGen
	a.timer = time.AfterFunc(wait, func() { expire(key, device, a, gen) })
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

// stopTimers stops every playback's idle timer, for a server shutting down.
func (c *clientPlaybacks) stopTimers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, devices := range c.attempts {
		for _, a := range devices {
			a.timer.Stop()
		}
	}
}

// release drops a playback whose time ran out, reporting whether it was still
// the current one and, if so, the stop it never sent. gen is the arming that
// fired: a report that re-armed the timer while it fired has kept the
// playback alive.
func (c *clientPlaybacks) release(key, device string, a *clientAttempt, gen int) (clientScrobble, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempts[key][device] != a || a.timerGen != gen {
		return clientScrobble{}, false
	}
	c.removeLocked(key, device, a)
	if a.stopped || !a.started || a.lastPct < 0 {
		return clientScrobble{}, false
	}
	var item *clientItem
	if t := c.titles[key]; t != nil {
		item = t.item
	}
	return clientScrobble{verb: scrobble.VerbStop, item: item, pct: a.lastPct}, true
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
	var live *clientItem
	// Peeked, not fetched: a client reporting from its buffer must not keep
	// an idle session from being evicted.
	sess := s.sessionManager.PeekSession(s.clientPlayback.servedSession(clientPlaybackKey(stream, p.ContentID)))
	if sess != nil && sess.ContentID == p.ContentID {
		item, ok := s.scrobbleItemForSession(sess)
		live = &clientItem{item: item, addressable: ok, runtime: s.clientRuntimeSeconds(stream, sess)}
	}
	// One report's sends go out in order — a replaced playback's stop ahead
	// of the new one's start — or a target could take the stop last.
	var after *sync.WaitGroup
	for _, out := range s.clientPlayback.apply(stream, p, live, s.expireClientPlayback) {
		after = s.sendClientScrobble(stream, p.ContentID, out, after)
	}
}

func (s *Server) expireClientPlayback(key, device string, a *clientAttempt, gen int) {
	if out, ok := s.clientPlayback.release(key, device, a, gen); ok {
		logger.Debug("Client playback went quiet; reporting its last confirmed position", "content", key, "progress", int(out.pct))
		s.sendClientScrobble(a.stream, key, out, nil)
	}
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

// sendClientScrobble sends out once the sends in after are done, returning
// what a later send should wait on.
func (s *Server) sendClientScrobble(stream *auth.Stream, contentID string, out clientScrobble, after *sync.WaitGroup) *sync.WaitGroup {
	if out.item == nil {
		logger.Debug("Client playback not scrobbled: no playback session ever placed the title", "stream", streamLogName(stream), "content", contentID, "verb", out.verb)
		return after
	}
	if out.verb == scrobble.VerbStop && out.pct < 0 {
		// No runtime ever placed a position for this playback. The session's
		// served estimate is no stand-in: it spans every playback the session
		// has served.
		logger.Debug("Client playback stop not scrobbled: no runtime to place its position", "content", contentID)
		return after
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, out.verb)
		return after
	}
	if !out.item.addressable {
		logger.Debug("Client playback not scrobbled: no id a target can place", "content", contentID, "verb", out.verb)
		return after
	}
	return s.sendScrobble(targets, contentID, out.verb, out.item.item, out.pct, after)
}
