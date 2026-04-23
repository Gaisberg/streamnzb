package session

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/release"
	"streamnzb/pkg/usenet/pool"
)

type fakePlaybackStream struct {
	pos        int64
	closed     bool
	closeCalls int
}

func (f *fakePlaybackStream) Read(_ []byte) (int, error) {
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	return 0, io.EOF
}

func (f *fakePlaybackStream) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	switch whence {
	case io.SeekStart:
		f.pos = offset
	case io.SeekCurrent:
		f.pos += offset
	case io.SeekEnd:
		f.pos = offset
	}
	return f.pos, nil
}

func (f *fakePlaybackStream) Close() error {
	f.closed = true
	f.closeCalls++
	return nil
}

type fakeIndexer struct {
	data     []byte
	err      error
	calls    int
	lastURL  string
	typeName string
	wait     <-chan struct{}
	started  chan struct{}
	mu       sync.Mutex
}

func (*fakeIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) { return nil, nil }
func (f *fakeIndexer) DownloadNZB(ctx context.Context, rawURL string) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.lastURL = rawURL
	wait := f.wait
	started := f.started
	data := f.data
	err := f.err
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return data, err
}
func (*fakeIndexer) Ping() error             { return nil }
func (*fakeIndexer) Name() string            { return "fake" }
func (*fakeIndexer) GetUsage() indexer.Usage { return indexer.Usage{} }
func (f *fakeIndexer) Type() string {
	if f.typeName != "" {
		return f.typeName
	}
	return "newznab"
}

type fakeSegmentFetcher struct {
	data pool.SegmentData
	err  error
}

func (f *fakeSegmentFetcher) FetchSegment(_ context.Context, _ *nzb.Segment, _ []string) (pool.SegmentData, error) {
	return f.data, f.err
}

func (f *fakeSegmentFetcher) FetchSegmentFirst(ctx context.Context, segment *nzb.Segment, groups []string) (pool.SegmentData, error) {
	return f.FetchSegment(ctx, segment, groups)
}

func TestSessionCloseClearsHeavyReferences(t *testing.T) {
	logger.Init("ERROR")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	file := loader.NewFile(ctx, &nzb.File{Subject: "test.mkv"}, nil, nil, nil)
	s := &Session{
		ID:          "sess-1",
		NZB:         &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 10}}}}},
		Files:       []*loader.File{file},
		File:        file,
		Blueprint:   &struct{ Data []byte }{Data: make([]byte, 1024)},
		Release:     &release.Release{Title: "release"},
		ContentIDs:  &AvailReportMeta{ImdbID: "tt123"},
		Clients:     map[string]time.Time{"127.0.0.1": time.Now()},
		downloadURL: "https://example.invalid/nzb",
		indexer:     &fakeIndexer{},
		cancel:      cancel,
	}

	s.Close()

	if s.NZB != nil || s.Blueprint != nil || s.Release != nil || s.ContentIDs != nil {
		t.Fatalf("expected heavy session references to be cleared")
	}
	if s.Files != nil || s.File != nil || s.Clients != nil {
		t.Fatalf("expected loader and client references to be cleared")
	}
	if s.downloadURL != "" || s.indexer != nil || s.cancel != nil {
		t.Fatalf("expected deferred session state to be cleared")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatalf("expected Close to cancel session context")
	}
}

