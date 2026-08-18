package stremio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/metrics"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/playback"
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
}

func (s *StreamMonitor) Read(p []byte) (n int, err error) {
	s.readCalls.Add(1)
	readStart := time.Now()
	n, err = s.ReadSeekCloser.Read(p)
	s.readBlocked.Add(time.Since(readStart).Nanoseconds())
	if n > 0 {
		s.bytesRead.Add(int64(n))
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
	s.ensureDeferredSessionsForPlaylist(list, key, stream)
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
// per session; the sync.Map guard lives here because every HTTP range request
// re-runs the playback open path and each save gzips the full NZB.
func (s *Server) notePendingLibrarySave(sess *session.Session, bp unpack.Blueprint, name string, size int64) {
	sessionID := sess.ID
	if _, already := s.pendingLibrarySavedIDs.LoadOrStore(sessionID, struct{}{}); already {
		return
	}
	go s.saveSessionToLibrary(sess, bp, name, size, persistence.LibraryStatusPending)
	if done := sess.Done(); done != nil {
		go func() {
			<-done
			s.pendingLibrarySavedIDs.Delete(sessionID)
		}()
	}
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
	base := strings.TrimSuffix(s.baseURL, "/")
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
	if len(s.availNZBIndexerHosts) == 0 {
		return nil
	}
	selections := streamIndexerSelections(stream)
	if len(selections) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(selections))
	hosts := make([]string, 0, len(selections))
	for _, name := range selections {
		host := strings.TrimSpace(s.availNZBIndexerHosts[name])
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

func looksLikeTMDBID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	_, err := strconv.Atoi(value)
	return err == nil
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

// ensureDeferredSessionsForPlaylist creates deferred sessions for every candidate in the play list,
// keyed by the same slot path used in stream URLs, so handlePlay can serve without resolving or hitting indexers.
func (s *Server) ensureDeferredSessionsForPlaylist(list *playlistResult, key StreamSlotKey, stream *auth.Stream) {
	if list == nil || list.Params == nil {
		return
	}
	n := len(list.Candidates)
	createdCount := 0
	reusedCount := 0
	replacedCount := 0
	for i := 0; i < n; i++ {
		cand := list.Candidates[i]
		if cand.Release == nil || cand.Release.Link == "" {
			continue
		}
		playPath := key.SlotPath(i)
		if len(list.SlotPaths) == n {
			playPath = list.SlotPaths[i]
		}
		downloadURL := addAPIKeyToDownloadURL(cand.Release.Link, s.config.Indexers)
		idx := s.indexer
		if cand.Release.SourceIndexer != nil {
			if ii, ok := cand.Release.SourceIndexer.(indexer.Indexer); ok {
				idx = ii
			}
		}
		_, outcome, err := s.sessionManager.CreateDeferredSessionWithFetcherOutcome(playPath, downloadURL, cand.Release, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream), s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
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

func (s *Server) resolveStreamSlotFromPlaylist(key StreamSlotKey, index int, list *playlistResult, stream *auth.Stream) (*session.Session, error) {
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
	rel := cand.Release
	if rel == nil || rel.Link == "" {
		return nil, fmt.Errorf("no release at slot %s", requestedSlotPath)
	}
	downloadURL := addAPIKeyToDownloadURL(rel.Link, s.config.Indexers)
	idx := s.indexer
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
	pendingSlot string // slot we last redirected to; held idempotent until it commits or fails
	nextIndex   int    // next index to search from when advancing; starts at 1
}

// advanceNextReleaseCursor returns the next non-failed slot for the given (device, key).
// It is commit-gated: after redirecting to a slot, all subsequent calls return the same slot
// until it either commits (was successfully served, tracked by recordedSuccessSessionIDs) or
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
	v, _ := s.nextReleaseIndex.LoadOrStore(stateKey, &nextReleaseCursor{nextIndex: 1})
	cursor := v.(*nextReleaseCursor)

	cursor.mu.Lock()
	defer cursor.mu.Unlock()

	// If we have a pending slot, decide whether to stay or advance.
	if cursor.pendingSlot != "" {
		if s.sessionManager.GetSlotFailedDuringPlayback(cursor.pendingSlot) {
			// Pending slot failed; fall through to find the next.
			cursor.pendingSlot = ""
		} else if _, committed := s.recordedSuccessSessionIDs.Load(cursor.pendingSlot); !committed {
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
	if nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig); nextID != "" && deriveErr == nil {
		logger.Info(logMsg, "from", sessionID, "to", nextID)
		w.Header().Set("Location", s.baseURLWithToken(streamConfig)+"/play/"+nextID)
		w.WriteHeader(http.StatusFound)
		return
	}
	forceDisconnect(w, r, s.baseURL, streamConfig.IsErrorVideoMuted(s.config))
}

// handlePlay: resolve session (by slot path or existing), recover after cache/session eviction,
// optionally redirect if slot previously failed, then loop: try play → on error/probe/seek failure switch to next fallback → serve content.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream) {
	playStart := time.Now()
	sessionID := strings.TrimPrefix(r.URL.Path, "/play/")
	requestedSessionID := sessionID
	logger.Debug("Play request", "session", sessionID)

	// The user committed to a stream; stop any speculative pre-probe sweep for this
	// content so it stops competing with interactive playback for NNTP connections.
	s.cancelPreProbeForContentSlot(sessionID)

	var (
		sess         *session.Session
		stream       io.ReadSeekCloser
		name         string
		size         int64
		providerName string
	)

	var err error
	sess, err = s.sessionManager.GetSession(sessionID)
	if err != nil {
		// The session may have been deleted by a concurrent internal failover (e.g. exceeded failure threshold).
		// If the slot was marked as failed, redirect the client to the next working slot rather than 404.
		if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
			s.redirectToNextSlotOrFail(w, r, sessionID, streamConfig,
				"Session deleted (slot failed during playback), redirecting to next")
			return
		}
		recoveredSess, recoveredID, recoverErr := s.recoverPlaySessionAfterEviction(r.Context(), sessionID, streamConfig)
		if recoverErr != nil {
			logger.Debug("Play: session not found", "slot", sessionID, "err", err, "recovery_err", recoverErr)
			http.Error(w, "Session expired or not found", http.StatusNotFound)
			return
		}
		logger.Info("Play: recovered after cache/session eviction", "requested", sessionID, "playing", recoveredID)
		sess = recoveredSess
		sessionID = recoveredID
	} else if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
		s.redirectToNextSlotOrFail(w, r, sessionID, streamConfig,
			"Redirecting to next fallback (slot failed during playback)")
		return
	}

	streamMode := ""

	var mergedCtx context.Context
	var mergedCancel context.CancelFunc
	// No response (headers or body) is sent until we pass the probe below and call ServeContent. That way we can fail over without the client having seen any response.
	for {
		// Skip slots we've already marked as failed; use cache so we never try them.
		if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
			if nextSess, nextID, switchErr := s.switchToNextFallback(r.Context(), sess, streamConfig); nextID != "" && switchErr == nil {
				logger.Info("Skipping known-failed slot, trying next fallback", "from", sessionID, "to", nextID)
				sess, sessionID = nextSess, nextID
				continue
			}
			forceDisconnect(w, r, s.baseURL, streamConfig.IsErrorVideoMuted(s.config))
			return
		}
		if nextSlotID, deriveErr := s.deriveNextSlotID(r.Context(), sess.ID, streamConfig); deriveErr == nil {
			s.prefetchNextFallbackNZB(nextSlotID, streamConfig)
		}
		// Use a context that cancels when either the request ends or the session is closed (e.g. user closed from dashboard).
		// That way closing the session aborts playback and stops downloading immediately.
		mergedCtx, mergedCancel = context.WithCancel(r.Context())
		go func(sess *session.Session, done <-chan struct{}, cancel context.CancelFunc) {
			select {
			case <-done:
				return
			case <-sess.Done():
				logger.Debug("playback aborted: session closed", "session", sess.ID)
				cancel()
			}
		}(sess, mergedCtx.Done(), mergedCancel)
		// Record preload at most once per session: subsequent HTTP requests (seeks, range retries)
		// for the same session must not insert another "Preload" row that would never be resolved.
		if _, alreadyPreloaded := s.recordedPreloadSessionIDs.LoadOrStore(sessionID, struct{}{}); !alreadyPreloaded {
			s.recordPreloadAttempt(sess)
			// When the session is evicted, clear the key so future plays of the same slot get a fresh row.
			go func(id string, done <-chan struct{}) {
				<-done
				s.recordedPreloadSessionIDs.Delete(id)
			}(sessionID, sess.Done())
		}

		s.sessionManager.BeginPlaybackStartup(sessionID)
		preparedStream, prepareErr := s.preparePlaybackStream(mergedCtx, sess)
		s.sessionManager.EndPlaybackStartup(sessionID)
		if prepareErr != nil {
			mergedCancel()
			if isPlayPrepareCancellation(prepareErr) {
				logger.Debug("play prepare canceled", "session", sessionID, "err", prepareErr)
				return
			}
			if errors.Is(prepareErr, ErrPlaybackStartupTimeout) {
				logger.Warn("Playback startup timed out", "session", sessionID, "err", prepareErr)
			}
			temporaryLimitErr := isIndexerLimitErr(prepareErr)
			// Indexer limit errors are temporary and should not poison the slot for later retries
			// after the quota resets or is raised manually.
			if !temporaryLimitErr {
				s.sessionManager.SetSlotFailedDuringPlayback(sessionID)
			}
			availOutcome := s.reportBadReleaseOutcome(sess, prepareErr, true)
			// Gate failure recording: concurrent goroutines for the same session (Stremio's automatic
			// re-requests) must not each insert a Failure row. Only the first one wins.
			if _, alreadyFailed := s.recordedFailureSessionIDs.LoadOrStore(sessionID, struct{}{}); !alreadyFailed {
				s.recordFailureAttempt(sess, prepareErr, availOutcome)
				go func(id string, done <-chan struct{}) {
					<-done
					s.recordedFailureSessionIDs.Delete(id)
				}(sessionID, sess.Done())
			}
			s.sessionManager.DeleteSession(sessionID)
			// A throttled indexer must not end the attempt: the remaining
			// candidates usually come from other indexers, and stopping here
			// hands the client nothing over a failure that says only "not from
			// this indexer, not right now". The slot stays unpoisoned above so
			// it can be retried once the limit clears; the indexer's own
			// cooldown keeps the walk past its other candidates cheap.
			if streamFailoverEnabled(streamConfig) {
				if nextSess, nextID, switchErr := s.switchToNextFallback(r.Context(), sess, streamConfig); nextID != "" && switchErr == nil {
					logger.Info("Playback failover advanced", "from", sessionID, "to", nextID, "err", prepareErr)
					sess, sessionID = nextSess, nextID
					continue
				}
			}
			logger.Info("No more fallback slots", "last", sessionID, "err", prepareErr)
			forceDisconnect(w, r, s.baseURL, streamConfig.IsErrorVideoMuted(s.config))
			return
		}

		stream = preparedStream.Stream
		name = preparedStream.Spec.Name
		size = preparedStream.Spec.Size
		streamMode = preparedStream.Mode
		break
	}
	if sessionID != requestedSessionID {
		if stream != nil {
			stream.Close()
		}
		nextURL := s.baseURLWithToken(streamConfig) + "/play/" + sessionID
		if r.URL.RawQuery != "" {
			nextURL += "?" + r.URL.RawQuery
		}
		logger.Info("Redirecting client to resolved/failover slot", "from", requestedSessionID, "to", sessionID)
		w.Header().Set("Location", nextURL)
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	defer mergedCancel()
	servedSessionID := sessionID
	requestedRange := r.Header.Get("Range")
	userAgent := r.Header.Get("User-Agent")
	var closeReason atomic.Value
	var closeStreamOnce sync.Once
	closeStream := func(reason string) {
		closeStreamOnce.Do(func() {
			closeReason.Store(reason)
			if stream != nil {
				logger.Debug("play handler closing stream", "session", servedSessionID, "reason", reason)
				stream.Close()
				stream = nil
			}
		})
	}
	go func(done <-chan struct{}) {
		<-done
		closeStream("playback canceled")
	}(mergedCtx.Done())

	// After internal failover we serve a different file; don't apply the original request's Range to it.
	failedOver := sessionID != requestedSessionID
	if failedOver {
		r.Header.Del("Range")
	}

	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	s.sessionManager.MarkPlaybackValidated(sessionID)
	s.sessionManager.StartPlayback(sessionID, clientIP)
	var endPlaybackOnce sync.Once
	endPlayback := func() { s.sessionManager.EndPlayback(sessionID, clientIP) }
	defer endPlaybackOnce.Do(endPlayback)
	// When client cancels (e.g. stop in Stremio), request context is cancelled; end playback so we stop downloading and session can be evicted.
	go func() {
		<-r.Context().Done()
		endPlaybackOnce.Do(endPlayback)
	}()

	// serveFailureRecorded is set to true when onReadError records a failure for the
	// currently-served session. The success defer checks this flag so it never
	// overwrites a Failure with an OK (the "flip-flop" bug).
	serveFailureRecorded := false
	onReadError := func(playbackSessionID string, readErr error) {
		// Trigger slot failover for any permanent mid-stream error:
		//   - 430 No Such Article (segment missing across all selected providers)
		//   - yEnc decode failure (data corruption)
		//   - ErrTooManyZeroFills (too many segments failed across all providers)
		// All three mean the slot is unrecoverable. SetSlotFailedDuringPlayback marks it so the
		// existing redirect logic at the top of handlePlay redirects the player to the next slot
		// on reconnect, without requiring the user to manually switch in Stremio.
		if !isSegmentUnavailableErr(readErr) && !isDataCorruptErr(readErr) && !errors.Is(readErr, unpack.ErrTooManyZeroFills) {
			return
		}
		s.sessionManager.SetSlotFailedDuringPlayback(playbackSessionID)
		if errSess, _ := s.sessionManager.GetSession(playbackSessionID); errSess != nil {
			errSess.ResetPlaybackStream()
			availOutcome := s.reportBadReleaseOutcome(errSess, readErr, true)
			if _, alreadySucceeded := s.recordedSuccessSessionIDs.Load(playbackSessionID); !alreadySucceeded {
				if _, alreadyFailed := s.recordedFailureSessionIDs.LoadOrStore(playbackSessionID, struct{}{}); !alreadyFailed {
					s.recordFailureAttempt(errSess, readErr, availOutcome)
					go func(id string, done <-chan struct{}) {
						<-done
						s.recordedFailureSessionIDs.Delete(id)
					}(playbackSessionID, errSess.Done())
				}
			}
			if playbackSessionID == sessionID {
				serveFailureRecorded = true
			}
		}
	}
	monitoredStream := &StreamMonitor{
		ReadSeekCloser: stream,
		sessionID:      sessionID,
		clientIP:       clientIP,
		manager:        s.sessionManager,
		onReadError:    onReadError,
		lastUpdate:     time.Now(),
	}

	bufW := newMediaResponseWriter(w, name)
	var ttffOnce sync.Once
	bufW.onFirstWrite = func() {
		ttffOnce.Do(func() {
			pName := providerName
			if rel := sess.Release(); pName == "" && rel != nil {
				pName = rel.Indexer
			}
			ttffDuration := time.Since(playStart)
			metrics.Default().RecordPlaybackTTFF(metrics.PlaybackTTFFSample{
				Timestamp:    time.Now(),
				SessionID:    sessionID,
				ProviderName: pName,
				TTFF:         ttffDuration,
				IsCacheHit:   streamMode != "",
			})
			if s.attemptRecorder != nil && sess != nil {
				p := s.recordAttemptParamsForOutcome(sess, true)
				p.TTFFMS = ttffDuration.Milliseconds()
				s.attemptRecorder.UpdatePendingAttempt(p)
			}
			logger.Debug("TTFF recorded", "session", sessionID, "ttff_ms", ttffDuration.Milliseconds(), "provider", pName)
		})
	}
	serveStartedAt := time.Now()
	monitoredStream.onProgress = func() {
		s.commitGoodAttemptIfQualified(sess, sessionID, requestedSessionID, serveStartedAt)
	}
	// Per-window serve telemetry: splits each ~10s window into time blocked
	// reading (usenet/reader side) vs writing (client/proxy side), so a
	// buffering report can be attributed to the right leg of the pipeline.
	// Called only from the serving goroutine, so plain closure state is safe.
	windowStart := serveStartedAt
	var windowBytes int64
	var windowReadBlocked, windowWriteBlocked time.Duration
	monitoredStream.onServeWindow = func() {
		now := time.Now()
		streamStats := monitoredStream.Snapshot()
		writeBlocked := time.Duration(bufW.writeBlocked.Load())
		logger.Debug("Serve window",
			"session", sessionID,
			"window", now.Sub(windowStart).Round(time.Millisecond),
			"bytes", streamStats.BytesRead-windowBytes,
			"read_blocked", (streamStats.ReadBlocked - windowReadBlocked).Round(time.Millisecond),
			"write_blocked", (writeBlocked - windowWriteBlocked).Round(time.Millisecond),
			"served_total", streamStats.BytesRead,
		)
		windowStart = now
		windowBytes = streamStats.BytesRead
		windowReadBlocked = streamStats.ReadBlocked
		windowWriteBlocked = writeBlocked
	}
	effectiveRange := r.Header.Get("Range")
	probeLikeServe := false
	probeLikeServeReason := ""

	logger.Debug("play handler serving stream", "session", sessionID, "name", name, "size", size)
	logger.Debug("Serving media",
		"session", sessionID,
		"requested_session", requestedSessionID,
		"name", name,
		"size", size,
		"method", r.Method,
		"requested_range", requestedRange,
		"effective_range", effectiveRange,
		"user_agent", userAgent,
		"client_ip", clientIP,
		"failed_over", failedOver,
		"stream_mode", streamMode,
	)
	sess.BeginServeProviderTracking()
	defer func() {
		sess.EndServeProviderTracking()
		closeStream("handler exit")
		bufW.Flush()

		responseStats := bufW.Snapshot()
		streamStats := monitoredStream.Snapshot()
		closeReasonText := ""
		if v := closeReason.Load(); v != nil {
			closeReasonText = v.(string)
		}
		probeLikeServe, probeLikeServeReason = classifyProbeLikeServe(r, size, effectiveRange, responseStats, streamStats, closeReasonText)

		logger.Debug("Finished serving media",
			"session", sessionID,
			"requested_session", requestedSessionID,
			"method", r.Method,
			"requested_range", requestedRange,
			"effective_range", effectiveRange,
			"user_agent", userAgent,
			"response_status", responseStats.StatusCode,
			"response_wrote_header", responseStats.WroteHeader,
			"response_content_range", responseStats.ContentRange,
			"response_content_length", responseStats.ContentLength,
			"response_content_type", responseStats.ContentType,
			"response_accept_ranges", responseStats.AcceptRanges,
			"response_bytes", responseStats.BytesWritten,
			"response_writes", responseStats.WriteCalls,
			"response_flushes", responseStats.FlushCalls,
			"response_flush_error", responseStats.FlushError,
			"stream_bytes", streamStats.BytesRead,
			"stream_reads", streamStats.ReadCalls,
			"stream_eof", streamStats.SawEOF,
			"stream_error", streamStats.LastReadError,
			"stream_read_blocked", streamStats.ReadBlocked.Round(time.Millisecond),
			"response_write_blocked", responseStats.WriteBlocked.Round(time.Millisecond),
			"request_context_err", errorString(r.Context().Err()),
			"serve_context_err", errorString(mergedCtx.Err()),
			"failed_over", failedOver,
			"stream_mode", streamMode,
			"probe_like", probeLikeServe,
			"probe_reason", probeLikeServeReason,
			"serve_failure_recorded", serveFailureRecorded,
			"close_reason", closeReasonText,
			"duration", time.Since(serveStartedAt),
		)
	}()

	// Report good only after serving, so bytes-read threshold can be met (StreamMonitor tracks bytes).
	// Record success at most once per session: multiple HTTP requests (e.g. range/seek) for the same stream
	// would otherwise each run this defer and create duplicate "OK" entries in NZB history.
	// If onReadError already recorded a failure for this session, skip — we must not flip it back to OK.
	defer func() {
		if serveFailureRecorded {
			return
		}
		probeLikeServe, probeLikeServeReason = classifyProbeLikeServe(
			r,
			size,
			effectiveRange,
			bufW.Snapshot(),
			monitoredStream.Snapshot(),
			errorString(r.Context().Err()),
		)
		if probeLikeServe {
			logger.Debug("Skipping success bookkeeping for probe-like play request",
				"session", sessionID,
				"requested_session", requestedSessionID,
				"effective_range", effectiveRange,
				"stream_mode", streamMode,
				"reason", probeLikeServeReason,
			)
			return
		}
		serveDuration := time.Since(serveStartedAt)
		minBytes := availnzb.DefaultMinBytesToReportGood
		minDuration := availnzb.DefaultMinDurationToReportGood
		if s.availReporter != nil {
			minBytes = s.availReporter.MinBytesToReportGood
			minDuration = s.availReporter.MinDurationToReportGood
		}
		if s.commitGoodAttemptIfQualified(sess, sessionID, requestedSessionID, serveStartedAt) {
			return
		}
		if !availnzb.QualifiesGood(sess, serveDuration, minBytes, minDuration) {
			reason := fmt.Sprintf(
				"Playback ended too early to classify this release as good. Threshold not reached (%d/%d bytes, %ds/%ds).",
				sess.BytesRead(),
				minBytes,
				int(serveDuration/time.Second),
				int(minDuration/time.Second),
			)
			s.logBelowGoodThresholdOnce(sess, sessionID, requestedSessionID, serveDuration, minBytes, minDuration, reason)
			s.recordPendingAttempt(sess, reason, availnzb.SkippedOutcome("Playback ended before the good threshold was reached."))
			return
		}
		// Safety fallback: this path should normally already have returned via commitGoodAttemptIfQualified.
		s.commitGoodAttemptIfQualified(sess, sessionID, requestedSessionID, serveStartedAt)
	}()

	http.ServeContent(bufW, r, name, time.Time{}, monitoredStream)
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
func (s *Server) purgeFailedRelease(sess *session.Session, reason string, durable bool) {
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
			if releaseTitle != "" {
				libStore.MarkStatusByTitle(releaseTitle, persistence.LibraryStatusBad, reason)
			}
		}
		// 1b. Persist the verdict so the release stays filtered across restarts —
		// in-memory cache purges alone let the indexer re-offer the same broken
		// release after every restart.
		if badStore := s.attemptRecorder.BadReleaseStore(); badStore != nil && detailsURL != "" {
			ttl := config.DefaultBadReleaseTTLHours * time.Hour
			if s.config != nil {
				ttl = s.config.EffectiveBadReleaseTTL()
			}
			if ttl > 0 {
				badStore.MarkBad(detailsURL, releaseTitle, reason, ttl)
				logger.Info("Recorded persistent bad-release verdict", "url", detailsURL, "title", releaseTitle, "ttl", ttl)
			}
		}
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

func (s *Server) applyReportedBadReleaseToCaches(sess *session.Session, outcome availnzb.ReportOutcome, durable bool) {
	if s == nil || sess == nil {
		return
	}
	s.purgeFailedRelease(sess, outcome.Reason, durable)
}

// reportBadReleaseOutcome runs the shared bad-release ritual: derive the
// default outcome, report to AvailNZB when permitted, then apply the verdict
// to the playlist/raw caches. requireReportGate preserves the serve-path rule
// that only definitive failures reach AvailNZB; the speculative pre-probe
// path gates on preProbeConfirmsBadRelease before calling and passes false.
func (s *Server) reportBadReleaseOutcome(sess *session.Session, streamErr error, requireReportGate bool) availnzb.ReportOutcome {
	availOutcome := availOutcomeForFailure(streamErr)
	canReport := s.availReporter != nil && (!requireReportGate || s.shouldReportBadReleaseToAvailNZB(streamErr))
	if canReport {
		if shouldReportBadReleaseAllProviders(streamErr) {
			availOutcome = s.availReporter.ReportBadAllProviders(sess, streamErr.Error())
		} else {
			availOutcome = s.availReporter.ReportBad(sess, streamErr.Error())
		}
	}
	s.applyReportedBadReleaseToCaches(sess, availOutcome, conclusiveBadRelease(streamErr))
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
	s.clearPendingAttemptResolution(sess.ID)
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

func (s *Server) clearPendingAttemptResolution(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.pendingAttemptResolutions.Delete(sessionID)
}

func (s *Server) schedulePendingAttemptResolution(sess *session.Session, reason string, availOutcome availnzb.ReportOutcome) {
	if s == nil || s.attemptRecorder == nil || sess == nil || sess.ID == "" {
		return
	}
	sessionID := sess.ID
	token := time.Now().UnixNano()
	s.pendingAttemptResolutions.Store(sessionID, token)
	finalReason := pendingAttemptResolutionReason(reason)
	go func() {
		ticker := time.NewTicker(pendingAttemptResolutionDelay)
		defer ticker.Stop()

		for {
			<-ticker.C
			current, ok := s.pendingAttemptResolutions.Load(sessionID)
			if !ok || current != token {
				return
			}
			if !sess.IsActivelyServing() {
				break
			}
		}

		s.pendingAttemptResolutions.Delete(sessionID)

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

func (s *Server) logBelowGoodThresholdOnce(sess *session.Session, sessionID, requestedSessionID string, serveDuration time.Duration, minBytes int64, minDuration time.Duration, reason string) {
	if s == nil {
		return
	}
	if _, already := s.loggedThresholdSkipIDs.LoadOrStore(sessionID, struct{}{}); already {
		return
	}
	if sess != nil {
		if done := sess.Context().Done(); done != nil {
			go func() {
				<-done
				s.loggedThresholdSkipIDs.Delete(sessionID)
			}()
		}
	}
	bytesRead := int64(0)
	if sess != nil {
		bytesRead = sess.BytesRead()
	}
	logger.Debug("Skipping success bookkeeping below good threshold",
		"session", sessionID,
		"requested_session", requestedSessionID,
		"bytes_read", bytesRead,
		"serve_duration", serveDuration,
		"min_bytes", minBytes,
		"min_duration", minDuration,
		"reason", reason,
	)
}

func (s *Server) commitGoodAttemptIfQualified(sess *session.Session, sessionID, requestedSessionID string, serveStartedAt time.Time) bool {
	if sess == nil {
		return false
	}
	if _, already := s.recordedSuccessSessionIDs.Load(sessionID); already {
		return true
	}
	serveDuration := time.Since(serveStartedAt)
	minBytes := availnzb.DefaultMinBytesToReportGood
	minDuration := availnzb.DefaultMinDurationToReportGood
	if s.availReporter != nil {
		minBytes = s.availReporter.MinBytesToReportGood
		minDuration = s.availReporter.MinDurationToReportGood
	}
	if !availnzb.QualifiesGood(sess, serveDuration, minBytes, minDuration) {
		return false
	}
	availOutcome := availnzb.ReportOutcome{}
	if s.availReporter != nil {
		availOutcome = s.availReporter.ReportGood(sess, serveDuration)
	}
	if _, already := s.recordedSuccessSessionIDs.LoadOrStore(sessionID, struct{}{}); already {
		return true
	}
	s.recordAttempt(sess, true, "", availOutcome)
	s.loggedThresholdSkipIDs.Delete(sessionID)
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
	if done := sess.Context().Done(); done != nil {
		go func() {
			<-done
			s.recordedSuccessSessionIDs.Delete(sessionID)
		}()
	}
	logger.Info("Playback attempt reached good threshold",
		"session", sessionID,
		"requested_session", requestedSessionID,
		"bytes_read", sess.BytesRead(),
		"serve_duration", serveDuration,
		"min_bytes", minBytes,
		"min_duration", minDuration,
	)
	return true
}

func (s *Server) handleDebugPlay(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream) {
	if debugValue := strings.ToLower(strings.TrimSpace(os.Getenv("STREAMNZB_DEBUG_PLAY"))); debugValue != "1" && debugValue != "true" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	nzbPath := r.URL.Query().Get("nzb")
	if nzbPath == "" {
		http.Error(w, "Missing 'nzb' query parameter (URL or file path)", http.StatusBadRequest)
		return
	}

	logger.Info("Debug Play request", "nzb", nzbPath)

	var nzbData []byte
	var err error

	if strings.HasPrefix(nzbPath, "/") || (len(nzbPath) > 2 && nzbPath[1] == ':') {

		logger.Debug("Reading NZB from local file", "path", nzbPath)
		nzbData, err = os.ReadFile(nzbPath)
		if err != nil {
			logger.Error("Failed to read local NZB file", "path", nzbPath, "err", err)
			http.Error(w, "Failed to read local NZB file: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {

		dlCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		nzbData, err = s.indexer.DownloadNZB(dlCtx, nzbPath)
		cancel()
		if err != nil {

			httpClient := &http.Client{Timeout: 60 * time.Second}
			resp, httpErr := httpClient.Get(nzbPath)
			if httpErr != nil {
				logger.Error("Failed to download NZB", "url", nzbPath, "err", err, "httpErr", httpErr)
				http.Error(w, "Failed to download NZB: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				msg := fmt.Sprintf("Failed to download NZB (HTTP %d)", resp.StatusCode)
				logger.Error(msg, "url", nzbPath)
				http.Error(w, msg, http.StatusInternalServerError)
				return
			}

			nzbData, err = io.ReadAll(resp.Body)
			if err != nil {
				http.Error(w, "Failed to read NZB body", http.StatusInternalServerError)
				return
			}
		}
	}

	nzbParsed, err := nzb.ParseWithContext(r.Context(), bytes.NewReader(nzbData))
	if err != nil {
		logger.Error("Failed to parse NZB", "err", err)
		http.Error(w, "Failed to parse NZB", http.StatusInternalServerError)
		return
	}

	sessionID := fmt.Sprintf("debug-%x", nzbPath)

	sess, err := s.sessionManager.CreateSession(sessionID, nzbParsed, nil, nil)
	if err != nil {
		logger.Error("Failed to create session", "err", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	files := sess.Files()
	if len(files) == 0 {
		http.Error(w, "No files in NZB", http.StatusInternalServerError)
		return
	}
	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for i := range files {
		unpackFiles[i] = files[i]
	}
	password := ""
	if sessNZB := sess.NZB(); sessNZB != nil {
		password = sessNZB.Password()
	}
	// Cancel stream when either request ends or session is closed (e.g. from dashboard).
	mergedCtx, mergedCancel := context.WithCancel(r.Context())
	go func(done <-chan struct{}) {
		select {
		case <-done:
			return
		case <-sess.Done():
			logger.Debug("debug play aborted: session closed", "session", sessionID)
			mergedCancel()
		}
	}(mergedCtx.Done())
	defer mergedCancel()
	target := unpack.EpisodeTarget{}
	if sess.ContentIDs != nil {
		target = unpack.EpisodeTarget{Season: sess.ContentIDs.Season, Episode: sess.ContentIDs.Episode, Absolute: sess.ContentIDs.AbsoluteEpisode}
	}
	hints := unpack.StreamSelectionHints{
		// Debug play should be permissive and mirror nzbdav-style direct playback
		// behavior for odd/obfuscated NZBs where strict content typing is absent.
		AllowLargestDirectFallback: true,
	}
	startupTimeout := s.playbackStartupTimeout()
	if startupTimeout < 2*time.Minute {
		startupTimeout = 2 * time.Minute
	}
	openCtx, openCancel := context.WithTimeout(mergedCtx, startupTimeout)
	defer openCancel()
	stream, name, size, bp, err := unpack.GetMediaStreamForEpisodeWithHints(openCtx, unpackFiles, sess.Blueprint(), password, target, hints)
	playback.CacheReturnedBlueprint(sess, bp)
	if err != nil {
		logger.Error("Failed to open media stream", "err", err)
		http.Error(w, "Failed to open media stream: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var closeStreamOnce sync.Once
	closeStream := func(reason string) {
		closeStreamOnce.Do(func() {
			if stream != nil {
				logger.Debug("debug play closing stream", "session", sessionID, "reason", reason)
				stream.Close()
				stream = nil
			}
		})
	}
	defer closeStream("handler exit")
	go func(done <-chan struct{}) {
		<-done
		closeStream("playback canceled")
	}(mergedCtx.Done())

	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}

	s.sessionManager.StartPlayback(sessionID, clientIP)
	var endPlaybackOnce sync.Once
	endPlayback := func() { s.sessionManager.EndPlayback(sessionID, clientIP) }
	defer endPlaybackOnce.Do(endPlayback)
	go func() {
		<-r.Context().Done()
		endPlaybackOnce.Do(endPlayback)
	}()

	monitoredStream := &StreamMonitor{
		ReadSeekCloser: stream,
		sessionID:      sessionID,
		clientIP:       clientIP,
		manager:        s.sessionManager,
		lastUpdate:     time.Now(),
	}

	logger.Info("Serving debug media", "name", name, "size", size)
	logger.Debug("HTTP Request", "method", r.Method, "range", r.Header.Get("Range"), "user_agent", r.Header.Get("User-Agent"))

	bufW := newMediaResponseWriter(w, name)
	defer bufW.Flush()
	http.ServeContent(bufW, r, name, time.Time{}, monitoredStream)

	logger.Debug("Finished serving debug media")
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
func (s *Server) preProbeSlots(cacheKey string, slotPaths []string, maxAttempts int) {
	if maxAttempts <= 0 || len(slotPaths) == 0 {
		return
	}
	if maxAttempts > len(slotPaths) {
		maxAttempts = len(slotPaths)
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		defer s.registerPreProbeCancel(cacheKey, cancel)()

		for i := 0; i < maxAttempts; i++ {
			sess, err := s.sessionManager.GetSession(slotPaths[i])
			// Library-sourced releases were already validated when stored, so
			// re-probing buys nothing. They still consume an attempt, matching
			// the previous behaviour.
			if rel := sess.Release(); err != nil || (rel != nil && rel.IsLibraryResult()) {
				if err == nil {
					logger.Debug("Skipping speculative pre-probing for library candidate", "title", sess.ReportReleaseName())
				}
				continue
			}
			ok, probeErr := s.preProbeSessionSync(ctx, sess)
			if ok {
				logger.Info("Speculative failover pre-probing found ready stream candidate",
					"attempt", i+1, "max_attempts", maxAttempts, "slot", sess.ID, "title", sess.ReportReleaseName())
				return
			}
			logger.Debug("Speculative failover pre-probing candidate failed, trying next failover candidate",
				"attempt", i+1, "max_attempts", maxAttempts, "slot", sess.ID, "err", probeErr)
			if s.preProbeConfirmsBadRelease(probeErr) {
				s.reportBadReleaseOutcome(sess, probeErr, false)
			}
		}
	}()
}

func (s *Server) speculativelyPreProbeTopPlaylistCandidates(key StreamSlotKey, list *playlistResult, stream *auth.Stream) {
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

	s.preProbeSlots(key.CacheKey(), slotPaths, s.config.EffectiveSpeculativePreProbingMaxAttempts())
}

func (s *Server) speculativelyPreProbeTopFailoverOrder(order []string) {
	if len(order) == 0 {
		return
	}
	s.preProbeSlots(preProbeCacheKeyFromSlot(order[0]), order, s.config.EffectiveSpeculativePreProbingMaxAttempts())
}

// saveSessionToLibrary upserts the session's NZB + blueprint into the library.
// status follows the lifecycle: LibraryStatusPending as soon as the NZB and
// blueprint are known (so nothing is lost if validation/playback is interrupted),
// then LibraryStatusGood once validated or played through — a pending re-save
// never downgrades an existing good/bad verdict (enforced in StoreItem).
func (s *Server) saveSessionToLibrary(sess *session.Session, bp unpack.Blueprint, mediaFileName string, mediaFileSize int64, status string) {
	rel := sess.Release()
	if s == nil || s.attemptRecorder == nil || sess == nil || rel == nil {
		return
	}
	if s.config != nil && !s.config.EffectiveLibraryAutoSave() {
		return
	}
	libStore := s.attemptRecorder.LibraryStore()
	if libStore == nil {
		return
	}

	bpJSON := ""
	if data, ok := unpack.SerializeArchiveBlueprint(bp); ok {
		// Reusable serialized form (plaintext STORE-mode RAR) — rehydrated on replay.
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
		if s.config != nil {
			_ = libStore.EnforceQuota(s.config.EffectiveLibraryMaxItems(), s.config.EffectiveLibraryMaxSizeMB())
		}
	}
}
