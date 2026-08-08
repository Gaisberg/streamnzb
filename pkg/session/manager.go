package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/fileutil"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/release"
	"streamnzb/pkg/usenet/pool"
)

type PlaybackStreamSpec struct {
	Key  string
	Name string
	Size int64
}

type PlaybackStreamSnapshot struct {
	Spec           PlaybackStreamSpec
	StartupInfo    seek.StreamStartInfo
	HasStartupInfo bool
}

type playbackStreamState struct {
	spec           PlaybackStreamSpec
	stream         io.ReadSeekCloser
	opening        bool
	inUse          bool
	startupInfo    seek.StreamStartInfo
	hasStartupInfo bool
	cond           *sync.Cond
}

type playbackLease struct {
	session *Session
	state   *playbackStreamState
	closed  bool
}

type Session struct {
	ID         string
	StreamName string

	// nzbData, files, file, blueprint, rel and clients are mutated under mu by
	// the lazy NZB download and by close/teardown; read them only through the
	// locked accessors (NZB, Files, File, Blueprint, Release) and snapshot into
	// a local before dereferencing — never re-fetch after a nil check.
	nzbData *nzb.NZB
	files   []*loader.File
	file    *loader.File

	blueprint           unpack.Blueprint
	CreatedAt           time.Time
	LastAccess          time.Time
	ActivePlays         int32
	PlaybackValidatedAt time.Time // when probe/prepare proved the file is playable; separate from playback end bookkeeping
	PlaybackStartedAt   time.Time // when ActivePlays went from 0 to >0; used to evict stuck sessions
	PlaybackEndedAt     time.Time // when ActivePlays went to 0; used to evict session soon after stream stops
	playbackStarting    int       // number of in-flight playback startups; prevents background refresh from replacing the session mid-startup
	clients             map[string]time.Time
	mu                  sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	rel      *release.Release
	NZBBytes []byte

	ContentIDs *AvailReportMeta

	// ContentType and ContentID are the request context (e.g. "movie"/"series" and "tt123" or "tmdb:123:1:2") for NZB attempt history.
	ContentType          string
	ContentID            string
	ContentTitle         string
	selectedPlaybackFile string

	downloadURL string
	indexer     indexer.Indexer

	nzbDownloadInFlight bool
	nzbDownloadDone     chan struct{}
	nzbDownloadErr      error

	segmentFetcher     loader.SegmentFetcher
	providerHosts      []string
	attemptedProviders map[string]struct{}
	usedProviders      map[string]struct{}
	servedProviders    map[string]struct{}
	serveTrackingDepth int

	bytesRead atomic.Int64 // bytes read during playback; used for AvailNZB good-report threshold
	playback  *playbackStreamState

	mediaCaps *MediaCapabilities // codec/profile/HDR metadata captured during probing

	libraryBlueprintJSON string // serialized archive blueprint from the library, rehydrated onto Blueprint once loader files exist
}

// MediaCapabilities holds client-relevant properties of the selected playback
// file, captured by ffprobe during (pre-)probing. It lets the addon distinguish
// "this release is broken" from "this client cannot decode this codec."
type MediaCapabilities struct {
	VideoCodec    string
	AudioCodec    string
	Width         int
	Height        int
	Profile       string
	PixFmt        string
	BitDepth      int
	HDR           string // "", "HDR10", "HDR10+", "HLG"
	DolbyVision   bool
	ColorTransfer string
	CodecTag      string
}

// Summary renders a short human-readable capability string suitable for a
// Stremio stream description, e.g. "hevc Main 10 2160p 10-bit HDR10".
func (c *MediaCapabilities) Summary() string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	if c.VideoCodec != "" {
		parts = append(parts, c.VideoCodec)
	}
	if c.Profile != "" {
		parts = append(parts, c.Profile)
	}
	if c.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dp", c.Height))
	}
	if c.BitDepth > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit", c.BitDepth))
	}
	if c.DolbyVision {
		parts = append(parts, "Dolby Vision")
	} else if c.HDR != "" {
		parts = append(parts, c.HDR)
	}
	if c.AudioCodec != "" {
		parts = append(parts, c.AudioCodec)
	}
	return strings.Join(parts, " ")
}

// Done returns a channel that is closed when the session is closed (e.g. user closed from dashboard).
// Use with request context so playback aborts when either the client disconnects or the session is closed.
func (s *Session) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.ctx.Done()
}

func (s *Session) Context() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func (s *Session) IsWarm() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nzbData != nil
}

// NZB returns the parsed NZB, or nil before the lazy download completes or
// after teardown. Snapshot into a local before dereferencing.
func (s *Session) NZB() *nzb.NZB {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nzbData
}

// Files returns the loader files backing this session (nil after teardown).
func (s *Session) Files() []*loader.File {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files
}

// File returns the single pre-selected loader file, if any.
func (s *Session) File() *loader.File {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file
}

// Blueprint returns the cached archive blueprint (nil when not yet scanned or
// after teardown).
func (s *Session) Blueprint() unpack.Blueprint {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blueprint
}

// Release returns the release this session was created from (nil after
// teardown). Snapshot into a local before dereferencing.
func (s *Session) Release() *release.Release {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rel
}

func (s *Session) IsNZBDownloadInFlight() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nzbDownloadInFlight
}

func (s *Session) ReleaseURL() string {
	s.mu.Lock()
	rel, downloadURL := s.rel, s.downloadURL
	s.mu.Unlock()
	if rel != nil && rel.DetailsURL != "" {
		return rel.DetailsURL
	}
	return downloadURL
}

func (s *Session) ReportSize() int64 {
	s.mu.Lock()
	nzbData, rel := s.nzbData, s.rel
	s.mu.Unlock()
	if nzbData != nil {
		return nzbData.TotalSize()
	}
	if rel != nil {
		return rel.Size
	}
	return 0
}

func (s *Session) ReportReleaseName() string {
	if rel := s.Release(); rel != nil {
		return rel.Title
	}
	return ""
}

func (s *Session) ReleaseIndexer() string {
	if rel := s.Release(); rel != nil {
		return rel.Indexer
	}
	return ""
}

func (s *Session) ProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.providerHosts...)
}

// sortedHostSet renders a provider-host set as a sorted slice. Callers must
// already hold s.mu.
func sortedHostSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(set))
	for host := range set {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// addHostsLocked inserts the non-empty hosts into *set, creating it on first
// use. Callers must already hold s.mu.
func addHostsLocked(set *map[string]struct{}, hosts ...string) {
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if *set == nil {
			*set = make(map[string]struct{})
		}
		(*set)[host] = struct{}{}
	}
}

func (s *Session) UsedProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedHostSet(s.usedProviders)
}

func (s *Session) AttemptedProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedHostSet(s.attemptedProviders)
}

func (s *Session) ServedProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedHostSet(s.servedProviders)
}

func (s *Session) RecordAttemptedProviderHost(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addHostsLocked(&s.attemptedProviders, host)
}

func (s *Session) RecordAttemptedProviderHosts(hosts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addHostsLocked(&s.attemptedProviders, hosts...)
}

