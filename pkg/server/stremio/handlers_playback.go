package stremio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/playback"
	"streamnzb/pkg/release"
	searchparser "streamnzb/pkg/search/parser"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp"
)

type writeTimeoutResponseWriter struct {
	http.ResponseWriter
	timeout time.Duration
	rc      *http.ResponseController
}

func newWriteTimeoutResponseWriter(w http.ResponseWriter, timeout time.Duration) *writeTimeoutResponseWriter {
	return &writeTimeoutResponseWriter{
		ResponseWriter: w,
		timeout:        timeout,
		rc:             http.NewResponseController(w),
	}
}

func (w *writeTimeoutResponseWriter) Write(p []byte) (n int, err error) {
	if setErr := w.rc.SetWriteDeadline(time.Now().Add(w.timeout)); setErr != nil {
		return 0, setErr
	}
	return w.ResponseWriter.Write(p)
}

type bufferedResponseWriter struct {
	http.ResponseWriter
	bw           *bufio.Writer
	statusCode   int
	wroteHeader  bool
	bytesWritten int64
	writeCalls   int64
	flushCalls   int64
	flushError   string
	onFirstWrite func()
	writeBlocked atomic.Int64 // ns spent in Write (client/proxy backpressure)
}

func newBufferedResponseWriter(w http.ResponseWriter, size int) *bufferedResponseWriter {
	return &bufferedResponseWriter{
		ResponseWriter: w,
		bw:             bufio.NewWriterSize(w, size),
	}
}

func (b *bufferedResponseWriter) Write(p []byte) (n int, err error) {
	if !b.wroteHeader {
		b.statusCode = http.StatusOK
		b.wroteHeader = true
		if b.onFirstWrite != nil {
			b.onFirstWrite()
		}
	}
	b.writeCalls++
	writeStart := time.Now()
	n, err = b.bw.Write(p)
	b.writeBlocked.Add(time.Since(writeStart).Nanoseconds())
	b.bytesWritten += int64(n)
	return n, err
}

func (b *bufferedResponseWriter) WriteHeader(statusCode int) {
	if !b.wroteHeader {
		b.statusCode = statusCode
		b.wroteHeader = true
		if b.onFirstWrite != nil {
			b.onFirstWrite()
		}
	}
	b.ResponseWriter.WriteHeader(statusCode)
}

