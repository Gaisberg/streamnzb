package playback

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/next/reporting"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/pool"
)

func TestServeNZBURLCreatesDeferredSessionAndServesPlayback(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	svc := NewServiceWithOptions(Options{
		DownloadHostAPIKeys: []DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		SessionManager:      manager,
		Indexer:             playbackTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		},
	})
	rawNZBURL := "https://api.indexer.example/api?t=get&guid=abc"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play?nzburl="+url.QueryEscape(rawNZBURL), nil)

	if err := svc.ServeNZBURL(rec, req, rawNZBURL, ""); err != nil {
		t.Fatalf("ServeNZBURL: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	normalized, err := svc.NormalizeDownloadURL(rawNZBURL)
	if err != nil {
		t.Fatalf("NormalizeDownloadURL: %v", err)
	}
	sess, err := manager.GetSession(resolveSessionID(normalized))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Release == nil || sess.Release.Link != normalized {
		t.Fatalf("unexpected session release %#v", sess.Release)
	}
	if sess.ContentType != "" || sess.ContentID != "" || sess.ContentIDs != nil {
		t.Fatalf("expected empty content context, got type=%q id=%q ids=%#v", sess.ContentType, sess.ContentID, sess.ContentIDs)
	}
}

func TestResolveNZBURLCreatesDeferredSessionAndReusesSessionID(t *testing.T) {
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	svc := NewServiceWithOptions(Options{
		DownloadHostAPIKeys: []DownloadHostAPIKey{{Host: "indexer.example", APIKey: "secret"}},
		SessionManager:      manager,
		Indexer:             playbackTestIndexer{},
	})
	rawNZBURL := "https://api.indexer.example/api?t=get&guid=abc"

	firstSessionID, firstDownloadURL, err := svc.ResolveNZBURL(rawNZBURL, "tvdb:456:1:2")
	if err != nil {
		t.Fatalf("ResolveNZBURL first call: %v", err)
	}
	secondSessionID, secondDownloadURL, err := svc.ResolveNZBURL(rawNZBURL, "tvdb:456:1:2")
	if err != nil {
		t.Fatalf("ResolveNZBURL second call: %v", err)
	}
	if firstSessionID != secondSessionID {
		t.Fatalf("expected reused session ID %q, got %q", firstSessionID, secondSessionID)
	}
	if firstDownloadURL != secondDownloadURL {
		t.Fatalf("expected reused download URL %q, got %q", firstDownloadURL, secondDownloadURL)
	}
	sess, err := manager.GetSession(firstSessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Release == nil || sess.Release.Link != firstDownloadURL {
		t.Fatalf("unexpected session release %#v", sess.Release)
	}
	if sess.ContentType != "series" || sess.ContentID != "tvdb:456:1:2" {
		t.Fatalf("unexpected content context type=%q id=%q", sess.ContentType, sess.ContentID)
	}
	if sess.ContentIDs == nil || sess.ContentIDs.TvdbID != "456" || sess.ContentIDs.Season != 1 || sess.ContentIDs.Episode != 2 {
		t.Fatalf("unexpected content ids %#v", sess.ContentIDs)
	}
}

func TestResolveAndPrepareNZBURLCachesPreparationForLaterPlayback(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	openCalls := 0
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		openCalls++
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	rawNZBURL := "https://indexer.example/get?id=abc"

	sessionID, downloadURL, err := svc.ResolveAndPrepareNZBURL(context.Background(), rawNZBURL, "tvdb:456:1:2")
	if err != nil {
		t.Fatalf("ResolveAndPrepareNZBURL: %v", err)
	}
	if downloadURL != rawNZBURL {
		t.Fatalf("expected download URL %q, got %q", rawNZBURL, downloadURL)
	}
	if openCalls != 2 {
		t.Fatalf("expected resolve preflight to probe+open once, got %d opens", openCalls)
	}

	sess, err := manager.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.PlaybackValidatedAt.IsZero() {
		t.Fatal("expected resolve preflight to mark playback validated")
	}
	snapshot, ok := sess.PlaybackStreamSnapshot()
	if !ok {
		t.Fatal("expected resolve preflight to cache playback snapshot")
	}
	if !snapshot.HasStartupInfo || !snapshot.StartupInfo.HeaderValid {
		t.Fatalf("expected cached valid startup info, got %#v", snapshot)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play?session_id="+url.QueryEscape(sessionID), nil)
	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP after resolve preflight: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("expected playback body of %d bytes, got %d", len(data), rec.Body.Len())
	}
	if openCalls != 3 {
		t.Fatalf("expected later /play to reuse cached probe and only reopen once, got %d opens", openCalls)
	}
	if reporter.good == nil || reporter.good.ID != sessionID {
		t.Fatalf("expected success report for %q, got %#v", sessionID, reporter.good)
	}
}

func TestResolveAndPrepareNZBURLReturnsPlaybackStartupFailureForUnplayableNZB(t *testing.T) {
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return nil, "", 0, errors.New("compressed archive")
	})
	rawNZBURL := "https://indexer.example/get?id=abc"

	sessionID, downloadURL, err := svc.ResolveAndPrepareNZBURL(context.Background(), rawNZBURL, "")
	if err == nil {
		t.Fatal("expected ResolveAndPrepareNZBURL error")
	}
	if !errors.Is(err, ErrPlaybackStartup) {
		t.Fatalf("expected ErrPlaybackStartup, got %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected session ID to still be returned on resolve preflight failure")
	}
	if downloadURL != rawNZBURL {
		t.Fatalf("expected download URL %q, got %q", rawNZBURL, downloadURL)
	}
	if reporter.bad == nil || reporter.bad.ID != sessionID {
		t.Fatalf("expected bad report for %q, got %#v", sessionID, reporter.bad)
	}
	if reporter.badReason != "compressed archive" {
		t.Fatalf("unexpected bad reason %q", reporter.badReason)
	}
	sess, getErr := manager.GetSession(sessionID)
	if getErr != nil {
		t.Fatalf("GetSession: %v", getErr)
	}
	if !sess.PlaybackValidatedAt.IsZero() {
		t.Fatal("expected failed resolve preflight to avoid playback validation")
	}
	if _, ok := sess.PlaybackStreamSnapshot(); ok {
		t.Fatal("expected failed resolve preflight to avoid caching playback snapshot")
	}
}