func (s *Session) RecordConfiguredProviderHostsAttempted() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	addHostsLocked(&s.attemptedProviders, s.providerHosts...)
}

// RecordUsedProviderHost marks a host as used, which also implies attempted.
func (s *Session) RecordUsedProviderHost(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addHostsLocked(&s.attemptedProviders, host)
	addHostsLocked(&s.usedProviders, host)
}

func (s *Session) RecordServedProviderHost(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	addHostsLocked(&s.servedProviders, host)
}

func (s *Session) BeginServeProviderTracking() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serveTrackingDepth++
}

func (s *Session) EndServeProviderTracking() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.serveTrackingDepth > 0 {
		s.serveTrackingDepth--
	}
}

func (s *Session) IsServeProviderTrackingEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveTrackingDepth > 0
}

type sessionTrackingFetcher struct {
	session *Session
	base    loader.SegmentFetcher
}

func (f *sessionTrackingFetcher) record(data pool.SegmentData) {
	if f == nil || f.session == nil {
		return
	}
	f.session.RecordUsedProviderHost(data.ProviderHost)
	if f.session.IsServeProviderTrackingEnabled() {
		f.session.RecordServedProviderHost(data.ProviderHost)
	}
}

func (f *sessionTrackingFetcher) FetchSegment(ctx context.Context, segment *nzb.Segment, groups []string) (pool.SegmentData, error) {
	if f == nil || f.base == nil {
		return pool.SegmentData{}, fmt.Errorf("segment fetcher unavailable")
	}
	data, err := f.base.FetchSegment(ctx, segment, groups)
	if err == nil {
		f.record(data)
	} else {
		f.session.RecordAttemptedProviderHosts(pool.AttemptedProviderHosts(err))
	}
	return data, err
}

func (f *sessionTrackingFetcher) FetchSegmentFirst(ctx context.Context, segment *nzb.Segment, groups []string) (pool.SegmentData, error) {
	if f == nil || f.base == nil {
		return pool.SegmentData{}, fmt.Errorf("segment fetcher unavailable")
	}
	first, ok := f.base.(loader.SegmentFirstFetcher)
	if !ok {
		return f.FetchSegment(ctx, segment, groups)
	}
	data, err := first.FetchSegmentFirst(ctx, segment, groups)
	if err == nil {
		f.record(data)
	} else {
		f.session.RecordAttemptedProviderHosts(pool.AttemptedProviderHosts(err))
	}
	return data, err
}

type sessionTrackingFetcherWithStat struct {
	*sessionTrackingFetcher
	statter loader.SegmentStatter
}

func (f *sessionTrackingFetcherWithStat) StatSegment(ctx context.Context, messageID string, groups []string) (bool, error) {
	if f == nil || f.statter == nil {
		return false, fmt.Errorf("segment statter unavailable")
	}
	exists, err := f.statter.StatSegment(ctx, messageID, groups)
	if err != nil {
		f.session.RecordAttemptedProviderHosts(pool.AttemptedProviderHosts(err))
		return exists, err
	}
	if !exists && f.session != nil {
		f.session.RecordConfiguredProviderHostsAttempted()
	}
	return exists, nil
}

func attachProviderTracking(session *Session, fetcher loader.SegmentFetcher) loader.SegmentFetcher {
	if session == nil || fetcher == nil {
		return fetcher
	}
	wrapped := &sessionTrackingFetcher{session: session, base: fetcher}
	if statter, ok := fetcher.(loader.SegmentStatter); ok {
		return &sessionTrackingFetcherWithStat{sessionTrackingFetcher: wrapped, statter: statter}
	}
	return wrapped
}

// BytesRead returns the number of bytes read from this session during playback.
func (s *Session) BytesRead() int64 {
	return s.bytesRead.Load()
}

// AddBytesRead adds n to the session's bytes-read counter (called from stream read path).
func (s *Session) AddBytesRead(n int64) {
	if n > 0 {
		s.bytesRead.Add(n)
	}
}

// IsActivelyServing returns true if at least one goroutine is currently serving this session
// (i.e. http.ServeContent is running). Used by tryPlaySlot to avoid killing an active stream
// when a concurrent background request (e.g. Stremio's /next/ poll) re-enters the play handler.
func (s *Session) IsActivelyServing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ActivePlays > 0
}

// HasPreviouslyServed returns true if this session has already validated its playback source.
// This is intentionally separate from PlaybackEndedAt: probe requests should still establish
// that the file is playable, but they should not be conflated with meaningful playback ending.
// Used by tryPlaySlot to skip IsFailed() for sessions that proved their file was good but whose
// ActivePlays is momentarily 0 between the client cancelling an initial probe and sending a
// follow-up range request.
// HasPreviouslyServed reports whether playback was ever validated for this
// session. It is the same condition as IsPlaybackValidated, named for the
// serve-path callers that ask "has this already worked once?".
func (s *Session) HasPreviouslyServed() bool {
	return s.IsPlaybackValidated()
}

func (s *Session) IsPlaybackValidated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.PlaybackValidatedAt.IsZero()
}

// MaxPlaybackDuration is the maximum time a session can stay in "active playback"
// before being evicted even if EndPlayback was never called (e.g. stuck connection).
const MaxPlaybackDuration = 6 * time.Hour

// FailoverOrderTTL is how long stream failover order entries are kept before expiry in cleanup().
const FailoverOrderTTL = 24 * time.Hour

// PostPlaybackEvictTTL is how long a session stays in memory after playback ends (ActivePlays=0)
// before being evicted. Long enough that pause/resume does not require a new stream from the catalog.
const PostPlaybackEvictTTL = 15 * time.Minute

// clientStaleTTL is how long a Clients map entry is kept before it is treated as stale in cleanup().
// Matches the 60-second window used in GetActiveSessions.
const clientStaleTTL = 60 * time.Second

// deferredSessionReplaceGrace is a short protection window after a session was last touched.
// It avoids replacing a slot that was just handed to playback while startup bookkeeping is still catching up.
const deferredSessionReplaceGrace = 5 * time.Second

type failoverOrderEntry struct {
	order     []string
	expiresAt time.Time
}

type Manager struct {
	sessions                 map[string]*Session
	usenetPool               *pool.Pool
	estimator                *loader.SegmentSizeEstimator
	ttl                      time.Duration
	postPlaybackEvictTTL     time.Duration
	maxPlaybackDuration      time.Duration
	mu                       sync.RWMutex
	failoverOrder            sync.Map
	slotFailedDuringPlayback sync.Map // slotPath -> *failedSlotEntry (430 during streaming)
	stopCh                   chan struct{}
	stopOnce                 sync.Once
}

type failedSlotEntry struct {
	expiresAt time.Time
}

func (s *Session) SetBlueprint(bp unpack.Blueprint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blueprint = bp
}

// SetRelease attaches the release this session was created from.
func (s *Session) SetRelease(rel *release.Release) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rel = rel
}

func (s *Session) SetSelectedPlaybackFile(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectedPlaybackFile = name
}

func (s *Session) SelectedPlaybackFile() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedPlaybackFile
}

// SetMediaCapabilities records codec/profile/HDR metadata captured during probing.
func (s *Session) SetMediaCapabilities(c *MediaCapabilities) {
	if s == nil || c == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mediaCaps = c
}

