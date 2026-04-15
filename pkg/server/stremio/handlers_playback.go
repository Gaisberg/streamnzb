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
	"sort"
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
	"streamnzb/pkg/media/loader"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
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
	}
	b.writeCalls++
	n, err = b.bw.Write(p)
	b.bytesWritten += int64(n)
	return n, err
}

func (b *bufferedResponseWriter) WriteHeader(statusCode int) {
	if !b.wroteHeader {
		b.statusCode = statusCode
		b.wroteHeader = true
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
	}
}

type StreamMonitor struct {
	io.ReadSeekCloser
	sessionID     string
	clientIP      string
	manager       *session.Manager
	onReadError   func(slotPath string, err error)
	onProgress    func()
	lastUpdate    time.Time
	mu            sync.Mutex
	readErrorOnce sync.Once
	bytesRead     atomic.Int64
	readCalls     atomic.Int64
	sawEOF        atomic.Bool
	lastReadErr   atomic.Value
}

func (s *StreamMonitor) Read(p []byte) (n int, err error) {
	s.readCalls.Add(1)
	n, err = s.ReadSeekCloser.Read(p)
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
		s.mu.Lock()
		if time.Since(s.lastUpdate) > 10*time.Second {
			s.manager.KeepAlive(s.sessionID, s.clientIP)
			s.lastUpdate = time.Now()
		}
		s.mu.Unlock()
	}
	return n, err
}