func TestServeHTTPReportsPlaybackSuccessAfterServingBytes(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)

	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if reporter.good == nil || reporter.good.ID != sessionID {
		t.Fatalf("expected success report for %q, got %#v", sessionID, reporter.good)
	}
	if reporter.bad != nil {
		t.Fatalf("expected no bad report, got %#v", reporter.bad)
	}
}

func TestServeHTTPReportsPlaybackStartupFailureWhenStreamIsUnplayable(t *testing.T) {
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return nil, "", 0, errors.New("compressed archive")
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)

	err := svc.ServeHTTP(rec, req, sessionID)
	if err == nil {
		t.Fatal("expected ServeHTTP error")
	}
	if !errors.Is(err, ErrPlaybackStartup) {
		t.Fatalf("expected ErrPlaybackStartup, got %v", err)
	}
	if reporter.bad == nil || reporter.bad.ID != sessionID {
		t.Fatalf("expected bad report for %q, got %#v", sessionID, reporter.bad)
	}
	if reporter.badReason != "compressed archive" {
		t.Fatalf("unexpected bad reason %q", reporter.badReason)
	}
}

func TestServeHTTPSkipsSuccessReportingForHeadRequests(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/play/"+sessionID, nil)

	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if reporter.good != nil {
		t.Fatalf("expected HEAD request to skip success reporting, got %#v", reporter.good)
	}
}