func TestCreateSessionAssignsFileOwners(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	nzbData := &nzb.NZB{Files: []nzb.File{{Subject: "video.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 10}}}}}

	s, err := m.CreateSession("sess-1", nzbData, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if s.File == nil {
		t.Fatalf("expected session file to be created")
	}
	if got := s.File.OwnerSessionID(); got != "sess-1" {
		t.Fatalf("expected owner session id sess-1, got %q", got)
	}
	if got := m.sessionsWithFilesIDs(); len(got) != 1 || got[0] != "sess-1" {
		t.Fatalf("expected sessionsWithFilesIDs to return sess-1, got %v", got)
	}
	s.Close()
}

func TestMarkPlaybackValidatedSeparatesValidationFromPlaybackEnd(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{ID: "sess-1", Clients: make(map[string]time.Time)}
	m := &Manager{sessions: map[string]*Session{"sess-1": s}}

	if s.HasPreviouslyServed() {
		t.Fatal("expected unvalidated session to report false")
	}

	m.MarkPlaybackValidated("sess-1")

	if !s.HasPreviouslyServed() {
		t.Fatal("expected validated session to report true")
	}
	if !s.PlaybackEndedAt.IsZero() {
		t.Fatal("expected validation to stay separate from playback end bookkeeping")
	}
}

func TestCreateSessionSelectsRequestedEpisode(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	nzbData := &nzb.NZB{Files: []nzb.File{
		{Subject: "Show.S01E06.1080p.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 60}}},
		{Subject: "Show.S01E05.1080p.mkv", Segments: []nzb.Segment{{ID: "<b>", Bytes: 50}}},
	}}

	s, err := m.CreateSession("sess-target", nzbData, nil, &AvailReportMeta{Season: 1, Episode: 5})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if got := s.File.Name(); !strings.Contains(got, "S01E05") {
		t.Fatalf("expected target episode file, got %q", got)
	}
	if len(s.Files) != 1 {
		t.Fatalf("expected one selected file, got %d", len(s.Files))
	}
	s.Close()
}

func TestGetOrDownloadNZBSelectsRequestedEpisode(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	data := marshalTestNZB(t, &nzb.NZB{Files: []nzb.File{
		{Subject: "Show.S01E06.1080p.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 60}}},
		{Subject: "Show.S01E05.1080p.mkv", Segments: []nzb.Segment{{ID: "<b>", Bytes: 50}}},
	}})
	idx := &fakeIndexer{data: data}
	s, err := m.CreateDeferredSession("sess-lazy", "https://example.invalid/get?nzb=1&apikey=test", nil, idx, &AvailReportMeta{Season: 1, Episode: 5}, "series", "tmdb:1:1:5", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}
	if _, err := s.GetOrDownloadNZB(m); err != nil {
		t.Fatalf("GetOrDownloadNZB returned error: %v", err)
	}
	if idx.calls != 1 {
		t.Fatalf("expected DownloadNZB to be called once, got %d", idx.calls)
	}
	if s.File == nil {
		t.Fatal("expected session file after lazy load")
	}
	if got := s.File.Name(); !strings.Contains(got, "S01E05") {
		t.Fatalf("expected target episode file after lazy load, got %q", got)
	}
	if len(s.Files) != 1 {
		t.Fatalf("expected one selected file after lazy load, got %d", len(s.Files))
	}
	s.Close()
}

func TestCreateDeferredSessionTracksUsedProviderHosts(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{sessions: make(map[string]*Session)}
	fetcher := &fakeSegmentFetcher{
		data: pool.SegmentData{
			Body:         []byte("segment-data"),
			Size:         int64(len("segment-data")),
			ProviderHost: "news.example.net",
		},
	}
	s, err := m.CreateDeferredSessionWithFetcher(
		"sess-provider-hosts",
		"https://example.invalid/get?nzb=1",
		nil,
		&fakeIndexer{},
		nil,
		"movie",
		"tt1375666",
		"Inception",
		"Stream01",
		fetcher,
		[]string{"configured.example.net"},
	)
	if err != nil {
		t.Fatalf("CreateDeferredSessionWithFetcher returned error: %v", err)
	}

	data, err := s.segmentFetcher.FetchSegment(context.Background(), &nzb.Segment{ID: "<seg1>"}, nil)
	if err != nil {
		t.Fatalf("FetchSegment returned error: %v", err)
	}
	if got := data.ProviderHost; got != "news.example.net" {
		t.Fatalf("ProviderHost = %q, want %q", got, "news.example.net")
	}

	used := s.UsedProviderHosts()
	if len(used) != 1 || used[0] != "news.example.net" {
		t.Fatalf("UsedProviderHosts = %v, want [news.example.net]", used)
	}
	if configured := s.ProviderHosts(); len(configured) != 1 || configured[0] != "configured.example.net" {
		t.Fatalf("ProviderHosts = %v, want [configured.example.net]", configured)
	}
	if served := s.ServedProviderHosts(); len(served) != 0 {
		t.Fatalf("ServedProviderHosts = %v, want none before serve tracking", served)
	}

	s.BeginServeProviderTracking()
	if _, err := s.segmentFetcher.FetchSegment(context.Background(), &nzb.Segment{ID: "<seg2>"}, nil); err != nil {
		t.Fatalf("FetchSegment during serve tracking returned error: %v", err)
	}
	s.EndServeProviderTracking()

	served := s.ServedProviderHosts()
	if len(served) != 1 || served[0] != "news.example.net" {
		t.Fatalf("ServedProviderHosts = %v, want [news.example.net]", served)
	}
}

func TestCreateDeferredSessionReplacesStaleDeferredSessionWhenSourceChanges(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{sessions: make(map[string]*Session)}
	first, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-replace",
		"https://example.invalid/get?nzb=1",
		&release.Release{Title: "Old Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(first) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateCreated {
		t.Fatalf("expected first session to be created, got %q", outcome)
	}
	first.mu.Lock()
	first.LastAccess = time.Now().Add(-2 * deferredSessionReplaceGrace)
	first.mu.Unlock()

	second, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-replace",
		"https://example.invalid/get?nzb=2",
		&release.Release{Title: "New Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(second) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateReplaced {
		t.Fatalf("expected second session to replace the stale session, got %q", outcome)
	}
	if first == second {
		t.Fatal("expected stale deferred session to be replaced")
	}
	if second.Release == nil || second.Release.Title != "New Release" {
		t.Fatalf("expected replacement session to carry new release, got %#v", second.Release)
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("expected replaced session to be closed")
	}

	third, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-replace",
		"https://example.invalid/get?nzb=2",
		&release.Release{Title: "Newest Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(third) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateExisting {
		t.Fatalf("expected third session to reuse the current session, got %q", outcome)
	}
	if third != second {
		t.Fatal("expected matching deferred session to be reused")
	}
}

func TestCreateDeferredSessionDoesNotReplaceActiveOrStartingSession(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{sessions: make(map[string]*Session)}
	first, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-protected",
		"https://example.invalid/get?nzb=1",
		&release.Release{Title: "Old Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(first) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateCreated {
		t.Fatalf("expected first session to be created, got %q", outcome)
	}

	first.mu.Lock()
	first.LastAccess = time.Now().Add(-2 * deferredSessionReplaceGrace)
	first.ActivePlays = 1
	first.mu.Unlock()

	second, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-protected",
		"https://example.invalid/get?nzb=2",
		&release.Release{Title: "New Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(second) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateExisting {
		t.Fatalf("expected active session to be reused, got %q", outcome)
	}
	if second != first {
		t.Fatal("expected active session to stay in place")
	}

	first.mu.Lock()
	first.ActivePlays = 0
	first.playbackStarting = 1
	first.LastAccess = time.Now().Add(-2 * deferredSessionReplaceGrace)
	first.mu.Unlock()

	third, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-protected",
		"https://example.invalid/get?nzb=3",
		&release.Release{Title: "Newest Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(third) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateExisting {
		t.Fatalf("expected startup-protected session to be reused, got %q", outcome)
	}
	if third != first {
		t.Fatal("expected startup-protected session to stay in place")
	}

	first.mu.Lock()
	first.playbackStarting = 0
	first.nzbDownloadInFlight = true
	first.LastAccess = time.Now().Add(-2 * deferredSessionReplaceGrace)
	first.mu.Unlock()

	fourth, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-protected",
		"https://example.invalid/get?nzb=4",
		&release.Release{Title: "Final Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(fourth) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateExisting {
		t.Fatalf("expected downloading session to be reused, got %q", outcome)
	}
	if fourth != first {
		t.Fatal("expected downloading session to stay in place")
	}
}

func TestCreateDeferredSessionDoesNotReplaceRecentlyAccessedSession(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{sessions: make(map[string]*Session)}
	first, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-grace",
		"https://example.invalid/get?nzb=1",
		&release.Release{Title: "Old Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(first) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateCreated {
		t.Fatalf("expected first session to be created, got %q", outcome)
	}

	first.mu.Lock()
	first.LastAccess = time.Now()
	first.mu.Unlock()

	second, outcome, err := m.CreateDeferredSessionWithFetcherOutcome(
		"sess-grace",
		"https://example.invalid/get?nzb=2",
		&release.Release{Title: "New Release"},
		&fakeIndexer{},
		nil,
		"series",
		"tt1190634:5:1",
		"The Boys",
		"Stream01",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateDeferredSession(second) returned error: %v", err)
	}
	if outcome != DeferredSessionCreateExisting {
		t.Fatalf("expected recently accessed session to be reused, got %q", outcome)
	}
	if second != first {
		t.Fatal("expected recently accessed session to stay in place")
	}
}

func TestServeProviderTrackingUsesDepthCounter(t *testing.T) {
	s := &Session{}

	s.BeginServeProviderTracking()
	s.BeginServeProviderTracking()
	if !s.IsServeProviderTrackingEnabled() {
		t.Fatal("expected serve provider tracking to be enabled")
	}

	s.EndServeProviderTracking()
	if !s.IsServeProviderTrackingEnabled() {
		t.Fatal("expected serve provider tracking to remain enabled while another serve window is still active")
	}

	s.EndServeProviderTracking()
	if s.IsServeProviderTrackingEnabled() {
		t.Fatal("expected serve provider tracking to be disabled after the final serve window ends")
	}
}

func TestCreateSessionKeepsBroadCandidatesWhenEpisodeMatchUnknown(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	nzbData := &nzb.NZB{Files: []nzb.File{
		{Subject: "Altered.Carbon.Release.A.part02.rar", Segments: []nzb.Segment{{ID: "<a>", Bytes: 310}}},
		{Subject: "Altered.Carbon.Release.B.part01.rar", Segments: []nzb.Segment{{ID: "<b>", Bytes: 420}}},
		{Subject: "Altered.Carbon.Release.A.part01.rar", Segments: []nzb.Segment{{ID: "<c>", Bytes: 300}}},
		{Subject: "Altered.Carbon.Release.B.part02.rar", Segments: []nzb.Segment{{ID: "<d>", Bytes: 410}}},
	}}

	s, err := m.CreateSession("sess-broad", nzbData, nil, &AvailReportMeta{Season: 2, Episode: 1})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if len(s.Files) != 4 {
		t.Fatalf("expected broad fallback candidate set, got %d files", len(s.Files))
	}
	var sawA, sawB bool
	for _, file := range s.Files {
		name := file.Name()
		if strings.Contains(name, "Release.A") {
			sawA = true
		}
		if strings.Contains(name, "Release.B") {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected files from both fallback groups, sawA=%v sawB=%v", sawA, sawB)
	}
	s.Close()
}

func TestGetOrDownloadNZBKeepsBroadCandidatesWhenEpisodeMatchUnknown(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	data := marshalTestNZB(t, &nzb.NZB{Files: []nzb.File{
		{Subject: "Altered.Carbon.Release.A.part02.rar", Segments: []nzb.Segment{{ID: "<a>", Bytes: 310}}},
		{Subject: "Altered.Carbon.Release.B.part01.rar", Segments: []nzb.Segment{{ID: "<b>", Bytes: 420}}},
		{Subject: "Altered.Carbon.Release.A.part01.rar", Segments: []nzb.Segment{{ID: "<c>", Bytes: 300}}},
		{Subject: "Altered.Carbon.Release.B.part02.rar", Segments: []nzb.Segment{{ID: "<d>", Bytes: 410}}},
	}})
	idx := &fakeIndexer{data: data}
	s, err := m.CreateDeferredSession("sess-broad-lazy", "https://example.invalid/get?nzb=1&apikey=test", nil, idx, &AvailReportMeta{Season: 2, Episode: 1}, "series", "tmdb:1:2:1", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}
	if _, err := s.GetOrDownloadNZB(m); err != nil {
		t.Fatalf("GetOrDownloadNZB returned error: %v", err)
	}
	if len(s.Files) != 4 {
		t.Fatalf("expected broad fallback candidate set after lazy load, got %d files", len(s.Files))
	}
	var sawA, sawB bool
	for _, file := range s.Files {
		name := file.Name()
		if strings.Contains(name, "Release.A") {
			sawA = true
		}
		if strings.Contains(name, "Release.B") {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected files from both fallback groups after lazy load, sawA=%v sawB=%v", sawA, sawB)
	}
	s.Close()
}

func TestGetOrDownloadNZBDownloadsKeylessURL(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	data := marshalTestNZB(t, &nzb.NZB{Files: []nzb.File{{
		Subject:  "Movie.2024.1080p.mkv",
		Segments: []nzb.Segment{{ID: "<a>", Bytes: 60}},
	}}})
	idx := &fakeIndexer{data: data, typeName: "newznab"}
	s, err := m.CreateDeferredSession("sess-keyless", "https://nzbfinder.ws/api?t=get&id=abc123", nil, idx, nil, "movie", "tt123", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}
	if _, err := s.GetOrDownloadNZB(m); err != nil {
		t.Fatalf("GetOrDownloadNZB returned error: %v", err)
	}
	if idx.calls != 1 {
		t.Fatalf("expected DownloadNZB to be called once for keyless URL, got %d", idx.calls)
	}
	if idx.lastURL != "https://nzbfinder.ws/api?t=get&id=abc123" {
		t.Fatalf("DownloadNZB called with URL %q", idx.lastURL)
	}
	s.Close()
}

func TestGetOrDownloadNZBWithContextHonorsCancellation(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	idx := &fakeIndexer{err: context.DeadlineExceeded}
	s, err := m.CreateDeferredSession("sess-cancel", "https://example.invalid/get?nzb=1", nil, idx, nil, "movie", "tt123", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.GetOrDownloadNZBWithContext(ctx, m); err == nil {
		t.Fatal("expected canceled context error")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if idx.calls != 0 {
		t.Fatalf("expected DownloadNZB not to be called, got %d calls", idx.calls)
	}
	s.Close()
}

func TestGetOrDownloadNZBWithContextDeduplicatesConcurrentDownloads(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	data := marshalTestNZB(t, &nzb.NZB{Files: []nzb.File{{
		Subject:  "Movie.2024.1080p.mkv",
		Segments: []nzb.Segment{{ID: "<a>", Bytes: 60}},
	}}})
	wait := make(chan struct{})
	started := make(chan struct{}, 2)
	idx := &fakeIndexer{data: data, wait: wait, started: started}
	s, err := m.CreateDeferredSession("sess-shared-download", "https://example.invalid/get?nzb=1", nil, idx, nil, "movie", "tt123", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}

	errCh := make(chan error, 2)
	startGate := make(chan struct{})
	ready := make(chan struct{}, 2)
	go func() {
		ready <- struct{}{}
		<-startGate
		_, err := s.GetOrDownloadNZBWithContext(context.Background(), m)
		errCh <- err
	}()
	go func() {
		ready <- struct{}{}
		<-startGate
		_, err := s.GetOrDownloadNZBWithContext(context.Background(), m)
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for goroutines to be ready")
		}
	}
	close(startGate)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NZB download to start")
	}

	close(wait)

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("GetOrDownloadNZBWithContext returned error: %v", err)
		}
	}

	idx.mu.Lock()
	calls := idx.calls
	idx.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected DownloadNZB to be called once, got %d", calls)
	}
	if s.NZB == nil {
		t.Fatal("expected session NZB to be populated")
	}
	s.Close()
}

func TestGetOrDownloadNZBWithContextDoesNotRepopulateClosedSession(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	data := marshalTestNZB(t, &nzb.NZB{Files: []nzb.File{{
		Subject:  "Movie.2024.1080p.mkv",
		Segments: []nzb.Segment{{ID: "<a>", Bytes: 60}},
	}}})
	wait := make(chan struct{})
	started := make(chan struct{}, 1)
	idx := &fakeIndexer{data: data, wait: wait, started: started}
	s, err := m.CreateDeferredSession("sess-close-during-download", "https://example.invalid/get?nzb=1", nil, idx, nil, "movie", "tt123", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}

	errCh := make(chan error, 1)
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	go func() {
		_, err := s.GetOrDownloadNZBWithContext(callerCtx, m)
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NZB download to start")
	}

	s.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected lazy NZB download to stop for closed session")
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected session close to cancel merged download context")
	}
	if s.NZB != nil || s.Files != nil || s.File != nil {
		t.Fatal("expected closed session to stay empty after lazy download finishes")
	}
}

func TestGetOrDownloadNZBWithContextPropagatesLeaderFailureToWaiters(t *testing.T) {
	logger.Init("ERROR")

	m := &Manager{
		sessions:  make(map[string]*Session),
		estimator: loader.NewSegmentSizeEstimator(),
	}
	wait := make(chan struct{})
	started := make(chan struct{}, 1)
	idx := &fakeIndexer{err: context.DeadlineExceeded, wait: wait, started: started}
	s, err := m.CreateDeferredSession("sess-shared-download-error", "https://example.invalid/get?nzb=1", nil, idx, nil, "movie", "tt123", "", "")
	if err != nil {
		t.Fatalf("CreateDeferredSession returned error: %v", err)
	}

	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := s.GetOrDownloadNZBWithContext(context.Background(), m)
			errCh <- err
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NZB download to start")
	}

	close(wait)

	for i := 0; i < 2; i++ {
		err := <-errCh
		if err == nil {
			t.Fatal("expected lazy download error")
		}
		if !strings.Contains(err.Error(), "failed to lazy download NZB") {
			t.Fatalf("expected wrapped lazy download error, got %v", err)
		}
	}

	idx.mu.Lock()
	calls := idx.calls
	idx.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected DownloadNZB to be called once, got %d", calls)
	}
}

func TestAcquirePlaybackStreamReusesSameKey(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{}
	spec := PlaybackStreamSpec{Key: "sess-1|video.mkv|100", Name: "video.mkv", Size: 100}
	openCalls := 0
	first := &fakePlaybackStream{}

	lease1, reused, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		openCalls++
		return first, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream first call error: %v", err)
	}
	if reused {
		t.Fatal("expected first acquire to open a new stream")
	}
	if err := lease1.Close(); err != nil {
		t.Fatalf("closing first lease: %v", err)
	}
	if first.closed {
		t.Fatal("closing a lease should not close the underlying stream")
	}

	lease2, reused, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		openCalls++
		return &fakePlaybackStream{}, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream second call error: %v", err)
	}
	if !reused {
		t.Fatal("expected second acquire to reuse the existing stream")
	}
	if openCalls != 1 {
		t.Fatalf("expected open to be called once, got %d", openCalls)
	}
	if _, err := lease2.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("reused lease seek failed: %v", err)
	}
	if err := lease2.Close(); err != nil {
		t.Fatalf("closing second lease: %v", err)
	}
}

