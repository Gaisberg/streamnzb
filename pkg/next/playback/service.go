package playback

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/next/reporting"
	"streamnzb/pkg/release"
	"streamnzb/pkg/session"
)

var (
	ErrInvalidRequest  = errors.New("invalid playback request")
	ErrNotReady        = errors.New("playback service not ready")
	ErrPlaybackStartup = errors.New("playback startup failed")
	ErrSessionNotFound = errors.New("playback session not found")
)

type DownloadHostAPIKey struct {
	Host   string
	APIKey string
}

type Options struct {
	DownloadHostAPIKeys []DownloadHostAPIKey
	SessionManager      *session.Manager
	Indexer             indexer.Indexer
	Reporting           *reporting.Service
	OpenStream          streamOpener
}

type Service struct {
	downloadHostAPIKeys []DownloadHostAPIKey
	sessionManager      *session.Manager
	indexer             indexer.Indexer
	reporting           *reporting.Service
	openStream          streamOpener
}

type streamOpener func(context.Context, *session.Session, *session.Manager) (unpack.ReadSeekCloser, string, int64, error)

type preparedPlaybackStream struct {
	Stream         unpack.ReadSeekCloser
	Name           string
	Size           int64
	Spec           session.PlaybackStreamSpec
	StartupInfo    seek.StreamStartInfo
	HasStartupInfo bool
}

type playbackStartupError struct {
	phase string
	err   error
}

func (e playbackStartupError) Error() string {
	if e.phase == "" {
		return fmt.Sprintf("%s: %v", ErrPlaybackStartup, e.err)
	}
	return fmt.Sprintf("%s during %s: %v", ErrPlaybackStartup, e.phase, e.err)
}

func (e playbackStartupError) Unwrap() error {
	return e.err
}

func (e playbackStartupError) Is(target error) bool {
	return target == ErrPlaybackStartup
}

func wrapPlaybackStartupError(phase string, err error) error {
	if err == nil || errors.Is(err, ErrPlaybackStartup) {
		return err
	}
	return playbackStartupError{phase: strings.TrimSpace(phase), err: err}
}

func NewService() *Service {
	return NewServiceWithOptions(Options{})
}

func NewServiceWithOptions(opts Options) *Service {
	openStream := opts.OpenStream
	if openStream == nil {
		openStream = openPlaybackSource
	}

	downloadHostAPIKeys := make([]DownloadHostAPIKey, 0, len(opts.DownloadHostAPIKeys))
	for _, auth := range opts.DownloadHostAPIKeys {
		host := strings.ToLower(strings.TrimSpace(auth.Host))
		apiKey := strings.TrimSpace(auth.APIKey)
		if host == "" || apiKey == "" {
			continue
		}
		downloadHostAPIKeys = append(downloadHostAPIKeys, DownloadHostAPIKey{Host: host, APIKey: apiKey})
	}

	return &Service{
		downloadHostAPIKeys: downloadHostAPIKeys,
		sessionManager:      opts.SessionManager,
		indexer:             opts.Indexer,
		reporting:           opts.Reporting,
		openStream:          openStream,
	}
}

func (s *Service) ServeNZBURL(w http.ResponseWriter, r *http.Request, raw, metadataID string) error {
	sessionID, _, err := s.ResolveNZBURL(raw, metadataID)
	if err != nil {
		return err
	}
	return s.ServeHTTP(w, r, sessionID)
}

func (s *Service) ResolveNZBURL(raw, metadataID string) (string, string, error) {
	content, err := resolveOptionalMetadataContext(metadataID)
	if err != nil {
		return "", "", err
	}
	return s.ensureDeferredSession(raw, nil, content.ids, content.contentType, content.contentID)
}