func TestServeHTTPReportsPlaybackReadFailureWhenStreamBreaks(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	openCalls := 0
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		openCalls++
		if openCalls == 1 {
			return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		}
		return &playbackFailingReadSeekCloser{
			Reader: bytes.NewReader(data),
			err:    errors.New("segment unavailable: 430"),
		}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)

	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if reporter.bad == nil || reporter.bad.ID != sessionID {
		t.Fatalf("expected bad report for %q, got %#v", sessionID, reporter.bad)
	}
	if reporter.good != nil {
		t.Fatalf("expected no success report, got %#v", reporter.good)
	}
}

func TestServeHTTPIgnoresTimeOffsetQuery(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID+"?t=5", nil)

	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d when t= is ignored, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Fatalf("expected no content range when t= is ignored, got %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("expected full body when t= is ignored, got %d bytes", rec.Body.Len())
	}
	if reporter.good == nil || reporter.good.ID != sessionID {
		t.Fatalf("expected success report for %q, got %#v", sessionID, reporter.good)
	}
	if reporter.bad != nil {
		t.Fatalf("expected no bad report, got %#v", reporter.bad)
	}
}

func TestServeHTTPReportsPlaybackSuccessForNonProbeRangeRequests(t *testing.T) {
	data := newPlaybackProbeableBytes(bytes.Repeat([]byte("a"), 2<<20))
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)
	req.Header.Set("Range", "bytes=1024-2047")

	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected %d for range playback, got %d", http.StatusPartialContent, rec.Code)
	}
	if rec.Body.Len() != 1024 {
		t.Fatalf("expected 1024-byte ranged body, got %d bytes", rec.Body.Len())
	}
	if reporter.good == nil || reporter.good.ID != sessionID {
		t.Fatalf("expected success report for %q, got %#v", sessionID, reporter.good)
	}
	if reporter.bad != nil {
		t.Fatalf("expected no bad report, got %#v", reporter.bad)
	}
}

func TestServeHTTPSkipsSuccessReportingForTailRangeProbes(t *testing.T) {
	data := newPlaybackProbeableBytes(bytes.Repeat([]byte("b"), 2<<20))
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	start := len(data) - 8
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)
	req.Header.Set("Range", "bytes="+strconv.Itoa(start)+"-")

	if err := svc.ServeHTTP(rec, req, sessionID); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("expected %d for tail range playback, got %d", http.StatusPartialContent, rec.Code)
	}
	if rec.Body.Len() != len(data)-start {
		t.Fatalf("expected %d-byte tail body, got %d bytes", len(data)-start, rec.Body.Len())
	}
	if reporter.good != nil {
		t.Fatalf("expected tail probe to skip success reporting, got %#v", reporter.good)
	}
	if reporter.bad != nil {
		t.Fatalf("expected no bad report, got %#v", reporter.bad)
	}
}

func TestOpenPlaybackSourceCachesSuccessfulDirectBlueprint(t *testing.T) {
	stale := &unpack.DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 1, Target: unpack.EpisodeTarget{Season: 1, Episode: 4}}
	sess := &session.Session{
		ID:  "sess-open-success",
		NZB: &nzb.NZB{},
		Files: []*loader.File{
			newPlaybackLoaderFile(t, "Show.S01E01.mkv", []byte("ep1")),
			newPlaybackLoaderFile(t, "Show.S01E04.mkv", []byte("ep4")),
		},
		ContentIDs: &session.AvailReportMeta{Season: 1, Episode: 1},
	}
	sess.SetBlueprint(stale)

	stream, name, size, err := openPlaybackSource(context.Background(), sess, nil)
	if err != nil {
		t.Fatalf("openPlaybackSource returned error: %v", err)
	}
	defer stream.Close()

	if name != "Show.S01E01.mkv" {
		t.Fatalf("expected selected file %q, got %q", "Show.S01E01.mkv", name)
	}
	if size != int64(len("ep1")) {
		t.Fatalf("expected stream size %d, got %d", len("ep1"), size)
	}
	if got := sess.SelectedPlaybackFile(); got != name {
		t.Fatalf("expected selected playback file %q, got %q", name, got)
	}
	bp, ok := sess.Blueprint.(*unpack.DirectBlueprint)
	if !ok {
		t.Fatalf("expected direct blueprint, got %T", sess.Blueprint)
	}
	if bp == stale {
		t.Fatal("expected stale blueprint to be replaced")
	}
	if bp.Target != (unpack.EpisodeTarget{Season: 1, Episode: 1}) {
		t.Fatalf("expected direct blueprint target %#v, got %#v", unpack.EpisodeTarget{Season: 1, Episode: 1}, bp.Target)
	}
	data, readErr := io.ReadAll(stream)
	if readErr != nil {
		t.Fatalf("failed to read returned stream: %v", readErr)
	}
	if string(data) != "ep1" {
		t.Fatalf("expected stream data %q, got %q", "ep1", string(data))
	}
}