// MediaCapabilities returns the captured media capabilities, or nil if unprobed.
func (s *Session) MediaCapabilities() *MediaCapabilities {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mediaCaps
}

func (s *Session) ensurePlaybackStateLocked() *playbackStreamState {
	if s.playback == nil {
		s.playback = &playbackStreamState{}
		s.playback.cond = sync.NewCond(&s.mu)
	}
	return s.playback
}

func (s *Session) PlaybackStreamSnapshot() (PlaybackStreamSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.playback == nil || s.playback.spec.Key == "" {
		return PlaybackStreamSnapshot{}, false
	}
	return PlaybackStreamSnapshot{
		Spec:           s.playback.spec,
		StartupInfo:    s.playback.startupInfo,
		HasStartupInfo: s.playback.hasStartupInfo,
	}, true
}

func (s *Session) SetPlaybackStreamStartInfo(key string, info seek.StreamStartInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.playback == nil || s.playback.spec.Key == "" || s.playback.spec.Key != key {
		return
	}
	s.playback.startupInfo = info
	s.playback.hasStartupInfo = true
}

func (s *Session) CachePlaybackStreamSnapshot(spec PlaybackStreamSpec, info seek.StreamStartInfo, hasStartupInfo bool) {
	if spec.Key == "" {
		return
	}

	var oldStream io.ReadSeekCloser

	s.mu.Lock()
	state := s.ensurePlaybackStateLocked()
	if state.stream != nil && !state.inUse && !state.opening {
		oldStream = state.stream
		state.stream = nil
	}
	state.spec = spec
	if hasStartupInfo {
		state.startupInfo = info
		state.hasStartupInfo = true
	} else {
		state.startupInfo = seek.StreamStartInfo{}
		state.hasStartupInfo = false
	}
	s.LastAccess = time.Now()
	if state.cond != nil {
		state.cond.Broadcast()
	}
	s.mu.Unlock()

	if oldStream != nil {
		_ = oldStream.Close()
	}
}

func (s *Session) AcquirePlaybackStream(spec PlaybackStreamSpec, open func() (io.ReadSeekCloser, error)) (io.ReadSeekCloser, bool, error) {
	stream, reused, _, err := s.acquirePlaybackStream(spec, open, false)
	return stream, reused, err
}

func (s *Session) TryAcquirePlaybackStream(spec PlaybackStreamSpec, open func() (io.ReadSeekCloser, error)) (io.ReadSeekCloser, bool, bool, error) {
	return s.acquirePlaybackStream(spec, open, true)
}

func (s *Session) acquirePlaybackStream(spec PlaybackStreamSpec, open func() (io.ReadSeekCloser, error), allowBusySameKey bool) (io.ReadSeekCloser, bool, bool, error) {
	if spec.Key == "" {
		return nil, false, false, fmt.Errorf("playback stream key required")
	}

	s.mu.Lock()
	state := s.ensurePlaybackStateLocked()
	for {
		if state.stream != nil && state.spec.Key == spec.Key && !state.inUse && !state.opening {
			state.spec = spec
			state.inUse = true
			s.LastAccess = time.Now()
			s.mu.Unlock()
			return &playbackLease{session: s, state: state}, true, false, nil
		}
		if allowBusySameKey && state.spec.Key == spec.Key && (state.opening || state.inUse) {
			s.LastAccess = time.Now()
			s.mu.Unlock()
			return nil, false, true, nil
		}
		if state.opening || state.inUse {
			state.cond.Wait()
			continue
		}

		oldStream := state.stream
		oldKey := state.spec.Key
		state.stream = nil
		state.spec = spec
		state.opening = true
		state.inUse = false
		if oldKey != spec.Key {
			state.startupInfo = seek.StreamStartInfo{}
			state.hasStartupInfo = false
		}
		s.LastAccess = time.Now()
		s.mu.Unlock()

		if oldStream != nil {
			_ = oldStream.Close()
		}

		stream, err := open()

		s.mu.Lock()
		state.opening = false
		if err != nil {
			if state.stream == nil && state.spec.Key == spec.Key {
				state.spec = PlaybackStreamSpec{}
				state.startupInfo = seek.StreamStartInfo{}
				state.hasStartupInfo = false
			}
			state.cond.Broadcast()
			s.mu.Unlock()
			return nil, false, false, err
		}

		state.stream = stream
		state.spec = spec
		state.inUse = true
		s.LastAccess = time.Now()
		state.cond.Broadcast()
		s.mu.Unlock()
		return &playbackLease{session: s, state: state}, false, false, nil
	}
}

// resetLocked clears the cached playback stream state and returns the detached
// stream, which the caller must close OUTSIDE the lock - Close can touch the
// network on some stream types.
func (st *playbackStreamState) resetLocked() io.ReadSeekCloser {
	if st == nil {
		return nil
	}
	stream := st.stream
	st.stream = nil
	st.spec = PlaybackStreamSpec{}
	st.opening = false
	st.inUse = false
	st.startupInfo = seek.StreamStartInfo{}
	st.hasStartupInfo = false
	if st.cond != nil {
		st.cond.Broadcast()
	}
	return stream
}

func (s *Session) ResetPlaybackStream() {
	s.mu.Lock()
	stream := s.playback.resetLocked()
	s.mu.Unlock()

	if stream != nil {
		_ = stream.Close()
	}
}

func (l *playbackLease) activeStream() io.ReadSeekCloser {
	if l == nil || l.session == nil || l.state == nil {
		return nil
	}
	l.session.mu.Lock()
	defer l.session.mu.Unlock()
	if l.closed || l.session.playback != l.state {
		return nil
	}
	return l.state.stream
}

func (l *playbackLease) Read(p []byte) (int, error) {
	stream := l.activeStream()
	if stream == nil {
		return 0, io.ErrClosedPipe
	}
	return stream.Read(p)
}

func (l *playbackLease) Seek(offset int64, whence int) (int64, error) {
	stream := l.activeStream()
	if stream == nil {
		return 0, io.ErrClosedPipe
	}
	return stream.Seek(offset, whence)
}

func (l *playbackLease) Close() error {
	if l == nil || l.session == nil || l.state == nil {
		return nil
	}
	l.session.mu.Lock()
	defer l.session.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.session.playback == l.state {
		l.state.inUse = false
		l.session.LastAccess = time.Now()
		if l.state.cond != nil {
			l.state.cond.Broadcast()
		}
	}
	return nil
}

func NewManager(usenetPool *pool.Pool, ttl time.Duration) *Manager {
	m := &Manager{
		sessions:             make(map[string]*Session),
		usenetPool:           usenetPool,
		estimator:            loader.NewSegmentSizeEstimator(),
		ttl:                  ttl,
		postPlaybackEvictTTL: 4 * time.Hour,
		maxPlaybackDuration:  MaxPlaybackDuration,
		stopCh:               make(chan struct{}),
	}

	go m.cleanupLoop()
	return m
}

func (m *Manager) SetTTL(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ttl = d
}

func (m *Manager) SetPostPlaybackEvictTTL(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.postPlaybackEvictTTL = d
}