func (b *bufferedResponseWriter) Flush() {
	b.flushCalls++
	if err := b.bw.Flush(); err != nil {
		b.flushError = err.Error()
	}
	if f, ok := b.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type bufferedResponseSnapshot struct {
	StatusCode    int
	WroteHeader   bool
	ContentRange  string
	ContentLength string
	ContentType   string
	AcceptRanges  string
	BytesWritten  int64
	WriteCalls    int64
	FlushCalls    int64
	FlushError    string
	WriteBlocked  time.Duration
}

func (b *bufferedResponseWriter) Snapshot() bufferedResponseSnapshot {
	return bufferedResponseSnapshot{
		StatusCode:    b.statusCode,
		WroteHeader:   b.wroteHeader,
		ContentRange:  b.Header().Get("Content-Range"),
		ContentLength: b.Header().Get("Content-Length"),
		ContentType:   b.Header().Get("Content-Type"),
		AcceptRanges:  b.Header().Get("Accept-Ranges"),
		BytesWritten:  b.bytesWritten,
		WriteCalls:    b.writeCalls,
		FlushCalls:    b.flushCalls,
		FlushError:    b.flushError,
		WriteBlocked:  time.Duration(b.writeBlocked.Load()),
	}
}

type StreamMonitor struct {
	io.ReadSeekCloser
	sessionID     string
	clientIP      string
	manager       *session.Manager
	onReadError   func(slotPath string, err error)
	onProgress    func()
	onServeWindow func()
	lastUpdate    time.Time
	mu            sync.Mutex
	readErrorOnce sync.Once
	bytesRead     atomic.Int64
	readCalls     atomic.Int64
	readBlocked   atomic.Int64 // ns spent in Read (usenet/reader backpressure)
	sawEOF        atomic.Bool
	lastReadErr   atomic.Value
	// pos/maxPos track the byte offset ServeContent has read up to, via the
	// Seek it performs for the Range header plus the reads that follow. maxPos
	// is the furthest offset this serve delivered — the watched-progress
	// signal, folded into the session by the serve bookkeeping.
	pos    atomic.Int64
	maxPos atomic.Int64
}

func (s *StreamMonitor) Seek(offset int64, whence int) (int64, error) {
	n, err := s.ReadSeekCloser.Seek(offset, whence)
	if err == nil {
		s.pos.Store(n)
	}
	return n, err
}

func (s *StreamMonitor) Read(p []byte) (n int, err error) {
	s.readCalls.Add(1)
	readStart := time.Now()
	n, err = s.ReadSeekCloser.Read(p)
	s.readBlocked.Add(time.Since(readStart).Nanoseconds())
	if n > 0 {
		s.bytesRead.Add(int64(n))
		newPos := s.pos.Add(int64(n))
		for {
			max := s.maxPos.Load()
			if newPos <= max || s.maxPos.CompareAndSwap(max, newPos) {
				break
			}
		}
		if s.manager != nil {
			s.manager.AddBytesRead(s.sessionID, int64(n))
		}
		if s.onProgress != nil {
			s.onProgress()
		}
	}
	if errors.Is(err, io.EOF) {
		s.sawEOF.Store(true)
	} else if err != nil {
		s.lastReadErr.Store(err.Error())
	}
	if err != nil && s.onReadError != nil {
		s.readErrorOnce.Do(func() {
			s.onReadError(s.sessionID, err)
		})
	}
	if s.manager != nil && time.Since(s.lastUpdate) > 10*time.Second {
		windowElapsed := false
		s.mu.Lock()
		if time.Since(s.lastUpdate) > 10*time.Second {
			s.manager.KeepAlive(s.sessionID, s.clientIP)
			s.lastUpdate = time.Now()
			windowElapsed = true
		}
		s.mu.Unlock()
		if windowElapsed && s.onServeWindow != nil {
			s.onServeWindow()
		}
	}
	return n, err
}

type streamMonitorSnapshot struct {
	BytesRead     int64
	ReadCalls     int64
	MaxPos        int64
	SawEOF        bool
	LastReadError string
	ReadBlocked   time.Duration
}

func (s *StreamMonitor) Snapshot() streamMonitorSnapshot {
	lastReadErr := ""
	if v := s.lastReadErr.Load(); v != nil {
		lastReadErr = v.(string)
	}
	return streamMonitorSnapshot{
		BytesRead:     s.bytesRead.Load(),
		ReadCalls:     s.readCalls.Load(),
		MaxPos:        s.maxPos.Load(),
		SawEOF:        s.sawEOF.Load(),
		LastReadError: lastReadErr,
		ReadBlocked:   time.Duration(s.readBlocked.Load()),
	}
}

func (s *StreamMonitor) Close() error {
	if s.ReadSeekCloser != nil {
		return s.ReadSeekCloser.Close()
	}
	return nil
}

func classifyProbeLikeServe(r *http.Request, size int64, effectiveRange string, responseStats bufferedResponseSnapshot, streamStats streamMonitorSnapshot, closeReason string) (bool, string) {
	if r == nil {
		return false, ""
	}
	if r.Method == http.MethodHead {
		return true, "head_request"
	}
	isNearEOFEofRequest := streamStats.SawEOF && isNearEOFRange(effectiveRange, size)
	if isNearEOFEofRequest {
		if responseStats.BytesWritten == 0 && streamStats.BytesRead == 0 {
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
	if errors.Is(r.Context().Err(), context.Canceled) || errors.Is(r.Context().Err(), context.DeadlineExceeded) || closeReason == "playback canceled" {
		return true, "empty_canceled_request"
	}
	return true, "empty_request"
}

func isSmallTailProbe(responseStats bufferedResponseSnapshot, streamStats streamMonitorSnapshot) bool {
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

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) buildStreamsForKey(ctx context.Context, key StreamSlotKey, stream *auth.Stream, baseURL string) ([]Stream, *playlistResult, error) {
	list, err := s.bootstrapPlaylistForPlay(ctx, key, stream)
	if err != nil {
		if strings.Contains(err.Error(), "no candidates found") {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	showAll := streamResultsMode(stream) == "display_all"
	// Custom formatting applies to plain Stremio responses only: AIOStreams
	// re-parses descriptions on its side, so its format stays fixed.
	var format *resultFormat
	if list != nil && !list.IsAIOStreams {
		format = s.resultFormatForStream(stream)
	}
	return buildStreamsFromPlaylist(list, key, streamDisplayName(stream), ServiceName(stream), baseURL, showAll, format), list, nil
}

// streamDisplayName is the label clients render next to each result. The
// stream config's name rides on a second line under the service name, so
// multiple installed configs are tellable apart in Stremio.
// streamDisplayName is the bold label on every result: the stream's service
// name, with the stream's own name under it. A stream that set an addon name
// gets that instead of the default, and drops the second line — the override is
// already the name it wants to be known by, so repeating the stream name under
// it would undo the point of setting one.
func streamDisplayName(stream *auth.Stream) string {
	service := ServiceName(stream)
	if stream != nil && NormalizeAddonName(stream.AddonName) != "" {
		return service
	}
	name := ""
	if stream != nil {
		name = strings.TrimSpace(stream.Username)
	}
	if name == "" {
		return service
	}
	return service + "\n" + name
}

// bootstrapPlaylistForPlay rebuilds the play list and deferred sessions the same way as /stream.
func (s *Server) bootstrapPlaylistForPlay(ctx context.Context, key StreamSlotKey, stream *auth.Stream) (*playlistResult, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil {
		return nil, err
	}
	if list == nil || len(list.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates found")
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil || len(list.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates found")
	}
	for _, slotPath := range list.SlotPaths {
		s.sessionManager.ClearSlotFailedDuringPlayback(slotPath)
	}
	s.refreshLiveDeferredSessionsForPlaylist(list, key, stream)
	return list, nil
}

// recoverPlaySessionAfterEviction re-runs the /stream bootstrap and resolves to a playable slot.
// Prioritizes the requested slot index from sessionID, falling back sequentially and wrapping around.
func (s *Server) recoverPlaySessionAfterEviction(ctx context.Context, sessionID string, stream *auth.Stream) (*session.Session, string, error) {
	streamId, contentType, id, requestedIndex, ok := parseStreamSlotID(sessionID)
	if !ok {
		return nil, "", fmt.Errorf("invalid slot path %q", sessionID)
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
	list, err := s.bootstrapPlaylistForPlay(ctx, key, stream)
	if err != nil {
		return nil, "", err
	}
	currentID := ""
	currentIndex := -1
	startAtRequested := false
	if ok && requestedIndex < len(list.Candidates) {
		currentIndex = requestedIndex - 1
		startAtRequested = true
	}
	scannedFromStart := !startAtRequested

	for {
		nextID := s.deriveNextSlotIDFromPlaylist(currentID, key, currentIndex, list, stream)
		if nextID == "" {
			if !scannedFromStart {
				scannedFromStart = true
				currentID = ""
				currentIndex = -1
				continue
			}
			return nil, "", fmt.Errorf("no playable slot in rebuilt play list")
		}
		nextSess, err := s.sessionManager.GetSession(nextID)
		if err != nil {
			_, _, _, nextIndex, nextOK := parseStreamSlotID(nextID)
			if !nextOK {
				return nil, "", fmt.Errorf("invalid recovered slot %q", nextID)
			}
			nextSess, err = s.resolveStreamSlotFromPlaylist(key, nextIndex, list, stream)
		}
		if err == nil {
			return nextSess, nextID, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		logger.Debug("Play recovery skipped unresolvable slot", "slot", nextID, "err", err)
		s.sessionManager.SetSlotFailedDuringPlayback(nextID)
		currentID = nextID
		_, _, _, currentIndex, _ = parseStreamSlotID(currentID)
	}
}

func (s *Server) applyExposedPlaylistOrder(list *playlistResult, key StreamSlotKey, stream *auth.Stream) *playlistResult {
	if list == nil {
		return nil
	}
	if !streamUsesAIOStreamsProfile(stream) {
		return list
	}
	list = filterPlaylistToAvailableForAIOStreams(list)
	if list == nil || len(list.Candidates) == 0 {
		return list
	}
	if order := s.sessionManager.GetStreamFailoverOrder(streamToken(stream), key.CacheKey()); len(order) > 0 {
		list = filterPlaylistByOrder(list, key, order)
	}
	return list
}

const defaultStreamID = "default"

func (s *Server) GetStreams(ctx context.Context, contentType, id string, stream *auth.Stream) ([]Stream, error) {
	const streamRequestTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, streamRequestTimeout)
	defer cancel()
	baseURL := s.baseURLWithToken(stream)
	key := StreamSlotKey{StreamID: streamID(stream), ContentType: contentType, ID: id}
	streams, _, err := s.buildStreamsForKey(ctx, key, stream, baseURL)
	if err != nil {
		return nil, err
	}
	if sink := getStreamSinkFromContext(ctx); sink != nil {
		for _, st := range streams {
			if !sink(st) {
				break
			}
		}
	}
	return streams, nil
}

// failPlayback answers a request whose candidates have run out. A Stremio
// player gets the error video — a human sees "stream failed" instead of a
// spinner — but a direct-play session's consumer is an API client, a script
// or an external player: those need a real status code and a cause, not
// 2.4 MB of placeholder bytes behind a 200 that read as success. The split is
// by the session's origin (the /api/play surface stamps ContentType
// "direct"), never by sniffing the client.
func failPlayback(w http.ResponseWriter, r *http.Request, sess *session.Session, baseURL string, muted bool, cause error) {
	if sess != nil && sess.ContentType == "direct" {
		msg := "playback failed: no playable stream and no remaining candidates"
		if cause != nil {
			msg = "playback failed: " + cause.Error()
		}
		logger.Info("Direct-play session failed; answering with an error status", "session", sess.ID, "err", cause)
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		http.Error(w, msg, http.StatusBadGateway)
		return
	}
	forceDisconnect(w, r, baseURL, muted)
}

func forceDisconnect(w http.ResponseWriter, r *http.Request, baseURL string, muted bool) {
	filename := "failure.mp4"
	if muted {
		filename = "failure_muted.mp4"
	}
	errorVideoURL := strings.TrimSuffix(baseURL, "/") + "/error/" + filename
	logger.Info("Redirecting to error video", "url", errorVideoURL, "muted", muted)

	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, r, errorVideoURL, http.StatusTemporaryRedirect)
}

func addAPIKeyToDownloadURL(downloadURL string, indexers []config.IndexerConfig) string {
	if downloadURL == "" || len(indexers) == 0 {
		return downloadURL
	}
	u, err := url.Parse(downloadURL)
	if err != nil {
		return downloadURL
	}
	q := u.Query()
	if q.Get("t") == "get" && q.Get("id") == "" && q.Get("guid") != "" {
		q.Set("id", q.Get("guid"))
		u.RawQuery = q.Encode()
	}
	downloadHost := strings.ToLower(u.Hostname())
	for _, idx := range indexers {
		idxU, err := url.Parse(idx.URL)
		if err != nil || idx.APIKey == "" {
			continue
		}
		idxHost := strings.ToLower(idxU.Hostname())
		if idxHost == downloadHost ||
			strings.TrimPrefix(idxHost, "api.") == downloadHost ||
			strings.TrimPrefix(downloadHost, "api.") == idxHost {
			q := u.Query()
			q.Set("apikey", idx.APIKey)
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return downloadURL
}

type streamSinkKeyType struct{}

var streamSinkKey = streamSinkKeyType{}

const streamSlotPrefix = "stream:"

var ErrPlaybackStartupTimeout = playback.ErrPlaybackStartupTimeout
var ErrFirstSegmentUnavailable = playback.ErrFirstSegmentUnavailable

func (s *Server) playbackStartupTimeout() time.Duration {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil {
		return time.Duration(config.DefaultPlaybackStartupTimeoutSeconds) * time.Second
	}
	return cfg.EffectivePlaybackStartupTimeout()
}

func (s *Server) effectiveFFprobePath() string {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil {
		return ""
	}
	return cfg.EffectiveFFprobePath()
}

// notePendingLibrarySave persists the pending (pre-verdict) library entry once
// per session. The gate matters because every HTTP range request re-runs the
// playback open path and each save gzips the full NZB.
func (s *Server) notePendingLibrarySave(sess *session.Session, bp unpack.Blueprint, name string, size int64) {
	if !sess.Once(oncePendingLibrarySaved) {
		return
	}
	go s.saveSessionToLibrary(sess, bp, name, size, persistence.LibraryStatusPending)
}

type StreamSlotKey struct {
	StreamID    string
	ContentType string
	ID          string
}

func (k StreamSlotKey) SlotPath(index int) string {
	return formatStreamSlotPath(k.StreamID, k.ContentType, k.ID, index)
}

func (k StreamSlotKey) CacheKey() string {
	return k.StreamID + ":" + k.ContentType + ":" + k.ID
}

func (s *Server) baseURLWithToken(stream *auth.Stream) string {
	rt := s.runtime()
	base := strings.TrimSuffix(rt.baseURL, "/")
	if stream != nil && stream.Token != "" {
		base += "/" + stream.Token
	}
	return base
}

func streamToken(stream *auth.Stream) string {
	if stream != nil {
		return stream.Token
	}
	return ""
}

func streamID(stream *auth.Stream) string {
	if stream != nil && strings.TrimSpace(stream.Username) != "" {
		return strings.TrimSpace(stream.Username)
	}
	return defaultStreamID
}

func streamSearchQueryNames(stream *auth.Stream, contentType string) []string {
	if stream == nil {
		return nil
	}
	if contentType == "movie" {
		return append([]string(nil), stream.MovieSearchQueries...)
	}
	return append([]string(nil), stream.SeriesSearchQueries...)
}

func allSearchQueryNames(queries []config.SearchQueryConfig) []string {
	names := make([]string, 0, len(queries))
	for _, query := range queries {
		name := strings.TrimSpace(query.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

func streamUsesAvailNZB(stream *auth.Stream) bool {
	if stream == nil || stream.UseAvailNZB == nil {
		return true
	}
	return *stream.UseAvailNZB
}

func streamUsesAIOStreamsProfile(stream *auth.Stream) bool {
	if stream == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(stream.FilterSortingMode), "aiostreams")
}

func streamIndexerMode(stream *auth.Stream) string {
	if stream == nil || strings.TrimSpace(stream.IndexerMode) == "" {
		return "combine"
	}
	mode := strings.ToLower(strings.TrimSpace(stream.IndexerMode))
	switch mode {
	case "failover":
		return "failover"
	default:
		return "combine"
	}
}

func streamCombinesResults(stream *auth.Stream) bool {
	if stream == nil || stream.CombineResults == nil {
		return true
	}
	return *stream.CombineResults
}

func streamFailoverEnabled(stream *auth.Stream) bool {
	if stream == nil || stream.EnableFailover == nil {
		return true
	}
	return *stream.EnableFailover
}

func streamIndexerSelections(stream *auth.Stream) []string {
	if stream == nil {
		return nil
	}
	return append([]string(nil), stream.IndexerSelections...)
}

func (s *Server) indexerHostsForStream(stream *auth.Stream) []string {
	rt := s.runtime()
	if len(rt.availNZBIndexerHosts) == 0 {
		return nil
	}
	selections := streamIndexerSelections(stream)
	if len(selections) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(selections))
	hosts := make([]string, 0, len(selections))
	for _, name := range selections {
		host := strings.TrimSpace(rt.availNZBIndexerHosts[name])
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

func streamResultsMode(stream *auth.Stream) string {
	if streamUsesAIOStreamsProfile(stream) {
		return "display_all"
	}
	if stream == nil || strings.TrimSpace(stream.ResultsMode) == "" {
		return "combined_stream"
	}
	mode := strings.ToLower(strings.TrimSpace(stream.ResultsMode))
	switch mode {
	case "display_all":
		return "display_all"
	default:
		return "combined_stream"
	}
}

func hasResolvedIdentifiers(req indexer.SearchRequest) bool {
	return strings.TrimSpace(req.IMDbID) != "" || strings.TrimSpace(req.TMDBID) != "" || strings.TrimSpace(req.TVDBID) != "" || strings.TrimSpace(req.KitsuID) != ""
}

func hasUsableIDSearchIdentifier(req indexer.SearchRequest, contentType string) bool {
	if strings.TrimSpace(req.KitsuID) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "movie":
		return strings.TrimSpace(req.IMDbID) != "" || strings.TrimSpace(req.TMDBID) != ""
	case "series", "anime":
		return strings.TrimSpace(req.TVDBID) != "" || strings.TrimSpace(req.TMDBID) != "" || strings.TrimSpace(req.IMDbID) != ""
	default:
		return hasResolvedIdentifiers(req)
	}
}

func hasPreparedTextQueries(req indexer.SearchRequest) bool {
	return strings.TrimSpace(req.Query) != ""
}

func applyStreamIndexerSelection(req *indexer.SearchRequest, stream *auth.Stream) {
	if req == nil {
		return
	}
	req.IndexerMode = streamIndexerMode(stream)
	if stream == nil || len(stream.IndexerOverrides) == 0 || len(req.EffectiveByIndexer) == 0 {
		return
	}

	selected := make(map[string]config.IndexerSearchConfig, len(stream.IndexerOverrides))
	for name, override := range stream.IndexerOverrides {
		selected[name] = override
	}

	disableID := true
	disableText := true

	for name, effective := range req.EffectiveByIndexer {
		override, isSelected := selected[name]
		if isSelected {
			if effective == nil {
				copyOverride := override
				req.EffectiveByIndexer[name] = &copyOverride
				continue
			}
			if override.DisableIdSearch != nil {
				effective.DisableIdSearch = override.DisableIdSearch
			}
			if override.DisableStringSearch != nil {
				effective.DisableStringSearch = override.DisableStringSearch
			}
			continue
		}
		if effective == nil {
			req.EffectiveByIndexer[name] = &config.IndexerSearchConfig{
				DisableIdSearch:     &disableID,
				DisableStringSearch: &disableText,
			}
			continue
		}
		effective.DisableIdSearch = &disableID
		effective.DisableStringSearch = &disableText
	}

}

func formatStreamSlotPath(streamID, contentType, id string, index int) string {
	return streamSlotPrefix + streamID + ":" + contentType + ":" + id + ":" + strconv.Itoa(index)
}

type StreamSink func(Stream) bool

func WithStreamSink(ctx context.Context, sink StreamSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, streamSinkKey, sink)
}

func getStreamSinkFromContext(ctx context.Context) StreamSink {
	if v := ctx.Value(streamSinkKey); v != nil {
		if f, ok := v.(StreamSink); ok {
			return f
		}
	}
	return nil
}

// refreshLiveDeferredSessionsForPlaylist re-binds the deferred sessions that
// already exist to a freshly built play list, so a slot whose release moved
// under it is replaced instead of serving what it was bound to before.
//
// Slots with no session are deliberately left alone. Creating one per candidate
// meant a hundred manager-lock round trips — plus a gzip inflate for every
// candidate the library had cached — on a play path that needs exactly one of
// them, and every route to a slot (the play request itself, the failover walk,
// the fallback prefetch) already resolves it from this same list on demand.
func (s *Server) refreshLiveDeferredSessionsForPlaylist(list *playlistResult, key StreamSlotKey, stream *auth.Stream) {
	rt := s.runtime()
	if list == nil || list.Params == nil {
		return
	}
	n := len(list.Candidates)
	createdCount := 0
	reusedCount := 0
	replacedCount := 0
	skippedCount := 0
	for i := 0; i < n; i++ {
		cand := list.Candidates[i]
		if cand.Release == nil || cand.Release.Link == "" {
			continue
		}
		playPath := key.SlotPath(i)
		if len(list.SlotPaths) == n {
			playPath = list.SlotPaths[i]
		}
		if s.sessionManager.PeekSession(playPath) == nil {
			skippedCount++
			continue
		}
		rel := s.slotCopyForPlay(playPath, cand.Release)
		downloadURL := addAPIKeyToDownloadURL(rel.Link, rt.config.Indexers)
		idx := rt.indexer
		if rel.SourceIndexer != nil {
			if ii, ok := rel.SourceIndexer.(indexer.Indexer); ok {
				idx = ii
			}
		}
		_, outcome, err := s.sessionManager.CreateDeferredSessionWithFetcherOutcome(playPath, downloadURL, rel, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream), s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
		if err != nil {
			logger.Debug("Create deferred session for play list failed", "slot", playPath, "err", err)
			continue
		}
		switch outcome {
		case session.DeferredSessionCreateCreated:
			createdCount++
		case session.DeferredSessionCreateExisting:
			reusedCount++
		case session.DeferredSessionCreateReplaced:
			replacedCount++
		}
	}
	if createdCount > 0 || replacedCount > 0 || reusedCount > 0 {
		logger.Debug(
			"Deferred sessions refreshed",
			"stream", key.StreamID,
			"type", key.ContentType,
			"id", key.ID,
			"candidates", n,
			"created", createdCount,
			"reused", reusedCount,
			"replaced", replacedCount,
			"deferred_to_first_play", skippedCount,
		)
	}
}

func (s *Server) resolveStreamSlot(ctx context.Context, key StreamSlotKey, index int, stream *auth.Stream) (*session.Session, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil {
		return nil, fmt.Errorf("build play list: %w", err)
	}
	if list == nil {
		return nil, fmt.Errorf("build play list: no candidates found")
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil || len(list.Candidates) == 0 {
		return nil, fmt.Errorf("build play list: no candidates found")
	}
	return s.resolveStreamSlotFromPlaylist(key, index, list, stream)
}

// slotCopyForPlay picks which copy of a candidate's release the slot plays.
// The primary leads; a failed attempt moves the slot's cursor on to one of the
// same-release variants the merge kept, and the session is rebuilt against
// that NZB under the same slot id — so this switch, unlike the walk to a
// different release, never has to redirect the client.
func (s *Server) slotCopyForPlay(slotPath string, rel *release.Release) *release.Release {
	if rel == nil {
		return nil
	}
	s.sessionManager.NoteSlotCopies(slotPath, rel.CopyCount())
	if copyRel := rel.CopyAt(s.sessionManager.SlotCopyIndex(slotPath)); copyRel != nil {
		return copyRel
	}
	return rel
}

func (s *Server) resolveStreamSlotFromPlaylist(key StreamSlotKey, index int, list *playlistResult, stream *auth.Stream) (*session.Session, error) {
	rt := s.runtime()
	requestedSlotPath := key.SlotPath(index)
	candidateIndex := index
	if len(list.SlotPaths) == len(list.Candidates) {
		candidateIndex = -1
		for i, slotPath := range list.SlotPaths {
			if slotPath == requestedSlotPath {
				candidateIndex = i
				break
			}
		}
	}
	if candidateIndex < 0 || candidateIndex >= len(list.Candidates) {
		return nil, fmt.Errorf("slot %s not found in play list", requestedSlotPath)
	}
	cand := list.Candidates[candidateIndex]
	rel := s.slotCopyForPlay(requestedSlotPath, cand.Release)
	if rel == nil || rel.Link == "" {
		return nil, fmt.Errorf("no release at slot %s", requestedSlotPath)
	}
	downloadURL := addAPIKeyToDownloadURL(rel.Link, rt.config.Indexers)
	idx := rt.indexer
	if rel.SourceIndexer != nil {
		if i, ok := rel.SourceIndexer.(indexer.Indexer); ok {
			idx = i
		}
	}
	sessionID := requestedSlotPath
	_, _, err := s.sessionManager.CreateDeferredSessionWithFetcherOutcome(sessionID, downloadURL, rel, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream), s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
	if err != nil {
		return nil, fmt.Errorf("create deferred session: %w", err)
	}
	sess, err := s.sessionManager.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// handleNextRelease redirects the client to the next non-failed slot.
// For slot :0 (the "next release" stream URL, which is always anchored to slot 0), a per-device cursor
// advances through releases so repeated clicks return :1, :2, :3, ... rather than always :1.
// For slot :N (direct progression from a known position), deriveNextSlotID is used as-is.
func (s *Server) handleNextRelease(w http.ResponseWriter, r *http.Request, stream *auth.Stream) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/next/")
	if sessionID == "" {
		http.Error(w, "Missing stream slot", http.StatusBadRequest)
		return
	}
	streamId, contentType, id, currentIndex, ok := parseStreamSlotID(sessionID)
	if !ok {
		http.Error(w, "Invalid stream slot", http.StatusBadRequest)
		return
	}
	if streamId == "" {
		streamId = defaultStreamID
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}

	var nextSlotID string
	var err error
	if currentIndex == 0 {
		// The "next release" stream URL is always anchored to slot :0 regardless of how many times the
		// user has already clicked "next". Use a cursor so successive clicks advance through the list.
		nextSlotID, err = s.advanceNextReleaseCursor(r.Context(), key, stream)
	} else {
		// Called from a specific non-zero slot (e.g. AIOStreams failover order progression).
		nextSlotID, err = s.deriveNextSlotID(r.Context(), sessionID, stream)
	}
	if err != nil || nextSlotID == "" {
		http.Error(w, "No next release available", http.StatusNotFound)
		return
	}
	nextURL := s.baseURLWithToken(stream) + "/play/" + nextSlotID + "?next=1"
	logger.Info("Next release redirect", "from", sessionID, "to", nextSlotID)
	w.Header().Set("Location", nextURL)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, r, nextURL, http.StatusTemporaryRedirect)
}

type nextReleaseCursor struct {
	mu          sync.Mutex
	pendingSlot string    // slot we last redirected to; held idempotent until it commits or fails
	nextIndex   int       // next index to search from when advancing; starts at 1
	lastTouched time.Time // when this cursor was last used; sweepSearchCaches drops idle ones
}

// advanceNextReleaseCursor returns the next non-failed slot for the given (device, key).
// It is commit-gated: after redirecting to a slot, all subsequent calls return the same slot
// until it either commits (was successfully served, tracked by onceSuccessRecorded) or
// fails (marked by SetSlotFailedDuringPlayback). This prevents Stremio's automatic re-requests
// of the /next/ URL from prematurely advancing through the playlist.
func (s *Server) advanceNextReleaseCursor(ctx context.Context, key StreamSlotKey, stream *auth.Stream) (string, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return "", err
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil {
		return "", nil
	}
	n := len(list.Candidates)
	useSlotPaths := len(list.SlotPaths) == n

	stateKey := streamToken(stream) + "|" + key.CacheKey()
	// Stamped at creation, not left zero: the sweep reads lastTouched, and a
	// brand-new cursor would otherwise look idle forever between the store and
	// the lock below.
	v, _ := s.nextReleaseIndex.LoadOrStore(stateKey, &nextReleaseCursor{nextIndex: 1, lastTouched: time.Now()})
	cursor := v.(*nextReleaseCursor)

	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	cursor.lastTouched = time.Now()

	// If we have a pending slot, decide whether to stay or advance.
	if cursor.pendingSlot != "" {
		if s.sessionManager.GetSlotFailedDuringPlayback(cursor.pendingSlot) {
			// Pending slot failed; fall through to find the next.
			cursor.pendingSlot = ""
		} else if !s.slotCommitted(cursor.pendingSlot) {
			// Pending slot is alive but not yet committed (still loading/probing).
			// This is a Stremio automatic retry of the /next/ URL – return the same slot
			// so we don't prematurely skip to the next release.
			return cursor.pendingSlot, nil
		} else {
			// Committed successfully; the user is intentionally advancing. Fall through.
			cursor.pendingSlot = ""
		}
	}

	// Find the next non-failed slot starting from nextIndex.
	startIdx := cursor.nextIndex
	if startIdx < 1 {
		startIdx = 1
	}
	for i := startIdx; i < n; i++ {
		slotPath := key.SlotPath(i)
		if useSlotPaths {
			slotPath = list.SlotPaths[i]
		}
		if !s.sessionManager.GetSlotFailedDuringPlayback(slotPath) {
			cursor.pendingSlot = slotPath
			cursor.nextIndex = i + 1
			return slotPath, nil
		}
	}
	// All candidates exhausted. Reset so the next call starts fresh (e.g. after
	// failed states are cleared or the user circles back to try again).
	cursor.nextIndex = 1
	cursor.pendingSlot = ""
	return "", nil
}

func parseStreamSlotID(sessionID string) (streamId, contentType, id string, index int, ok bool) {
	if !strings.HasPrefix(sessionID, streamSlotPrefix) {
		return "", "", "", 0, false
	}
	rest := strings.TrimPrefix(sessionID, streamSlotPrefix)
	parts := strings.Split(rest, ":")
	if len(parts) < 3 {
		return "", "", "", 0, false
	}
	index, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return "", "", "", 0, false
	}
	if len(parts) == 3 {
		contentType = parts[0]
		id = parts[1]
		return "", contentType, id, index, true
	}
	streamId = parts[0]
	contentType = parts[1]
	id = strings.Join(parts[2:len(parts)-1], ":")
	return streamId, contentType, id, index, true
}

func isSegmentUnavailableErr(err error) bool {
	if nntp.IsArticleNotFound(err) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), "segment unavailable") {
			return true
		}
	}
	return false
}

// isDataCorruptErr returns true for yEnc decode failures that indicate a segment is corrupt
// across all providers (i.e. the article data itself is damaged on Usenet). These should
// trigger slot failover and AvailNZB bad reporting just like missing articles.
func isDataCorruptErr(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		s := e.Error()
		if strings.Contains(s, "rapidyenc") || strings.Contains(s, "data corruption") || strings.Contains(s, "yend") {
			return true
		}
	}
	return false
}

// isFatalStreamErr reports whether a playback read error means this release
// will never deliver the bytes that were asked for: the article is gone from
// every provider, the data that came back was corrupt, or the file has more
// holes than the zero-fill policy tolerates. Isolated holes never reach here —
// the loader fills those and keeps the stream running.
func isFatalStreamErr(err error) bool {
	if err == nil {
		return false
	}
	return isSegmentUnavailableErr(err) || isDataCorruptErr(err) || errors.Is(err, unpack.ErrTooManyZeroFills)
}

// primeRangeStart pulls the first byte of the range the client asked for before
// any status line is committed.
//
// http.ServeContent writes the 206 and its Content-Length up front and only then
// starts reading, so a release that cannot deliver the range answers with a
// full-length response that never produces a byte — the client sits on a promise
// nobody intends to keep. Reading first turns that into a verdict the handler can
// still act on, so a fatally damaged release fails over (or ends) promptly
// instead of stalling behind an honest-looking header.
//
// The byte comes from the same stream and the same segment ServeContent would
// fetch first, and the pool caches both hits and unanimous 430s, so a healthy
// stream pays a cache lookup rather than a second download.
func primeRangeStart(stream io.ReadSeeker, rangeHeader string, size int64) error {
	if stream == nil || size <= 0 {
		return nil
	}
	start := int64(0)
	if strings.TrimSpace(rangeHeader) != "" {
		parsed, ok := parseRangeStart(rangeHeader)
		if !ok {
			// Suffix, multi-range or malformed: which byte ServeContent sends
			// first is not knowable here, and priming the wrong one would be a
			// verdict about an offset nobody asked for.
			return nil
		}
		start = parsed
	}
	if start < 0 || start >= size {
		// Past EOF: ServeContent answers from the size alone, without reading.
		return nil
	}
	if _, err := stream.Seek(start, io.SeekStart); err != nil {
		return err
	}
	var probe [1]byte
	_, err := stream.Read(probe[:])
	if _, seekErr := stream.Seek(0, io.SeekStart); err == nil {
		err = seekErr
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func isPlayPrepareCancellation(err error) bool {
	if err == nil || errors.Is(err, ErrPlaybackStartupTimeout) {
		return false
	}
	return errors.Is(err, context.Canceled)
}

// isIndexerLimitErr reports whether a failure came from the indexer declining
// to serve us — a quota, a throttle, or a 429/5xx on the grab — rather than
// from anything wrong with the release.
//
// indexer.ErrRateLimited is the authoritative signal. The string matching below
// stays as a backstop for limit errors that reach us across a boundary that
// flattens the wrap chain (persisted attempt reasons, proxied indexers), which
// is also how a plain HTTP 429 used to slip through: it matched none of these
// phrases and so earned the release a full-TTL ban.
func isIndexerLimitErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, indexer.ErrRateLimited) {
		return true
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "download limit reached") ||
		strings.Contains(value, "api limit reached") ||
		strings.Contains(value, "request limit reached") ||
		strings.Contains(value, "returned status 429")
}

// redirectToNextSlotOrFail sends the client to the next failover slot, or
// disconnects it when no slot is left. Used on the two paths where a slot is
// already known to have failed before any response was written.
func (s *Server) redirectToNextSlotOrFail(w http.ResponseWriter, r *http.Request, sessionID string, streamConfig *auth.Stream, logMsg string) {
	rt := s.runtime()
	if nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig); nextID != "" && deriveErr == nil {
		logger.Info(logMsg, "from", sessionID, "to", nextID)
		w.Header().Set("Location", s.baseURLWithToken(streamConfig)+"/play/"+nextID)
		w.WriteHeader(http.StatusFound)
		return
	}
	sess, _ := s.sessionManager.GetSession(sessionID)
	failPlayback(w, r, sess, rt.baseURL, streamConfig.IsErrorVideoMuted(rt.config), nil)
}

// handlePlay: resolve session (by slot path or existing), recover after cache/session eviction,
// optionally redirect if slot previously failed, then loop: try play → on error/probe/seek failure switch to next fallback → serve content.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream) {
	playStart := time.Now()
	requestedSessionID := strings.TrimPrefix(r.URL.Path, "/play/")
	logger.Debug("Play request", "session", requestedSessionID)

	// The user committed to a stream; stop any speculative pre-probe sweep for
	// this content so it stops competing with interactive playback for NNTP
	// connections.
	s.cancelPreProbeForContentSlot(requestedSessionID)

	resolved, ok := s.resolvePlaybackSlot(w, r, streamConfig, requestedSessionID)
	if !ok {
		// Already answered: a 404, a redirect to the slot that opened, or the
		// error video once the candidates ran out.
		return
	}
	defer resolved.cancel()

	s.servePlaybackStream(w, r, streamConfig, resolved, playStart)
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

func applyMediaResponseHeaders(w http.ResponseWriter, name string) {
	if contentType := mediaContentType(name); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filepath.Base(name)))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func newMediaResponseWriter(w http.ResponseWriter, name string) *bufferedResponseWriter {
	applyMediaResponseHeaders(w, name)
	return newBufferedResponseWriter(newWriteTimeoutResponseWriter(w, 10*time.Minute), 256*1024)
}

// preparedPlaybackStream is the server-side name for playback.Prepared.
type preparedPlaybackStream = playback.Prepared

func (s *Server) preparePlaybackStream(ctx context.Context, sess *session.Session) (preparedPlaybackStream, error) {
	return s.playback.Prepare(ctx, sess)
}

// prefetchNextFallbackAfterStart warms the next fallback once the client has
// bytes in hand.
//
// Deriving the next slot and pulling its NZB used to happen before the stream
// was even opened, which put a second NZB download — and the archive scan
// behind it — in the way of the play the user is waiting on. Failover is a
// contingency; it can be warmed on the far side of first byte.
func (s *Server) prefetchNextFallbackAfterStart(sessionID string, stream *auth.Stream) {
	if sessionID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		nextSlotID, err := s.deriveNextSlotID(ctx, sessionID, stream)
		if err != nil || nextSlotID == "" {
			return
		}
		s.prefetchNextFallbackNZB(nextSlotID, stream)
	}()
}

// prefetchNextFallbackNZB starts a background goroutine to resolve the next fallback slot and
// download its NZB so that when we fail over to it, tryPlaySlot may find the NZB already loaded.
func (s *Server) prefetchNextFallbackNZB(nextSlotID string, stream *auth.Stream) {
	if nextSlotID == "" {
		return
	}
	go func(id string, stream *auth.Stream) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		sess, err := s.getOrResolveSession(ctx, id, stream)
		if err != nil {
			return
		}
		if _, err := sess.GetOrDownloadNZBWithContext(ctx, s.sessionManager); err != nil {
			logger.Trace("Prefetch NZB failed (next fallback)", "slot", id, "err", err)
		}
	}(nextSlotID, stream)
}