func (s *Service) ResolveAndPrepareNZBURL(ctx context.Context, raw, metadataID string) (string, string, error) {
	sessionID, downloadURL, err := s.ResolveNZBURL(raw, metadataID)
	if err != nil {
		return "", "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sess, err := s.sessionManager.GetSession(strings.TrimSpace(sessionID))
	if err != nil {
		return sessionID, downloadURL, fmt.Errorf("%w: %s", ErrSessionNotFound, strings.TrimSpace(sessionID))
	}

	ctx, cancel := mergedPlaybackContext(ctx, sess.Done())
	defer cancel()

	prepared, err := s.preparePlaybackStream(ctx, sess)
	if err != nil {
		s.reportBadPlayback(sess, err)
		return sessionID, downloadURL, err
	}
	defer prepared.Stream.Close()

	s.sessionManager.MarkPlaybackValidated(sess.ID)
	return sessionID, downloadURL, nil
}

func (s *Service) NormalizeDownloadURL(raw string) (string, error) {
	return s.normalizeDownloadURL(raw)
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request, sessionID string) error {
	if s.sessionManager == nil || s.indexer == nil {
		return ErrNotReady
	}

	sess, err := s.sessionManager.GetSession(strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, strings.TrimSpace(sessionID))
	}

	ctx, cancel := mergedPlaybackContext(r.Context(), sess.Done())
	defer cancel()

	prepared, err := s.preparePlaybackStream(ctx, sess)
	if err != nil {
		s.reportBadPlayback(sess, err)
		return err
	}
	defer prepared.Stream.Close()

	monitoredStream := &streamMonitor{
		ReadSeekCloser: prepared.Stream,
		sessionID:      sess.ID,
		manager:        s.sessionManager,
	}

	ip := clientIP(r.RemoteAddr)
	s.sessionManager.StartPlayback(sess.ID, ip)
	defer s.sessionManager.EndPlayback(sess.ID, ip)
	s.sessionManager.MarkPlaybackValidated(sess.ID)

	trackedWriter := &playbackResponseWriter{ResponseWriter: w}
	applyMediaResponseHeaders(trackedWriter, prepared.Name)
	http.ServeContent(trackedWriter, r, prepared.Name, time.Time{}, monitoredStream)

	effectiveRange := strings.TrimSpace(r.Header.Get("Range"))
	responseStats := trackedWriter.Snapshot()
	streamStats := monitoredStream.Snapshot()
	probeLike, _ := classifyProbeLikeServe(r, prepared.Size, effectiveRange, responseStats, streamStats, ctx.Err())
	if ctx.Err() != nil {
		return nil
	}
	if readErr := streamStats.LastReadError; readErr != nil {
		s.reportBadPlayback(sess, readErr)
		return nil
	}
	if probeLike {
		return nil
	}
	if streamStats.BytesRead > 0 && s.reporting != nil {
		s.reporting.ReportPlaybackSuccess(sess)
	}
	return nil
}

func (s *Service) preparePlaybackStream(ctx context.Context, sess *session.Session) (preparedPlaybackStream, error) {
	prepared := preparedPlaybackStream{}

	snapshot, haveSnapshot := sess.PlaybackStreamSnapshot()
	if haveSnapshot {
		prepared.Spec = snapshot.Spec
		prepared.StartupInfo = snapshot.StartupInfo
		prepared.HasStartupInfo = snapshot.HasStartupInfo
	}

	if !haveSnapshot || !snapshot.HasStartupInfo {
		spec, startupInfo, err := s.probePlaybackSource(ctx, sess)
		if err != nil {
			return preparedPlaybackStream{}, wrapPlaybackStartupError("probe", err)
		}
		prepared.Spec = spec
		prepared.StartupInfo = startupInfo
		prepared.HasStartupInfo = true
	}

	if prepared.Spec.Key == "" {
		return preparedPlaybackStream{}, fmt.Errorf("playback stream spec missing for session %s", sess.ID)
	}

	stream, name, size, err := s.openStream(ctx, sess, s.sessionManager)
	if err != nil {
		sess.ResetPlaybackStream()
		return preparedPlaybackStream{}, wrapPlaybackStartupError("open", err)
	}
	if name != prepared.Spec.Name || size != prepared.Spec.Size {
		_ = stream.Close()
		sess.ResetPlaybackStream()
		return preparedPlaybackStream{}, fmt.Errorf("playback stream changed during open: expected %q (%d), got %q (%d)", prepared.Spec.Name, prepared.Spec.Size, name, size)
	}

	prepared.Stream = stream
	prepared.Name = name
	prepared.Size = size
	sess.CachePlaybackStreamSnapshot(prepared.Spec, prepared.StartupInfo, prepared.HasStartupInfo)
	return prepared, nil
}