// Shutdown stops the background cleanup goroutine. Call during application shutdown.
// Safe to call more than once.
func (m *Manager) Shutdown() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}

type AvailReportMeta struct {
	ImdbID  string
	TmdbID  string
	TvdbID  string
	KitsuID string
	Season  int
	Episode int
	// AbsoluteEpisode is the anime absolute episode number for the requested
	// season/episode (0 when unknown or not applicable). Episode selection
	// accepts absolute-numbered files alongside season/episode matches.
	AbsoluteEpisode int
}

func (m *Manager) CreateSession(sessionID string, nzbData *nzb.NZB, rel *release.Release, contentIDs *AvailReportMeta) (*Session, error) {
	return m.CreateSessionWithFetcher(sessionID, nzbData, rel, contentIDs, nil, nil)
}

func (m *Manager) CreateSessionWithFetcher(sessionID string, nzbData *nzb.NZB, rel *release.Release, contentIDs *AvailReportMeta, segmentFetcher loader.SegmentFetcher, providerHosts []string) (*Session, error) {
	logger.Trace("session CreateSession start", "id", sessionID)
	m.mu.Lock()
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		m.mu.Unlock()
		logger.Trace("session CreateSession existing", "id", sessionID)
		return existing, nil
	}
	m.mu.Unlock()

	logger.Trace("session CreateSession heavy work", "id", sessionID)
	contentFiles := selectSessionContentFiles(nzbData, contentIDs)
	if len(contentFiles) == 0 {
		if detail := nzbData.DescribeMissingContent(); detail != "" {
			return nil, fmt.Errorf("no content files found in NZB: %s", detail)
		}
		return nil, fmt.Errorf("no content files found in NZB")
	}
	m.mu.RLock()
	usenetPool := m.usenetPool
	estimator := m.estimator
	m.mu.RUnlock()
	if segmentFetcher == nil && usenetPool != nil {
		segmentFetcher = usenetPool.Subset(nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{
		ID:             sessionID,
		nzbData:        nzbData,
		rel:            rel,
		ContentIDs:     contentIDs,
		CreatedAt:      time.Now(),
		LastAccess:     time.Now(),
		clients:        make(map[string]time.Time),
		ctx:            ctx,
		cancel:         cancel,
		segmentFetcher: segmentFetcher,
		providerHosts:  append([]string(nil), providerHosts...),
	}
	session.segmentFetcher = attachProviderTracking(session, session.segmentFetcher)
	loaderFiles := buildLoaderFiles(ctx, sessionID, contentFiles, session.segmentFetcher, estimator)
	session.files = loaderFiles
	session.file = loaderFiles[0]

	logger.Debug("session CreateSession", "id", sessionID, "files", len(loaderFiles))
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		cancel()
		return existing, nil
	}
	m.sessions[sessionID] = session
	logger.Trace("session CreateSession done", "id", sessionID)
	return session, nil
}

func selectSessionContentFiles(nzbData *nzb.NZB, contentIDs *AvailReportMeta) []*nzb.FileInfo {
	if nzbData == nil {
		return nil
	}
	season, episode, absolute := 0, 0, 0
	if contentIDs != nil {
		season = contentIDs.Season
		episode = contentIDs.Episode
		absolute = contentIDs.AbsoluteEpisode
	}
	return nzbData.GetSessionContentFilesForEpisode(season, episode, absolute)
}

func buildLoaderFiles(ctx context.Context, ownerID string, contentFiles []*nzb.FileInfo, fetcher loader.SegmentFetcher, estimator *loader.SegmentSizeEstimator) []*loader.File {
	loaderFiles := make([]*loader.File, 0, len(contentFiles))
	for _, info := range contentFiles {
		lf := loader.NewFile(ctx, info.File, estimator, fetcher)
		lf.SetOwnerSessionID(ownerID)
		loaderFiles = append(loaderFiles, lf)
	}
	return loaderFiles
}

// VerifyLibraryNZB parses nzbBytes, builds a loader file for the first selected
// content file, and STATs its first segment — a cheap liveness check for a cached
// release, without creating a session. Used by the library freshness sweep to
// prune releases whose articles have expired. Returns (exists, err); a transient
// error (nil-pool, timeout) should leave the item untouched for the next sweep.
func (m *Manager) VerifyLibraryNZB(ctx context.Context, nzbBytes []byte, contentIDs *AvailReportMeta) (bool, error) {
	if m == nil {
		return false, fmt.Errorf("nil manager")
	}
	if len(nzbBytes) == 0 {
		return false, fmt.Errorf("empty nzb")
	}
	parsed, err := nzb.ParseWithContext(ctx, bytes.NewReader(nzbBytes))
	if err != nil {
		return false, fmt.Errorf("parse nzb: %w", err)
	}
	contentFiles := selectSessionContentFiles(parsed, contentIDs)
	if len(contentFiles) == 0 {
		return false, fmt.Errorf("no content files in nzb")
	}

	m.mu.RLock()
	usenetPool := m.usenetPool
	estimator := m.estimator
	m.mu.RUnlock()

	var fetcher loader.SegmentFetcher
	if usenetPool != nil {
		fetcher = usenetPool.Subset(nil)
	}
	if fetcher == nil {
		return false, fmt.Errorf("no usenet providers available")
	}

	files := buildLoaderFiles(ctx, "library-verify", contentFiles[:1], fetcher, estimator)
	if len(files) == 0 {
		return false, fmt.Errorf("no loader files built")
	}
	return files[0].CheckFirstSegmentExists(ctx)
}

type DeferredSessionCreateOutcome string

const (
	DeferredSessionCreateCreated  DeferredSessionCreateOutcome = "created"
	DeferredSessionCreateExisting DeferredSessionCreateOutcome = "existing"
	DeferredSessionCreateReplaced DeferredSessionCreateOutcome = "replaced"
)

func (m *Manager) CreateDeferredSession(sessionID, downloadURL string, rel *release.Release, idx indexer.Indexer, contentIDs *AvailReportMeta, contentType, contentID, contentTitle, streamName string) (*Session, error) {
	return m.CreateDeferredSessionWithFetcher(sessionID, downloadURL, rel, idx, contentIDs, contentType, contentID, contentTitle, streamName, nil, nil)
}

func (m *Manager) CreateDeferredSessionWithFetcher(sessionID, downloadURL string, rel *release.Release, idx indexer.Indexer, contentIDs *AvailReportMeta, contentType, contentID, contentTitle, streamName string, segmentFetcher loader.SegmentFetcher, providerHosts []string) (*Session, error) {
	session, _, err := m.CreateDeferredSessionWithFetcherOutcome(sessionID, downloadURL, rel, idx, contentIDs, contentType, contentID, contentTitle, streamName, segmentFetcher, providerHosts)
	return session, err
}