func TestOpenPlaybackSourceCachesFailedBlueprintOnTargetedEpisodeMiss(t *testing.T) {
	stale := &unpack.DirectBlueprint{FileName: "Show.S01E04.mkv", FileIndex: 0, Target: unpack.EpisodeTarget{Season: 1, Episode: 4}}
	sess := &session.Session{
		ID:  "sess-open-failure",
		NZB: &nzb.NZB{},
		Files: []*loader.File{
			newPlaybackLoaderFile(t, "Show.S01E04.mkv", []byte("ep4")),
		},
		ContentIDs: &session.AvailReportMeta{Season: 1, Episode: 1},
	}
	sess.SetBlueprint(stale)

	stream, name, size, err := openPlaybackSource(context.Background(), sess, nil)
	if err == nil {
		if stream != nil {
			_ = stream.Close()
		}
		t.Fatal("expected targeted episode miss error")
	}
	if !errors.Is(err, unpack.ErrEpisodeTargetNotFound) {
		t.Fatalf("expected ErrEpisodeTargetNotFound, got %v", err)
	}
	if stream != nil {
		t.Fatal("expected no stream on targeted episode miss")
	}
	if name != "" {
		t.Fatalf("expected no selected file name, got %q", name)
	}
	if size != 0 {
		t.Fatalf("expected size 0, got %d", size)
	}
	if got := sess.SelectedPlaybackFile(); got != "" {
		t.Fatalf("expected no selected playback file, got %q", got)
	}
	bp, ok := sess.Blueprint.(*unpack.FailedBlueprint)
	if !ok {
		t.Fatalf("expected failed blueprint, got %T", sess.Blueprint)
	}
	if errors.Is(bp.Err, unpack.ErrEpisodeTargetNotFound) == false {
		t.Fatalf("expected failed blueprint error to wrap ErrEpisodeTargetNotFound, got %v", bp.Err)
	}
	if bp.Target != (unpack.EpisodeTarget{Season: 1, Episode: 1}) {
		t.Fatalf("expected failed blueprint target %#v, got %#v", unpack.EpisodeTarget{Season: 1, Episode: 1}, bp.Target)
	}
}

func TestServeHTTPDoesNotReportPlaybackWhenRequestIsCanceled(t *testing.T) {
	opened := make(chan *playbackCancelAwareReadSeekCloser, 1)
	data := newPlaybackProbeableBytes([]byte("video-data-more"))
	openCalls := 0
	manager, reporter, svc := newPlaybackTestService(t, func(ctx context.Context, _ *session.Session, _ *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		openCalls++
		if openCalls == 1 {
			return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		}
		stream := &playbackCancelAwareReadSeekCloser{
			Reader:        bytes.NewReader(data),
			ctx:           ctx,
			cancelReadErr: func() error { return ctx.Err() },
			firstRead:     make(chan struct{}),
			firstChunk:    5,
		}
		opened <- stream
		return stream, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil).WithContext(ctx)
	errCh := make(chan error, 1)

	go func() {
		errCh <- svc.ServeHTTP(rec, req, sessionID)
	}()

	stream := waitForPlaybackTestStream(t, opened)
	waitForPlaybackSignal(t, stream.firstRead, "first read")
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ServeHTTP: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ServeHTTP after request cancel")
	}

	if reporter.good != nil || reporter.bad != nil {
		t.Fatalf("expected canceled request to skip reporting, got good=%#v bad=%#v", reporter.good, reporter.bad)
	}
	sess, err := manager.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.ActivePlays != 0 {
		t.Fatalf("expected ActivePlays to be 0 after cancel, got %d", sess.ActivePlays)
	}
	if sess.PlaybackEndedAt.IsZero() {
		t.Fatal("expected PlaybackEndedAt to be set after cancel")
	}
}

