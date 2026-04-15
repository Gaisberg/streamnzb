package stremio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

var testLoggerOnce sync.Once

func initFailoverTestLogger() {
	testLoggerOnce.Do(func() {
		logger.Init("ERROR")
	})
}

func TestSwitchToNextFallbackSkipsUnresolvableCandidate(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}
	currentID := key.SlotPath(0)
	skippedID := key.SlotPath(1)
	wantID := key.SlotPath(2)

	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result: &playlistResult{
			Candidates: []triage.Candidate{
				{Release: &release.Release{Link: "https://example.invalid/0"}},
				{},
				{Release: &release.Release{Link: "https://example.invalid/2"}},
			},
			Params: &SearchParams{ContentType: key.ContentType, ID: key.ID},
		},
		until: time.Now().Add(time.Minute),
	})
	server.rawSearchCache.Store(key.StreamID+":"+key.ContentType+":"+key.ID, &rawSearchCacheEntry{
		raw: &rawSearchResult{
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
	if _, ok := server.playlistCache.Load(key.CacheKey()); !ok {
		t.Fatalf("expected playlist cache to survive failover skipping")
	}
	if _, ok := server.rawSearchCache.Load(key.StreamID + ":" + key.ContentType + ":" + key.ID); !ok {
		t.Fatalf("expected raw search cache to survive failover skipping")
	}
}

func TestApplyReportedBadReleaseToCachesMarksCachedReleaseUnavailable(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	manager := session.NewManager(nil, nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "series", ID: "tt123:1:4"}
	failedDetailsURL := "https://example.invalid/details/failed"
	otherDetailsURL := "https://example.invalid/details/other"

	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result: &playlistResult{
			Candidates: []triage.Candidate{
				{Release: &release.Release{Title: "failed", DetailsURL: failedDetailsURL}},
				{Release: &release.Release{Title: "other", DetailsURL: otherDetailsURL}},
			},
			CachedAvailable: map[string]bool{
				failedDetailsURL: true,
				otherDetailsURL:  true,
			},
			UnavailableDetailsURLs: map[string]bool{},
		},
		until: time.Now().Add(time.Minute),
	})
	server.rawSearchCache.Store(key.StreamID+":"+key.ContentType+":"+key.ID, &rawSearchCacheEntry{
		raw: &rawSearchResult{
			Params: &SearchParams{ContentType: key.ContentType, ID: key.ID},
			IndexerReleases: []*release.Release{
				{Title: "failed", DetailsURL: failedDetailsURL, Available: &availTrue},
				{Title: "other", DetailsURL: otherDetailsURL, Available: &availTrue},
			},
			Avail: &AvailContext{
				ByDetailsURL: map[string]*availnzb.ReleaseWithStatus{
					failedDetailsURL: {
						Release:   &release.Release{Title: "failed", DetailsURL: failedDetailsURL, Available: &availTrue},
						Available: true,
					},
				},
				AvailableByDetailsURL: map[string]bool{
					failedDetailsURL: true,
					otherDetailsURL:  true,
				},
				UnavailableByDetailsURL: map[string]bool{},
				Result: &availnzb.ReleasesResult{
					Releases: []*availnzb.ReleaseWithStatus{
						{
							Release:   &release.Release{Title: "failed", DetailsURL: failedDetailsURL, Available: &availTrue},
							Available: true,
						},
					},
				},
			},
		},
		until: time.Now().Add(time.Minute),
	})

	server.applyReportedBadReleaseToCaches(&session.Session{
		ID:      key.SlotPath(0),
		Release: &release.Release{DetailsURL: failedDetailsURL},
	}, availnzb.SentOutcome(false))

	cachedPlaylistValue, ok := server.playlistCache.Load(key.CacheKey())
	if !ok {
		t.Fatalf("expected playlist cache entry to remain available")
	}
	cachedPlaylist := cachedPlaylistValue.(*playlistCacheEntry).result
	if len(cachedPlaylist.Candidates) != 1 || cachedPlaylist.Candidates[0].Release == nil || cachedPlaylist.Candidates[0].Release.DetailsURL != otherDetailsURL {
		t.Fatalf("expected failed release to be removed from cached playlist, got %#v", cachedPlaylist.Candidates)
	}
	if len(cachedPlaylist.SlotPaths) != 1 || cachedPlaylist.SlotPaths[0] != key.SlotPath(1) {
		t.Fatalf("expected cached playlist slot paths to preserve original fallback order, got %#v", cachedPlaylist.SlotPaths)
	}
	if nextID := server.deriveNextSlotIDFromPlaylist(key.SlotPath(0), key, 0, cachedPlaylist, nil); nextID != key.SlotPath(1) {
		t.Fatalf("expected failover to advance to the next original slot after cache update, got %q", nextID)
	}
	if cachedPlaylist.FirstIsAvailGood != true {
		t.Fatalf("expected first remaining candidate to stay avail-good")
	}
	if !cachedPlaylist.UnavailableDetailsURLs[failedDetailsURL] {
		t.Fatalf("expected failed release to be marked unavailable in cached playlist")
	}
	if cachedPlaylist.CachedAvailable[failedDetailsURL] {
		t.Fatalf("expected failed release to be removed from cached available set")
	}

	cachedRawValue, ok := server.rawSearchCache.Load(key.StreamID + ":" + key.ContentType + ":" + key.ID)
	if !ok {
		t.Fatalf("expected raw search cache entry to remain available")
	}
	cachedRaw := cachedRawValue.(*rawSearchCacheEntry).raw
	if !cachedRaw.Avail.UnavailableByDetailsURL[failedDetailsURL] {
		t.Fatalf("expected raw avail context to mark failed release unavailable")
	}
	if cachedRaw.Avail.AvailableByDetailsURL[failedDetailsURL] {
		t.Fatalf("expected raw avail context to remove failed release from available set")
	}
	if rel := cachedRaw.Avail.ByDetailsURL[failedDetailsURL]; rel == nil || rel.Available {
		t.Fatalf("expected raw avail cache entry to be marked unavailable, got %#v", rel)
	}
	if cachedRaw.IndexerReleases[0].Available == nil || *cachedRaw.IndexerReleases[0].Available {
		t.Fatalf("expected cached raw indexer release to be marked unavailable")
	}
}

func TestForceDisconnectRedirectsToErrorVideo(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost:11470/play/test", nil)
	forceDisconnect(recorder, req, "http://localhost:11470/")
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

func TestClassifyPlaybackStartupErrWrapsOwnTimeout(t *testing.T) {
	initFailoverTestLogger()
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	err := classifyPlaybackStartupErr("probe", 5*time.Second, ctx, context.DeadlineExceeded)
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

	err := classifyPlaybackStartupErr("open", 5*time.Second, ctx, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if !isPlayPrepareCancellation(err) {
		t.Fatalf("expected canceled prepare error to stay classified as cancellation, got %v", err)
	}
}

func TestIsIndexerLimitErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "download limit", err: errors.New("failed to lazy download NZB: download limit reached for Easynews"), want: true},
		{name: "api limit", err: errors.New("API limit reached for NZBPlanet"), want: true},
		{name: "request limit", err: errors.New("NzbPlanet request limit reached (code 429): daily quota exhausted"), want: true},
		{name: "segment unavailable", err: errors.New("segment unavailable: first segment not found (430)"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIndexerLimitErr(tt.err); got != tt.want {
				t.Fatalf("isIndexerLimitErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