func (m *Manager) CreateDeferredSessionWithFetcherOutcome(sessionID, downloadURL string, rel *release.Release, idx indexer.Indexer, contentIDs *AvailReportMeta, contentType, contentID, contentTitle, streamName string, segmentFetcher loader.SegmentFetcher, providerHosts []string) (*Session, DeferredSessionCreateOutcome, error) {
	logger.Trace("session CreateDeferredSession start", "id", sessionID)
	m.mu.Lock()
	var replaced *Session
	defer func() {
		m.mu.Unlock()
		if replaced != nil {
			replaced.closeWithLogging(false, "replaced_stale_deferred_session")
		}
	}()

	if existing, ok := m.sessions[sessionID]; ok {
		now := time.Now()
		existing.mu.Lock()
		existingURL := existing.downloadURL
		lastAccess := existing.LastAccess
		replaceable := existing.ActivePlays == 0 &&
			existing.PlaybackValidatedAt.IsZero() &&
			!existing.nzbDownloadInFlight &&
			existing.playbackStarting == 0 &&
			now.Sub(lastAccess) >= deferredSessionReplaceGrace
		sameDownloadURL := existingURL == downloadURL
		if sameDownloadURL || !replaceable {
			existing.LastAccess = now
		}
		existing.mu.Unlock()
		if !sameDownloadURL && replaceable {
			logger.Trace("session CreateDeferredSession replacing stale deferred session",
				"id", sessionID,
				"old_download_url", existingURL,
				"new_download_url", downloadURL)
			delete(m.sessions, sessionID)
			replaced = existing
		} else {
			logger.Trace("session CreateDeferredSession existing", "id", sessionID)
			return existing, DeferredSessionCreateExisting, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	trackedFetcher := segmentFetcher
	session := &Session{
		ID:             sessionID,
		StreamName:     streamName,
		nzbData:        nil,
		rel:            rel,
		ContentIDs:     contentIDs,
		ContentType:    contentType,
		ContentID:      contentID,
		ContentTitle:   contentTitle,
		downloadURL:    downloadURL,
		indexer:        idx,
		CreatedAt:      time.Now(),
		LastAccess:     time.Now(),
		clients:        make(map[string]time.Time),
		ctx:            ctx,
		cancel:         cancel,
		segmentFetcher: trackedFetcher,
		providerHosts:  append([]string(nil), providerHosts...),
	}
	session.segmentFetcher = attachProviderTracking(session, trackedFetcher)
	if rel != nil && rel.SourceIndexer != nil {
		if libItem, ok := rel.SourceIndexer.(*persistence.LibraryItem); ok && libItem != nil {
			if len(libItem.NZBData) > 0 {
				if decompressed, err := persistence.DecompressNZB(libItem.NZBData); err == nil && len(decompressed) > 0 {
					session.NZBBytes = decompressed
				} else {
					session.NZBBytes = libItem.NZBData
				}
			}
			// Restore capabilities captured during the original pre-probe so a library
			// replay carries codec/HDR metadata without re-probing.
			if libItem.MediaCapsJSON != "" {
				var caps MediaCapabilities
				if err := json.Unmarshal([]byte(libItem.MediaCapsJSON), &caps); err == nil {
					session.mediaCaps = &caps
				}
			}
			// Stash the serialized archive blueprint; it is rehydrated onto Blueprint
			// once loader files exist (see GetOrDownloadNZBWithContext), letting a
			// library replay skip the expensive ScanArchive.
			session.libraryBlueprintJSON = libItem.BlueprintJSON
		}
	}
	m.sessions[sessionID] = session
	logger.Trace("session CreateDeferredSession done", "id", sessionID)
	if replaced != nil {
		return session, DeferredSessionCreateReplaced, nil
	}
	return session, DeferredSessionCreateCreated, nil
}

func (s *Session) GetOrDownloadNZB(manager *Manager) (*nzb.NZB, error) {
	return s.GetOrDownloadNZBWithContext(s.Context(), manager)
}

func (s *Session) GetOrDownloadNZBWithContext(ctx context.Context, manager *Manager) (*nzb.NZB, error) {
	for {
		s.mu.Lock()
		if s.nzbData != nil {
			nzb := s.nzbData
			s.mu.Unlock()
			return nzb, nil
		}
		if s.downloadURL == "" || s.indexer == nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("session has no NZB and no deferred download info")
		}
		sessionCtx := s.ctx
		effectiveCtx, releaseEffectiveCtx := mergeCancellationContexts(ctx, sessionCtx)
		if err := effectiveCtx.Err(); err != nil {
			s.mu.Unlock()
			releaseEffectiveCtx()
			return nil, fmt.Errorf("failed to lazy download NZB: %w", err)
		}
		if s.nzbDownloadInFlight {
			done := s.nzbDownloadDone
			s.mu.Unlock()
			select {
			case <-done:
				releaseEffectiveCtx()
				s.mu.Lock()
				nzb := s.nzbData
				inFlightErr := s.nzbDownloadErr
				s.mu.Unlock()
				if nzb != nil {
					return nzb, nil
				}
				if inFlightErr != nil {
					return nil, inFlightErr
				}
				continue
			case <-effectiveCtx.Done():
				releaseEffectiveCtx()
				return nil, fmt.Errorf("failed to lazy download NZB: %w", effectiveCtx.Err())
			}
		}

		nzbURL := s.downloadURL
		idx := s.indexer
		contentIDs := s.ContentIDs
		sessionID := s.ID
		segmentFetcher := s.segmentFetcher
		itemTitle := ""
		indexerName := ""
		if s.rel != nil {
			itemTitle = s.rel.Title
			indexerName = s.rel.Indexer
		}
		sessionFileCtx := sessionCtx
		if sessionFileCtx == nil {
			sessionFileCtx = context.Background()
		}
		s.nzbDownloadInFlight = true
		s.nzbDownloadErr = nil
		done := make(chan struct{})
		s.nzbDownloadDone = done
		s.mu.Unlock()

		go func() {
			downloadCtx, cancel := context.WithTimeout(sessionFileCtx, 60*time.Second)
			defer cancel()

			var data []byte
			var err error
			s.mu.Lock()
			cachedBytes := s.NZBBytes
			s.mu.Unlock()

			if len(cachedBytes) > 0 {
				data = cachedBytes
				logger.Trace("Using cached NZB bytes from SQLite library", "title", itemTitle)
			} else if idx == nil {
				err = fmt.Errorf("no indexer available for NZB download")
			} else {
				logger.Trace("Lazy Downloading NZB (direct)...", "title", itemTitle, "indexer", indexerName)
				data, err = idx.DownloadNZB(downloadCtx, nzbURL)
			}

			var parsedNZB *nzb.NZB
			var loaderFiles []*loader.File
			if err == nil {
				if len(data) == 0 {
					logger.Debug("NZB download returned empty body", "indexer", indexerName, "title", itemTitle)
					err = fmt.Errorf("NZB download returned empty body (indexer: %s)", indexerName)
				} else {
					parsedNZB, err = nzb.ParseWithContext(downloadCtx, bytes.NewReader(data))
					if err != nil {
						snippet := string(data)
						if len(snippet) > 200 {
							snippet = snippet[:200] + "..."
						}
						logger.Debug("Failed to parse NZB", "indexer", indexerName, "title", itemTitle, "len", len(data), "snippet", snippet, "err", err)
						err = fmt.Errorf("failed to parse lazy downloaded NZB: %w", err)
					}
				}
			}

			if err == nil {
				contentFiles := selectSessionContentFiles(parsedNZB, contentIDs)
				if len(contentFiles) == 0 {
					logger.Error("Lazy load: no content files in NZB",
						"title", itemTitle,
						"indexer", indexerName,
						"nzb_files", len(parsedNZB.Files),
						"details", "see DEBUG log GetContentFiles returned empty for file list")
					err = fmt.Errorf("no content files found in lazy NZB")
				} else {
					manager.mu.RLock()
					usenetPool := manager.usenetPool
					estimator := manager.estimator
					manager.mu.RUnlock()
					if segmentFetcher == nil && usenetPool != nil {
						segmentFetcher = usenetPool.Subset(nil)
					}
					loaderFiles = buildLoaderFiles(sessionFileCtx, sessionID, contentFiles, segmentFetcher, estimator)
				}
			}

			finalErr := err
			if finalErr != nil {
				if !(strings.Contains(finalErr.Error(), "failed to lazy download NZB") ||
					strings.Contains(finalErr.Error(), "no content files found in lazy NZB") ||
					strings.Contains(finalErr.Error(), "NZB download returned empty body")) {
					finalErr = fmt.Errorf("failed to lazy download NZB: %w", finalErr)
				}
			}

			s.mu.Lock()
			if finalErr == nil && sessionFileCtx.Err() != nil {
				finalErr = fmt.Errorf("failed to lazy download NZB: %w", sessionFileCtx.Err())
			}
			if finalErr == nil && s.nzbData == nil {
				s.nzbData = parsedNZB
				s.NZBBytes = data
				s.files = loaderFiles
				s.file = loaderFiles[0]
				logger.Debug("session GetOrDownloadNZB created loader files", "id", s.ID, "files", len(loaderFiles))
				// Best-effort: rehydrate a persisted library blueprint against the fresh
				// loader files so playback skips ScanArchive. Falls back silently to a
				// full scan (Blueprint stays nil) if the volume set no longer matches.
				if s.blueprint == nil && s.libraryBlueprintJSON != "" {
					unpackables := make([]unpack.UnpackableFile, len(loaderFiles))
					for i := range loaderFiles {
						unpackables[i] = loaderFiles[i]
					}
					if bp, ok := unpack.RehydrateArchiveBlueprint([]byte(s.libraryBlueprintJSON), unpackables); ok {
						s.blueprint = bp
						logger.Debug("Reused persisted archive blueprint from library (skipped rescan)", "id", s.ID, "file", bp.MainFileName, "parts", len(bp.Parts))
					}
				}
			}
			s.nzbDownloadErr = finalErr
			s.nzbDownloadInFlight = false
			s.nzbDownloadDone = nil
			close(done)
			s.mu.Unlock()
		}()

		select {
		case <-done:
			releaseEffectiveCtx()
			s.mu.Lock()
			nzb := s.nzbData
			downloadErr := s.nzbDownloadErr
			s.mu.Unlock()
			if nzb != nil {
				return nzb, nil
			}
			return nil, downloadErr
		case <-effectiveCtx.Done():
			releaseEffectiveCtx()
			return nil, fmt.Errorf("failed to lazy download NZB: %w", effectiveCtx.Err())
		}
	}
}

func mergeCancellationContexts(callerCtx, sessionCtx context.Context) (context.Context, func()) {
	switch {
	case callerCtx == nil && sessionCtx == nil:
		return context.Background(), func() {}
	case callerCtx == nil:
		return sessionCtx, func() {}
	case sessionCtx == nil || callerCtx == sessionCtx:
		return callerCtx, func() {}
	default:
		mergedCtx, cancel := context.WithCancel(callerCtx)
		stopWatchingSession := context.AfterFunc(sessionCtx, cancel)
		return mergedCtx, func() {
			stopWatchingSession()
			cancel()
		}
	}
}

func failoverOrderMapKey(streamToken, streamKey string) string {
	if streamKey == "" {
		return streamToken
	}
	return streamToken + "|" + streamKey
}

func (m *Manager) SetStreamFailoverOrder(streamToken, streamKey string, order []string) {
	if len(order) == 0 {
		return
	}
	cp := make([]string, len(order))
	copy(cp, order)
	m.failoverOrder.Store(failoverOrderMapKey(streamToken, streamKey), &failoverOrderEntry{
		order:     cp,
		expiresAt: time.Now().Add(FailoverOrderTTL),
	})
}

// GetStreamFailoverOrder returns the stored failover order for this stream token and stream key.
// It tries key-specific storage first, then falls back to token-only (legacy) if streamKey is set.
// Returns nil if the entry is missing or expired.
func (m *Manager) GetStreamFailoverOrder(streamToken, streamKey string) []string {
	now := time.Now()
	if streamKey != "" {
		if val, ok := m.failoverOrder.Load(failoverOrderMapKey(streamToken, streamKey)); ok && val != nil {
			if ent, ok := val.(*failoverOrderEntry); ok && ent != nil && now.Before(ent.expiresAt) {
				return ent.order
			}
		}
	}
	val, ok := m.failoverOrder.Load(streamToken)
	if !ok || val == nil {
		return nil
	}
	ent, ok := val.(*failoverOrderEntry)
	if !ok || ent == nil || now.After(ent.expiresAt) {
		return nil
	}
	return ent.order
}

// SetSlotFailedDuringPlayback marks the slot as having failed with 430 during playback.
// Subsequent play requests for this slot should redirect to the next fallback.
func (m *Manager) SetSlotFailedDuringPlayback(slotPath string) {
	if slotPath == "" {
		return
	}
	expiresAt := time.Now().Add(m.ttl)
	m.slotFailedDuringPlayback.Store(slotPath, &failedSlotEntry{expiresAt: expiresAt})
}

// GetSlotFailedDuringPlayback returns true if this slot recently failed during playback (430).
func (m *Manager) GetSlotFailedDuringPlayback(slotPath string) bool {
	val, ok := m.slotFailedDuringPlayback.Load(slotPath)
	if !ok || val == nil {
		return false
	}
	ent, ok := val.(*failedSlotEntry)
	if !ok || ent == nil || time.Now().After(ent.expiresAt) {
		return false
	}
	return true
}

func (m *Manager) ClearSlotFailedDuringPlayback(slotPath string) {
	if slotPath == "" {
		return
	}
	m.slotFailedDuringPlayback.Delete(slotPath)
}

// HasActiveSessionForContentID returns true if any session with the given content type
// and content ID exists in the session map (regardless of whether it is actively serving).
// Used to detect Stremio's next-episode preload: Stremio sends E04's play request within
// milliseconds of the user clicking E03, before E03 has incremented ActivePlays. Checking
// for session existence (not ActivePlays > 0) avoids the 2–3 second race window.
func (m *Manager) HasActiveSessionForContentID(contentType, contentID string) bool {
	if contentType == "" || contentID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		s.mu.Lock()
		ct := s.ContentType
		cid := s.ContentID
		s.mu.Unlock()
		if ct == contentType && cid == contentID {
			return true
		}
	}
	return false
}