func TestServeHTTPDoesNotReportPlaybackWhenSessionIsDeletedMidStream(t *testing.T) {
	opened := make(chan *playbackCancelAwareReadSeekCloser, 1)
	data := newPlaybackProbeableBytes([]byte("video-data-more"))
	openCalls := 0
	manager, reporter, svc := newPlaybackTestService(t, func(ctx context.Context, _ *session.Session, _ *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		openCalls++
		if openCalls == 1 {
			return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		}
		stream := &playbackCancelAwareReadSeekCloser{
			Reader:        bytes.NewReader(data),
			ctx:           ctx,
			cancelReadErr: func() error { return io.EOF },
			firstRead:     make(chan struct{}),
			firstChunk:    5,
		}
		opened <- stream
		return stream, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)
	errCh := make(chan error, 1)

	go func() {
		errCh <- svc.ServeHTTP(rec, req, sessionID)
	}()

	stream := waitForPlaybackTestStream(t, opened)
	waitForPlaybackSignal(t, stream.firstRead, "first read")
	manager.DeleteSession(sessionID)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ServeHTTP: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ServeHTTP after session deletion")
	}

	if reporter.good != nil || reporter.bad != nil {
		t.Fatalf("expected session deletion to skip reporting, got good=%#v bad=%#v", reporter.good, reporter.bad)
	}
	if _, err := manager.GetSession(sessionID); err == nil {
		t.Fatalf("expected session %q to be deleted", sessionID)
	}
}

func TestPreparePlaybackStreamCachesStartupInfoAndSkipsReprobe(t *testing.T) {
	data := newPlaybackProbeableBytes([]byte("video-data"))
	openCalls := 0
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	svc := NewServiceWithOptions(Options{
		SessionManager: manager,
		Indexer:        playbackTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			openCalls++
			return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
		},
	})
	sess, _ := createPlaybackTestSession(t, manager)

	prepared, err := svc.preparePlaybackStream(context.Background(), sess)
	if err != nil {
		t.Fatalf("preparePlaybackStream first call: %v", err)
	}
	if err := prepared.Stream.Close(); err != nil {
		t.Fatalf("closing first prepared stream: %v", err)
	}
	if openCalls != 2 {
		t.Fatalf("expected probe+open on first prepare, got %d opens", openCalls)
	}
	snapshot, ok := sess.PlaybackStreamSnapshot()
	if !ok {
		t.Fatal("expected cached playback snapshot after first prepare")
	}
	if !snapshot.HasStartupInfo {
		t.Fatal("expected cached startup info after first prepare")
	}
	if !snapshot.StartupInfo.HeaderValid {
		t.Fatal("expected cached startup info to mark header valid")
	}

	prepared, err = svc.preparePlaybackStream(context.Background(), sess)
	if err != nil {
		t.Fatalf("preparePlaybackStream second call: %v", err)
	}
	if err := prepared.Stream.Close(); err != nil {
		t.Fatalf("closing second prepared stream: %v", err)
	}
	if openCalls != 3 {
		t.Fatalf("expected second prepare to reuse snapshot and only reopen once, got %d opens", openCalls)
	}
}