// deriveNextSlotID returns the next non-failed slot path after currentID by consulting the cached play list.
// If the stream uses the AIOStreams profile and has a failover order, it advances through that order; otherwise it increments the index.
func (s *Server) deriveNextSlotID(ctx context.Context, currentID string, stream *auth.Stream) (string, error) {
	streamId, contentType, id, currentIndex, ok := parseStreamSlotID(currentID)
	if !ok {
		return "", nil
	}
	key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return "", err
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil {
		return "", nil
	}
	return s.deriveNextSlotIDFromPlaylist(currentID, key, currentIndex, list, stream), nil
}

func (s *Server) deriveNextSlotIDFromPlaylist(currentID string, key StreamSlotKey, currentIndex int, list *playlistResult, stream *auth.Stream) string {
	n := len(list.Candidates)
	useSlotPaths := len(list.SlotPaths) == n
	startIndex := currentIndex + 1
	if useSlotPaths {
		foundCurrent := false
		for i, slotPath := range list.SlotPaths {
			if slotPath == currentID {
				startIndex = i + 1
				foundCurrent = true
				break
			}
		}
		if !foundCurrent {
			startIndex = 0
			for i, slotPath := range list.SlotPaths {
				_, _, _, slotIndex, ok := parseStreamSlotID(slotPath)
				if !ok {
					continue
				}
				if slotIndex <= currentIndex {
					startIndex = i + 1
					continue
				}
				break
			}
		}
		if startIndex > n {
			return ""
		}
	}

	// Sequential fallback: increment index, skip slots already marked as failed.
	for i := startIndex; i < n; i++ {
		slotPath := key.SlotPath(i)
		if useSlotPaths {
			slotPath = list.SlotPaths[i]
		}
		if !s.sessionManager.GetSlotFailedDuringPlayback(slotPath) {
			return slotPath
		}
	}
	return ""
}