func (s *Service) probePlaybackSource(ctx context.Context, sess *session.Session) (session.PlaybackStreamSpec, seek.StreamStartInfo, error) {
	probeStream, probeName, probeSize, err := s.openStream(ctx, sess, s.sessionManager)
	if err != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, err
	}
	defer probeStream.Close()

	startInfo, inspectErr := seek.InspectStreamStart(probeStream, probeSize, probeName, unpack.ProbeSize)
	if inspectErr != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, fmt.Errorf("probe inspect: %w", inspectErr)
	}
	if !startInfo.HeaderValid {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, fmt.Errorf("probe: invalid container header for %s", probeName)
	}

	return newPlaybackStreamSpec(sess.ID, probeName, probeSize), startInfo, nil
}

func newPlaybackStreamSpec(sessionID, name string, size int64) session.PlaybackStreamSpec {
	return session.PlaybackStreamSpec{
		Key:  fmt.Sprintf("%s|%s|%d", sessionID, name, size),
		Name: name,
		Size: size,
	}
}

func (s *Service) reportBadPlayback(sess *session.Session, err error) {
	reportErr := reportablePlaybackError(err)
	if s.reporting == nil || sess == nil || !shouldReportBadPlayback(reportErr) {
		return
	}
	s.reporting.ReportPlaybackFailure(sess, reportErr.Error())
}

func reportablePlaybackError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrPlaybackStartup) {
		if cause := errors.Unwrap(err); cause != nil {
			return cause
		}
	}
	return err
}

type contentContext struct {
	contentType string
	contentID   string
	ids         *session.AvailReportMeta
}

func resolveOptionalMetadataContext(metadataID string) (contentContext, error) {
	metadataID = strings.TrimSpace(metadataID)
	if metadataID == "" {
		return contentContext{}, nil
	}

	ids := &session.AvailReportMeta{}
	contentType := ""
	searchID := metadataID
	parts := strings.Split(metadataID, ":")
	if len(parts) > 1 {
		switch parts[0] {
		case "tmdb", "tvdb":
			searchID = parts[1]
			if parts[0] == "tvdb" {
				ids.TvdbID = searchID
				contentType = "series"
			}
			if len(parts) >= 3 {
				ids.Season, _ = strconv.Atoi(parts[2])
			}
			if len(parts) >= 4 {
				ids.Episode, _ = strconv.Atoi(parts[3])
			}
		default:
			searchID = parts[0]
			if len(parts) >= 2 {
				ids.Season, _ = strconv.Atoi(parts[1])
			}
			if len(parts) >= 3 {
				ids.Episode, _ = strconv.Atoi(parts[2])
			}
		}
	}
	if strings.HasPrefix(searchID, "tt") {
		ids.ImdbID = searchID
	}
	if (ids.Season > 0 && ids.Episode == 0) || (ids.Season == 0 && ids.Episode > 0) {
		return contentContext{}, fmt.Errorf("%w: metadata_id must include both season and episode", ErrInvalidRequest)
	}
	if ids.Season > 0 && ids.Episode > 0 {
		contentType = "series"
	}
	if ids.ImdbID == "" && ids.TvdbID == "" && ids.Season == 0 && ids.Episode == 0 {
		ids = nil
	}

	return contentContext{contentType: contentType, contentID: metadataID, ids: ids}, nil
}

func (s *Service) normalizeDownloadURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: invalid link", ErrInvalidRequest)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("%w: link must use http or https", ErrInvalidRequest)
	}

	q := parsed.Query()
	if q.Get("t") == "get" && q.Get("id") == "" && q.Get("guid") != "" {
		q.Set("id", q.Get("guid"))
		parsed.RawQuery = q.Encode()
	}
	if len(s.downloadHostAPIKeys) == 0 {
		return parsed.String(), nil
	}

	downloadHost := strings.ToLower(parsed.Hostname())
	for _, auth := range s.downloadHostAPIKeys {
		idxHost := strings.ToLower(strings.TrimSpace(auth.Host))
		if idxHost == downloadHost || strings.TrimPrefix(idxHost, "api.") == downloadHost || strings.TrimPrefix(downloadHost, "api.") == idxHost {
			q = parsed.Query()
			q.Set("apikey", auth.APIKey)
			parsed.RawQuery = q.Encode()
			return parsed.String(), nil
		}
	}

	return parsed.String(), nil
}