func TestServeHTTPRejectsInvalidStartupHeaderBeforeServing(t *testing.T) {
	data := []byte("not-a-valid-mkv")
	manager, reporter, svc := newPlaybackTestService(t, func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
		return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(data)}, "episode.mkv", int64(len(data)), nil
	})
	_, sessionID := createPlaybackTestSession(t, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+sessionID, nil)
	err := svc.ServeHTTP(rec, req, sessionID)
	if err == nil {
		t.Fatal("expected ServeHTTP error for invalid startup header")
	}
	if !strings.Contains(err.Error(), "invalid container header") {
		t.Fatalf("expected invalid container header error, got %v", err)
	}
	if reporter.good != nil {
		t.Fatalf("expected no success report, got %#v", reporter.good)
	}
	if reporter.bad == nil || reporter.bad.ID != sessionID {
		t.Fatalf("expected bad report for %q, got %#v", sessionID, reporter.bad)
	}
	sess, getErr := manager.GetSession(sessionID)
	if getErr != nil {
		t.Fatalf("GetSession: %v", getErr)
	}
	if !sess.PlaybackValidatedAt.IsZero() {
		t.Fatal("expected invalid startup probe to avoid validation")
	}
	if _, ok := sess.PlaybackStreamSnapshot(); ok {
		t.Fatal("expected invalid startup probe to avoid caching playback snapshot")
	}
}

func TestPreparePlaybackStreamRejectsChangedPlaybackSource(t *testing.T) {
	probeData := newPlaybackProbeableBytes([]byte("video-a"))
	bodyData := newPlaybackProbeableBytes([]byte("video-b"))
	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	openCalls := 0
	svc := NewServiceWithOptions(Options{
		SessionManager: manager,
		Indexer:        playbackTestIndexer{},
		OpenStream: func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			openCalls++
			if openCalls == 1 {
				return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(probeData)}, "episode-a.mkv", int64(len(probeData)), nil
			}
			return &playbackBytesReadSeekCloser{Reader: bytes.NewReader(bodyData)}, "episode-b.mkv", int64(len(bodyData)), nil
		},
	})
	sess, _ := createPlaybackTestSession(t, manager)

	_, err := svc.preparePlaybackStream(context.Background(), sess)
	if err == nil {
		t.Fatal("expected preparePlaybackStream error when reopened stream changes")
	}
	if !strings.Contains(err.Error(), "playback stream changed during open") {
		t.Fatalf("expected changed-stream error, got %v", err)
	}
	if _, ok := sess.PlaybackStreamSnapshot(); ok {
		t.Fatal("expected changed playback source to reset cached snapshot")
	}
}

type playbackBytesReadSeekCloser struct {
	*bytes.Reader
}

func (b *playbackBytesReadSeekCloser) Close() error { return nil }

type playbackFailingReadSeekCloser struct {
	*bytes.Reader
	err    error
	failed bool
}

func (b *playbackFailingReadSeekCloser) Read(p []byte) (int, error) {
	if b.failed {
		return 0, b.err
	}
	b.failed = true
	n, readErr := b.Reader.Read(p)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return n, readErr
	}
	if n == 0 {
		return 0, b.err
	}
	return n, b.err
}

func (b *playbackFailingReadSeekCloser) Close() error { return nil }

type playbackCancelAwareReadSeekCloser struct {
	*bytes.Reader
	ctx           context.Context
	cancelReadErr func() error
	firstRead     chan struct{}
	firstReadOnce sync.Once
	firstChunk    int
	hasReadChunk  bool
}

func (b *playbackCancelAwareReadSeekCloser) Read(p []byte) (int, error) {
	if !b.hasReadChunk {
		b.hasReadChunk = true
		b.firstReadOnce.Do(func() {
			close(b.firstRead)
		})
		if b.firstChunk > 0 && len(p) > b.firstChunk {
			p = p[:b.firstChunk]
		}
		n, err := b.Reader.Read(p)
		if errors.Is(err, io.EOF) && n > 0 {
			return n, nil
		}
		return n, err
	}
	if b.Reader.Len() > 0 {
		<-b.ctx.Done()
		if b.cancelReadErr != nil {
			return 0, b.cancelReadErr()
		}
		return 0, b.ctx.Err()
	}
	<-b.ctx.Done()
	if b.cancelReadErr != nil {
		return 0, b.cancelReadErr()
	}
	return 0, b.ctx.Err()
}