// switchToNextFallback derives the next fallback slot from the cached play list and returns its session.
// Returns (nil, "", nil) when there is no next slot, or (nil, "", err) on a resolve error.
func (s *Server) switchToNextFallback(ctx context.Context, sess *session.Session, stream *auth.Stream) (*session.Session, string, error) {
	currentID := sess.ID
	streamID, contentType, id, currentIndex, ok := parseStreamSlotID(currentID)
	if !ok {
		return nil, "", nil
	}
	key := StreamSlotKey{StreamID: streamID, ContentType: contentType, ID: id}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return nil, "", err
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil {
		return nil, "", nil
	}
	for {
		nextID := s.deriveNextSlotIDFromPlaylist(currentID, key, currentIndex, list, stream)
		if nextID == "" {
			return nil, "", nil
		}
		nextSess, err := s.sessionManager.GetSession(nextID)
		if err != nil {
			_, _, _, nextIndex, nextOK := parseStreamSlotID(nextID)
			if !nextOK {
				return nil, "", nil
			}
			nextSess, err = s.resolveStreamSlotFromPlaylist(key, nextIndex, list, stream)
		}
		if err == nil {
			return nextSess, nextID, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		logger.Info("Skipping unresolved fallback slot", "from", currentID, "to", nextID, "err", err)
		s.sessionManager.SetSlotFailedDuringPlayback(nextID)
		currentID = nextID
		_, _, _, currentIndex, ok = parseStreamSlotID(currentID)
		if !ok {
			return nil, "", nil
		}
	}
}

func (s *Server) getOrResolveSession(ctx context.Context, sessionID string, stream *auth.Stream) (*session.Session, error) {
	sess, err := s.sessionManager.GetSession(sessionID)
	if err == nil {
		return sess, nil
	}
	if streamId, contentType, id, index, ok := parseStreamSlotID(sessionID); ok {
		key := StreamSlotKey{StreamID: streamId, ContentType: contentType, ID: id}
		sess, err = s.resolveStreamSlot(ctx, key, index, stream)
		if err != nil {
			return nil, err
		}
		return sess, nil
	}
	return nil, err
}

// purgeFailedRelease retires a release after a failed attempt. durable says
// whether the failure actually proved the release broken: only then does the
// verdict outlive the attempt as a library bad status and a multi-day ban. An
// inconclusive failure still fails the slot and drops the release from the
// in-memory playlist so failover moves on, but leaves nothing behind — the
// release is offered again on the next search.
func (s *Server) purgeFailedRelease(sess *session.Session, reason string, durable bool, copyOnly bool) {
	rt := s.runtime()
	if s == nil || sess == nil {
		return
	}
	detailsURL := strings.TrimSpace(sess.ReleaseURL())
	releaseTitle := ""
	if rel := sess.Release(); rel != nil {
		releaseTitle = strings.TrimSpace(rel.Title)
	}

	if !durable {
		logger.Info("Release attempt failed inconclusively; no persistent bad verdict recorded",
			"url", detailsURL, "title", releaseTitle, "reason", reason)
	}

	// 1. Mark bad in the library (kept stored so the Library UI can show and
	// filter bad releases; excluded from playback candidates by the store).
	if durable && s.attemptRecorder != nil {
		if libStore := s.attemptRecorder.LibraryStore(); libStore != nil {
			if detailsURL != "" {
				libStore.MarkStatusByDetailsURL(detailsURL, persistence.LibraryStatusBad, reason)
				logger.Info("Marked release bad in library", "url", detailsURL, "title", releaseTitle, "reason", reason)
			}
			// Title marking condemns every copy of the release at once, so it
			// waits until the release itself has been given up on. One copy's
			// missing articles say nothing about another indexer's NZB.
			if releaseTitle != "" && !copyOnly {
				libStore.MarkStatusByTitle(releaseTitle, persistence.LibraryStatusBad, reason)
			}
		}
		// 1b. Persist the verdict so the release stays filtered across restarts —
		// in-memory cache purges alone let the indexer re-offer the same broken
		// release after every restart.
		if badStore := s.attemptRecorder.BadReleaseStore(); badStore != nil && detailsURL != "" {
			ttl := config.DefaultBadReleaseTTLHours * time.Hour
			if rt.config != nil {
				ttl = rt.config.EffectiveBadReleaseTTL()
			}
			if ttl > 0 {
				badStore.MarkBad(detailsURL, releaseTitle, reason, ttl)
				logger.Info("Recorded persistent bad-release verdict", "url", detailsURL, "title", releaseTitle, "ttl", ttl)
			}
		}
	}

	// A copy-scoped verdict stops here: the persistent record keeps this NZB
	// out of future searches, while the slot stays playable through the copy
	// the cursor has already moved to. Removing the candidate or marking the
	// slot failed would throw away the copies that have not been tried.
	if copyOnly {
		logger.Info("Retired one copy of a release; slot continues with the next copy",
			"url", detailsURL, "title", releaseTitle, "slot", sess.ID, "reason", reason)
		return
	}

	// 2. Invalidate in-memory caches (playlistCache and rawSearchCache)
	if streamID, contentType, id, _, ok := parseStreamSlotID(sess.ID); ok && detailsURL != "" {
		s.markCachedReleaseUnavailable(StreamSlotKey{
			StreamID:    streamID,
			ContentType: contentType,
			ID:          id,
		}, detailsURL, sess.ID)
	}

	// Also filter out bad release from all active playlist & rawSearch entries in memory
	if detailsURL != "" {
		s.playlistCache.Range(func(key, value interface{}) bool {
			if ent, _ := value.(*playlistCacheEntry); ent != nil && ent.result != nil {
				nextResult := clonePlaylistResult(ent.result)
				if markPlaylistResultUnavailable(nextResult, StreamSlotKey{}, detailsURL, sess.ID) {
					if len(nextResult.Candidates) == 0 {
						s.playlistCache.Delete(key)
					} else {
						s.playlistCache.Store(key, &playlistCacheEntry{result: nextResult, until: ent.until})
					}
				}
			}
			return true
		})
		s.rawSearchCache.Range(func(key, value interface{}) bool {
			if ent, _ := value.(*rawSearchCacheEntry); ent != nil && ent.raw != nil {
				nextRaw := cloneRawSearchResult(ent.raw)
				if markRawSearchResultUnavailable(nextRaw, detailsURL) {
					if len(nextRaw.IndexerReleases) == 0 {
						s.rawSearchCache.Delete(key)
					} else {
						s.rawSearchCache.Store(key, &rawSearchCacheEntry{raw: nextRaw, until: ent.until})
					}
				}
			}
			return true
		})
	}

	// 3. Mark slot failed in session manager so failover redirects immediately
	s.sessionManager.SetSlotFailedDuringPlayback(sess.ID)
}

func (s *Server) applyReportedBadReleaseToCaches(sess *session.Session, outcome availnzb.ReportOutcome, durable, copyOnly bool) {
	if s == nil || sess == nil {
		return
	}
	s.purgeFailedRelease(sess, outcome.Reason, durable, copyOnly)
}

// reportBadReleaseOutcome runs the shared bad-release ritual: derive the
// default outcome, report to AvailNZB when permitted, then apply the verdict
// to the playlist/raw caches. requireReportGate preserves the serve-path rule
// that only definitive failures reach AvailNZB; the speculative pre-probe
// path gates on preProbeConfirmsBadRelease before calling and passes false.
//
// copyOnly says the failure retires one NZB rather than the release behind it,
// which is what a slot that still has an untried same-release copy needs: the
// report and the persistent verdict are keyed by details URL and stay
// accurate, while the candidate and the slot survive.
func (s *Server) reportBadReleaseOutcome(sess *session.Session, streamErr error, requireReportGate, copyOnly bool) availnzb.ReportOutcome {
	rt := s.runtime()
	availOutcome := availOutcomeForFailure(streamErr)
	canReport := rt.availReporter != nil && (!requireReportGate || s.shouldReportBadReleaseToAvailNZB(streamErr))
	if canReport {
		if shouldReportBadReleaseAllProviders(streamErr) {
			availOutcome = rt.availReporter.ReportBadAllProviders(sess, streamErr.Error())
		} else {
			availOutcome = rt.availReporter.ReportBad(sess, streamErr.Error())
		}
	}
	s.applyReportedBadReleaseToCaches(sess, availOutcome, conclusiveBadRelease(streamErr), copyOnly)
	return availOutcome
}

// conclusiveBadRelease reports whether a failure proves the release itself is
// broken, as opposed to the attempt having been cut short. Only a conclusive
// failure earns a durable verdict; the cases carved out here are all ones the
// code already describes as temporary elsewhere, yet each of them used to
// leave the release banned for the full bad-release TTL.
func conclusiveBadRelease(streamErr error) bool {
	if streamErr == nil {
		return false
	}
	switch {
	case errors.Is(streamErr, ErrPlaybackStartupTimeout),
		errors.Is(streamErr, context.DeadlineExceeded),
		errors.Is(streamErr, context.Canceled),
		errors.Is(streamErr, unpack.ErrArchiveFastProbe),
		// Our episode matcher failed to line the target up with a file in the
		// archive. That is a verdict about the match, not about the release —
		// obfuscated inner filenames and multi-episode packs both land here
		// while holding the episode we asked for.
		errors.Is(streamErr, unpack.ErrEpisodeTargetNotFound),
		isIndexerLimitErr(streamErr):
		return false
	}
	return true
}

// preProbeConfirmsBadRelease decides whether a speculative pre-probe failure is a
// confident bad-release signal worth poisoning the slot and reporting to AvailNZB.
//
// It extends the serve-time classifier with cause-aware detection: the forced-decode
// probe surfaces the underlying stream error, so a missing (430) or corrupt article
// discovered *past* the STAT-sampled header — exactly what the deep probe is for —
// counts as confirmed bad. Inconclusive failures (timeout, cancellation, or a merely
// undecodable-on-client codec) do NOT match, so a transient blip never poisons a good
// release.
func (s *Server) preProbeConfirmsBadRelease(streamErr error) bool {
	if streamErr == nil {
		return false
	}
	return s.shouldReportBadReleaseToAvailNZB(streamErr) || isSegmentUnavailableErr(streamErr) || isDataCorruptErr(streamErr)
}

func (s *Server) shouldReportBadReleaseToAvailNZB(streamErr error) bool {
	if errors.Is(streamErr, unpack.ErrArchiveFastProbe) {
		return false
	}
	// Fast mode skips some deeper diagnostics (e.g. exhaustive archive checks).
	// Only report bad availability when failure is still clearly definitive.
	return isDefinitiveUnavailableStartupErr(streamErr) || isDataCorruptErr(streamErr)
}

func shouldReportBadReleaseAllProviders(streamErr error) bool {
	if streamErr == nil {
		return false
	}
	// This means failures exceeded the tolerated missing-segment threshold,
	// so we report using all active providers for better AvailNZB signal quality.
	return errors.Is(streamErr, unpack.ErrTooManyZeroFills) || isDefinitiveUnavailableStartupErr(streamErr)
}

func isDefinitiveUnavailableStartupErr(streamErr error) bool {
	if streamErr == nil {
		return false
	}
	// This specific startup probe error means the first segment is missing (430)
	// across selected providers, which is a reliable bad-release signal.
	return errors.Is(streamErr, ErrFirstSegmentUnavailable)
}

func normalizeAttemptReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	switch {
	case lower == "eof":
		return "No playable media stream could be opened from this release (EOF)."
	case strings.Contains(lower, "playback ended too early to classify this release"):
		return trimmed
	default:
		return trimmed
	}
}