type streamMonitorSnapshot struct {
	BytesRead     int64
	ReadCalls     int64
	SawEOF        bool
	LastReadError string
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
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildPlaylist(ctx, key, isAIOStreams, stream)
	if err != nil {
		return nil, nil, err
	}
	if list == nil || len(list.Candidates) == 0 {
		return nil, nil, nil
	}
	list = s.applyExposedPlaylistOrder(list, key, stream)
	if list == nil || len(list.Candidates) == 0 {
		return nil, nil, nil
	}
	for _, slotPath := range list.SlotPaths {
		s.sessionManager.ClearSlotFailedDuringPlayback(slotPath)
	}
	// Create deferred sessions for each slot path we will expose, so handlePlay can serve without hitting indexers.
	s.ensureDeferredSessionsForPlaylist(list, key, stream)
	streamName := "StreamNZB"
	showAll := streamResultsMode(stream) == "display_all"
	return buildStreamsFromPlaylist(list, key, streamName, baseURL, showAll), list, nil
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

func forceDisconnect(w http.ResponseWriter, r *http.Request, baseURL string) {
	errorVideoURL := strings.TrimSuffix(baseURL, "/") + "/error/failure.mp4"
	logger.Info("Redirecting to error video", "url", errorVideoURL)

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

var ErrPlaybackStartupTimeout = errors.New("playback startup timeout")

func (s *Server) playbackStartupTimeout() time.Duration {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	if cfg == nil {
		return time.Duration(config.DefaultPlaybackStartupTimeoutSeconds) * time.Second
	}
	return cfg.EffectivePlaybackStartupTimeout()
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

func (k StreamSlotKey) RawCacheKey() string {
	return k.ContentType + ":" + k.ID
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

func (s *Server) allAvailIndexerHosts() []string {
	if len(s.availNZBIndexerHosts) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(s.availNZBIndexerHosts))
	hosts := make([]string, 0, len(s.availNZBIndexerHosts))
	for _, host := range s.availNZBIndexerHosts {
		host = strings.TrimSpace(host)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
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

func streamIndexerOverrides(stream *auth.Stream) map[string]config.IndexerSearchConfig {
	if stream == nil || len(stream.IndexerOverrides) == 0 {
		return nil
	}
	out := make(map[string]config.IndexerSearchConfig, len(stream.IndexerOverrides))
	for name, override := range stream.IndexerOverrides {
		out[name] = override
	}
	return out
}

func streamResultsMode(stream *auth.Stream) string {
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

func stableIndexerOverridesKey(overrides map[string]config.IndexerSearchConfig) string {
	if len(overrides) == 0 {
		return "none"
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		return "error"
	}
	return string(data)
}

func streamSearchQueryCacheKey(stream *auth.Stream, contentType string) string {
	names := streamSearchQueryNames(stream, contentType)
	queryComponent := "none"
	if streamCombinesResults(stream) && len(names) > 0 {
		sort.Strings(names)
	}
	if len(names) > 0 {
		queryComponent = strings.Join(names, ",")
	}
	return fmt.Sprintf(
		"stream=%s|queries=%s|providers=%s|selected_indexers=%s|overrides=%s|avail=%t|indexers=%s|combine=%t|failover=%t|results=%s",
		streamID(stream),
		queryComponent,
		strings.Join(streamProviderSelections(stream), ","),
		strings.Join(streamIndexerSelections(stream), ","),
		stableIndexerOverridesKey(streamIndexerOverrides(stream)),
		streamUsesAvailNZB(stream),
		streamIndexerMode(stream),
		streamCombinesResults(stream),
		streamFailoverEnabled(stream),
		streamResultsMode(stream),
	)
}

func hasResolvedIdentifiers(req indexer.SearchRequest) bool {
	return strings.TrimSpace(req.IMDbID) != "" || strings.TrimSpace(req.TMDBID) != "" || strings.TrimSpace(req.TVDBID) != ""
}

func hasPreparedTextQueries(req indexer.SearchRequest) bool {
	return strings.TrimSpace(req.Query) != "" || strings.TrimSpace(req.FilterQuery) != ""
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
		if _, err := s.sessionManager.CreateDeferredSessionWithFetcher(playPath, downloadURL, cand.Release, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream), s.segmentFetcherForStream(stream), s.providerHostsForStream(stream)); err != nil {
			logger.Debug("Create deferred session for play list failed", "slot", playPath, "err", err)
		}
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
	_, err := s.sessionManager.CreateDeferredSessionWithFetcher(sessionID, downloadURL, rel, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, list.Params.ContentTitle, streamID(stream), s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
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
	for e := err; e != nil; e = errors.Unwrap(e) {
		s := e.Error()
		if strings.Contains(s, "segment unavailable") || strings.Contains(s, "430") || strings.Contains(s, "no such article") {
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

func classifyPlaybackStartupErr(phase string, startupTimeout time.Duration, startupCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(startupCtx.Err(), context.DeadlineExceeded) {
		return playbackStartupTimeoutErr(startupTimeout, phase, err)
	}
	return err
}

func playbackStartupTimeoutErr(startupTimeout time.Duration, phase string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%w during %s after %s: %v", ErrPlaybackStartupTimeout, phase, startupTimeout, cause)
	}
	return fmt.Errorf("%w during %s after %s", ErrPlaybackStartupTimeout, phase, startupTimeout)
}

func isPlayPrepareCancellation(err error) bool {
	if err == nil || errors.Is(err, ErrPlaybackStartupTimeout) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isIndexerLimitErr(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "download limit reached") ||
		strings.Contains(value, "api limit reached") ||
		strings.Contains(value, "request limit reached")
}

type playbackSourceOpenResult struct {
	stream io.ReadSeekCloser
	err    error
}

// handlePlay: resolve session (by slot path or existing), optionally redirect if slot previously failed,
// then loop: try play → on error/probe/seek failure switch to next fallback → serve content.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/play/")
	requestedSessionID := sessionID
	logger.Debug("Play request", "session", sessionID)

	var (
		sess   *session.Session
		stream io.ReadSeekCloser
		name   string
		size   int64
	)

	var err error
	sess, err = s.sessionManager.GetSession(sessionID)
	if err != nil {
		// The session may have been deleted by a concurrent internal failover (e.g. exceeded failure threshold).
		// If the slot was marked as failed, redirect the client to the next working slot rather than 404.
		if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
			if nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig); nextID != "" && deriveErr == nil {
				nextURL := s.baseURLWithToken(streamConfig) + "/play/" + nextID
				logger.Info("Session deleted (slot failed during playback), redirecting to next", "from", sessionID, "to", nextID)
				w.Header().Set("Location", nextURL)
				w.WriteHeader(http.StatusFound)
				return
			}
			forceDisconnect(w, r, s.baseURL)
			return
		}
		// Never resolve or create sessions in the play handler; do not hit indexers here.
		// If the session was evicted (e.g. after pause), the client must get a new stream from the catalog.
		logger.Debug("Play: session not found", "slot", sessionID, "err", err)
		http.Error(w, "Session expired or not found", http.StatusNotFound)
		return
	}

	// If this slot failed during playback (430), redirect client to next fallback so retries get a working stream.
	if streamFailoverEnabled(streamConfig) && s.sessionManager.GetSlotFailedDuringPlayback(sessionID) {
		if nextID, deriveErr := s.deriveNextSlotID(r.Context(), sessionID, streamConfig); nextID != "" && deriveErr == nil {
			nextURL := s.baseURLWithToken(streamConfig) + "/play/" + nextID
			logger.Info("Redirecting to next fallback (slot failed during playback)", "from", sessionID, "to", nextID)
			w.Header().Set("Location", nextURL)
			w.WriteHeader(http.StatusFound)
			return
		}
		forceDisconnect(w, r, s.baseURL)
		return
	}

	tSec, hasTimeOffset := seek.ParseTSeconds(r.URL.Query().Get("t"))
	wantTimeOffset := r.Header.Get("Range") == "" && hasTimeOffset
	var startupInfo seek.StreamStartInfo
	var haveStartupInfo bool
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
			forceDisconnect(w, r, s.baseURL)
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

		preparedStream, prepareErr := s.preparePlaybackStream(mergedCtx, sess, requestedSessionID, sessionID, wantTimeOffset)
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
			availOutcome := availOutcomeForFailure(prepareErr)
			if shouldReportBadRelease(prepareErr) && s.availReporter != nil {
				availOutcome = s.availReporter.ReportBad(sess, prepareErr.Error())
			}
			s.applyReportedBadReleaseToCaches(sess, availOutcome)
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
			if !temporaryLimitErr && streamFailoverEnabled(streamConfig) {
				if nextSess, nextID, switchErr := s.switchToNextFallback(r.Context(), sess, streamConfig); nextID != "" && switchErr == nil {
					logger.Info("Playback failover advanced", "from", sessionID, "to", nextID, "err", prepareErr)
					sess, sessionID = nextSess, nextID
					continue
				}
			}
			logger.Info("No more fallback slots", "last", sessionID, "err", prepareErr)
			forceDisconnect(w, r, s.baseURL)
			return
		}

		stream = preparedStream.Stream
		name = preparedStream.Spec.Name
		size = preparedStream.Spec.Size
		startupInfo = preparedStream.StartupInfo
		haveStartupInfo = preparedStream.HasStartupInfo
		streamMode = preparedStream.Mode
		break
	}
	defer mergedCancel()
	servedSessionID := sessionID
	requestedRange := r.Header.Get("Range")
	requestedTimeOffset := r.URL.Query().Get("t")
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

	// After internal failover we serve a different file; don't apply the original request's Range or t= to it.
	failedOver := sessionID != requestedSessionID
	if failedOver {
		r.Header.Del("Range")
	}
	if !failedOver && wantTimeOffset && r.Header.Get("Range") == "" && haveStartupInfo {
		if byteOffset, seekOK := seek.TimeToByteOffsetFromDuration(size, startupInfo.DurationSec, tSec); seekOK {
			r.Header.Set("Range", "bytes="+strconv.FormatInt(byteOffset, 10)+"-")
		}
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
			if _, alreadySucceeded := s.recordedSuccessSessionIDs.Load(playbackSessionID); alreadySucceeded {
				return
			}
			errSess.ResetPlaybackStream()
			availOutcome := availOutcomeForFailure(readErr)
			if shouldReportBadRelease(readErr) && s.availReporter != nil {
				availOutcome = s.availReporter.ReportBad(errSess, readErr.Error())
			}
			s.applyReportedBadReleaseToCaches(errSess, availOutcome)
			if _, alreadyFailed := s.recordedFailureSessionIDs.LoadOrStore(playbackSessionID, struct{}{}); !alreadyFailed {
				s.recordFailureAttempt(errSess, readErr, availOutcome)
				go func(id string, done <-chan struct{}) {
					<-done
					s.recordedFailureSessionIDs.Delete(id)
				}(playbackSessionID, errSess.Done())
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
	serveStartedAt := time.Now()
	monitoredStream.onProgress = func() {
		s.commitGoodAttemptIfQualified(sess, sessionID, requestedSessionID, serveStartedAt)
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
		"time_offset", requestedTimeOffset,
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
			"time_offset", requestedTimeOffset,
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

type preparedPlaybackStream struct {
	Stream         io.ReadSeekCloser
	Spec           session.PlaybackStreamSpec
	StartupInfo    seek.StreamStartInfo
	HasStartupInfo bool
	Mode           string
}

// preparePlaybackStream probes with a temporary reader when startup metadata is missing,
// then opens a fresh playback reader for the current HTTP request while caching the
// validated playback spec/startup metadata on the session.
func (s *Server) preparePlaybackStream(ctx context.Context, sess *session.Session, requestedSessionID, currentSessionID string, wantTimeOffset bool) (preparedPlaybackStream, error) {
	preparedStream := preparedPlaybackStream{}
	needDurationProbe := wantTimeOffset && currentSessionID == requestedSessionID

	snapshot, haveSnapshot := sess.PlaybackStreamSnapshot()
	if haveSnapshot {
		preparedStream.Spec = snapshot.Spec
		preparedStream.StartupInfo = snapshot.StartupInfo
		preparedStream.HasStartupInfo = snapshot.HasStartupInfo
	}

	needProbe := !haveSnapshot || !snapshot.HasStartupInfo || (needDurationProbe && !snapshot.StartupInfo.DurationKnown)
	if needProbe {
		startupTimeout := s.playbackStartupTimeout()
		probeCtx, cancel := context.WithTimeout(ctx, startupTimeout)
		spec, startupInfo, err := s.probePlaybackSource(probeCtx, sess, needDurationProbe)
		err = classifyPlaybackStartupErr("probe", startupTimeout, probeCtx, err)
		cancel()
		if err != nil {
			return preparedPlaybackStream{}, err
		}
		preparedStream.Spec = spec
		preparedStream.StartupInfo = startupInfo
		preparedStream.HasStartupInfo = true
	}

	if preparedStream.Spec.Key == "" {
		return preparedPlaybackStream{}, fmt.Errorf("playback stream spec missing for session %s", sess.ID)
	}

	stream, err := s.openExpectedPlaybackSourceWithStartupTimeout(ctx, sess, preparedStream.Spec, s.playbackStartupTimeout())
	if err != nil {
		sess.ResetPlaybackStream()
		return preparedPlaybackStream{}, err
	}

	preparedStream.Stream = stream
	preparedStream.Mode = "per_request"
	sess.CachePlaybackStreamSnapshot(preparedStream.Spec, preparedStream.StartupInfo, preparedStream.HasStartupInfo)
	return preparedStream, nil
}

func (s *Server) openExpectedPlaybackSourceWithStartupTimeout(ctx context.Context, sess *session.Session, spec session.PlaybackStreamSpec, startupTimeout time.Duration) (io.ReadSeekCloser, error) {
	openCtx, cancel := context.WithCancel(ctx)
	resultCh := make(chan playbackSourceOpenResult, 1)
	done := make(chan struct{})
	go func() {
		stream, err := s.openExpectedPlaybackSource(openCtx, sess, spec)
		select {
		case resultCh <- playbackSourceOpenResult{stream: stream, err: err}:
		case <-done:
			if stream != nil {
				_ = stream.Close()
			}
		}
	}()

	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()

	cleanup := func() {
		close(done)
		cancel()
		select {
		case res := <-resultCh:
			if res.stream != nil {
				_ = res.stream.Close()
			}
		default:
		}
	}

	select {
	case res := <-resultCh:
		close(done)
		if res.err != nil {
			cancel()
			return nil, res.err
		}
		return res.stream, nil
	case <-timer.C:
		cleanup()
		return nil, playbackStartupTimeoutErr(startupTimeout, "open", nil)
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	}
}

func (s *Server) openExpectedPlaybackSource(ctx context.Context, sess *session.Session, spec session.PlaybackStreamSpec) (io.ReadSeekCloser, error) {
	bodyStream, bodyName, bodySize, err := s.openPlaybackSource(ctx, sess)
	if err != nil {
		return nil, err
	}
	if bodyName != spec.Name || bodySize != spec.Size {
		_ = bodyStream.Close()
		return nil, fmt.Errorf("playback stream changed during open: expected %q (%d), got %q (%d)", spec.Name, spec.Size, bodyName, bodySize)
	}
	return bodyStream, nil
}

// probePlaybackSource validates the selected media and gathers startup metadata using
// a disposable probe reader so that small scans never disturb the session body stream.
func (s *Server) probePlaybackSource(ctx context.Context, sess *session.Session, needDurationProbe bool) (session.PlaybackStreamSpec, seek.StreamStartInfo, error) {
	probeStream, probeName, probeSize, err := s.openPlaybackSource(ctx, sess)
	if err != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, err
	}
	defer probeStream.Close()

	inspectBytes := unpack.ProbeSize
	if needDurationProbe && seek.MaxBytesToRead > inspectBytes {
		inspectBytes = seek.MaxBytesToRead
	}

	startInfo, inspectErr := seek.InspectStreamStart(probeStream, probeSize, probeName, inspectBytes)
	if inspectErr != nil {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, fmt.Errorf("probe inspect: %w", inspectErr)
	}
	if !startInfo.HeaderValid {
		return session.PlaybackStreamSpec{}, seek.StreamStartInfo{}, fmt.Errorf("probe: invalid container header for %s", probeName)
	}

	return newPlaybackStreamSpec(sess.ID, probeName, probeSize), startInfo, nil
}

// newPlaybackStreamSpec creates the stable session/file key used to cache validated
// playback metadata and verify that fresh per-request readers still target the same file.
func newPlaybackStreamSpec(sessionID, name string, size int64) session.PlaybackStreamSpec {
	return session.PlaybackStreamSpec{
		Key:  fmt.Sprintf("%s|%s|%d", sessionID, name, size),
		Name: name,
		Size: size,
	}
}

func cacheReturnedPlaybackBlueprint(sess *session.Session, bp interface{}) {
	if sess == nil || bp == nil {
		return
	}
	sess.SetBlueprint(bp)
}

// openPlaybackSource opens a fresh reader for the currently selected playback source.
// Callers decide whether that reader is used as a disposable probe stream or as the
// request-local body stream for a single /play response.
func (s *Server) openPlaybackSource(ctx context.Context, sess *session.Session) (io.ReadSeekCloser, string, int64, error) {
	sessionID := sess.ID
	if _, err := sess.GetOrDownloadNZB(s.sessionManager); err != nil {
		logger.Error("Failed to lazy load NZB", "id", sessionID, "err", err)
		return nil, "", 0, err
	}

	files := sess.Files
	if len(files) == 0 {
		if sess.File != nil {
			files = []*loader.File{sess.File}
		} else {
			logger.Error("No files in session", "id", sessionID)
			if sess.NZB != nil {
				s.validator.InvalidateCache(sess.NZB.Hash())
			}
			return nil, "", 0, fmt.Errorf("no files in session %s", sessionID)
		}
	}

	// STAT check first segment before opening stream (nzbdav-style); fail fast on 430.
	if len(files) > 0 {
		exists, statErr := files[0].CheckFirstSegmentExists(ctx)
		if statErr != nil {
			logger.Debug("Stat first segment failed", "id", sessionID, "err", statErr)
			return nil, "", 0, statErr
		}
		if !exists {
			return nil, "", 0, fmt.Errorf("segment unavailable: first segment not found (430)")
		}
	}

	// Skip IsFailed() when the session is actively serving OR has already validated playback.
	// Stremio often cancels the initial probe request (dropping ActivePlays back to 0) immediately
	// before sending a follow-up range request. During that brief gap IsActivelyServing() is false,
	// but HasPreviouslyServed() tells us the file was already validated.
	// If the file is truly bad during streaming, onReadError will catch it.
	if !sess.IsActivelyServing() && !sess.HasPreviouslyServed() {
		for _, f := range files {
			if f.IsFailed() {
				logger.Error("Session file has too many failures", "session", sessionID, "file", f.Name())
				if sess.NZB != nil {
					s.validator.InvalidateCache(sess.NZB.Hash())
				}
				return nil, "", 0, fmt.Errorf("file %s exceeded failure threshold", f.Name())
			}
		}
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
		logger.Error("Failed to open media stream", "id", sessionID, "err", err)
		if sess.NZB != nil {
			s.validator.InvalidateCache(sess.NZB.Hash())
		}
		return nil, "", 0, err
	}
	sess.SetSelectedPlaybackFile(name)
	return stream, name, size, nil
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
		if _, err := sess.GetOrDownloadNZB(s.sessionManager); err != nil {
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

func (s *Server) applyReportedBadReleaseToCaches(sess *session.Session, outcome availnzb.ReportOutcome) {
	if s == nil || sess == nil || outcome.Status != "sent" {
		return
	}
	detailsURL := strings.TrimSpace(sess.ReleaseURL())
	if detailsURL == "" {
		return
	}
	streamID, contentType, id, _, ok := parseStreamSlotID(sess.ID)
	if !ok {
		return
	}
	s.markCachedReleaseUnavailable(StreamSlotKey{
		StreamID:    streamID,
		ContentType: contentType,
		ID:          id,
	}, detailsURL, sess.ID)
}

func shouldReportBadRelease(streamErr error) bool {
	errMsg := streamErr.Error()
	if !strings.Contains(errMsg, "compressed") && !strings.Contains(errMsg, "encrypted") &&
		!strings.Contains(errMsg, "EOF") && !errors.Is(streamErr, unpack.ErrTooManyZeroFills) &&
		!errors.Is(streamErr, unpack.ErrEpisodeTargetNotFound) {
		return false
	}
	return true
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

// recordAttemptParams builds persistence params from a session.
func (s *Server) recordAttemptParams(sess *session.Session) persistence.RecordAttemptParams {
	return s.recordAttemptParamsForOutcome(sess, false)
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
		SlotPath:     sess.ID,
	}
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

func (s *Server) recordAttemptParamsForFailure(sess *session.Session) persistence.RecordAttemptParams {
	params := s.recordAttemptParamsForOutcome(sess, false)
	return params
}

// recordPreloadAttempt inserts a preload row when we are about to try playing a slot (result not yet known).
func (s *Server) recordPreloadAttempt(sess *session.Session) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParamsForOutcome(sess, false)
	s.attemptRecorder.RecordPreloadAttempt(p)
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
}

// recordAttempt writes one NZB attempt to the persistence layer when attemptRecorder is set (or updates existing preload row).
func (s *Server) recordAttempt(sess *session.Session, success bool, failureReason string, availOutcome availnzb.ReportOutcome) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParamsForOutcome(sess, success)
	p.Success = success
	p.FailureReason = normalizeAttemptReason(failureReason)
	p.AvailStatus = availOutcome.Status
	p.AvailReason = availOutcome.Reason
	s.attemptRecorder.RecordAttempt(p)
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
}

func (s *Server) recordFailureAttempt(sess *session.Session, streamErr error, availOutcome availnzb.ReportOutcome) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParamsForFailure(sess)
	p.Success = false
	p.FailureReason = normalizeAttemptReason(streamErr.Error())
	p.AvailStatus = availOutcome.Status
	p.AvailReason = availOutcome.Reason
	s.attemptRecorder.RecordAttempt(p)
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
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
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
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
	if streamConfig == nil || streamConfig.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
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

	nzbParsed, err := nzb.Parse(bytes.NewReader(nzbData))
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

	files := sess.Files
	if len(files) == 0 {
		http.Error(w, "No files in NZB", http.StatusInternalServerError)
		return
	}
	unpackFiles := make([]unpack.UnpackableFile, len(files))
	for i := range files {
		unpackFiles[i] = files[i]
	}
	password := ""
	if sess.NZB != nil {
		password = sess.NZB.Password()
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
		target = unpack.EpisodeTarget{Season: sess.ContentIDs.Season, Episode: sess.ContentIDs.Episode}
	}
	stream, name, size, bp, err := unpack.GetMediaStreamForEpisode(mergedCtx, unpackFiles, sess.Blueprint, password, target)
	cacheReturnedPlaybackBlueprint(sess, bp)
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

	if tStr := r.URL.Query().Get("t"); tStr != "" && r.Header.Get("Range") == "" {
		if tSec, parseOK := seek.ParseTSeconds(tStr); parseOK {
			if startInfo, err := seek.InspectStreamStart(stream, size, name, seek.MaxBytesToRead); err == nil {
				if byteOffset, seekOK := seek.TimeToByteOffsetFromDuration(size, startInfo.DurationSec, tSec); seekOK {
					r.Header.Set("Range", "bytes="+strconv.FormatInt(byteOffset, 10)+"-")
				}
			}
		}
	}

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
