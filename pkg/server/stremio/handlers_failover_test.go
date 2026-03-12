package stremio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/session"
)

var testLoggerOnce sync.Once

func initFailoverTestLogger() {
	testLoggerOnce.Do(func() {
		logger.Init("ERROR")
	})
}

type countingResolvePlayIndexer struct {
	downloadCalls atomic.Int32
}

func (*countingResolvePlayIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return nil, nil
}

func (i *countingResolvePlayIndexer) DownloadNZB(context.Context, string) ([]byte, error) {
	i.downloadCalls.Add(1)
	return nil, errors.New("download failed")
}

func (*countingResolvePlayIndexer) Ping() error { return nil }

func (*countingResolvePlayIndexer) Name() string { return "resolve-play-test" }

func (*countingResolvePlayIndexer) GetUsage() indexer.Usage { return indexer.Usage{} }

func TestSwitchToNextFallbackSkipsUnresolvableCandidate(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	currentID := key.SlotPath(0)
	skippedID := key.SlotPath(1)
	wantID := key.SlotPath(2)

	server.playListCache.Store(key.CacheKey(), &playListCacheEntry{
		result: &orderedPlayListResult{
			Candidates: []triage.Candidate{
				{Release: &release.Release{Link: "https://example.invalid/0"}},
				{},
				{Release: &release.Release{Link: "https://example.invalid/2"}},
			},
			Params: &SearchParams{ContentType: key.ContentType, ID: key.ID},
		},
		until: time.Now().Add(time.Minute),
	})

	nextSess, nextID, err := server.switchToNextFallback(context.Background(), &session.Session{ID: currentID}, nil)
	if err != nil {
		t.Fatalf("switchToNextFallback returned error: %v", err)
	}
	if nextID != wantID {
		t.Fatalf("nextID = %q, want %q", nextID, wantID)
	}
	if nextSess == nil || nextSess.ID != wantID {
		t.Fatalf("next session = %#v, want id %q", nextSess, wantID)
	}
	if !manager.GetSlotFailedDuringPlayback(skippedID) {
		t.Fatalf("expected skipped slot %q to be marked failed", skippedID)
	}
	if got, err := manager.GetSession(wantID); err != nil || got == nil {
		t.Fatalf("expected resolved fallback session %q, got (%v, %v)", wantID, got, err)
	}
	if manager.GetSlotFailedDuringPlayback(wantID) {
		t.Fatalf("did not expect resolved slot %q to be marked failed", wantID)
	}
}

func TestForceDisconnectRedirectsToErrorVideo(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	recorder := httptest.NewRecorder()
	forceDisconnect(recorder, "http://localhost:11470/")
	response := recorder.Result()

	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := response.Header.Get("Location"); got != "http://localhost:11470/error/failure.mp4" {
		t.Fatalf("Location = %q, want %q", got, "http://localhost:11470/error/failure.mp4")
	}
	if got := response.Header.Get("Connection"); got != "close" {
		t.Fatalf("Connection = %q, want %q", got, "close")
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-cache, no-store, must-revalidate")
	}
}

func TestSetupRoutesServesFailureVideoWithoutLegacyWebHandler(t *testing.T) {
	t.Parallel()

	server := &Server{}
	mux := http.NewServeMux()
	server.SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/error/failure.mp4", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	resp := rr.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want %q", got, "video/mp4")
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-cache, no-store, must-revalidate")
	}
	if body := rr.Body.Len(); body != 0 {
		t.Fatalf("body length = %d, want 0", body)
	}
}

func TestClassifyPlaybackStartupErrWrapsOwnTimeout(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	err := classifyPlaybackStartupErr("probe", ctx, context.DeadlineExceeded)
	if !errors.Is(err, ErrPlaybackStartupTimeout) {
		t.Fatalf("expected startup timeout error, got %v", err)
	}
	if isPlayPrepareCancellation(err) {
		t.Fatalf("startup timeout should trigger failover, got cancellation classification for %v", err)
	}
}

func TestClassifyPlaybackStartupErrPreservesParentCancellation(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := classifyPlaybackStartupErr("open", ctx, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if !isPlayPrepareCancellation(err) {
		t.Fatalf("expected canceled prepare error to stay classified as cancellation, got %v", err)
	}
}

func TestHandleResolvePlayAttemptsPlaybackWithoutFailoverRedirect(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	idx := &countingResolvePlayIndexer{}
	server := &Server{
		config:         &config.Config{},
		baseURL:        "http://localhost:11470",
		sessionManager: manager,
	}
	const sessionID = "resolve-test-session"
	if _, err := manager.CreateDeferredSession(sessionID, "https://example.invalid/file.nzb", nil, idx, nil, "movie", "tt123"); err != nil {
		t.Fatalf("CreateDeferredSession: %v", err)
	}
	manager.SetSlotFailedDuringPlayback(sessionID)

	req := httptest.NewRequest(http.MethodGet, "/resolve/play/"+sessionID, nil)
	rr := httptest.NewRecorder()

	server.handleResolvePlay(rr, req, nil)

	if got := idx.downloadCalls.Load(); got != 1 {
		t.Fatalf("DownloadNZB calls = %d, want 1", got)
	}
	resp := rr.Result()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := resp.Header.Get("Location"); got != "http://localhost:11470/error/failure.mp4" {
		t.Fatalf("Location = %q, want %q", got, "http://localhost:11470/error/failure.mp4")
	}
}
