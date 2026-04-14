package session

import (
	"bytes"
	"context"
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
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/fileutil"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/release"
	"streamnzb/pkg/usenet/nntp"
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
	NZB        *nzb.NZB
	Files      []*loader.File
	File       *loader.File
	StreamName string

	Blueprint           interface{}
	CreatedAt           time.Time
	LastAccess          time.Time
	ActivePlays         int32
	PlaybackValidatedAt time.Time // when probe/prepare proved the file is playable; separate from playback end bookkeeping
	PlaybackStartedAt   time.Time // when ActivePlays went from 0 to >0; used to evict stuck sessions
	PlaybackEndedAt     time.Time // when ActivePlays went to 0; used to evict session soon after stream stops
	Clients             map[string]time.Time
	mu                  sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	Release *release.Release

	ContentIDs *AvailReportMeta

	// ContentType and ContentID are the request context (e.g. "movie"/"series" and "tt123" or "tmdb:123:1:2") for NZB attempt history.
	ContentType          string
	ContentID            string
	ContentTitle         string
	selectedPlaybackFile string

	downloadURL string
	indexer     indexer.Indexer

	segmentFetcher     loader.SegmentFetcher
	providerHosts      []string
	usedProviders      map[string]struct{}
	servedProviders    map[string]struct{}
	serveTrackingDepth int

	bytesRead atomic.Int64 // bytes read during playback; used for AvailNZB good-report threshold
	playback  *playbackStreamState
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

func (s *Session) ReleaseURL() string {
	if s.Release != nil && s.Release.DetailsURL != "" {
		return s.Release.DetailsURL
	}
	return s.downloadURL
}

func (s *Session) ReportSize() int64 {
	if s.NZB != nil {
		return s.NZB.TotalSize()
	}
	if s.Release != nil {
		return s.Release.Size
	}
	return 0
}

func (s *Session) ReportReleaseName() string {
	if s.Release != nil {
		return s.Release.Title
	}
	return ""
}

func (s *Session) ReleaseIndexer() string {
	if s.Release != nil {
		return s.Release.Indexer
	}
	return ""
}

func (s *Session) ProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.providerHosts...)
}