func TestTryAcquirePlaybackStreamReportsBusyForSameKeyInUse(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{}
	spec := PlaybackStreamSpec{Key: "sess-1|video.mkv|100", Name: "video.mkv", Size: 100}
	openCalls := 0
	first := &fakePlaybackStream{}

	lease1, reused, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		openCalls++
		return first, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream first call error: %v", err)
	}
	if reused {
		t.Fatal("expected first acquire to open a new stream")
	}

	lease2, reused, busy, err := s.TryAcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		openCalls++
		return &fakePlaybackStream{}, nil
	})
	if err != nil {
		t.Fatalf("TryAcquirePlaybackStream busy call error: %v", err)
	}
	if lease2 != nil {
		t.Fatal("expected busy acquire to return no lease")
	}
	if reused {
		t.Fatal("expected busy acquire to report reused=false")
	}
	if !busy {
		t.Fatal("expected busy acquire to report busy=true")
	}
	if openCalls != 1 {
		t.Fatalf("expected open to still be called once, got %d", openCalls)
	}

	if err := lease1.Close(); err != nil {
		t.Fatalf("closing first lease: %v", err)
	}

	lease3, reused, busy, err := s.TryAcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		openCalls++
		return &fakePlaybackStream{}, nil
	})
	if err != nil {
		t.Fatalf("TryAcquirePlaybackStream reuse call error: %v", err)
	}
	if busy {
		t.Fatal("expected stream to no longer be busy after lease close")
	}
	if !reused {
		t.Fatal("expected same-key acquire after release to reuse existing stream")
	}
	if lease3 == nil {
		t.Fatal("expected a lease after busy stream was released")
	}
	if err := lease3.Close(); err != nil {
		t.Fatalf("closing third lease: %v", err)
	}
}