func availOutcomeForFailure(err error) availnzb.ReportOutcome {
	if err == nil {
		return availnzb.ReportOutcome{}
	}
	errMsg := strings.TrimSpace(err.Error())
	switch {
	// AvailNZB tracks article availability, and a compressed release's articles
	// are all present — it is simply packed in a form we can never stream. The
	// local bad-release verdict is durable; the shared database is left alone.
	case errors.Is(err, unpack.ErrCompressedArchive):
		return availnzb.SkippedOutcome("Not reported to AvailNZB because the release is compressed rather than unavailable; its articles are fine, it just cannot be streamed.")
	case errors.Is(err, unpack.ErrArchiveFastProbe):
		return availnzb.SkippedOutcome("Not reported to AvailNZB because fast mode used heuristic archive probing.")
	case strings.Contains(strings.ToLower(errMsg), "playback startup timeout"):
		return availnzb.SkippedOutcome("Not reported to AvailNZB because this startup timeout may be temporary and does not prove the release is bad.")
	case isIndexerLimitErr(err):
		return availnzb.SkippedOutcome("Not reported to AvailNZB because the failure was caused by an indexer or provider limit.")
	case isSegmentUnavailableErr(err):
		return availnzb.SkippedOutcome("Not reported to AvailNZB because this segment fetch failure does not reliably prove the release is bad.")
	default:
		return availnzb.ReportOutcome{}
	}
}