func (m *Manager) GetSession(sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	session.LastAccess = time.Now()
	session.mu.Unlock()

	return session, nil
}

// freeOSMemory runs GC and returns unused memory to the OS so RSS drops after session close.
func freeOSMemory() {
	debug.FreeOSMemory()
}

func (m *Manager) traceTeardownSnapshot(trigger, sessionID string) {
	m.logTeardownSnapshot(trigger, sessionID, "immediate")
	for _, delay := range []time.Duration{5 * time.Second, 15 * time.Second} {
		d := delay
		time.AfterFunc(d, func() {
			m.logTeardownSnapshot(trigger, sessionID, d.String())
		})
	}
}

func (m *Manager) logTeardownSnapshot(trigger, sessionID, checkpoint string) {
	m.mu.RLock()
	sessionPresent := false
	if sessionID != "" {
		_, sessionPresent = m.sessions[sessionID]
	}
	sessionsTotal := len(m.sessions)
	sessionsWithFilesIDs := m.sessionsWithFilesIDs()
	sessionsWithFiles := len(sessionsWithFilesIDs)
	activePlays := 0
	activeClients := 0
	for _, sess := range m.sessions {
		sess.mu.Lock()
		activePlays += int(sess.ActivePlays)
		activeClients += len(sess.clients)
		sess.mu.Unlock()
	}
	usenetPool := m.usenetPool
	m.mu.RUnlock()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	fields := []any{
		"trigger", trigger,
		"session", sessionID,
		"checkpoint", checkpoint,
		"session_present", sessionPresent,
		"sessions_total", sessionsTotal,
		"sessions_with_files", sessionsWithFiles,
		"sessions_with_files_ids", strings.Join(sessionsWithFilesIDs, ","),
		"active_plays", activePlays,
		"active_clients", activeClients,
		"live_segment_readers", loader.LiveSegmentReaders(),
		"live_segment_reader_details", strings.Join(loader.LiveSegmentReaderDetails(), "; "),
		"live_virtual_streams", unpack.LiveVirtualStreams(),
		"heap_alloc_bytes", mem.HeapAlloc,
		"heap_inuse_bytes", mem.HeapInuse,
		"heap_idle_bytes", mem.HeapIdle,
		"heap_released_bytes", mem.HeapReleased,
		"heap_objects", mem.HeapObjects,
		"num_gc", mem.NumGC,
	}
	if usenetPool != nil {
		snapshot := usenetPool.TraceSnapshot()
		fields = append(fields,
			"usenet_in_flight_fetches", snapshot.InFlightFetches,
			"usenet_cache", snapshot.CacheSummary(),
			"usenet_providers", snapshot.ProviderSummary(),
		)
	}
	logger.Trace("session teardown snapshot", fields...)
}