func TestAcquirePlaybackStreamNewKeyClosesOldStreamAndClearsStartupInfo(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{}
	firstSpec := PlaybackStreamSpec{Key: "sess-1|video-a.mkv|100", Name: "video-a.mkv", Size: 100}
	secondSpec := PlaybackStreamSpec{Key: "sess-1|video-b.mkv|200", Name: "video-b.mkv", Size: 200}
	first := &fakePlaybackStream{}
	second := &fakePlaybackStream{}

	lease1, _, err := s.AcquirePlaybackStream(firstSpec, func() (io.ReadSeekCloser, error) {
		return first, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream first key error: %v", err)
	}
	s.SetPlaybackStreamStartInfo(firstSpec.Key, seek.StreamStartInfo{HeaderValid: true, DurationSec: 120, DurationKnown: true})
	if err := lease1.Close(); err != nil {
		t.Fatalf("closing first lease: %v", err)
	}

	lease2, reused, err := s.AcquirePlaybackStream(secondSpec, func() (io.ReadSeekCloser, error) {
		return second, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream second key error: %v", err)
	}
	if reused {
		t.Fatal("expected different key to open a new stream")
	}
	if first.closeCalls != 1 {
		t.Fatalf("expected old stream to be closed once, got %d", first.closeCalls)
	}
	snapshot, ok := s.PlaybackStreamSnapshot()
	if !ok {
		t.Fatal("expected playback snapshot after second acquire")
	}
	if snapshot.Spec.Key != secondSpec.Key {
		t.Fatalf("expected snapshot key %q, got %q", secondSpec.Key, snapshot.Spec.Key)
	}
	if snapshot.HasStartupInfo {
		t.Fatal("expected startup info to be cleared when key changes")
	}
	if err := lease2.Close(); err != nil {
		t.Fatalf("closing second lease: %v", err)
	}
}

func TestCachePlaybackStreamSnapshotStoresMetadataWithoutReusableStream(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{}
	spec := PlaybackStreamSpec{Key: "sess-1|video.mkv|100", Name: "video.mkv", Size: 100}
	info := seek.StreamStartInfo{HeaderValid: true, DurationSec: 120, DurationKnown: true}
	openCalls := 0

	s.CachePlaybackStreamSnapshot(spec, info, true)

	snapshot, ok := s.PlaybackStreamSnapshot()
	if !ok {
		t.Fatal("expected playback snapshot to be cached")
	}
	if snapshot.Spec != spec {
		t.Fatalf("expected cached spec %#v, got %#v", spec, snapshot.Spec)
	}
	if !snapshot.HasStartupInfo {
		t.Fatal("expected cached startup info to be present")
	}
	if snapshot.StartupInfo != info {
		t.Fatalf("expected cached startup info %#v, got %#v", info, snapshot.StartupInfo)
	}

	lease, reused, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		openCalls++
		return &fakePlaybackStream{}, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream after caching snapshot error: %v", err)
	}
	if reused {
		t.Fatal("expected cached snapshot without stream to force a fresh open")
	}
	if openCalls != 1 {
		t.Fatalf("expected open to be called once, got %d", openCalls)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("closing lease: %v", err)
	}
}

func TestCachePlaybackStreamSnapshotClosesRetainedUnusedStream(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{}
	spec := PlaybackStreamSpec{Key: "sess-1|video.mkv|100", Name: "video.mkv", Size: 100}
	stream := &fakePlaybackStream{}

	lease, _, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		return stream, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream error: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("closing lease: %v", err)
	}

	info := seek.StreamStartInfo{HeaderValid: true, DurationSec: 120, DurationKnown: true}
	s.CachePlaybackStreamSnapshot(spec, info, true)

	if stream.closeCalls != 1 {
		t.Fatalf("expected retained playback stream to be closed once, got %d", stream.closeCalls)
	}
	snapshot, ok := s.PlaybackStreamSnapshot()
	if !ok {
		t.Fatal("expected playback snapshot after caching")
	}
	if snapshot.Spec != spec {
		t.Fatalf("expected cached spec %#v, got %#v", spec, snapshot.Spec)
	}
	if !snapshot.HasStartupInfo || snapshot.StartupInfo != info {
		t.Fatalf("expected cached startup info %#v, got %#v (present=%v)", info, snapshot.StartupInfo, snapshot.HasStartupInfo)
	}
}