func (s *Service) ensureDeferredSession(raw string, rel *release.Release, contentIDs *session.AvailReportMeta, contentType, contentID string) (string, string, error) {
	downloadURL, err := s.normalizeDownloadURL(raw)
	if err != nil {
		return "", "", err
	}
	if s.sessionManager == nil || s.indexer == nil {
		return "", "", ErrNotReady
	}
	if rel == nil {
		rel = &release.Release{}
	}
	rel = &release.Release{
		Title:       strings.TrimSpace(rel.Title),
		Link:        downloadURL,
		DetailsURL:  strings.TrimSpace(rel.DetailsURL),
		Size:        rel.Size,
		Indexer:     strings.TrimSpace(rel.Indexer),
		QuerySource: strings.TrimSpace(rel.QuerySource),
	}
	sessionID := resolveSessionID(downloadURL)
	if _, err := s.sessionManager.CreateDeferredSession(sessionID, downloadURL, rel, s.indexer, contentIDs, strings.TrimSpace(contentType), strings.TrimSpace(contentID)); err != nil {
		return "", "", err
	}
	return sessionID, downloadURL, nil
}
func resolveSessionID(downloadURL string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(downloadURL)))
	return "resolve-" + hex.EncodeToString(sum[:])
}

func cacheReturnedPlaybackBlueprint(sess *session.Session, bp interface{}) {
	if sess == nil || bp == nil {
		return
	}
	sess.SetBlueprint(bp)
}

func openPlaybackSource(ctx context.Context, sess *session.Session, manager *session.Manager) (unpack.ReadSeekCloser, string, int64, error) {
	if _, err := sess.GetOrDownloadNZB(manager); err != nil {
		return nil, "", 0, err
	}

	files := sess.Files
	if len(files) == 0 {
		if sess.File == nil {
			return nil, "", 0, fmt.Errorf("no files in session %s", sess.ID)
		}
		files = []*loader.File{sess.File}
	}

	password := ""
	if sess.NZB != nil {
		password = sess.NZB.Password()
	}

	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for i := range files {
		unpackFiles[i] = files[i]
	}

	target := unpack.EpisodeTarget{}
	if sess.ContentIDs != nil {
		target = unpack.EpisodeTarget{Season: sess.ContentIDs.Season, Episode: sess.ContentIDs.Episode}
	}

	stream, name, size, bp, err := unpack.GetMediaStreamForEpisode(ctx, unpackFiles, sess.Blueprint, password, target)
	cacheReturnedPlaybackBlueprint(sess, bp)
	if err != nil {
		return nil, "", 0, err
	}
	sess.SetSelectedPlaybackFile(name)
	return stream, name, size, nil
}

func shouldReportBadPlayback(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, unpack.ErrTooManyZeroFills) || errors.Is(err, unpack.ErrEpisodeTargetNotFound) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := strings.ToLower(e.Error())
		if strings.Contains(msg, "segment unavailable") || strings.Contains(msg, "no such article") || strings.Contains(msg, " data corruption") ||
			strings.Contains(msg, "rapidyenc") || strings.Contains(msg, " yend") || strings.Contains(msg, "compressed") ||
			strings.Contains(msg, "encrypted") || strings.Contains(msg, "invalid container header") || msg == "unexpected eof" {
			return true
		}
		if strings.Contains(msg, "430") {
			return true
		}
	}
	return false
}

func mergedPlaybackContext(ctx context.Context, done <-chan struct{}) (context.Context, context.CancelFunc) {
	mergedCtx, cancel := context.WithCancel(ctx)
	if done == nil {
		return mergedCtx, cancel
	}
	go func() {
		select {
		case <-mergedCtx.Done():
		case <-done:
			cancel()
		}
	}()
	return mergedCtx, cancel
}

type streamMonitor struct {
	unpack.ReadSeekCloser
	sessionID   string
	manager     *session.Manager
	mu          sync.Mutex
	bytesRead   int64
	sawEOF      bool
	lastReadErr error
}