// hasSessionsWithFiles reports whether any session in m.sessions has loader files
// loaded (i.e. is actively streaming or has its NZB materialised).
// Caller must hold m.mu (read or write) before calling.
func (m *Manager) hasSessionsWithFiles() bool {
	return len(m.sessionsWithFilesIDs()) > 0
}

func (m *Manager) sessionsWithFilesIDs() []string {
	ids := make([]string, 0)
	for id, s := range m.sessions {
		s.mu.Lock()
		has := s.file != nil || len(s.files) > 0
		s.mu.Unlock()
		if has {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// maybePurgePoolCache purges the shared pool segment cache when no remaining
// session has active loader files.  Call after removing a session from the map
// and closing it, while NOT holding m.mu (it acquires a read lock internally).
func (m *Manager) maybePurgePoolCache() {
	m.mu.RLock()
	usenetPool := m.usenetPool
	active := m.hasSessionsWithFiles()
	m.mu.RUnlock()
	if usenetPool == nil {
		return
	}
	if !active {
		logger.Debug("session: no sessions with active files, purging segment cache")
		usenetPool.PurgeCache()
	}
}

func (m *Manager) DeleteSession(sessionID string) {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if sess != nil {
		logger.Debug("session DeleteSession closing", "id", sessionID)
		sess.Close()
		// Purge the shared pool segment cache if no remaining session still has
		// loader files — deferred (catalog) sessions never set Files, so this
		// correctly fires when streaming ends even though the map is non-empty.
		m.maybePurgePoolCache()
		m.traceTeardownSnapshot("delete_session", sessionID)
		// Suggest returning freed memory to the OS so RSS drops (Go keeps heap by default).
		go freeOSMemory()
	} else {
		logger.Trace("session DeleteSession no session", "id", sessionID)
	}
}

// ClearBlueprintCache clears cached playback blueprints from active sessions so
// subsequent playback opens rebuild unpack selection state.
func (m *Manager) ClearBlueprintCache() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	snapshot := make([]*Session, 0, len(m.sessions))
	for _, sess := range m.sessions {
		snapshot = append(snapshot, sess)
	}
	m.mu.RUnlock()

	cleared := 0
	for _, sess := range snapshot {
		if sess == nil {
			continue
		}
		sess.mu.Lock()
		if sess.blueprint != nil {
			sess.blueprint = nil
			cleared++
		}
		sess.mu.Unlock()
	}
	logger.Info("session blueprint cache cleared", "sessions", len(snapshot), "cleared", cleared)
	return cleared
}

func (s *Session) Close() {
	s.closeWithLogging(true, "")
}

func (s *Session) closeWithLogging(debugLog bool, reason string) {
	s.mu.Lock()
	if debugLog {
		logger.Debug("session Close", "id", s.ID)
	} else {
		logger.Trace("session Close", "id", s.ID, "reason", reason)
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	// Detach the playback stream under the lock but close it outside: Close may
	// touch the network on some stream types, and holding s.mu across IO is the
	// exact hazard the playback-lease code is careful to avoid.
	oldStream := s.playback.resetLocked()
	s.playback = nil
	s.selectedPlaybackFile = ""
	s.playbackStarting = 0

	// Release heavyweight references so a closed session cannot pin NZB / unpack /
	// loader graphs or deferred-download state after it has been removed.
	s.files = nil
	s.file = nil
	s.nzbData = nil
	s.blueprint = nil
	s.rel = nil
	s.ContentIDs = nil
	s.clients = nil
	s.downloadURL = ""
	s.indexer = nil
	s.mu.Unlock()

	if oldStream != nil {
		_ = oldStream.Close()
	}
	logger.Trace("session Close released references", "id", s.ID)
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *Manager) cleanup() {
	m.mu.Lock()

	now := time.Now()
	var toClose []*Session
	var closedIDs []string
	var idleEvictions int
	var postPlaybackEvictions int
	var stuckPlaybackEvictions int
	for id, session := range m.sessions {
		session.mu.Lock()
		// Evict stale Clients entries before computing hasActivePlayback.
		// Without this, a disconnected client whose IP was never seen by GetActiveSessions
		// keeps len(session.clients) > 0, which blocks all eviction paths indefinitely.
		for ip, lastSeen := range session.clients {
			if now.Sub(lastSeen) > clientStaleTTL {
				delete(session.clients, ip)
			}
		}
		hasActivePlayback := session.ActivePlays > 0 || len(session.clients) > 0
		evictIdle := !hasActivePlayback && session.PlaybackEndedAt.IsZero() && now.Sub(session.LastAccess) > m.ttl
		evictPostPlayback := !hasActivePlayback && !session.PlaybackEndedAt.IsZero() && now.Sub(session.PlaybackEndedAt) > m.postPlaybackEvictTTL
		evictStuckPlayback := hasActivePlayback && !session.PlaybackStartedAt.IsZero() && now.Sub(session.PlaybackStartedAt) > m.maxPlaybackDuration
		if evictIdle || evictPostPlayback || evictStuckPlayback {
			delete(m.sessions, id)
			toClose = append(toClose, session)
			closedIDs = append(closedIDs, id)
			switch {
			case evictStuckPlayback:
				stuckPlaybackEvictions++
			case evictPostPlayback:
				postPlaybackEvictions++
			default:
				idleEvictions++
			}
		}
		session.mu.Unlock()
	}
	shouldPurgeCache := len(toClose) > 0 && m.usenetPool != nil && !m.hasSessionsWithFiles()
	m.mu.Unlock()

	for _, s := range toClose {
		s.closeWithLogging(false, "cleanup")
	}
	if len(toClose) > 0 {
		logger.Debug(
			"session cleanup evicted sessions",
			"count", len(toClose),
			"idle", idleEvictions,
			"post_playback", postPlaybackEvictions,
			"stuck_playback", stuckPlaybackEvictions,
			"purge_segment_cache", shouldPurgeCache,
		)
	}
	if shouldPurgeCache {
		m.usenetPool.PurgeCache()
	}
	if len(toClose) > 0 {
		for _, id := range closedIDs {
			m.traceTeardownSnapshot("cleanup_evict", id)
		}
		go freeOSMemory()
	}

	m.slotFailedDuringPlayback.Range(func(key, val any) bool {
		if ent, ok := val.(*failedSlotEntry); ok && now.After(ent.expiresAt) {
			m.slotFailedDuringPlayback.Delete(key)
		}
		return true
	})

	m.failoverOrder.Range(func(key, val any) bool {
		if ent, ok := val.(*failoverOrderEntry); ok {
			if now.After(ent.expiresAt) {
				m.failoverOrder.Delete(key)
			}
			return true
		}
		// Legacy: value was stored as []string without TTL; remove so next store uses failoverOrderEntry
		m.failoverOrder.Delete(key)
		return true
	})

}

// withSession runs fn against the named session with s.mu held, or does
// nothing when the session is gone. Unlike GetSession it does NOT touch
// LastAccess, so callers that want the access time refreshed set it in fn -
// making that write visible rather than an accident of the lookup helper.
func (m *Manager) withSession(id string, fn func(*Session)) {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (m *Manager) StartPlayback(id, ip string) {
	m.withSession(id, func(s *Session) {
		now := time.Now()
		if s.ActivePlays == 0 {
			s.PlaybackStartedAt = now
		}
		s.ActivePlays++
		s.clients[ip] = now
		s.LastAccess = now
	})
}

func (m *Manager) MarkPlaybackValidated(id string) {
	m.withSession(id, func(s *Session) {
		now := time.Now()
		if s.PlaybackValidatedAt.IsZero() {
			s.PlaybackValidatedAt = now
		}
		s.playbackStarting = 0
		s.LastAccess = now
	})
}

func (m *Manager) BeginPlaybackStartup(id string) {
	m.withSession(id, func(s *Session) {
		s.playbackStarting++
		s.LastAccess = time.Now()
	})
}

func (m *Manager) EndPlaybackStartup(id string) {
	m.withSession(id, func(s *Session) {
		if s.playbackStarting > 0 {
			s.playbackStarting--
		}
		s.LastAccess = time.Now()
	})
}

func (m *Manager) EndPlayback(id, ip string) {
	var plays int32
	m.withSession(id, func(s *Session) {
		now := time.Now()
		if s.ActivePlays > 0 {
			s.ActivePlays--
		}
		if s.ActivePlays == 0 {
			s.PlaybackStartedAt = time.Time{}
			s.PlaybackEndedAt = now // so cleanup can evict session after PostPlaybackEvictTTL
		}
		s.clients[ip] = now
		s.LastAccess = now
		plays = s.ActivePlays
	})
	if plays == 0 {
		m.traceTeardownSnapshot("playback_ended", id)
	}
}

func (m *Manager) KeepAlive(id, ip string) {
	m.withSession(id, func(s *Session) {
		now := time.Now()
		s.LastAccess = now
		s.clients[ip] = now
	})
}

// AddBytesRead adds n to the session's bytes-read counter. Used by the stream read path to track data downloaded before reporting good to AvailNZB.
func (m *Manager) AddBytesRead(sessionID string, n int64) {
	if n <= 0 {
		return
	}
	m.mu.RLock()
	s := m.sessions[sessionID]
	m.mu.RUnlock()
	if s != nil {
		s.AddBytesRead(n)
	}
}

type ActiveSessionInfo struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Clients   []string `json:"clients"`
	StartTime string   `json:"start_time"`
}

func (m *Manager) GetActiveSessions() []ActiveSessionInfo {
	logger.Trace("session GetActiveSessions start")
	m.mu.RLock()
	snapshot := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		snapshot = append(snapshot, s)
	}
	m.mu.RUnlock()

	var result []ActiveSessionInfo
	for _, s := range snapshot {

		if !s.mu.TryLock() {
			continue
		}

		for ip, lastSeen := range s.clients {
			if time.Since(lastSeen) > 60*time.Second {
				delete(s.clients, ip)
			}
		}
		isActive := len(s.clients) > 0
		if isActive {
			clients := make([]string, 0, len(s.clients))
			for ip := range s.clients {
				clients = append(clients, ip)
			}
			title := "Unknown"
			if s.rel != nil && s.rel.Title != "" {
				title = s.rel.Title
			} else if s.nzbData != nil && len(s.nzbData.Files) > 0 {
				parts := strings.Split(fileutil.ExtractFilename(s.nzbData.Files[0].Subject), ".")
				if len(parts) > 1 {
					title = strings.Join(parts[:len(parts)-1], ".")
				} else {
					title = parts[0]
				}
			}
			result = append(result, ActiveSessionInfo{
				ID:        s.ID,
				Title:     title,
				Clients:   clients,
				StartTime: s.CreatedAt.Format(time.Kitchen),
			})
		}
		s.mu.Unlock()
	}
	logger.Trace("session GetActiveSessions done", "sessions", len(snapshot), "active", len(result))
	return result
}

func (m *Manager) UpdateUsenetPool(up *pool.Pool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usenetPool = up
}

func (m *Manager) SegmentFetcherForProviders(providerIDs []string) loader.SegmentFetcher {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.usenetPool == nil {
		return nil
	}
	subset := m.usenetPool.Subset(providerIDs)
	if subset == nil {
		return m.usenetPool
	}
	return subset
}

func (m *Manager) ProviderHostsForProviders(providerIDs []string) []string {
	fetcher := m.SegmentFetcherForProviders(providerIDs)
	if fetcher == nil {
		return nil
	}
	if p, ok := fetcher.(*pool.Pool); ok {
		return p.ProviderHosts()
	}
	return nil
}