func TestSessionCloseClosesPlaybackStream(t *testing.T) {
	logger.Init("ERROR")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Session{ctx: ctx, cancel: cancel}
	stream := &fakePlaybackStream{}
	spec := PlaybackStreamSpec{Key: "sess-1|video.mkv|100", Name: "video.mkv", Size: 100}

	lease, _, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		return stream, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream error: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("closing lease: %v", err)
	}

	s.Close()

	if stream.closeCalls != 1 {
		t.Fatalf("expected underlying stream to be closed once, got %d", stream.closeCalls)
	}
	if s.playback != nil {
		t.Fatal("expected playback state to be cleared on session close")
	}
}

func TestSelectedPlaybackFileSurvivesResetPlaybackStream(t *testing.T) {
	logger.Init("ERROR")

	s := &Session{}
	s.SetSelectedPlaybackFile("Altered.Carbon.S02E03.1080p.mkv")
	spec := PlaybackStreamSpec{Key: "sess-1|video.mkv|100", Name: "video.mkv", Size: 100}

	lease, _, err := s.AcquirePlaybackStream(spec, func() (io.ReadSeekCloser, error) {
		return &fakePlaybackStream{}, nil
	})
	if err != nil {
		t.Fatalf("AcquirePlaybackStream error: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("closing lease: %v", err)
	}

	s.ResetPlaybackStream()

	if got := s.SelectedPlaybackFile(); got != "Altered.Carbon.S02E03.1080p.mkv" {
		t.Fatalf("SelectedPlaybackFile() = %q, want %q", got, "Altered.Carbon.S02E03.1080p.mkv")
	}
}

func marshalTestNZB(t *testing.T, doc *nzb.NZB) []byte {
	t.Helper()
	data, err := xml.Marshal(doc)
	if err != nil {
		t.Fatalf("xml.Marshal: %v", err)
	}
	return data
}
