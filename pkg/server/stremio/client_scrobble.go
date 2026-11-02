package stremio

import (
	"strings"
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
// transport's own when that client has reported nothing within the report
// grace — the case of a client that does not report. Every other serve,
// Stremio's included, scrobbles from the transport as it always has, whatever
// a Jellyfin client is doing.
//
// A serve is matched to its own client's reports, and only to reports about
// it: ones made while it runs, or a start made just ahead of it. A previous
// playback's stop is no proof the client reports this one. When nothing can
// tell the serve's client from another (reportingServe.matches), the serve is
// taken for a client that does not report: a playback scrobbled twice is
// better than one not scrobbled at all.
//
// Reports are tracked per playback: one device's one play session, not the
// media session, which every device playing the title shares and which
// outlives a playback by hours. A playback starts with the client's first
// report, and ends with its stop — or with a new play session from the same
// device, which stops it at its last confirmed position if it never stopped.
// A start repeated within the play session (after a seek, or coming back from
// the background) is the same playback. Reports after a stop are a resume, a
// new playback, except a repeated stop and progress that trails the stop by
// less than the stop settle, which is the stopped playback's own late report.
// One that reports nothing at all for the idle TTL is taken as gone — an app
// closed mid-play — and its last confirmed position goes out as the stop it
// never sent. Nothing is ever reported past a position the client confirmed,
// and nothing is reported without one.
//
// What a title is scrobbled as — its ids and runtime — is taken from the media
// session while it is live and kept with the title, so a player still
// reporting from its buffer after the session was evicted is still scrobbled.
// Sends go out in the order they were decided (scrobbleLanes).

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

// clientPlaybackTimings are how long client playback tracking waits, and how
// much it tracks. Each server has its own, so a test shortening them on its
// server races nothing another test's server is still running.
type clientPlaybackTimings struct {
	// idleTTL is how long a playback may report nothing before it counts as
	// gone. Clients report every few seconds while playing and on every pause
	// and resume; the window is long so a paused player is not taken for a
	// closed one.
	idleTTL time.Duration
	// stopGrace keeps a stopped playback around so a client repeating its
	// stop does not report twice.
	stopGrace time.Duration
	// stopSettle is how long after a stop a progress report is still taken
	// for the stopped playback's own, sent before the stop and delivered after
	// it, rather than a resume.
	stopSettle time.Duration
	// reportGrace is how long a reporting client's serve waits, once it
	// qualifies or ends, for the client's first report before taking the
	// client for one that does not report: a player may open and drop a
	// request before it says it is playing.
	reportGrace time.Duration
	// limit bounds the playbacks, and the titles, tracked at once. Past it a
	// new one is not tracked, and its serves scrobble from the transport.
	limit int
}

var defaultClientPlaybackTimings = clientPlaybackTimings{
	idleTTL:     30 * time.Minute,
	stopGrace:   2 * time.Minute,
	stopSettle:  10 * time.Second,
	reportGrace: time.Minute,
	limit:       1000,
}

// clientReportLead is how long before a serve began a client's start still
// counts as about that serve: clients report the start as they open the
// stream, which can land just ahead of the request that serves it.
const clientReportLead = time.Minute

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
// the transport last served it from to a reporting client (a report names the
// title, not the session, which failover can change mid-play), what it is
// scrobbled as, the reports a serve beginning now could still be about, and
// the reporting serves waiting to learn whether their client reports.
type clientTitle struct {
	sessionID string
	active    time.Time // last served or reported
	item      *clientItem
	reporters map[string]reportMark // reporterKey → its last report, within clientReportLead
	serves    map[*reportingServe]struct{}
}

// reportMark is one client's last report of a title.
type reportMark struct {
	device, address string
	at              time.Time
	stop            bool
}

func reporterKey(device, address string) string {
	if device != "" {
		return "id:" + device
	}
	return "anon:" + address
}

type clientPlaybacks struct {
	mu       sync.Mutex
	timings  clientPlaybackTimings                // zero for defaultClientPlaybackTimings
	attempts map[string]map[string]*clientAttempt // stream|content → device
	count    int
	titles   map[string]*clientTitle // stream|content
	// scheduled are the serves with a fallback timer pending.
	scheduled map[*reportingServe]struct{}
	closed    bool // shut down: nothing new is tracked or scheduled
}

// reportingServe is one serve to a client that reports its own playback.
type reportingServe struct {
	// device is the DeviceId the request carried, empty when none, and
	// deviceFromURL marks one read off the stream URL rather than the
	// request's own headers.
	device        string
	deviceFromURL bool
	address       string
	began         time.Time
	// reported is set once the serve's client has reported the title: while
	// the serve runs, or with a start just ahead of it.
	reported atomic.Bool
	// startDecided is set once the serve has qualified and settled whether
	// its client or the transport sends its start.
	startDecided atomic.Bool
	// start is the transport's fallback start, nil when none is due, run at
	// most once; the fallback stop runs it first, so a start still due goes
	// out ahead of the stop.
	startOnce sync.Once
	start     func()        // guarded by clientPlaybacks.mu
	timers    []*time.Timer // pending fallbacks; guarded by clientPlaybacks.mu
}

// matches reports whether a report from device at address is from the serve's
// own client. A DeviceId the request sent itself is matched as it is. One read
// off the stream URL names the device the URL was handed to, which a cached or
// shared URL may not be, so it counts only from the serve's own address. A
// serve naming no device is matched only by a report naming none from its
// address: a device that names itself is another client, even behind the same
// reverse proxy. Addresses only ever narrow a match; none makes one alone.
func (rs *reportingServe) matches(device, address string) bool {
	switch {
	case rs.device == "":
		return device == "" && address == rs.address
	case rs.deviceFromURL:
		return device == rs.device && address == rs.address
	default:
		return device == rs.device
	}
}

func (c *clientPlaybacks) timing() clientPlaybackTimings {
	if c.timings == (clientPlaybackTimings{}) {
		return defaultClientPlaybackTimings
	}
	return c.timings
}

func clientPlaybackKey(stream *auth.Stream, contentID string) string {
	return stream.Username + "|" + contentID
}

// titleLocked returns the title's entry, making room for a new one when the
// table is full, or nil when it cannot.
func (c *clientPlaybacks) titleLocked(key string, now time.Time) *clientTitle {
	if t := c.titles[key]; t != nil {
		t.active = now
		for k, m := range t.reporters {
			if now.Sub(m.at) > clientReportLead {
				delete(t.reporters, k)
			}
		}
		return t
	}
	if c.titles == nil {
		c.titles = make(map[string]*clientTitle)
	}
	c.reclaimLocked(now, false)
	if len(c.titles) >= c.timing().limit {
		c.reclaimLocked(now, true)
		if len(c.titles) >= c.timing().limit {
			return nil
		}
	}
	t := &clientTitle{active: now, reporters: map[string]reportMark{}, serves: map[*reportingServe]struct{}{}}
	c.titles[key] = t
	return t
}

// reclaimLocked drops the titles nothing is playing or serving: those idle for
// the idle TTL, or every one when all is set. What they kept only helps a
// client resuming from its buffer long after it stopped.
func (c *clientPlaybacks) reclaimLocked(now time.Time, all bool) {
	for k, t := range c.titles {
		if len(t.serves) == 0 && len(c.attempts[k]) == 0 && (all || now.Sub(t.active) > c.timing().idleTTL) {
			delete(c.titles, k)
		}
	}
}

// noteServed records which media session a stream's title is being served
// from to a reporting client.
func (c *clientPlaybacks) noteServed(stream *auth.Stream, sess *session.Session) {
	if stream == nil || sess == nil || sess.ContentID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if t := c.titleLocked(clientPlaybackKey(stream, sess.ContentID), time.Now()); t != nil {
		t.sessionID = sess.ID
	}
}

// register starts matching the title's reports to a reporting serve. A start
// its client made just ahead of the serve counts; a stop from before the
// serve began is the previous playback's. A serve the table has no room for
// is never matched, so the transport scrobbles it.
func (c *clientPlaybacks) register(stream *auth.Stream, contentID string, rs *reportingServe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	t := c.titleLocked(clientPlaybackKey(stream, contentID), time.Now())
	if t == nil {
		return
	}
	for _, m := range t.reporters {
		if rs.matches(m.device, m.address) && !m.at.Before(rs.began.Add(-clientReportLead)) && (!m.stop || !m.at.Before(rs.began)) {
			rs.reported.Store(true)
		}
	}
	t.serves[rs] = struct{}{}
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

// apply folds one report into its playback and hands send what to send, in
// order, while still holding the lock: a later report's sends are queued
// behind this one's. live is what the title's live media session says it is,
// nil when there is none. A report that cannot be tracked is not recorded as
// reported, so the transport still scrobbles that playback.
func (c *clientPlaybacks) apply(stream *auth.Stream, p ClientPlayback, live *clientItem, expire func(key, device string, a *clientAttempt, gen int), send func(clientScrobble)) {
	key := clientPlaybackKey(stream, p.ContentID)
	device := p.playbackKey()
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.attempts == nil {
		c.attempts = make(map[string]map[string]*clientAttempt)
	}
	a := c.attempts[key][device]
	if a != nil && p.PlaySession != "" && p.PlaySession == a.replaced {
		// shortcut: only the last replaced play session is remembered; a client
		// switching sessions twice before the first one's late report lands
		// would still have it taken for a new playback.
		return
	}
	// Only a play session different from the one the playback has is a new
	// playback: a client that names it on some reports and not others is
	// still in the one it named.
	newPlayback := a == nil || (p.PlaySession != "" && a.playSession != "" && p.PlaySession != a.playSession)
	if !newPlayback && a.stopped {
		if p.Event == ClientPlaybackStop || (p.Event == ClientPlaybackProgress && now.Sub(a.stoppedAt) < c.timing().stopSettle) {
			// A repeated stop, or the stopped playback's late progress.
			return
		}
		newPlayback = true // resumed
	}
	title := c.titleLocked(key, now)
	if title == nil || (a == nil && c.count >= c.timing().limit) {
		return
	}
	title.reporters[reporterKey(p.Device, p.Address)] = reportMark{device: p.Device, address: p.Address, at: now, stop: p.Event == ClientPlaybackStop}
	for rs := range title.serves {
		if rs.matches(p.Device, p.Address) {
			rs.reported.Store(true)
		}
	}
	// Newer is better only when a target can place it: a session closing, or
	// one opened without ids, would leave the final stop nothing to go by.
	if live != nil && (live.addressable || title.item == nil || !title.item.addressable) {
		if live.runtime <= 0 && title.item != nil {
			live.runtime = title.item.runtime
		}
		title.item = live
	}
	pct := -1.0
	if title.item != nil && title.item.runtime > 0 && p.PositionSeconds >= 0 {
		pct = scrobble.ClampProgress(p.PositionSeconds / title.item.runtime * 100)
	}
	if newPlayback {
		// The device's earlier playback of the title is over, stopped or not:
		// a device plays one thing at a time. One that never stopped stops
		// now, at the last position it confirmed, ahead of the new one.
		replaced := ""
		if a != nil {
			c.removeLocked(key, device, a)
			if a.started && !a.stopped && a.lastPct >= 0 {
				send(clientScrobble{verb: scrobble.VerbStop, item: title.item, pct: a.lastPct})
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
	wait := c.timing().idleTTL
	switch p.Event {
	case ClientPlaybackStop:
		a.stopped, a.stoppedAt = true, now
		wait = c.timing().stopGrace
		send(clientScrobble{verb: scrobble.VerbStop, item: title.item, pct: a.lastPct})
	default:
		// The start waits for a live session to place the title, so a client
		// that reports before its stream is open starts on its next report.
		if !a.started && title.item != nil {
			a.started = true
			send(clientScrobble{verb: scrobble.VerbStart, item: title.item, pct: max(a.lastPct, 0)})
		}
	}
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timerGen++
	gen := a.timerGen
	a.timer = time.AfterFunc(wait, func() { expire(key, device, a, gen) })
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
// the current one, and if so hands send the stop it never sent while still
// holding the lock. gen is the arming that fired: a report that re-armed the
// timer while it fired has kept the playback alive.
func (c *clientPlaybacks) release(key, device string, a *clientAttempt, gen int, send func(clientScrobble)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.attempts[key][device] != a || a.timerGen != gen {
		return false
	}
	c.removeLocked(key, device, a)
	if a.stopped || !a.started || a.lastPct < 0 {
		return true
	}
	var item *clientItem
	if t := c.titles[key]; t != nil {
		item = t.item
	}
	send(clientScrobble{verb: scrobble.VerbStop, item: item, pct: a.lastPct})
	return true
}

// ReportClientPlayback takes one client report — Jellyfin's Sessions/Playing
// family — and scrobbles from it. It never blocks on a target: sends go out
// in the background, so a report is answered whatever the targets do.
func (s *Server) ReportClientPlayback(stream *auth.Stream, p ClientPlayback) {
	if s == nil || stream == nil || p.ContentID == "" {
		return
	}
	targets := scrobbleTargets(s.runtime(), stream)
	if len(targets) == 0 {
		return
	}
	var live *clientItem
	key := clientPlaybackKey(stream, p.ContentID)
	// Peeked, not fetched: a client reporting from its buffer must not keep
	// an idle session from being evicted.
	sess := s.sessionManager.PeekSession(s.clientPlayback.servedSession(key))
	if sess != nil && sess.ContentID == p.ContentID {
		item, ok := s.scrobbleItemForSession(sess)
		live = &clientItem{item: item, addressable: ok, runtime: s.clientRuntimeSeconds(stream, sess)}
	}
	s.clientPlayback.apply(stream, p, live, s.expireClientPlayback, func(out clientScrobble) {
		s.sendClientScrobble(targets, stream, key, p.ContentID, out)
	})
}

func (s *Server) expireClientPlayback(key, device string, a *clientAttempt, gen int) {
	targets := scrobbleTargets(s.runtime(), a.stream)
	contentID := strings.TrimPrefix(key, a.stream.Username+"|")
	s.clientPlayback.release(key, device, a, gen, func(out clientScrobble) {
		logger.Debug("Client playback went quiet; reporting its last confirmed position", "content", contentID, "progress", int(out.pct))
		s.sendClientScrobble(targets, a.stream, key, contentID, out)
	})
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

// sendClientScrobble queues one client playback send on the title's lane.
func (s *Server) sendClientScrobble(targets []scrobble.Target, stream *auth.Stream, lane, contentID string, out clientScrobble) {
	if out.item == nil {
		logger.Debug("Client playback not scrobbled: no playback session ever placed the title", "stream", streamLogName(stream), "content", contentID, "verb", out.verb)
		return
	}
	if out.verb == scrobble.VerbStop && out.pct < 0 {
		// No runtime ever placed a position for this playback. The session's
		// served estimate is no stand-in: it spans every playback the session
		// has served.
		logger.Debug("Client playback stop not scrobbled: no runtime to place its position", "content", contentID)
		return
	}
	if len(targets) == 0 {
		logUnlinkedScrobble(stream, out.verb)
		return
	}
	if !out.item.addressable {
		logger.Debug("Client playback not scrobbled: no id a target can place", "content", contentID, "verb", out.verb)
		return
	}
	s.sendScrobble(targets, lane, contentID, out.verb, out.item.item, out.pct)
}

// afterReportGrace runs fn once the report grace has passed, as one of the
// serve's pending fallbacks: counted with the sends it may make, so whatever
// waits on those waits on it too, and cancelled by Shutdown. Once Shutdown has
// run it schedules nothing.
func (s *Server) afterReportGrace(rs *reportingServe, fn func()) {
	c := &s.clientPlayback
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.scheduled == nil {
		c.scheduled = make(map[*reportingServe]struct{})
	}
	c.scheduled[rs] = struct{}{}
	s.scrobbleSends.Add(1)
	rs.timers = append(rs.timers, time.AfterFunc(c.timing().reportGrace, func() {
		defer s.scrobbleSends.Done()
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if !closed {
			fn()
		}
	}))
}

// runFallbackStart sends the serve's fallback start if one is due, once. A
// caller arriving while it runs waits for it, so a stop sent next is queued
// behind it.
func (s *Server) runFallbackStart(rs *reportingServe) {
	rs.startOnce.Do(func() {
		s.clientPlayback.mu.Lock()
		start, closed := rs.start, s.clientPlayback.closed
		s.clientPlayback.mu.Unlock()
		if start != nil && !closed {
			start()
		}
	})
}

// endReportingServe stops matching reports to a serve that has settled its
// stop, and cancels any fallback it still has pending.
func (s *Server) endReportingServe(stream *auth.Stream, contentID string, rs *reportingServe) {
	c := &s.clientPlayback
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.titles[clientPlaybackKey(stream, contentID)]; t != nil {
		delete(t.serves, rs)
	}
	s.cancelFallbacksLocked(rs)
}

func (s *Server) cancelFallbacksLocked(rs *reportingServe) {
	for _, t := range rs.timers {
		if t.Stop() {
			s.scrobbleSends.Done()
		}
	}
	rs.timers = nil
	delete(s.clientPlayback.scheduled, rs)
}

// shutdownClientPlayback stops every timer client playback tracking has
// pending, and anything new from being tracked or scheduled.
func (s *Server) shutdownClientPlayback() {
	c := &s.clientPlayback
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, devices := range c.attempts {
		for _, a := range devices {
			a.timer.Stop()
		}
	}
	for rs := range c.scheduled {
		s.cancelFallbacksLocked(rs)
	}
}