func (s *Server) recordAttemptParamsForOutcome(sess *session.Session, success bool) persistence.RecordAttemptParams {
	contentType := sess.ContentType
	if contentType == "" {
		contentType = "movie"
		if sess.ContentIDs != nil && (sess.ContentIDs.Season > 0 || sess.ContentIDs.Episode > 0) {
			contentType = "series"
		}
	}
	contentID := sess.ContentID
	if contentID == "" && sess.ContentIDs != nil {
		if sess.ContentIDs.ImdbID != "" {
			contentID = sess.ContentIDs.ImdbID
		} else if sess.ContentIDs.TvdbID != "" {
			contentID = fmt.Sprintf("tvdb:%s:%d:%d", sess.ContentIDs.TvdbID, sess.ContentIDs.Season, sess.ContentIDs.Episode)
		}
	}
	return persistence.RecordAttemptParams{
		StreamName:   sess.StreamName,
		ProviderName: providerNameFromHosts(providerHostsForOutcome(sess, success)),
		ContentType:  contentType,
		ContentID:    contentID,
		ContentTitle: sess.ContentTitle,
		IndexerName:  sess.ReleaseIndexer(),
		ReleaseTitle: sess.ReportReleaseName(),
		ReleaseURL:   sess.ReleaseURL(),
		ReleaseSize:  sess.ReportSize(),
		ServedFile:   sess.SelectedPlaybackFile(),
		MatchType:    matchTypeForSession(sess, contentType),
		SlotPath:     sess.ID,
	}
}