func (s *Session) UsedProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.usedProviders) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(s.usedProviders))
	for host := range s.usedProviders {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (s *Session) ServedProviderHosts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.servedProviders) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(s.servedProviders))
	for host := range s.servedProviders {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func (s *Session) RecordUsedProviderHost(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usedProviders == nil {
		s.usedProviders = make(map[string]struct{})
	}
	s.usedProviders[host] = struct{}{}
}

func (s *Session) RecordServedProviderHost(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.servedProviders == nil {
		s.servedProviders = make(map[string]struct{})
	}
	s.servedProviders[host] = struct{}{}
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
	return f.statter.StatSegment(ctx, messageID, groups)
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
func (s *Session) HasPreviouslyServed() bool {
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

type failoverOrderEntry struct {
	order     []string
	expiresAt time.Time
}

type Manager struct {
	sessions                 map[string]*Session
	pools                    []*nntp.ClientPool
	usenetPool               *pool.Pool
	estimator                *loader.SegmentSizeEstimator
	ttl                      time.Duration
	maxPlaybackDuration      time.Duration
	mu                       sync.RWMutex
	failoverOrder            sync.Map
	slotFailedDuringPlayback sync.Map // slotPath -> *failedSlotEntry (430 during streaming)
	stopCh                   chan struct{}
}

type failedSlotEntry struct {
	expiresAt time.Time
}

func (s *Session) SetBlueprint(bp interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Blueprint = bp
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

func (s *Session) ResetPlaybackStream() {
	s.mu.Lock()
	state := s.playback
	if state == nil {
		s.mu.Unlock()
		return
	}
	stream := state.stream
	state.stream = nil
	state.spec = PlaybackStreamSpec{}
	state.opening = false
	state.inUse = false
	state.startupInfo = seek.StreamStartInfo{}
	state.hasStartupInfo = false
	if state.cond != nil {
		state.cond.Broadcast()
	}
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

func NewManager(pools []*nntp.ClientPool, usenetPool *pool.Pool, ttl time.Duration) *Manager {
	m := &Manager{
		sessions:            make(map[string]*Session),
		pools:               pools,
		usenetPool:          usenetPool,
		estimator:           loader.NewSegmentSizeEstimator(),
		ttl:                 ttl,
		maxPlaybackDuration: MaxPlaybackDuration,
		stopCh:              make(chan struct{}),
	}

	go m.cleanupLoop()
	return m
}

// Shutdown stops the background cleanup goroutine. Call during application shutdown.
func (m *Manager) Shutdown() {
	select {
	case <-m.stopCh:
		// already closed
	default:
		close(m.stopCh)
	}
}

type AvailReportMeta struct {
	ImdbID  string
	TmdbID  string
	TvdbID  string
	Season  int
	Episode int
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
		return nil, fmt.Errorf("no content files found in NZB")
	}
	m.mu.RLock()
	pools := m.pools
	usenetPool := m.usenetPool
	estimator := m.estimator
	m.mu.RUnlock()
	if segmentFetcher == nil {
		segmentFetcher = usenetPool
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{
		ID:             sessionID,
		NZB:            nzbData,
		Release:        rel,
		ContentIDs:     contentIDs,
		CreatedAt:      time.Now(),
		LastAccess:     time.Now(),
		Clients:        make(map[string]time.Time),
		ctx:            ctx,
		cancel:         cancel,
		segmentFetcher: segmentFetcher,
		providerHosts:  append([]string(nil), providerHosts...),
	}
	session.segmentFetcher = attachProviderTracking(session, session.segmentFetcher)
	loaderFiles := buildLoaderFiles(ctx, sessionID, contentFiles, pools, session.segmentFetcher, estimator)
	session.Files = loaderFiles
	session.File = loaderFiles[0]

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
	if contentIDs != nil && contentIDs.Season > 0 && contentIDs.Episode > 0 {
		if files := nzbData.GetSessionContentFilesForEpisode(contentIDs.Season, contentIDs.Episode); len(files) > 0 {
			return files
		}
	}
	return nzbData.GetContentFiles()
}

func buildLoaderFiles(ctx context.Context, ownerID string, contentFiles []*nzb.FileInfo, pools []*nntp.ClientPool, usenetPool loader.SegmentFetcher, estimator *loader.SegmentSizeEstimator) []*loader.File {
	loaderFiles := make([]*loader.File, 0, len(contentFiles))
	for _, info := range contentFiles {
		var lf *loader.File
		if usenetPool != nil {
			lf = loader.NewFile(ctx, info.File, nil, estimator, usenetPool)
		} else {
			lf = loader.NewFile(ctx, info.File, pools, estimator, nil)
		}
		lf.SetOwnerSessionID(ownerID)
		loaderFiles = append(loaderFiles, lf)
	}
	return loaderFiles
}

func (m *Manager) CreateDeferredSession(sessionID, downloadURL string, rel *release.Release, idx indexer.Indexer, contentIDs *AvailReportMeta, contentType, contentID, contentTitle, streamName string) (*Session, error) {
	return m.CreateDeferredSessionWithFetcher(sessionID, downloadURL, rel, idx, contentIDs, contentType, contentID, contentTitle, streamName, nil, nil)
}

func (m *Manager) CreateDeferredSessionWithFetcher(sessionID, downloadURL string, rel *release.Release, idx indexer.Indexer, contentIDs *AvailReportMeta, contentType, contentID, contentTitle, streamName string, segmentFetcher loader.SegmentFetcher, providerHosts []string) (*Session, error) {
	logger.Trace("session CreateDeferredSession start", "id", sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.sessions[sessionID]; ok {
		existing.mu.Lock()
		existing.LastAccess = time.Now()
		existing.mu.Unlock()
		logger.Trace("session CreateDeferredSession existing", "id", sessionID)
		return existing, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	trackedFetcher := segmentFetcher
	session := &Session{
		ID:             sessionID,
		StreamName:     streamName,
		NZB:            nil,
		Release:        rel,
		ContentIDs:     contentIDs,
		ContentType:    contentType,
		ContentID:      contentID,
		ContentTitle:   contentTitle,
		downloadURL:    downloadURL,
		indexer:        idx,
		CreatedAt:      time.Now(),
		LastAccess:     time.Now(),
		Clients:        make(map[string]time.Time),
		ctx:            ctx,
		cancel:         cancel,
		segmentFetcher: trackedFetcher,
		providerHosts:  append([]string(nil), providerHosts...),
	}
	session.segmentFetcher = attachProviderTracking(session, trackedFetcher)
	m.sessions[sessionID] = session
	logger.Trace("session CreateDeferredSession done", "id", sessionID)
	return session, nil
}

func (s *Session) GetOrDownloadNZB(manager *Manager) (*nzb.NZB, error) {
	s.mu.Lock()
	if s.NZB != nil {
		nzb := s.NZB
		s.mu.Unlock()
		return nzb, nil
	}
	if s.downloadURL == "" || s.indexer == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("session has no NZB and no deferred download info")
	}
	nzbURL := s.downloadURL
	idx := s.indexer
	itemTitle := ""
	indexerName := ""
	if s.Release != nil {
		itemTitle = s.Release.Title
		indexerName = s.Release.Indexer
	}
	ctx := s.ctx
	s.mu.Unlock()

	var data []byte
	var err error
	downloadCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	logger.Trace("Lazy Downloading NZB (direct)...", "title", itemTitle, "indexer", indexerName)
	data, err = idx.DownloadNZB(downloadCtx, nzbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lazy download NZB: %w", err)
	}
	if len(data) == 0 {
		logger.Debug("NZB download returned empty body", "indexer", indexerName, "title", itemTitle, "url", nzbURL)
		return nil, fmt.Errorf("NZB download returned empty body (indexer: %s)", indexerName)
	}
	parsedNZB, err := nzb.Parse(bytes.NewReader(data))
	if err != nil {
		snippet := string(data)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		logger.Debug("Failed to parse NZB", "indexer", indexerName, "title", itemTitle, "url", nzbURL, "len", len(data), "snippet", snippet, "err", err)
		return nil, fmt.Errorf("failed to parse lazy downloaded NZB: %w", err)
	}
	contentFiles := selectSessionContentFiles(parsedNZB, s.ContentIDs)
	if len(contentFiles) == 0 {
		logger.Error("Lazy load: no content files in NZB",
			"title", itemTitle,
			"indexer", indexerName,
			"nzb_files", len(parsedNZB.Files),
			"details", "see DEBUG log GetContentFiles returned empty for file list")
		return nil, fmt.Errorf("no content files found in lazy NZB")
	}

	manager.mu.RLock()
	pools := manager.pools
	usenetPool := manager.usenetPool
	estimator := manager.estimator
	manager.mu.RUnlock()
	segmentFetcher := s.segmentFetcher
	if segmentFetcher == nil {
		segmentFetcher = usenetPool
	}

	loaderFiles := buildLoaderFiles(ctx, s.ID, contentFiles, pools, segmentFetcher, estimator)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.NZB != nil {
		return s.NZB, nil
	}
	s.NZB = parsedNZB
	s.Files = loaderFiles
	s.File = loaderFiles[0]
	logger.Debug("session GetOrDownloadNZB created loader files", "id", s.ID, "files", len(loaderFiles))
	return s.NZB, nil
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

func summarizeClientPools(pools []*nntp.ClientPool) string {
	if len(pools) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(pools))
	for i, p := range pools {
		if p == nil {
			parts = append(parts, fmt.Sprintf("pool[%d](nil)", i))
			continue
		}
		parts = append(parts, fmt.Sprintf("pool[%d](host=%s total=%d idle=%d active=%d)", i, p.Host(), p.TotalConnections(), p.IdleConnections(), p.ActiveConnections()))
	}
	return strings.Join(parts, "; ")
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
		activeClients += len(sess.Clients)
		sess.mu.Unlock()
	}
	usenetPool := m.usenetPool
	pools := append([]*nntp.ClientPool(nil), m.pools...)
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
	} else {
		fields = append(fields, "nntp_pools", summarizeClientPools(pools))
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
		has := s.File != nil || len(s.Files) > 0
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
	if m.usenetPool == nil {
		return
	}
	m.mu.RLock()
	active := m.hasSessionsWithFiles()
	m.mu.RUnlock()
	if !active {
		logger.Debug("session: no sessions with active files, purging segment cache")
		m.usenetPool.PurgeCache()
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

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	logger.Debug("session Close", "id", s.ID)
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.playback != nil {
		if s.playback.stream != nil {
			_ = s.playback.stream.Close()
			s.playback.stream = nil
		}
		s.playback.spec = PlaybackStreamSpec{}
		s.playback.opening = false
		s.playback.inUse = false
		s.playback.startupInfo = seek.StreamStartInfo{}
		s.playback.hasStartupInfo = false
		if s.playback.cond != nil {
			s.playback.cond.Broadcast()
		}
		s.playback = nil
	}
	s.selectedPlaybackFile = ""

	// Release heavyweight references so a closed session cannot pin NZB / unpack /
	// loader graphs or deferred-download state after it has been removed.
	s.Files = nil
	s.File = nil
	s.NZB = nil
	s.Blueprint = nil
	s.Release = nil
	s.ContentIDs = nil
	s.Clients = nil
	s.downloadURL = ""
	s.indexer = nil
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
	for id, session := range m.sessions {
		session.mu.Lock()
		// Evict stale Clients entries before computing hasActivePlayback.
		// Without this, a disconnected client whose IP was never seen by GetActiveSessions
		// keeps len(session.Clients) > 0, which blocks all eviction paths indefinitely.
		for ip, lastSeen := range session.Clients {
			if now.Sub(lastSeen) > clientStaleTTL {
				delete(session.Clients, ip)
			}
		}
		hasActivePlayback := session.ActivePlays > 0 || len(session.Clients) > 0
		evictIdle := !hasActivePlayback && now.Sub(session.LastAccess) > m.ttl
		evictPostPlayback := !hasActivePlayback && !session.PlaybackEndedAt.IsZero() && now.Sub(session.PlaybackEndedAt) > PostPlaybackEvictTTL
		evictStuckPlayback := hasActivePlayback && !session.PlaybackStartedAt.IsZero() && now.Sub(session.PlaybackStartedAt) > m.maxPlaybackDuration
		if evictIdle || evictPostPlayback || evictStuckPlayback {
			delete(m.sessions, id)
			toClose = append(toClose, session)
			closedIDs = append(closedIDs, id)
		}
		session.mu.Unlock()
	}
	shouldPurgeCache := len(toClose) > 0 && m.usenetPool != nil && !m.hasSessionsWithFiles()
	m.mu.Unlock()

	for _, s := range toClose {
		logger.Debug("session cleanup evicting", "id", s.ID)
		s.Close()
	}
	if shouldPurgeCache {
		logger.Debug("session cleanup: no sessions with active files, purging segment cache")
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

func (m *Manager) StartPlayback(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.ActivePlays == 0 {
			s.PlaybackStartedAt = time.Now()
		}
		s.ActivePlays++
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
}

func (m *Manager) MarkPlaybackValidated(id string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.PlaybackValidatedAt.IsZero() {
			s.PlaybackValidatedAt = time.Now()
		}
		s.mu.Unlock()
	}
}

func (m *Manager) EndPlayback(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		if s.ActivePlays > 0 {
			s.ActivePlays--
		}
		if s.ActivePlays == 0 {
			s.PlaybackStartedAt = time.Time{}
			s.PlaybackEndedAt = time.Now() // so cleanup can evict session after PostPlaybackEvictTTL
		}
		s.Clients[ip] = time.Now()
		plays := s.ActivePlays
		s.mu.Unlock()
		if plays == 0 {
			m.traceTeardownSnapshot("playback_ended", id)
		}
	}
}

func (m *Manager) KeepAlive(id, ip string) {
	s, err := m.GetSession(id)
	if err == nil {
		s.mu.Lock()
		s.LastAccess = time.Now()
		s.Clients[ip] = time.Now()
		s.mu.Unlock()
	}
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

		for ip, lastSeen := range s.Clients {
			if time.Since(lastSeen) > 60*time.Second {
				delete(s.Clients, ip)
			}
		}
		isActive := len(s.Clients) > 0
		if isActive {
			clients := make([]string, 0, len(s.Clients))
			for ip := range s.Clients {
				clients = append(clients, ip)
			}
			title := "Unknown"
			if s.Release != nil && s.Release.Title != "" {
				title = s.Release.Title
			} else if s.NZB != nil && len(s.NZB.Files) > 0 {
				parts := strings.Split(fileutil.ExtractFilename(s.NZB.Files[0].Subject), ".")
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

func (m *Manager) UpdatePools(pools []*nntp.ClientPool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pools = pools
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
	if len(providerIDs) == 0 {
		return m.usenetPool
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