func (b *playbackCancelAwareReadSeekCloser) Close() error { return nil }

func waitForPlaybackTestStream(t *testing.T, opened <-chan *playbackCancelAwareReadSeekCloser) *playbackCancelAwareReadSeekCloser {
	t.Helper()
	select {
	case stream := <-opened:
		return stream
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for test stream to open")
		return nil
	}
}

func waitForPlaybackSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func newPlaybackTestService(t *testing.T, open func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error)) (*session.Manager, *playbackTestReporter, *Service) {
	t.Helper()

	manager := session.NewManager(nil, nil, time.Minute, nil)
	t.Cleanup(manager.Shutdown)
	reporter := &playbackTestReporter{}
	reportingService := reporting.NewServiceWithOptions(reporting.Options{Enabled: true, Reporter: reporter})

	svc := NewServiceWithOptions(Options{
		SessionManager: manager,
		Indexer:        playbackTestIndexer{},
		Reporting:      reportingService,
		OpenStream: func(ctx context.Context, sess *session.Session, manager *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
			return open(ctx, sess, manager)
		},
	})

	return manager, reporter, svc
}

func createPlaybackTestSession(t *testing.T, manager *session.Manager) (*session.Session, string) {
	t.Helper()

	const sessionID = "sess-serve"
	sess, err := manager.CreateDeferredSession(sessionID, "https://indexer.example/get?id=abc", &release.Release{
		Title:      "Example",
		Link:       "https://indexer.example/get?id=abc",
		DetailsURL: "https://indexer.example/details/abc",
	}, playbackTestIndexer{}, &session.AvailReportMeta{ImdbID: "tt1234567"}, "movie", "tt1234567")
	if err != nil {
		t.Fatalf("CreateDeferredSession: %v", err)
	}
	return sess, sessionID
}

type playbackTestReporter struct {
	good      *session.Session
	bad       *session.Session
	badReason string
}

func newPlaybackProbeableBytes(payload []byte) []byte {
	data := make([]byte, 0, 8+len(payload))
	data = append(data, 0x1A, 0x45, 0xDF, 0xA3, 0x00, 0x00, 0x00, 0x00)
	data = append(data, payload...)
	return data
}

type playbackOpenSegmentFetcher struct {
	data []byte
}

func (f playbackOpenSegmentFetcher) FetchSegment(ctx context.Context, segment *nzb.Segment, groups []string) (pool.SegmentData, error) {
	return pool.SegmentData{Body: append([]byte(nil), f.data...), Size: int64(len(f.data))}, nil
}

func (r *playbackTestReporter) ReportGood(sess *session.Session) {
	r.good = sess
}

func (r *playbackTestReporter) ReportBad(sess *session.Session, reason string) {
	r.bad = sess
	r.badReason = reason
}

type playbackTestIndexer struct{}

func (playbackTestIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) {
	return &indexer.SearchResponse{}, nil
}

func (playbackTestIndexer) DownloadNZB(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (playbackTestIndexer) Ping() error { return nil }

func (playbackTestIndexer) Name() string { return "playback-test" }

func (playbackTestIndexer) GetUsage() indexer.Usage { return indexer.Usage{} }

func newPlaybackLoaderFile(t *testing.T, subject string, data []byte) *loader.File {
	t.Helper()

	return loader.NewFile(context.Background(), &nzb.File{
		Subject: subject,
		Groups:  []string{"alt.test"},
		Segments: []nzb.Segment{{
			ID:     subject + "-seg-1",
			Number: 1,
			Bytes:  int64(len(data)),
		}},
	}, nil, nil, playbackOpenSegmentFetcher{data: data}, nil)
}