func matchTypeForSession(sess *session.Session, contentType string) string {
	if sess == nil || contentType != "series" {
		return ""
	}
	releaseTitle := strings.TrimSpace(sess.ReportReleaseName())
	if releaseTitle == "" {
		return ""
	}
	targetSeason, targetEpisode := sessionTargetFromAttemptContext(sess)
	if targetSeason <= 0 {
		return ""
	}
	parsed := searchparser.ParseReleaseTitle(releaseTitle)
	if parsed == nil {
		return ""
	}
	if targetEpisode > 0 {
		switch parsed.EpisodeMatchRank(targetSeason, targetEpisode) {
		case 4:
			return "exact_episode"
		case 3:
			return "multi_episode"
		case 2:
			return "season_pack"
		case 1:
			return "complete_pack"
		default:
			return ""
		}
	}
	switch {
	case parsed.IsShowPack():
		return "complete_pack"
	case parsed.IsSeasonPack(targetSeason):
		return "season_pack"
	case parsed.HasSeason(targetSeason):
		return "season_match"
	default:
		return ""
	}
}

func allowLargestDirectFallbackForSession(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	switch strings.TrimSpace(sess.ContentType) {
	case "movie":
		return true
	case "series":
		return matchTypeForSession(sess, "series") == "exact_episode"
	default:
		return false
	}
}

func sessionTargetFromAttemptContext(sess *session.Session) (int, int) {
	if sess == nil {
		return 0, 0
	}
	if sess.ContentIDs != nil && sess.ContentIDs.Season > 0 {
		return sess.ContentIDs.Season, sess.ContentIDs.Episode
	}
	contentID := strings.TrimSpace(sess.ContentID)
	if contentID == "" {
		return 0, 0
	}
	parts := strings.Split(contentID, ":")
	if len(parts) < 3 {
		return 0, 0
	}
	season, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-2]))
	if err != nil || season <= 0 {
		return 0, 0
	}
	episode, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1]))
	if err != nil || episode < 0 {
		return season, 0
	}
	return season, episode
}

func providerNameFromHosts(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return strings.Join(hosts, ", ")
}

func providerHostsForOutcome(sess *session.Session, success bool) []string {
	if sess == nil {
		return nil
	}
	if success {
		return sess.ServedProviderHosts()
	}
	hosts := sess.UsedProviderHosts()
	if len(hosts) > 0 {
		return hosts
	}
	return sess.AttemptedProviderHosts()
}

// recordPreloadAttempt inserts a preload row when we are about to try playing a slot (result not yet known).
func (s *Server) recordPreloadAttempt(sess *session.Session) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParamsForOutcome(sess, false)
	s.attemptRecorder.RecordPreloadAttempt(p)
	s.notifyAttemptRecorded()
}

// recordAttempt writes one NZB attempt to the persistence layer when attemptRecorder is set (or updates existing preload row).
// notifyAttemptRecorded pokes the dashboard after an attempt row changes.
func (s *Server) notifyAttemptRecorded() {
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
}

func (s *Server) recordAttempt(sess *session.Session, success bool, failureReason string, availOutcome availnzb.ReportOutcome) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	s.clearPendingAttemptResolution(sess)
	p := s.recordAttemptParamsForOutcome(sess, success)
	p.Success = success
	p.FailureReason = normalizeAttemptReason(failureReason)
	p.AvailStatus = availOutcome.Status
	p.AvailReason = availOutcome.Reason
	s.attemptRecorder.RecordAttempt(p)
	s.notifyAttemptRecorded()
}

func (s *Server) recordFailureAttempt(sess *session.Session, streamErr error, availOutcome availnzb.ReportOutcome) {
	s.recordAttempt(sess, false, errorString(streamErr), availOutcome)
}

func (s *Server) recordPendingAttempt(sess *session.Session, reason string, availOutcome availnzb.ReportOutcome) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParamsForOutcome(sess, true)
	p.FailureReason = normalizeAttemptReason(reason)
	p.AvailStatus = availOutcome.Status
	p.AvailReason = availOutcome.Reason
	s.attemptRecorder.UpdatePendingAttempt(p)
	s.notifyAttemptRecorded()
	s.schedulePendingAttemptResolution(sess, reason, availOutcome)
}

const pendingAttemptResolutionDelay = 3 * time.Second

func pendingAttemptResolutionReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "Playback probe ended before the good threshold was reached."
	}
	return "Playback probe ended before the good threshold was reached. " + trimmed
}

func (s *Server) clearPendingAttemptResolution(sess *session.Session) {
	sess.CancelDeferred()
}

func (s *Server) schedulePendingAttemptResolution(sess *session.Session, reason string, availOutcome availnzb.ReportOutcome) {
	if s == nil || s.attemptRecorder == nil || sess == nil || sess.ID == "" {
		return
	}
	sessionID := sess.ID
	token := sess.BeginDeferred()
	finalReason := pendingAttemptResolutionReason(reason)
	go func() {
		ticker := time.NewTicker(pendingAttemptResolutionDelay)
		defer ticker.Stop()

		for {
			<-ticker.C
			// A newer resolution, or an explicit clear, supersedes this one.
			if !sess.DeferredIsCurrent(token) {
				return
			}
			if !sess.IsActivelyServing() {
				break
			}
		}

		sess.CancelDeferred()

		p := s.recordAttemptParamsForOutcome(sess, false)
		p.Success = false
		p.FailureReason = normalizeAttemptReason(finalReason)
		p.AvailStatus = availOutcome.Status
		p.AvailReason = availOutcome.Reason
		s.attemptRecorder.ResolvePendingAttempt(p)
		if s.onAttemptRecorded != nil {
			s.onAttemptRecorded()
		}
		logger.Debug("Resolved stale pending attempt", "session", sessionID, "reason", finalReason)
	}()
}

func (s *Server) logBelowGoodThresholdOnce(sess *session.Session, sessionID string, serveDuration time.Duration, minBytes int64, minDuration time.Duration, reason string) {
	if s == nil {
		return
	}
	// Gated on the session when there is one. Without a session there is
	// nothing to gate against, and this is only a diagnostic.
	if sess != nil && !sess.Once(onceThresholdLogged) {
		return
	}
	bytesRead := int64(0)
	if sess != nil {
		bytesRead = sess.BytesRead()
	}
	logger.Debug("Skipping success bookkeeping below good threshold",
		"session", sessionID,
		"bytes_read", bytesRead,
		"serve_duration", serveDuration,
		"min_bytes", minBytes,
		"min_duration", minDuration,
		"reason", reason,
	)
}

// commitGoodAttemptIfQualified records a successful attempt once the play has
// run long enough and far enough to count. It reports whether the session has
// committed, including when an earlier call already did it.
func (s *Server) commitGoodAttemptIfQualified(sess *session.Session, sessionID string, serveStartedAt time.Time) bool {
	rt := s.runtime()
	if sess == nil {
		return false
	}
	if sess.OnceDone(onceSuccessRecorded) {
		return true
	}
	serveDuration := time.Since(serveStartedAt)
	minBytes := availnzb.DefaultMinBytesToReportGood
	minDuration := availnzb.DefaultMinDurationToReportGood
	if rt.availReporter != nil {
		minBytes = rt.availReporter.MinBytesToReportGood
		minDuration = rt.availReporter.MinDurationToReportGood
	}
	if !availnzb.QualifiesGood(sess, serveDuration, minBytes, minDuration) {
		return false
	}
	availOutcome := availnzb.ReportOutcome{}
	if rt.availReporter != nil {
		availOutcome = rt.availReporter.ReportGood(sess, serveDuration)
	}
	if !sess.Once(onceSuccessRecorded) {
		return true
	}
	s.recordAttempt(sess, true, "", availOutcome)
	sess.ResetOnce(onceThresholdLogged)
	// Playback is proven real at this point — the moment to tell Simkl the
	// title is being watched (once per session; the stop with final progress
	// comes from the serve teardown).
	s.scrobbleSimklStart(sess)
	// Populate the library from a real successful play, not just speculative
	// pre-probe, so the cache reflects actual usage even when pre-probing is
	// limited or disabled. Skip sessions already sourced from the library.
	rel := sess.Release()
	if rel != nil && !rel.IsLibraryResult() {
		go s.saveSessionToLibrary(sess, sess.Blueprint(), sess.SelectedPlaybackFile(), 0, persistence.LibraryStatusGood)
	}
	// Flip any matching stored library rows to good. This covers sessions the
	// save above cannot: direct play carries no Release (title only, in
	// ContentTitle — set from the library item when played via its Play button),
	// and library-sourced sessions are deliberately not re-saved.
	if s.attemptRecorder != nil {
		if libStore := s.attemptRecorder.LibraryStore(); libStore != nil {
			const reason = "sustained playback reached good threshold"
			if rel != nil && rel.DetailsURL != "" {
				libStore.MarkStatusByDetailsURL(rel.DetailsURL, persistence.LibraryStatusGood, reason)
			}
			title := sess.ReportReleaseName()
			if title == "" {
				title = strings.TrimSpace(sess.ContentTitle)
			}
			if title != "" {
				libStore.MarkStatusByTitle(title, persistence.LibraryStatusGood, reason)
			}
		}
	}
	// A sustained good play overrides any stale persistent bad verdict (e.g. one
	// recorded after this playlist was cached, or a prior false positive).
	if s.attemptRecorder != nil && rel != nil && rel.DetailsURL != "" {
		if badStore := s.attemptRecorder.BadReleaseStore(); badStore != nil {
			badStore.Forget(rel.DetailsURL)
		}
	}
	logger.Info("Playback attempt reached good threshold",
		"session", sessionID,
		"bytes_read", sess.BytesRead(),
		"serve_duration", serveDuration,
		"min_bytes", minBytes,
		"min_duration", minDuration,
	)
	return true
}

func (s *Server) preProbeSessionSync(ctx context.Context, sess *session.Session) (bool, error) {
	return s.playback.PreProbe(ctx, sess)
}