type streamMonitorSnapshot struct {
	BytesRead     int64
	SawEOF        bool
	LastReadError error
}

func (s *streamMonitor) Read(p []byte) (int, error) {
	n, err := s.ReadSeekCloser.Read(p)
	s.mu.Lock()
	if n > 0 {
		s.bytesRead += int64(n)
		if s.manager != nil {
			s.manager.AddBytesRead(s.sessionID, int64(n))
		}
	}
	if errors.Is(err, io.EOF) {
		s.sawEOF = true
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if s.lastReadErr == nil {
			s.lastReadErr = err
		}
	}
	s.mu.Unlock()
	return n, err
}

func (s *streamMonitor) Snapshot() streamMonitorSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return streamMonitorSnapshot{
		BytesRead:     s.bytesRead,
		SawEOF:        s.sawEOF,
		LastReadError: s.lastReadErr,
	}
}

type playbackResponseSnapshot struct {
	BytesWritten int64
}

type playbackResponseWriter struct {
	http.ResponseWriter
	bytesWritten int64
}

func (w *playbackResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *playbackResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *playbackResponseWriter) Snapshot() playbackResponseSnapshot {
	return playbackResponseSnapshot{BytesWritten: w.bytesWritten}
}

func clientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

func applyMediaResponseHeaders(w http.ResponseWriter, name string) {
	if contentType := mediaContentType(name); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(name)))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func classifyProbeLikeServe(r *http.Request, size int64, effectiveRange string, responseStats playbackResponseSnapshot, streamStats streamMonitorSnapshot, serveErr error) (bool, string) {
	if r == nil {
		return false, ""
	}
	if r.Method == http.MethodHead {
		return true, "head_request"
	}
	isNearEOFRequest := isNearEOFRange(effectiveRange, size)
	isNearEOFEofRequest := streamStats.SawEOF && isNearEOFRequest
	if isNearEOFRequest {
		if isNearEOFEofRequest && responseStats.BytesWritten == 0 && streamStats.BytesRead == 0 {
			return true, "tail_eof_probe"
		}
		if isSmallTailProbe(responseStats, streamStats) {
			return true, "tail_small_eof_probe"
		}
	}
	if responseStats.BytesWritten != 0 || streamStats.BytesRead != 0 {
		return false, ""
	}
	if isNearEOFEofRequest {
		return true, "tail_eof_probe"
	}
	if errors.Is(r.Context().Err(), context.Canceled) || errors.Is(r.Context().Err(), context.DeadlineExceeded) || errors.Is(serveErr, context.Canceled) || errors.Is(serveErr, context.DeadlineExceeded) {
		return true, "empty_canceled_request"
	}
	return true, "empty_request"
}

func isSmallTailProbe(responseStats playbackResponseSnapshot, streamStats streamMonitorSnapshot) bool {
	const smallTailProbeLimit int64 = 256 << 10

	probeBytes := responseStats.BytesWritten
	if streamStats.BytesRead > probeBytes {
		probeBytes = streamStats.BytesRead
	}
	return probeBytes > 0 && probeBytes <= smallTailProbeLimit
}

func isNearEOFRange(rangeHeader string, size int64) bool {
	if size <= 0 {
		return false
	}
	start, ok := parseRangeStart(rangeHeader)
	if !ok || start < 0 || start >= size {
		return false
	}
	const eofProbeWindow int64 = 1 << 20
	return size-start <= eofProbeWindow
}

func parseRangeStart(rangeHeader string) (int64, bool) {
	rangeHeader = strings.TrimSpace(rangeHeader)
	if len(rangeHeader) < len("bytes=") || !strings.EqualFold(rangeHeader[:len("bytes=")], "bytes=") {
		return 0, false
	}
	spec := strings.TrimSpace(rangeHeader[len("bytes="):])
	if spec == "" || strings.Contains(spec, ",") {
		return 0, false
	}
	dash := strings.IndexByte(spec, '-')
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(spec[:dash]), 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}

func mediaContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mp4", ".m4v":
		return "video/mp4"
	default:
		return ""
	}
}