// preProbeCacheKeyFromSlot derives the StreamSlotKey cache key from a slot path,
// so the pre-probe cancel registry is keyed identically whether we register from a
// StreamSlotKey or cancel from a play request's session id.
func preProbeCacheKeyFromSlot(slotPath string) string {
	sid, ct, id, _, ok := parseStreamSlotID(slotPath)
	if !ok {
		return ""
	}
	return StreamSlotKey{StreamID: sid, ContentType: ct, ID: id}.CacheKey()
}

// preProbeCancelEntry boxes a cancel func so it can live in a sync.Map: func
// values are not comparable, so storing them bare would panic sync.Map's
// CompareAndDelete. A pointer to this struct is compared by identity instead.
type preProbeCancelEntry struct {
	cancel context.CancelFunc
}

// registerPreProbeCancel records the cancel func for an in-flight speculative
// pre-probe sweep under cacheKey, cancelling any older sweep for the same content.
// The returned cleanup removes the registration (only if still ours).
func (s *Server) registerPreProbeCancel(cacheKey string, cancel context.CancelFunc) func() {
	if cacheKey == "" {
		return func() {}
	}
	entry := &preProbeCancelEntry{cancel: cancel}
	if prev, loaded := s.preProbeCancels.Swap(cacheKey, entry); loaded {
		if pe, ok := prev.(*preProbeCancelEntry); ok && pe != nil && pe.cancel != nil {
			pe.cancel()
		}
	}
	return func() { s.preProbeCancels.CompareAndDelete(cacheKey, entry) }
}

// cancelPreProbeForContentSlot cancels any in-flight speculative pre-probe sweep
// for the content addressed by slotPath. Called when real playback starts so the
// speculative sweep stops competing for NNTP connections with interactive startup.
func (s *Server) cancelPreProbeForContentSlot(slotPath string) {
	cacheKey := preProbeCacheKeyFromSlot(slotPath)
	if cacheKey == "" {
		return
	}
	if v, ok := s.preProbeCancels.LoadAndDelete(cacheKey); ok {
		if pe, ok := v.(*preProbeCancelEntry); ok && pe != nil && pe.cancel != nil {
			logger.Debug("Cancelling speculative pre-probe; real playback started", "key", cacheKey)
			pe.cancel()
		}
	}
}

// preProbeSlots warms up to maxAttempts slots in order, stopping at the first
// one that probes clean. Failures that definitively prove a bad release are
// reported; anything inconclusive just moves to the next slot. Runs in the
// background under a 3-minute budget, cancellable via cacheKey when real
// playback starts.
func (s *Server) preProbeSlots(cacheKey string, slotPaths []string, maxAttempts int, stream *auth.Stream) {
	if maxAttempts <= 0 || len(slotPaths) == 0 {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		defer s.registerPreProbeCancel(cacheKey, cancel)()

		attempts := 0
		for _, slotPath := range slotPaths {
			if attempts >= maxAttempts {
				return
			}
			sess, err := s.sessionManager.GetSession(slotPath)
			// Library-sourced releases were already validated when stored, so
			// re-probing buys nothing — and skipping one no longer consumes an
			// attempt, so the budget is spent on candidates a probe can
			// actually tell us something about.
			if rel := sess.Release(); err != nil || (rel != nil && rel.IsLibraryResult()) {
				if err == nil {
					logger.Debug("Skipping speculative pre-probing for library candidate", "title", sess.ReportReleaseName())
				}
				continue
			}
			attempts++
			ok, probeErr := s.preProbeSessionSync(ctx, sess)
			if ok {
				logger.Info("Speculative failover pre-probing found ready stream candidate",
					"attempt", attempts, "max_attempts", maxAttempts, "slot", sess.ID, "title", sess.ReportReleaseName())
				return
			}
			logger.Debug("Speculative failover pre-probing candidate failed, trying next failover candidate",
				"attempt", attempts, "max_attempts", maxAttempts, "slot", sess.ID, "err", probeErr)
			if s.preProbeConfirmsBadRelease(probeErr) {
				// Moving the slot on to another copy of the same release keeps
				// the verdict on the NZB that failed. The real play then starts
				// on a copy the probe never condemned.
				copyOnly := s.sessionManager.AdvanceSlotCopy(sess.ID, stream.EffectiveVariantAttempts())
				s.reportBadReleaseOutcome(sess, probeErr, false, copyOnly)
			}
		}
	}()
}

func (s *Server) speculativelyPreProbeTopPlaylistCandidates(key StreamSlotKey, list *playlistResult, stream *auth.Stream) {
	rt := s.runtime()
	if streamUsesAIOStreamsProfile(stream) {
		logger.Debug("Skipping speculative pre-probing in /stream for AIOStreams profile; waiting for /failover-order", "stream", key.StreamID)
		return
	}
	if list == nil || len(list.Candidates) == 0 {
		return
	}

	slotPaths := make([]string, len(list.Candidates))
	useSlotPaths := len(list.SlotPaths) == len(list.Candidates)
	for i := range list.Candidates {
		if useSlotPaths {
			slotPaths[i] = list.SlotPaths[i]
			continue
		}
		slotPaths[i] = key.SlotPath(i)
	}

	s.preProbeSlots(key.CacheKey(), slotPaths, rt.config.EffectiveSpeculativePreProbingMaxAttempts(), stream)
}

func (s *Server) speculativelyPreProbeTopFailoverOrder(order []string, stream *auth.Stream) {
	rt := s.runtime()
	if len(order) == 0 {
		return
	}
	s.preProbeSlots(preProbeCacheKeyFromSlot(order[0]), order, rt.config.EffectiveSpeculativePreProbingMaxAttempts(), stream)
}

// sessionUnpackFiles adapts a session's loader files to the archive layer's
// interface, which is how a blueprint reaches the files it was built from.
func sessionUnpackFiles(sess *session.Session) []unpack.UnpackableFile {
	files := sess.Files()
	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for i := range files {
		unpackFiles[i] = files[i]
	}
	return unpackFiles
}

// saveSessionToLibrary upserts the session's NZB + blueprint into the library.
// status follows the lifecycle: LibraryStatusPending as soon as the NZB and
// blueprint are known (so nothing is lost if validation/playback is interrupted),
// then LibraryStatusGood once validated or played through — a pending re-save
// never downgrades an existing good/bad verdict (enforced in StoreItem).
func (s *Server) saveSessionToLibrary(sess *session.Session, bp unpack.Blueprint, mediaFileName string, mediaFileSize int64, status string) {
	rt := s.runtime()
	rel := sess.Release()
	if s == nil || s.attemptRecorder == nil || sess == nil || rel == nil {
		return
	}
	if rt.config != nil && !rt.config.EffectiveLibraryAutoSave() {
		return
	}
	libStore := s.attemptRecorder.LibraryStore()
	if libStore == nil {
		return
	}

	bpJSON := ""
	if data, ok := unpack.SerializeBlueprint(bp, sessionUnpackFiles(sess)); ok {
		// Reusable serialized form (plaintext STORE-mode RAR, or a direct file
		// and its segment map) — rehydrated on replay.
		bpJSON = string(data)
	} else if bp != nil {
		// Non-reusable blueprint type: keep a raw marshal so the HasBlueprint flag
		// still reflects presence, but it won't be rehydrated.
		if data, err := json.Marshal(bp); err == nil {
			bpJSON = string(data)
		}
	}

	capsJSON := ""
	if caps := sess.MediaCapabilities(); caps != nil {
		if data, err := json.Marshal(caps); err == nil {
			capsJSON = string(data)
		}
	}

	contentType := "movie"
	contentID := ""
	season := 0
	episode := 0
	var imdbID, tmdbID, tvdbID, kitsuID string

	if sess.ContentIDs != nil {
		imdbID = sess.ContentIDs.ImdbID
		tmdbID = sess.ContentIDs.TmdbID
		tvdbID = sess.ContentIDs.TvdbID
		kitsuID = sess.ContentIDs.KitsuID
		contentID = libraryContentID(imdbID, tmdbID, tvdbID, kitsuID)
		season = sess.ContentIDs.Season
		episode = sess.ContentIDs.Episode
		if season > 0 || episode > 0 {
			contentType = "series"
		}
	}

	item := &persistence.LibraryItem{
		ContentType:   contentType,
		ContentID:     contentID,
		ImdbID:        imdbID,
		TmdbID:        tmdbID,
		TvdbID:        tvdbID,
		KitsuID:       kitsuID,
		Season:        season,
		Episode:       episode,
		ReleaseTitle:  rel.Title,
		DetailsURL:    rel.DetailsURL,
		IndexerName:   rel.Indexer,
		SizeBytes:     rel.Size,
		NZBData:       sess.NZBBytes,
		BlueprintJSON: bpJSON,
		MediaFileName: mediaFileName,
		MediaFileSize: mediaFileSize,
		MediaCapsJSON: capsJSON,
		Status:        status,
	}
	if caps := sess.MediaCapabilities(); caps != nil {
		item.VideoCodec = caps.VideoCodec
		item.Height = caps.Height
		item.BitDepth = caps.BitDepth
		item.HDR = caps.HDR
		item.DolbyVision = caps.DolbyVision
		item.AudioCodec = caps.AudioCodec
	}

	if err := libStore.StoreItem(item); err != nil {
		logger.Warn("Failed to save session to library", "title", rel.Title, "err", err)
	} else {
		logger.Debug("Saved session to library", "title", rel.Title, "type", contentType, "id", contentID, "status", status)
		if rt.config != nil {
			_ = libStore.EnforceQuota(rt.config.EffectiveLibraryMaxItems(), rt.config.EffectiveLibraryMaxSizeMB())
		}
	}
}
