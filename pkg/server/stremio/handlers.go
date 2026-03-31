package stremio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
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
	"streamnzb/pkg/release"
	"streamnzb/pkg/search"
	"streamnzb/pkg/search/parser"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/services/metadata/tmdb"
	"streamnzb/pkg/services/metadata/tvdb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/validation"
)

var (
	availTrue  = true
	availFalse = false
)

type Server struct {
	mu                        sync.RWMutex
	manifest                  *Manifest
	version                   string
	baseURL                   string
	config                    *config.Config
	indexer                   indexer.Indexer
	validator                 *validation.Checker
	sessionManager            *session.Manager
	triageService             *triage.Service
	availClient               *availnzb.Client
	availReporter             *availnzb.Reporter
	availNZBIndexerHosts      []string
	tmdbClient                *tmdb.Client
	tvdbClient                *tvdb.Client
	streamManager             *auth.StreamManager
	playListCache             sync.Map
	rawSearchCache            sync.Map
	recordedSuccessSessionIDs sync.Map // session ID -> struct{}; record actual playback success only once per stream
	recordedPreloadSessionIDs sync.Map // session ID -> struct{}; record preload only once per session lifetime
	recordedFailureSessionIDs sync.Map // session ID -> struct{}; record failure only once per session lifetime (prevents concurrent goroutines from inserting duplicate rows)
	nextReleaseIndex          sync.Map // key: streamToken|key.CacheKey() → *nextReleaseCursor; tracks manual "next" progression
	webHandler                http.Handler
	apiHandler                http.Handler
	attemptRecorder           *persistence.StateManager
	onAttemptRecorded         func()
}

const FailoverOrderPath = "/failover_order"

type ServerOptions struct {
	Config               *config.Config
	BaseURL              string
	Port                 int
	Indexer              indexer.Indexer
	Validator            *validation.Checker
	SessionManager       *session.Manager
	TriageService        *triage.Service
	AvailClient          *availnzb.Client
	AvailNZBIndexerHosts []string
	TMDBClient           *tmdb.Client
	TVDBClient           *tvdb.Client
	StreamManager        *auth.StreamManager
	Version              string
	AttemptRecorder      *persistence.StateManager
}

func NewServer(opts *ServerOptions) (*Server, error) {
	if opts == nil {
		return nil, fmt.Errorf("ServerOptions is required")
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	availNZBMode := ""
	if opts.Config != nil {
		availNZBMode = opts.Config.AvailNZBMode
	}
	var resolvedAvailClient *availnzb.Client
	if availNZBMode != "disabled" {
		resolvedAvailClient = opts.AvailClient
	}
	var availReporter *availnzb.Reporter
	if resolvedAvailClient != nil {
		availReporter = availnzb.NewReporter(resolvedAvailClient, opts.Validator)
		if availNZBMode == "status_only" {
			availReporter.Disabled = true
		}
	}
	s := &Server{
		manifest:             NewManifest(version),
		version:              version,
		baseURL:              opts.BaseURL,
		config:               opts.Config,
		indexer:              opts.Indexer,
		validator:            opts.Validator,
		sessionManager:       opts.SessionManager,
		triageService:        opts.TriageService,
		availClient:          resolvedAvailClient,
		availReporter:        availReporter,
		availNZBIndexerHosts: opts.AvailNZBIndexerHosts,
		tmdbClient:           opts.TMDBClient,
		tvdbClient:           opts.TVDBClient,
		streamManager:        opts.StreamManager,
		attemptRecorder:      opts.AttemptRecorder,
	}

	if err := s.CheckPort(opts.Port); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Server) CheckPort(port int) error {
	address := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("addon port %d listen check failed: %w", port, err)
	}
	ln.Close()
	return nil
}

func (s *Server) SetWebHandler(h http.Handler) {
	s.webHandler = h
}

func (s *Server) SetAPIHandler(h http.Handler) {
	s.apiHandler = h
}

func (s *Server) ClearSearchCaches() {
	s.playListCache.Range(func(key, _ interface{}) bool {
		s.playListCache.Delete(key)
		return true
	})
	s.rawSearchCache.Range(func(key, _ interface{}) bool {
		s.rawSearchCache.Delete(key)
		return true
	})
	s.nextReleaseIndex.Range(func(key, _ interface{}) bool {
		s.nextReleaseIndex.Delete(key)
		return true
	})
	logger.Info("Search caches cleared")
}

// SetOnAttemptRecorded sets a callback invoked after each NZB attempt is recorded (e.g. to broadcast to WS clients).
func (s *Server) SetOnAttemptRecorded(f func()) {
	s.onAttemptRecorded = f
}

func (s *Server) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.version != "" {
		return s.version
	}
	return "dev"
}

func (s *Server) SetupRoutes(mux *http.ServeMux) {

	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		streamManager := s.streamManager
		webHandler := s.webHandler
		apiHandler := s.apiHandler
		s.mu.RUnlock()

		path := r.URL.Path
		var authenticatedStream *auth.Stream

		if path == "/error/failure.mp4" && webHandler != nil {
			webHandler.ServeHTTP(w, r)
			return
		}

		isStremioRoute := path == "/manifest.json" || path == FailoverOrderPath || strings.HasPrefix(path, "/stream/") || strings.HasPrefix(path, "/play/") || strings.HasPrefix(path, "/next/") || strings.HasPrefix(path, "/debug/play")

		trimmedPath := strings.TrimPrefix(path, "/")
		parts := strings.SplitN(trimmedPath, "/", 2)

		if len(parts) >= 1 && parts[0] != "" {
			token := parts[0]

			if streamManager != nil {
				stream, err := streamManager.AuthenticateToken(token, s.config.GetAdminUsername(), s.config.AdminToken)
				if err == nil && stream != nil {
					authenticatedStream = stream

					if len(parts) > 1 {
						path = "/" + parts[1]
					} else {
						path = "/"
					}
					r.URL.Path = path

					r = r.WithContext(auth.ContextWithStream(r.Context(), stream))

				} else if isStremioRoute {

					logger.Error("Unauthorized request - invalid stream token", "path", path, "remote", r.RemoteAddr)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}

			}
		} else if isStremioRoute {

			logger.Error("Unauthorized request - Stremio route requires stream token", "path", path, "remote", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if path == "/manifest.json" {
			s.handleManifest(w, r)
		} else if strings.HasPrefix(path, "/stream/") {
			s.handleStream(w, r, authenticatedStream)
		} else if strings.HasPrefix(path, "/play/") {
			s.handlePlay(w, r, authenticatedStream)
		} else if strings.HasPrefix(path, "/next/") {
			s.handleNextRelease(w, r, authenticatedStream)
		} else if path == FailoverOrderPath {
			s.handleFailoverOrder(w, r, authenticatedStream)
		} else if strings.HasPrefix(path, "/debug/play") {
			s.handleDebugPlay(w, r, authenticatedStream)
		} else if path == "/health" {
			s.handleHealth(w, r)
		} else if strings.HasPrefix(path, "/api/") {
			if apiHandler != nil {

				apiHandler.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
		} else {
			if webHandler != nil {
				webHandler.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
		}
	})

	mux.Handle("/", finalHandler)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	logger.Debug("Manifest request", "remote", r.RemoteAddr)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	s.mu.RLock()
	manifest := s.manifest
	s.mu.RUnlock()

	stream, _ := auth.StreamFromContext(r)
	isAdmin := stream != nil && stream.Username == s.config.GetAdminUsername()

	data, err := manifest.ToJSONForDevice(isAdmin)
	if err != nil {
		http.Error(w, "Failed to generate manifest", http.StatusInternalServerError)
		return
	}

	w.Write(data)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, stream *auth.Stream) {

	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	path = strings.TrimSuffix(path, ".json")

	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid stream URL", http.StatusBadRequest)
		return
	}

	contentType := parts[0]
	id := parts[1]

	logger.Info("Client request", "stream", func() string {
		if stream != nil {
			return stream.Username
		}
		return "legacy"
	}(), "type", contentType, "id", id)

	const streamRequestTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), streamRequestTimeout)
	defer cancel()

	logger.Trace("stream request start", "type", contentType, "id", id)
	baseURL := s.baseURLWithToken(stream)
	key := StreamSlotKey{StreamID: streamID(stream), ContentType: contentType, ID: id}
	streams, list, err := s.buildStreamsForKey(ctx, key, stream, baseURL)
	if err != nil {
		logger.Error("Error building play list", "err", err)
	}
	if streams == nil {
		streams = []Stream{}
	}
	logger.Debug("Stream finished",
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
		"indexer_mode", streamIndexerMode(stream),
		"search_requests_mode", func() string {
			if streamCombinesResults(stream) {
				return "combine"
			}
			return "first_hit"
		}(),
		"results_mode", streamResultsMode(stream),
		"candidate_results", func() int {
			if list != nil {
				return len(list.Candidates)
			}
			return 0
		}(),
		"final_results", len(streams),
	)

	response := StreamResponse{
		Streams: streams,
	}

	responseJSON, _ := json.MarshalIndent(response, "", "  ")
	logger.Trace("Sending stream response", "json", string(responseJSON))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	json.NewEncoder(w).Encode(response)
}

type failoverOrderRequest struct {
	Streams []struct {
		FailoverID string `json:"failoverId"`
	} `json:"streams"`
}

func (s *Server) handleFailoverOrder(w http.ResponseWriter, r *http.Request, stream *auth.Stream) {
	logger.Debug("Failover order request", "stream", stream.Username, "method", r.Method, "url", r.URL.Path)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	var req failoverOrderRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.Streams) == 0 {
		logger.Debug("Failover order: empty streams array, not storing")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var order []string
	for _, entry := range req.Streams {
		raw := strings.TrimSpace(entry.FailoverID)
		if raw == "" {
			continue
		}
		slotPath := raw
		if after, ok := strings.CutPrefix(raw, "streamnzb-"); ok {
			slotPath = after
		}
		// Only accept slot paths (stream:streamId:type:id:index); no legacy stream-ID-only entries.
		if !strings.HasPrefix(slotPath, streamSlotPrefix) {
			continue
		}
		if _, _, _, _, ok := parseStreamSlotID(slotPath); !ok {
			continue
		}
		order = append(order, slotPath)
	}
	if len(order) == 0 {
		var firstNonEmptyRaw string
		nonEmptyCount := 0
		for _, e := range req.Streams {
			trimmed := strings.TrimSpace(e.FailoverID)
			if trimmed != "" {
				nonEmptyCount++
				if firstNonEmptyRaw == "" {
					firstNonEmptyRaw = trimmed
					if len(firstNonEmptyRaw) > 80 {
						firstNonEmptyRaw = firstNonEmptyRaw[:80] + "..."
					}
				}
			}
		}
		logger.Info("Failover order: no entries stored (all skipped)", "stream", streamToken(stream), "requested", len(req.Streams), "nonEmptyFailoverIds", nonEmptyCount, "firstFailoverIdSample", firstNonEmptyRaw)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	streamKey := ""
	isAIOStreams := false
	for _, entry := range order {
		if strings.HasPrefix(entry, streamSlotPrefix) {
			if sid, contentType, id, _, ok := parseStreamSlotID(entry); ok {
				sk := StreamSlotKey{StreamID: sid, ContentType: contentType, ID: id}
				if sk.StreamID == "" {
					sk.StreamID = defaultStreamID
				}
				streamKey = sk.CacheKey()
				isAIOStreams = streamUsesAIOStreamsProfile(stream)
				break
			}
		}
	}
	if !isAIOStreams {
		logger.Debug("Failover order ignored for non-AIO stream profile", "stream", streamToken(stream), "streamKey", streamKey)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token := streamToken(stream)
	s.sessionManager.SetDeviceFailoverOrder(token, streamKey, order)
	sample := ""
	if len(order) > 0 {
		sample = order[0]
		if len(order) > 1 {
			sample += " ... " + order[len(order)-1]
		}
	}
	logger.Info("Failover order stored", "device", token, "streamKey", streamKey, "slots", len(order), "sample", sample)
	w.WriteHeader(http.StatusNoContent)
}

func bingeGroupLabelFromMeta(meta *parser.ParsedRelease) string {
	if meta == nil {
		return ""
	}
	if g := meta.ResolutionGroup(); g != "" && g != "sd" {
		return g
	}
	if meta.Resolution != "" {
		return meta.Resolution
	}
	return meta.Quality
}

func streamBehaviorHints(streamName, streamID string, rel *release.Release, cached *bool, bingeGroupLabel string) *BehaviorHints {
	bingeGroup := "streamnzb-" + streamID
	if bingeGroupLabel != "" {
		bingeGroup = "streamnzb-" + bingeGroupLabel
	}
	h := &BehaviorHints{
		NotWebReady: true,
		BingeGroup:  bingeGroup,
	}
	if rel != nil {
		h.Filename = rel.Title
		if rel.Size > 0 {
			h.VideoSize = rel.Size
		}
	}
	if cached != nil {
		h.Cached = cached
	}
	return h
}

func buildStreamsFromPlayList(list *orderedPlayListResult, key StreamSlotKey, streamName, baseURL string, showAll bool) []Stream {
	nameLeft := streamName
	if nameLeft == "" {
		nameLeft = key.StreamID
	}
	useSlotPaths := len(list.SlotPaths) == len(list.Candidates)
	var streams []Stream
	if showAll {
		for i, cand := range list.Candidates {
			relTitle := ""
			if cand.Release != nil && cand.Release.Title != "" {
				relTitle = cand.Release.Title
			} else {
				relTitle = fmt.Sprintf("Release %d", i+1)
			}
			isAvail := list.CachedAvailable != nil && cand.Release != nil && cand.Release.DetailsURL != "" && list.CachedAvailable[cand.Release.DetailsURL]
			sName := nameLeft
			if isAvail {
				sName = "⚡ " + nameLeft
			}
			desc := "StreamNZB\n" + relTitle
			playPath := key.SlotPath(i)
			if useSlotPaths {
				playPath = list.SlotPaths[i]
			}
			streamURL := baseURL + "/play/" + playPath
			failoverId := "streamnzb-" + playPath
			bingeLabel := bingeGroupLabelFromMeta(cand.Metadata)
			if bingeLabel == "" {
				bingeLabel = nameLeft
			}
			hints := streamBehaviorHints(nameLeft, key.StreamID, cand.Release, &isAvail, bingeLabel)
			streams = append(streams, Stream{
				FailoverID:    failoverId,
				Name:          sName,
				URL:           streamURL,
				Description:   desc,
				BehaviorHints: hints,
			})
		}
	} else {
		branding := "StreamNZB"
		if list.FirstIsAvailGood {
			branding = "StreamNZB [availNZB]"
		}
		var line2 string
		var firstRel *release.Release
		var firstMeta *parser.ParsedRelease
		if len(list.Candidates) > 0 {
			if list.Candidates[0].Release != nil {
				firstRel = list.Candidates[0].Release
				if list.FirstIsAvailGood && firstRel.Title != "" {
					line2 = firstRel.Title
				} else {
					line2 = fmt.Sprintf("%d possible releases", len(list.Candidates))
				}
			} else {
				line2 = fmt.Sprintf("%d possible releases", len(list.Candidates))
			}
			firstMeta = list.Candidates[0].Metadata
		}
		description := branding
		if line2 != "" {
			description = branding + "\n" + line2
		}
		playPath := key.SlotPath(0)
		if useSlotPaths {
			playPath = list.SlotPaths[0]
		}
		streamURL := baseURL + "/play/" + playPath
		firstAvail := list.FirstIsAvailGood
		failoverId := "streamnzb-" + playPath
		bingeLabel := bingeGroupLabelFromMeta(firstMeta)
		if bingeLabel == "" {
			bingeLabel = nameLeft
		}
		hints := streamBehaviorHints(nameLeft, key.StreamID, firstRel, &firstAvail, bingeLabel)
		streams = append(streams, Stream{
			FailoverID:    failoverId,
			Name:          nameLeft,
			URL:           streamURL,
			Description:   description,
			BehaviorHints: hints,
		})
		if len(list.Candidates) >= 2 {
			nextPath := playPath
			nextURL := baseURL + "/next/" + nextPath
			nextName := nameLeft + " (next release)"
			nextDesc := "StreamNZB\nTry next release in list"
			nextFailoverId := "streamnzb-" + nextPath
			nextHints := streamBehaviorHints(nameLeft, key.StreamID, nil, nil, nameLeft)
			streams = append(streams, Stream{
				FailoverID:    nextFailoverId,
				Name:          nextName,
				URL:           nextURL,
				Description:   nextDesc,
				BehaviorHints: nextHints,
			})
		}
	}
	return streams
}

// buildStreamsForKey runs the shared pipeline: build play list → optional AIO filter → device order → clear next bound → build Stream[].
// Returns (nil, nil, nil) when there are no candidates; (nil, nil, err) on error.
func (s *Server) buildStreamsForKey(ctx context.Context, key StreamSlotKey, stream *auth.Stream, baseURL string) ([]Stream, *orderedPlayListResult, error) {
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildOrderedPlayList(ctx, key, isAIOStreams, stream)
	if err != nil {
		return nil, nil, err
	}
	if list == nil || len(list.Candidates) == 0 {
		return nil, nil, nil
	}
	if isAIOStreams {
		list = filterPlayListToAvailableForAIOStreams(list)
		if list == nil || len(list.Candidates) == 0 {
			return nil, nil, nil
		}
	}
	if isAIOStreams {
		if order := s.sessionManager.GetDeviceFailoverOrder(streamToken(stream), key.CacheKey()); len(order) > 0 {
			list = filterPlayListByOrder(list, key, order)
		}
	}
	for _, slotPath := range list.SlotPaths {
		s.sessionManager.ClearSlotFailedDuringPlayback(slotPath)
	}
	// Create deferred sessions for each slot path we will expose, so handlePlay can serve without hitting indexers.
	s.ensureDeferredSessionsForPlayList(list, key, stream)
	streamName := "StreamNZB"
	showAll := streamResultsMode(stream) == "display_all"
	return buildStreamsFromPlayList(list, key, streamName, baseURL, showAll), list, nil
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

const (
	// playbackStartupTimeout bounds probe/open work before the first playable response is ready.
	// Slow archive-heavy startup should fail over rather than keep the player spinning indefinitely.
	playbackStartupTimeout = 5 * time.Second
)

var ErrPlaybackStartupTimeout = errors.New("playback startup timeout")

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
	if len(names) > 0 {
		if streamCombinesResults(stream) {
			sort.Strings(names)
		}
		queryComponent = strings.Join(names, ",")
	}
	if len(names) == 0 {
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

	if req.PerIndexerQuery == nil {
		return
	}
	for name := range req.PerIndexerQuery {
		if _, ok := selected[name]; !ok {
			delete(req.PerIndexerQuery, name)
		}
	}
}

func formatStreamSlotPath(streamID, contentType, id string, index int) string {
	return streamSlotPrefix + streamID + ":" + contentType + ":" + id + ":" + strconv.Itoa(index)
}

type orderedPlayListResult struct {
	Candidates       []triage.Candidate
	FirstIsAvailGood bool
	Params           *SearchParams

	CachedAvailable map[string]bool

	// UnavailableDetailsURLs is the set of release DetailsURLs known to be unavailable (AvailNZB false).
	// For AIOStreams we filter these out so we only return unknown or available (true).
	UnavailableDetailsURLs map[string]bool

	// SlotPaths, when set, gives the exact play path for each candidate (e.g. from failover order).
	// Must match len(Candidates); buildStreamsFromPlayList uses SlotPaths[i] instead of key.SlotPath(i).
	SlotPaths []string
}

type rawSearchResult struct {
	Params          *SearchParams
	AvailReleases   []*release.Release
	IndexerReleases []*release.Release
	CachedAvailable map[string]bool
	AvailResult     *availnzb.ReleasesResult
}

const playListCacheTTL = 10 * time.Minute

type playListCacheEntry struct {
	result *orderedPlayListResult
	until  time.Time
}

type rawSearchCacheEntry struct {
	raw   *rawSearchResult
	until time.Time
}

// filterPlayListByOrder keeps only candidates whose slot path appears in order (same key, valid index), in that order.
// SlotPaths on the result are set from order so stream URLs match the client. Non-slot-path entries are ignored.
func filterPlayListByOrder(list *orderedPlayListResult, key StreamSlotKey, order []string) *orderedPlayListResult {
	if list == nil || len(order) == 0 {
		return list
	}
	maxIndex := len(list.Candidates) - 1
	var filtered []triage.Candidate
	var paths []string
	for _, entry := range order {
		if !strings.HasPrefix(entry, streamSlotPrefix) {
			continue
		}
		sid, ct, id, idx, ok := parseStreamSlotID(entry)
		if !ok || idx < 0 || idx > maxIndex {
			continue
		}
		if ct != key.ContentType || id != key.ID {
			continue
		}
		if sid != "" && sid != key.StreamID {
			continue
		}
		filtered = append(filtered, list.Candidates[idx])
		paths = append(paths, entry)
	}
	if len(filtered) == 0 {
		return list
	}
	firstAvail := false
	if list.CachedAvailable != nil && filtered[0].Release != nil && filtered[0].Release.DetailsURL != "" {
		firstAvail = list.CachedAvailable[filtered[0].Release.DetailsURL]
	}
	return &orderedPlayListResult{
		Candidates:             filtered,
		FirstIsAvailGood:       firstAvail,
		Params:                 list.Params,
		CachedAvailable:        list.CachedAvailable,
		UnavailableDetailsURLs: list.UnavailableDetailsURLs,
		SlotPaths:              paths,
	}
}

// filterPlayListToAvailableForAIOStreams keeps only candidates that are unknown or available (true).
// Removes only those explicitly marked unavailable (AvailNZB false). Used when returning streams to AIOStreams.
func filterPlayListToAvailableForAIOStreams(list *orderedPlayListResult) *orderedPlayListResult {
	if list == nil || list.UnavailableDetailsURLs == nil || len(list.UnavailableDetailsURLs) == 0 {
		return list
	}
	var filtered []triage.Candidate
	for _, c := range list.Candidates {
		if c.Release == nil || c.Release.DetailsURL == "" {
			filtered = append(filtered, c)
			continue
		}
		if list.UnavailableDetailsURLs[c.Release.DetailsURL] {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == len(list.Candidates) {
		return list
	}
	firstAvail := false
	if len(filtered) > 0 && list.CachedAvailable != nil && filtered[0].Release != nil && filtered[0].Release.DetailsURL != "" {
		firstAvail = list.CachedAvailable[filtered[0].Release.DetailsURL]
	}
	return &orderedPlayListResult{
		Candidates:             filtered,
		FirstIsAvailGood:       firstAvail,
		Params:                 list.Params,
		CachedAvailable:        list.CachedAvailable,
		UnavailableDetailsURLs: list.UnavailableDetailsURLs,
		SlotPaths:              nil,
	}
}

// buildOrderedPlayList returns an ordered list of candidates for (stream, type, id).
// Raw search is cached by (contentType, id); play list is cached by (key, isAIOStreams).
// When isAIOStreams is true we skip triage/sort and do title dedup only (for AIOStreams client).
func (s *Server) buildOrderedPlayList(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*orderedPlayListResult, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	cacheKey := key.CacheKey()
	if isAIOStreams {
		cacheKey += "|noFilter"
	}
	cacheKey += "|queries=" + streamSearchQueryCacheKey(stream, key.ContentType)
	if v, ok := s.playListCache.Load(cacheKey); ok {
		if ent, _ := v.(*playListCacheEntry); ent != nil && time.Now().Before(ent.until) {
			logger.Debug("Play list cache hit", "key", cacheKey)
			return ent.result, nil
		}
	}
	list, err := s.buildOrderedPlayListUncached(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return list, err
	}
	s.playListCache.Store(cacheKey, &playListCacheEntry{result: list, until: time.Now().Add(playListCacheTTL)})
	return list, nil
}

func (s *Server) buildOrderedPlayListUncached(ctx context.Context, key StreamSlotKey, isAIOStreams bool, stream *auth.Stream) (*orderedPlayListResult, error) {
	raw, err := s.getOrBuildRawSearchResult(ctx, key.ContentType, key.ID, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	return s.buildOrderedPlayListFromRaw(raw, isAIOStreams, stream)
}

func (s *Server) getOrBuildRawSearchResult(ctx context.Context, contentType, id string, stream *auth.Stream) (*rawSearchResult, error) {
	rawKey := contentType + ":" + id + "|queries=" + streamSearchQueryCacheKey(stream, contentType)
	if v, ok := s.rawSearchCache.Load(rawKey); ok {
		if ent, _ := v.(*rawSearchCacheEntry); ent != nil && time.Now().Before(ent.until) {
			logger.Debug("Raw search cache hit", "key", rawKey)
			return ent.raw, nil
		}
	}
	raw, err := s.buildRawSearchResult(ctx, contentType, id, stream)
	if err != nil || raw == nil {
		return nil, err
	}
	s.rawSearchCache.Store(rawKey, &rawSearchCacheEntry{raw: raw, until: time.Now().Add(playListCacheTTL)})
	return raw, nil
}

func (s *Server) buildRawSearchResult(ctx context.Context, contentType, id string, stream *auth.Stream) (*rawSearchResult, error) {
	selectedQueries := streamSearchQueryNames(stream, contentType)
	if len(selectedQueries) == 0 {
		return nil, fmt.Errorf("stream is missing at least one %s search request", contentType)
	}

	params, err := s.buildSearchParamsBase(contentType, id, nil)
	if err != nil {
		return nil, err
	}
	logger.Debug("Stream metadata",
		"stream", func() string {
			if stream != nil {
				return stream.Username
			}
			return "legacy"
		}(),
		"type", contentType,
		"id", id,
		"imdb_id", params.ContentIDs.ImdbID,
		"tmdb_id", params.Req.TMDBID,
		"tvdb_id", params.ContentIDs.TvdbID,
		"season", params.ContentIDs.Season,
		"episode", params.ContentIDs.Episode,
		"title", metadataDisplayTitle(params.Metadata, contentType),
		"year", metadataDisplayYear(params.Metadata, contentType),
		"languages", metadataLanguageCount(params.Metadata, contentType),
	)
	contentIDs := params.ContentIDs
	imdbForText := params.ImdbForText
	tmdbForText := params.TmdbForText
	var tmdbResolver search.TMDBResolver
	if s.tmdbClient != nil {
		tmdbResolver = s.tmdbClient
	}

	var availReleases []*release.Release
	cachedAvailable := make(map[string]bool)
	var availResult *availnzb.ReleasesResult
	if streamUsesAvailNZB(stream) && s.availClient != nil && s.availClient.BaseURL != "" && (contentIDs.ImdbID != "" || contentIDs.TvdbID != "") {
		availResult, _ = s.availClient.GetReleases(contentIDs.ImdbID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, params.AvailIndexers, s.providerHostsForStream(stream))
	}

	indexerReleases := make([]*release.Release, 0)
	streamLabel := func() string {
		if stream != nil {
			return stream.Username
		}
		return "legacy"
	}()
	logger.Debug("Stream configuration",
		"stream", streamLabel,
		"type", contentType,
		"filter_sorting", func() string {
			if stream == nil || strings.TrimSpace(stream.FilterSortingMode) == "" {
				return "none"
			}
			return strings.ToLower(strings.TrimSpace(stream.FilterSortingMode))
		}(),
		"indexer_mode", streamIndexerMode(stream),
		"search_requests_mode", func() string {
			if streamCombinesResults(stream) {
				return "combine"
			}
			return "first_hit"
		}(),
		"results_mode", streamResultsMode(stream),
		"failover", streamFailoverEnabled(stream),
		"availnzb", streamUsesAvailNZB(stream),
		"providers", append([]string(nil), stream.ProviderSelections...),
		"indexers", append([]string(nil), stream.IndexerSelections...),
		"requests", append([]string(nil), selectedQueries...),
	)
	for _, name := range selectedQueries {
		searchQuery := s.config.GetSearchQueryByName(contentType, name)
		if searchQuery == nil {
			logger.Debug("Stream search query missing", "stream", func() string {
				if stream != nil {
					return stream.Username
				}
				return "legacy"
			}(), "content_type", contentType, "id", id, "query", name)
			continue
		}
		params.Req.StreamLabel = streamLabel
		params.Req.RequestLabel = searchQuery.Name
		profileParams, profileErr := s.buildSearchParamsFromBase(params, searchQuery)
		if profileErr != nil {
			return nil, profileErr
		}
		profileParams.Req.StreamLabel = streamLabel
		profileParams.Req.RequestLabel = searchQuery.Name
		applyStreamIndexerSelection(&profileParams.Req, stream)
		effectiveLimit := profileParams.Req.Limit
		if searchQuery.SearchResultLimit > 0 {
			effectiveLimit = searchQuery.SearchResultLimit
		}
		logger.Debug("Search request config",
			"stream", streamLabel,
			"request", searchQuery.Name,
			"search_mode", searchQuery.SearchMode,
			"type", contentType,
			"id", id,
			"movie_categories", searchQuery.MovieCategories,
			"tv_categories", searchQuery.TVCategories,
			"language", searchQuery.SearchTitleLanguage,
			"extra_terms", searchQuery.ExtraSearchTerms,
			"limit", effectiveLimit,
		)
		releases, runErr := search.RunIndexerSearches(s.indexer, tmdbResolver, profileParams.Req, contentType, profileParams.ContentIDs, profileParams.ImdbForText, profileParams.TmdbForText, s.config)
		if runErr != nil {
			return nil, runErr
		}
		if streamCombinesResults(stream) {
			indexerReleases = append(indexerReleases, releases...)
			continue
		}
		if len(releases) > 0 {
			indexerReleases = releases
			break
		}
	}
	streamInputResults := len(indexerReleases)
	indexerReleases = search.MergeAndDedupeSearchResults(indexerReleases)
	logger.Debug("Stream deduplication",
		"stream", streamLabel,
		"search_requests_mode", func() string {
			if streamCombinesResults(stream) {
				return "combine"
			}
			return "first_hit"
		}(),
		"input_results", streamInputResults,
		"final_results", len(indexerReleases),
	)

	if availResult != nil && len(params.AvailIndexers) == 0 {
		indexerDetailsURLs := make(map[string]bool)
		for _, r := range indexerReleases {
			if r != nil && r.DetailsURL != "" {
				indexerDetailsURLs[r.DetailsURL] = true
			}
		}
		if len(indexerDetailsURLs) > 0 {
			filtered := availResult.Releases[:0]
			for _, rws := range availResult.Releases {
				if rws == nil || rws.Release == nil {
					continue
				}
				if !indexerDetailsURLs[rws.Release.DetailsURL] {
					continue
				}
				filtered = append(filtered, rws)
			}
			availResult = &availnzb.ReleasesResult{ImdbID: availResult.ImdbID, Count: availResult.Count, Releases: filtered}
		}
	}

	if availResult != nil {
		for _, rws := range availResult.Releases {
			if rws == nil || rws.Release == nil || !rws.Available || rws.Release.Link == "" {
				continue
			}

			rws.Release.QuerySource = "id"
			availReleases = append(availReleases, rws.Release)
			cachedAvailable[rws.Release.DetailsURL] = true
		}
	}

	// Apply the same title filter to avail releases that RunIndexerSearches
	// applies to indexer results. Without this, releases with incorrect IMDB
	// metadata on the indexer side bypass the title check and can cause wrong
	// content to be served (e.g. "Dying Of The Light" for "Interstellar").
	filterQuery := search.BuildFilterQuery(tmdbResolver, params.Req, contentType, contentIDs, imdbForText, tmdbForText)
	if filterQuery != "" && len(availReleases) > 0 {
		availReleases = search.FilterResults(availReleases, contentType, filterQuery, params.Req.Season, params.Req.Episode)
	}

	return &rawSearchResult{
		Params:          params,
		AvailReleases:   availReleases,
		IndexerReleases: indexerReleases,
		CachedAvailable: cachedAvailable,
		AvailResult:     availResult,
	}, nil
}

func (s *Server) GetSearchReleases(ctx context.Context, contentType, id string) (*SearchReleasesResponse, error) {
	fallbackStream := &auth.Stream{Username: defaultStreamID}
	if contentType == "movie" {
		fallbackStream.MovieSearchQueries = allSearchQueryNames(s.config.MovieSearchQueries)
	} else {
		fallbackStream.SeriesSearchQueries = allSearchQueryNames(s.config.SeriesSearchQueries)
	}
	raw, err := s.getOrBuildRawSearchResult(ctx, contentType, id, fallbackStream)
	if err != nil || raw == nil {
		return nil, err
	}
	populateAvailable(raw)

	type releaseWithAvail struct {
		rel   *release.Release
		avail string
	}
	seenKey := make(map[string]bool)
	var unified []releaseWithAvail

	addRelease := func(r *release.Release) bool {
		key := release.Key(r)
		if key == "" || seenKey[key] {
			return true
		}
		seenKey[key] = true
		return false
	}

	setAvail := func(rel *release.Release, avail string) {
		if rel == nil {
			return
		}
		switch avail {
		case "Available":
			rel.Available = &availTrue
		case "Unavailable":
			rel.Available = &availFalse
		case "Unknown":
			if len(raw.CachedAvailable) > 0 && rel.DetailsURL != "" && raw.CachedAvailable[rel.DetailsURL] {
				rel.Available = &availTrue
			}

		}
	}

	if raw.AvailResult != nil {
		for _, rws := range raw.AvailResult.Releases {
			if rws == nil || rws.Release == nil {
				continue
			}
			r := rws.Release
			avail := "Unavailable"
			if rws.Available {
				avail = "Available"
			}
			if addRelease(r) {
				continue
			}
			setAvail(r, avail)
			unified = append(unified, releaseWithAvail{rel: r, avail: avail})
		}
	}
	for _, r := range raw.IndexerReleases {
		if r == nil {
			continue
		}
		if addRelease(r) {
			continue
		}
		setAvail(r, "Unknown")
		unified = append(unified, releaseWithAvail{rel: r, avail: "Unknown"})
	}

	// Triage all releases with the single default stream.
	releasesOnly := make([]*release.Release, 0, len(unified))
	for _, u := range unified {
		releasesOnly = append(releasesOnly, u.rel)
	}
	candidates := s.triageService.Filter(releasesOnly)
	releaseScores := make(map[string]struct {
		Fits  bool
		Score int
	}, len(candidates))
	for _, c := range candidates {
		if c.Release == nil {
			continue
		}
		releaseScores[release.Key(c.Release)] = struct {
			Fits  bool
			Score int
		}{Fits: true, Score: c.Score}
	}

	streamInfos := []SearchStreamInfo{{ID: defaultStreamID, Name: "StreamNZB"}}

	releasesOut := make([]SearchReleaseTag, 0, len(unified))
	for _, u := range unified {
		r := u.rel
		key := release.Key(r)
		ts := releaseScores[key]
		tags := []SearchStreamTag{{
			StreamID:   defaultStreamID,
			StreamName: "StreamNZB",
			Fits:       ts.Fits,
			Score:      ts.Score,
		}}
		idxName := r.Indexer
		if idxName == "" && r.SourceIndexer != nil {
			if idx, ok := r.SourceIndexer.(indexer.Indexer); ok {
				idxName = idx.Name()
			}
		}
		if idxName == "" {
			idxName = "Indexer"
		}
		releasesOut = append(releasesOut, SearchReleaseTag{
			Title:        r.Title,
			Link:         r.Link,
			DetailsURL:   r.DetailsURL,
			Size:         r.Size,
			Indexer:      idxName,
			Availability: u.avail,
			StreamTags:   tags,
		})
	}

	sort.Slice(releasesOut, func(i, j int) bool {
		si := releaseScores[release.Key(unified[i].rel)].Score
		sj := releaseScores[release.Key(unified[j].rel)].Score
		if si != sj {
			return si > sj
		}
		availOrder := map[string]int{"Available": 2, "Unknown": 1, "Unavailable": 0}
		return availOrder[releasesOut[i].Availability] > availOrder[releasesOut[j].Availability]
	})

	return &SearchReleasesResponse{Streams: streamInfos, Releases: releasesOut}, nil
}

func populateAvailable(raw *rawSearchResult) {
	if raw.AvailResult != nil {
		for _, rws := range raw.AvailResult.Releases {
			if rws == nil || rws.Release == nil {
				continue
			}
			if rws.Available {
				rws.Release.Available = &availTrue
			} else {
				rws.Release.Available = &availFalse
			}
		}
	}
	if len(raw.CachedAvailable) > 0 {
		for _, rel := range raw.IndexerReleases {
			if rel != nil && rel.DetailsURL != "" && raw.CachedAvailable[rel.DetailsURL] {
				rel.Available = &availTrue
			}
		}
	}
}

func streamProviderSelections(stream *auth.Stream) []string {
	if stream == nil {
		return nil
	}
	return append([]string(nil), stream.ProviderSelections...)
}

func (s *Server) providerHostsForStream(stream *auth.Stream) []string {
	if s.sessionManager != nil {
		if hosts := s.sessionManager.ProviderHostsForProviders(streamProviderSelections(stream)); len(hosts) > 0 {
			return hosts
		}
	}
	if s.validator != nil {
		return s.validator.GetProviderHosts()
	}
	return nil
}

func (s *Server) segmentFetcherForStream(stream *auth.Stream) loader.SegmentFetcher {
	if s.sessionManager == nil {
		return nil
	}
	return s.sessionManager.SegmentFetcherForProviders(streamProviderSelections(stream))
}

func buildAllReleasesFromRaw(raw *rawSearchResult) []*release.Release {
	seenURL := make(map[string]bool)
	var out []*release.Release
	for _, rel := range raw.AvailReleases {
		if rel == nil || rel.DetailsURL == "" {
			continue
		}
		if seenURL[rel.DetailsURL] {
			continue
		}
		seenURL[rel.DetailsURL] = true
		out = append(out, rel)
	}
	for _, rel := range raw.IndexerReleases {
		if rel == nil || rel.DetailsURL == "" {
			continue
		}
		if seenURL[rel.DetailsURL] {
			continue
		}
		seenURL[rel.DetailsURL] = true
		out = append(out, rel)
	}
	return out
}

func releasesToCandidates(releases []*release.Release) []triage.Candidate {
	var out []triage.Candidate
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		out = append(out, triage.Candidate{Release: rel, Score: 0})
	}
	return out
}

func (s *Server) buildOrderedPlayListFromRaw(raw *rawSearchResult, isAIOStreams bool, stream *auth.Stream) (*orderedPlayListResult, error) {
	populateAvailable(raw)

	unavailableDetailsURLs := make(map[string]bool)
	if raw.AvailResult != nil {
		for _, rws := range raw.AvailResult.Releases {
			if rws == nil || rws.Release == nil || rws.Available {
				continue
			}
			if rws.Release.DetailsURL != "" {
				unavailableDetailsURLs[rws.Release.DetailsURL] = true
			}
		}
	}

	allReleases := buildAllReleasesFromRaw(raw)
	var merged []triage.Candidate
	if isAIOStreams {
		merged = releasesToCandidates(allReleases)
	} else {
		merged = s.triageService.Filter(allReleases)
	}

	seenURL := make(map[string]bool)
	var seenTitle map[string]bool
	if isAIOStreams {
		seenTitle = make(map[string]bool)
	}
	filtered := merged[:0]
	for _, c := range merged {
		if c.Release == nil || c.Release.DetailsURL == "" {
			continue
		}
		if unavailableDetailsURLs[c.Release.DetailsURL] {
			continue
		}
		if seenURL[c.Release.DetailsURL] {
			continue
		}
		if seenTitle != nil && c.Release.Title != "" {
			titleKey := release.NormalizeTitleForDedup(c.Release.Title)
			if titleKey != "" {
				if seenTitle[titleKey] {
					continue
				}
				seenTitle[titleKey] = true
			}
		}
		seenURL[c.Release.DetailsURL] = true
		filtered = append(filtered, c)
	}
	merged = filtered

	if raw.AvailResult != nil && s.availClient != nil {
		ourBackbones, _ := s.availClient.OurBackbones(s.providerHostsForStream(stream))
		cachedUnhealthyForUs := make(map[string]bool)
		for _, rws := range raw.AvailResult.Releases {
			if rws == nil || rws.Release == nil {
				continue
			}
			if rws.Available {
				continue
			}
			if len(ourBackbones) > 0 && len(rws.Summary) > 0 {
				ourReported, ourHealthy := 0, 0
				for backbone, status := range rws.Summary {
					if ourBackbones[backbone] {
						ourReported++
						if status.Healthy {
							ourHealthy++
						}
					}
				}
				if ourReported > 0 && ourHealthy == 0 {
					cachedUnhealthyForUs[rws.Release.DetailsURL] = true
				}
			}
		}
		if len(cachedUnhealthyForUs) > 0 {
			filtered := merged[:0]
			for _, c := range merged {
				if c.Release == nil || !cachedUnhealthyForUs[c.Release.DetailsURL] {
					filtered = append(filtered, c)
				}
			}
			merged = filtered
		}
	}
	if !isAIOStreams {
		sort.Slice(merged, func(i, j int) bool {
			return streamScoreFromCandidate(merged[i]) > streamScoreFromCandidate(merged[j])
		})
	}

	firstIsAvailGood := false
	if len(merged) > 0 && merged[0].Release != nil && merged[0].Release.DetailsURL != "" {
		firstIsAvailGood = raw.CachedAvailable[merged[0].Release.DetailsURL]
	}
	return &orderedPlayListResult{
		Candidates:             merged,
		FirstIsAvailGood:       firstIsAvailGood,
		Params:                 raw.Params,
		CachedAvailable:        raw.CachedAvailable,
		UnavailableDetailsURLs: unavailableDetailsURLs,
	}, nil
}

func streamScoreFromCandidate(c triage.Candidate) int {
	return c.Score
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

type SearchParams struct {
	ContentType        string
	ID                 string
	Req                indexer.SearchRequest
	ContentIDs         *session.AvailReportMeta
	ImdbForText        string
	TmdbForText        string
	AvailIndexers      []string
	MovieTitleQueries  map[string][]string
	SeriesTitleQueries map[string][]string
	Metadata           *resolvedSearchMetadata
}

type resolvedSearchMetadata struct {
	MovieDetails      *tmdb.MovieDetails
	MovieTranslations *tmdb.MovieTranslationsResponse
	TVDetails         *tmdb.TVDetails
	TVTranslations    *tmdb.TVTranslationsResponse
}

func metadataDisplayTitle(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails != nil {
			if title := strings.TrimSpace(metadata.MovieDetails.Title); title != "" {
				return title
			}
			return strings.TrimSpace(metadata.MovieDetails.OriginalTitle)
		}
		return ""
	}
	if metadata.TVDetails != nil {
		if title := strings.TrimSpace(metadata.TVDetails.Name); title != "" {
			return title
		}
		return strings.TrimSpace(metadata.TVDetails.OriginalName)
	}
	return ""
}

func metadataDisplayYear(metadata *resolvedSearchMetadata, contentType string) string {
	if metadata == nil {
		return ""
	}
	if contentType == "movie" {
		if metadata.MovieDetails != nil && len(metadata.MovieDetails.ReleaseDate) >= 4 {
			return metadata.MovieDetails.ReleaseDate[:4]
		}
		return ""
	}
	if metadata.TVDetails != nil && len(metadata.TVDetails.FirstAirDate) >= 4 {
		return metadata.TVDetails.FirstAirDate[:4]
	}
	return ""
}

func metadataLanguageCount(metadata *resolvedSearchMetadata, contentType string) int {
	if metadata == nil {
		return 0
	}
	if contentType == "movie" {
		if metadata.MovieTranslations != nil {
			return len(metadata.MovieTranslations.Translations)
		}
		return 0
	}
	if metadata.TVTranslations != nil {
		return len(metadata.TVTranslations.Translations)
	}
	return 0
}

func localizedMovieTitleForLanguage(translations *tmdb.MovieTranslationsResponse, language string) string {
	if translations == nil || language == "" {
		return ""
	}
	langCode, countryCode := splitLanguageTagLocal(language)
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Title) == "" {
			continue
		}
		if countryCode != "" && strings.EqualFold(entry.ISO639_1, langCode) && strings.EqualFold(entry.ISO3166_1, countryCode) {
			return strings.TrimSpace(entry.Data.Title)
		}
	}
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Title) != "" && strings.EqualFold(entry.ISO639_1, langCode) {
			return strings.TrimSpace(entry.Data.Title)
		}
	}
	return ""
}

func localizedTVTitleForLanguage(translations *tmdb.TVTranslationsResponse, language string) string {
	if translations == nil || language == "" {
		return ""
	}
	langCode, countryCode := splitLanguageTagLocal(language)
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Name) == "" {
			continue
		}
		if countryCode != "" && strings.EqualFold(entry.ISO639_1, langCode) && strings.EqualFold(entry.ISO3166_1, countryCode) {
			return strings.TrimSpace(entry.Data.Name)
		}
	}
	for i := range translations.Translations {
		entry := translations.Translations[i]
		if strings.TrimSpace(entry.Data.Name) != "" && strings.EqualFold(entry.ISO639_1, langCode) {
			return strings.TrimSpace(entry.Data.Name)
		}
	}
	return ""
}

func splitLanguageTagLocal(tag string) (lang, country string) {
	tag = strings.TrimSpace(tag)
	if i := strings.Index(tag, "-"); i >= 0 {
		return tag[:i], tag[i+1:]
	}
	return tag, ""
}

func buildMovieQueriesFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool) []string {
	if metadata == nil || metadata.MovieDetails == nil {
		return nil
	}
	title := strings.TrimSpace(metadata.MovieDetails.Title)
	if localized := localizedMovieTitleForLanguage(metadata.MovieTranslations, language); localized != "" {
		title = localized
	}
	if title == "" {
		return nil
	}
	year := ""
	if includeYear && len(metadata.MovieDetails.ReleaseDate) >= 4 {
		year = metadata.MovieDetails.ReleaseDate[:4]
	}
	primary := strings.TrimSpace(title)
	if year != "" {
		primary += " " + year
	}
	queries := []string{strings.TrimSpace(primary)}

	originalTitle := strings.TrimSpace(metadata.MovieDetails.OriginalTitle)
	originalLang := strings.TrimSpace(metadata.MovieDetails.OriginalLanguage)
	if originalTitle == "" || originalLang == "" || strings.EqualFold(originalLang, "en") {
		return queries
	}
	if release.NormalizeTitle(originalTitle) == release.NormalizeTitle(title) {
		return queries
	}
	original := originalTitle
	if year != "" {
		original += " " + year
	}
	original = strings.TrimSpace(original)
	if original != "" && original != queries[0] {
		queries = append(queries, original)
	}
	return queries
}

func buildSeriesQueriesFromMetadata(metadata *resolvedSearchMetadata, language string, includeYear bool, season, episode string, useSeasonEpisodeParams bool) []string {
	if metadata == nil || metadata.TVDetails == nil {
		return nil
	}
	title := strings.TrimSpace(metadata.TVDetails.Name)
	if localized := localizedTVTitleForLanguage(metadata.TVTranslations, language); localized != "" {
		title = localized
	}
	if title == "" {
		return nil
	}
	year := ""
	if includeYear && len(metadata.TVDetails.FirstAirDate) >= 4 {
		year = metadata.TVDetails.FirstAirDate[:4]
	}
	primary := strings.TrimSpace(title)
	if year != "" {
		primary += " " + year
	}
	queries := []string{strings.TrimSpace(primary)}

	originalTitle := strings.TrimSpace(metadata.TVDetails.OriginalName)
	originalLang := strings.TrimSpace(metadata.TVDetails.OriginalLanguage)
	if originalTitle != "" && originalLang != "" && !strings.EqualFold(originalLang, "en") && release.NormalizeTitle(originalTitle) != release.NormalizeTitle(title) {
		original := originalTitle
		if year != "" {
			original += " " + year
		}
		original = strings.TrimSpace(original)
		if original != "" && original != queries[0] {
			queries = append(queries, original)
		}
	}

	if !useSeasonEpisodeParams {
		withEpisode := make([]string, 0, len(queries))
		for _, query := range queries {
			if query == "" {
				continue
			}
			withEpisode = append(withEpisode, appendSeasonEpisodeQuery(query, season, episode))
		}
		queries = withEpisode
	}
	return queries
}

func buildSeriesQueries(showName string) []string {
	return buildSeriesQueriesWithOptions(showName, "", false)
}

func buildSeriesQueriesWithOptions(showName, year string, includeYear bool) []string {
	showName = strings.TrimSpace(showName)
	if includeYear && strings.TrimSpace(year) != "" {
		showName = strings.TrimSpace(showName + " " + year)
	}
	if showName == "" {
		return nil
	}
	return []string{showName}
}

func (s *Server) buildSearchParamsBase(contentType, id string, searchQuery *config.SearchQueryConfig) (*SearchParams, error) {
	const searchLimit = 1000
	params := &SearchParams{
		ContentType:        contentType,
		ID:                 id,
		MovieTitleQueries:  make(map[string][]string),
		SeriesTitleQueries: make(map[string][]string),
		Metadata:           &resolvedSearchMetadata{},
	}
	req := indexer.SearchRequest{Limit: searchLimit}
	searchMode := ""
	useSeasonEpisodeParams := true
	if searchQuery != nil {
		searchMode = strings.ToLower(strings.TrimSpace(searchQuery.SearchMode))
		if searchQuery.UseSeasonEpisodeParams != nil {
			useSeasonEpisodeParams = *searchQuery.UseSeasonEpisodeParams
		}
	}
	req.UseSeasonEpisodeParams = useSeasonEpisodeParams

	searchID := id
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if parts[0] == "tmdb" && len(parts) >= 4 {
			searchID = parts[1]
			req.Season, req.Episode = parts[2], parts[3]
		} else if len(parts) >= 3 {
			searchID = parts[0]
			req.Season, req.Episode = parts[1], parts[2]
		} else if len(parts) > 0 {
			searchID = parts[0]
		}
	} else if strings.HasPrefix(id, "tmdb:") {
		searchID = strings.TrimPrefix(id, "tmdb:")
	}
	if searchMode == "id" {
		req.ForceIDSearch = true
		if contentType == "series" && req.Season != "" && req.Episode != "" && !useSeasonEpisodeParams {
			if seasonNum, err1 := strconv.Atoi(req.Season); err1 == nil {
				if episodeNum, err2 := strconv.Atoi(req.Episode); err2 == nil {
					req.Query = fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
				}
			}
			if req.Query == "" {
				req.Query = fmt.Sprintf("S%sE%s", req.Season, req.Episode)
			}
		}
	}
	if strings.HasPrefix(searchID, "tt") {
		req.IMDbID = searchID
	} else {
		req.TMDBID = searchID
	}
	imdbForText := req.IMDbID
	tmdbForText := req.TMDBID
	if contentType == "series" && strings.Contains(id, ":") {
		parts := strings.Split(id, ":")
		if parts[0] == "tmdb" && len(parts) >= 2 {
			tmdbForText = parts[1]
		}
	}
	if contentType == "movie" {
		req.Cat = "2000"
	} else {
		req.Cat = "5000"
	}

	if req.TMDBID == "" && req.IMDbID != "" && s.tmdbClient != nil {
		findResp, findErr := s.tmdbClient.Find(req.IMDbID, "imdb_id")
		if findErr == nil {
			if contentType == "movie" && len(findResp.MovieResults) > 0 {
				req.TMDBID = strconv.Itoa(findResp.MovieResults[0].ID)
				tmdbForText = req.TMDBID
			}
			if contentType == "series" && len(findResp.TVResults) > 0 {
				req.TMDBID = strconv.Itoa(findResp.TVResults[0].ID)
				tmdbForText = req.TMDBID
			}
		}
	}

	if contentType == "series" {
		if req.TMDBID != "" && s.tmdbClient != nil {
			if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
				if details, err := s.tmdbClient.GetTVDetails(tmdbIDNum); err == nil {
					params.Metadata.TVDetails = details
				}
				if translations, err := s.tmdbClient.GetTVTranslations(tmdbIDNum); err == nil {
					params.Metadata.TVTranslations = translations
				}
				if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "tv"); err == nil {
					if extIDs.TVDBID != 0 {
						req.TVDBID = strconv.Itoa(extIDs.TVDBID)
					}
					if extIDs.IMDbID != "" && req.IMDbID == "" {
						req.IMDbID = extIDs.IMDbID
						imdbForText = extIDs.IMDbID
					}
				}
			}
		}
		if req.IMDbID != "" && req.TVDBID == "" {
			if s.tvdbClient != nil {
				if tvdbID, err := s.tvdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID = tvdbID
				}
			}
			if req.TVDBID == "" && s.tmdbClient != nil {
				if tvdbID, err := s.tmdbClient.ResolveTVDBID(req.IMDbID); err == nil && tvdbID != "" {
					req.TVDBID = tvdbID
				}
			}
		}
	}
	seasonNum, _ := strconv.Atoi(req.Season)
	episodeNum, _ := strconv.Atoi(req.Episode)
	contentIDs := &session.AvailReportMeta{ImdbID: req.IMDbID, TvdbID: req.TVDBID, Season: seasonNum, Episode: episodeNum}
	if contentType == "movie" && req.TMDBID != "" && s.tmdbClient != nil {
		if tmdbIDNum, err := strconv.Atoi(req.TMDBID); err == nil {
			if details, err := s.tmdbClient.GetMovieDetails(tmdbIDNum); err == nil {
				params.Metadata.MovieDetails = details
			}
			if translations, err := s.tmdbClient.GetMovieTranslations(tmdbIDNum); err == nil {
				params.Metadata.MovieTranslations = translations
			}
			if extIDs, err := s.tmdbClient.GetExternalIDs(tmdbIDNum, "movie"); err == nil && extIDs.IMDbID != "" && contentIDs.ImdbID == "" {
				contentIDs.ImdbID = extIDs.IMDbID
				req.IMDbID = contentIDs.ImdbID
				imdbForText = contentIDs.ImdbID
			}
		}
	}
	contentIDs.ImdbID = req.IMDbID
	contentIDs.TvdbID = req.TVDBID
	params.Req = req
	params.ContentIDs = contentIDs
	params.ImdbForText = imdbForText
	params.TmdbForText = tmdbForText
	params.AvailIndexers = s.availNZBIndexerHosts
	return params, nil
}

func cloneSearchParams(base *SearchParams) *SearchParams {
	if base == nil {
		return nil
	}
	next := *base
	nextReq := base.Req
	next.Req = nextReq
	if base.ContentIDs != nil {
		contentIDs := *base.ContentIDs
		next.ContentIDs = &contentIDs
	}
	if base.AvailIndexers != nil {
		next.AvailIndexers = append([]string(nil), base.AvailIndexers...)
	}
	next.MovieTitleQueries = base.MovieTitleQueries
	next.SeriesTitleQueries = base.SeriesTitleQueries
	next.Metadata = base.Metadata
	return &next
}

func (s *Server) buildSearchParamsFromBase(base *SearchParams, searchQuery *config.SearchQueryConfig) (*SearchParams, error) {
	params := cloneSearchParams(base)
	if params == nil {
		return nil, fmt.Errorf("base search params are required")
	}
	contentType := params.ContentType
	req := &params.Req
	searchMode := ""
	includeYearInTextSearch := true
	useSeasonEpisodeParams := true
	var queryIndexerConfig *config.IndexerSearchConfig
	if searchQuery != nil {
		searchMode = strings.ToLower(strings.TrimSpace(searchQuery.SearchMode))
		queryIndexerConfig = searchQuery.AsIndexerSearchConfig()
		if searchQuery.IncludeYearInTextSearch != nil {
			includeYearInTextSearch = *searchQuery.IncludeYearInTextSearch
		}
		if searchQuery.UseSeasonEpisodeParams != nil {
			useSeasonEpisodeParams = *searchQuery.UseSeasonEpisodeParams
		}
	}
	req.UseSeasonEpisodeParams = useSeasonEpisodeParams
	req.ForceIDSearch = false
	req.Query = ""
	req.PerIndexerQuery = nil
	req.FilterQuery = ""
	if searchMode == "id" {
		req.ForceIDSearch = true
		if contentType == "series" && req.Season != "" && req.Episode != "" && !useSeasonEpisodeParams {
			if seasonNum, err1 := strconv.Atoi(req.Season); err1 == nil {
				if episodeNum, err2 := strconv.Atoi(req.Episode); err2 == nil {
					req.Query = fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
				}
			}
			if req.Query == "" {
				req.Query = fmt.Sprintf("S%sE%s", req.Season, req.Episode)
			}
		}
	}

	if len(s.config.Indexers) > 0 {
		req.EffectiveByIndexer = make(map[string]*config.IndexerSearchConfig)
		indexerTypeByName := make(map[string]string, len(s.config.Indexers))
		for i := range s.config.Indexers {
			ic := &s.config.Indexers[i]
			if ic.Enabled != nil && !*ic.Enabled {
				continue
			}
			eff := config.MergeIndexerSearch(ic, queryIndexerConfig, s.config)
			if strings.EqualFold(ic.Type, "easynews") {
				t := true
				eff.DisableIdSearch = &t
			}
			req.EffectiveByIndexer[ic.Name] = eff
			indexerTypeByName[ic.Name] = ic.Type
		}
		if searchMode != "id" {
			req.PerIndexerQuery = make(map[string][]string)
		}
		if searchMode != "id" {
			if contentType == "movie" {
				for name, eff := range req.EffectiveByIndexer {
					includeYear := includeYearInTextSearch
					if strings.EqualFold(indexerTypeByName[name], "easynews") {
						includeYear = false
					}
					lang := ""
					if eff.SearchTitleLanguage != nil {
						lang = *eff.SearchTitleLanguage
					}
					cacheKey := fmt.Sprintf("%s|%t|%t", lang, includeYear, useSeasonEpisodeParams)
					if queries, ok := params.MovieTitleQueries[cacheKey]; ok {
						req.PerIndexerQuery[name] = queries
						continue
					}
					queries := buildMovieQueriesFromMetadata(params.Metadata, lang, includeYear)
					if len(queries) == 0 {
						logger.Debug("Prepared movie search titles failed", "stream", req.StreamLabel, "request", req.RequestLabel, "language", lang, "err", "metadata unavailable")
						continue
					}
					params.MovieTitleQueries[cacheKey] = queries
					req.PerIndexerQuery[name] = queries
				}
			} else if req.Season != "" && req.Episode != "" {
				for name, eff := range req.EffectiveByIndexer {
					includeYear := includeYearInTextSearch
					if strings.EqualFold(indexerTypeByName[name], "easynews") {
						includeYear = false
					}
					lang := ""
					if eff.SearchTitleLanguage != nil {
						lang = *eff.SearchTitleLanguage
					}
					cacheKey := fmt.Sprintf("%s|%t", lang, includeYear)
					if queries, ok := params.SeriesTitleQueries[cacheKey]; ok {
						req.PerIndexerQuery[name] = queries
						continue
					}
					queries := buildSeriesQueriesFromMetadata(params.Metadata, lang, includeYear, req.Season, req.Episode, useSeasonEpisodeParams)
					if len(queries) == 0 {
						logger.Debug("Prepared series search titles failed", "stream", req.StreamLabel, "request", req.RequestLabel, "language", lang, "err", "metadata unavailable")
						continue
					}
					params.SeriesTitleQueries[cacheKey] = queries
					req.PerIndexerQuery[name] = queries
				}
			}
		}
	}
	if searchMode != "id" {
		if contentType == "movie" {
			queries := buildMovieQueriesFromMetadata(params.Metadata, "", includeYearInTextSearch)
			if len(queries) > 0 {
				req.FilterQuery = queries[0]
			}
		} else if req.Season != "" && req.Episode != "" {
			queries := buildSeriesQueriesFromMetadata(params.Metadata, "", false, req.Season, req.Episode, false)
			if len(queries) > 0 {
				req.FilterQuery = queries[0]
			}
		}
	}
	return params, nil
}

func (s *Server) buildSearchParams(contentType, id string, searchQuery *config.SearchQueryConfig) (*SearchParams, error) {
	base, err := s.buildSearchParamsBase(contentType, id, nil)
	if err != nil {
		return nil, err
	}
	return s.buildSearchParamsFromBase(base, searchQuery)
}

func appendSeasonEpisodeQuery(query, season, episode string) string {
	if season == "" || episode == "" {
		return strings.TrimSpace(query)
	}
	seasonNum, seasonErr := strconv.Atoi(season)
	episodeNum, episodeErr := strconv.Atoi(episode)
	suffix := fmt.Sprintf("S%sE%s", season, episode)
	if seasonErr == nil && episodeErr == nil {
		suffix = fmt.Sprintf("S%02dE%02d", seasonNum, episodeNum)
	}
	if strings.TrimSpace(query) == "" {
		return suffix
	}
	return strings.TrimSpace(query) + " " + suffix
}

func (s *Server) runAvailNZBPhase(ctx context.Context, params *SearchParams, stream *auth.Stream) ([]Stream, []*release.Release, *availnzb.ReleasesResult) {
	contentIDs := params.ContentIDs
	availIndexers := params.AvailIndexers
	if !streamUsesAvailNZB(stream) || s.availClient == nil || s.availClient.BaseURL == "" || (contentIDs.ImdbID == "" && contentIDs.TvdbID == "") {
		return nil, nil, nil
	}
	availResult, _ := s.availClient.GetReleases(contentIDs.ImdbID, contentIDs.TvdbID, contentIDs.Season, contentIDs.Episode, availIndexers, s.providerHostsForStream(stream))
	if availResult == nil || len(availResult.Releases) == 0 {
		return nil, nil, nil
	}
	var availReleases []*release.Release
	for _, rws := range availResult.Releases {
		if rws == nil || rws.Release == nil || !rws.Available || rws.Release.Link == "" {
			continue
		}
		availReleases = append(availReleases, rws.Release)
	}
	// Filter by title to reject releases with incorrect IMDB metadata.
	filterQuery := search.BuildFilterQuery(s.tmdbClient, params.Req, params.ContentType, contentIDs, params.ImdbForText, params.TmdbForText)
	if filterQuery != "" && len(availReleases) > 0 {
		availReleases = search.FilterResults(availReleases, params.ContentType, filterQuery, params.Req.Season, params.Req.Episode)
	}
	if len(availReleases) == 0 {
		return nil, nil, availResult
	}
	candidates := s.triageService.Filter(availReleases)
	logger.Debug("AvailNZB phase", "releases", len(availReleases), "after_triage", len(candidates))
	var streams []Stream
	seen := make(map[string]bool)
	for _, cand := range candidates {
		if cand.Release == nil {
			continue
		}
		rel := cand.Release
		norm := release.NormalizeTitle(rel.Title)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		downloadURL := addAPIKeyToDownloadURL(rel.Link, s.config.Indexers)
		sessionID := fmt.Sprintf("%x", md5.Sum([]byte(rel.DetailsURL)))
		_, err := s.sessionManager.CreateDeferredSessionWithFetcher(sessionID, downloadURL, rel, s.indexer, contentIDs, params.ContentType, params.ID, s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
		if err != nil {
			logger.Debug("AvailNZB deferred session failed", "title", rel.Title, "err", err)
			continue
		}
		var streamURL string
		if stream != nil {
			streamURL = s.baseURLWithToken(stream) + "/play/" + sessionID
		}
		sizeGB := float64(rel.Size) / (1024 * 1024 * 1024)
		displayTitle := rel.Title + "\n[AvailNZB]"
		stream := buildStreamMetadata(streamURL, displayTitle, cand, sizeGB, rel.Size, rel)
		streams = append(streams, stream)
	}
	sort.Slice(streams, func(i, j int) bool {
		return streamScore(streams[i]) > streamScore(streams[j])
	})
	return streams, availReleases, availResult
}

func (s *Server) GetAvailNZBStreams(ctx context.Context, contentType, id string, stream *auth.Stream) ([]Stream, error) {
	params, err := s.buildSearchParams(contentType, id, nil)
	if err != nil {
		return nil, err
	}
	streams, _, _ := s.runAvailNZBPhase(ctx, params, stream)
	if streams == nil {
		return []Stream{}, nil
	}
	return streams, nil
}

// ensureDeferredSessionsForPlayList creates deferred sessions for every candidate in the play list,
// keyed by the same slot path used in stream URLs, so handlePlay can serve without resolving or hitting indexers.
func (s *Server) ensureDeferredSessionsForPlayList(list *orderedPlayListResult, key StreamSlotKey, stream *auth.Stream) {
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
		if _, err := s.sessionManager.CreateDeferredSessionWithFetcher(playPath, downloadURL, cand.Release, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, s.segmentFetcherForStream(stream), s.providerHostsForStream(stream)); err != nil {
			logger.Debug("Create deferred session for play list failed", "slot", playPath, "err", err)
		}
	}
}

func (s *Server) resolveStreamSlot(ctx context.Context, key StreamSlotKey, index int, stream *auth.Stream) (*session.Session, error) {
	if key.StreamID == "" {
		key.StreamID = defaultStreamID
	}
	isAIOStreams := streamUsesAIOStreamsProfile(stream)
	list, err := s.buildOrderedPlayList(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return nil, fmt.Errorf("build play list: %w", err)
	}
	n := len(list.Candidates)
	if index < 0 || index >= n {
		return nil, fmt.Errorf("index %d out of range (candidates %d)", index, n)
	}
	cand := list.Candidates[index]
	rel := cand.Release
	if rel == nil || rel.Link == "" {
		return nil, fmt.Errorf("no release at index %d", index)
	}
	downloadURL := addAPIKeyToDownloadURL(rel.Link, s.config.Indexers)
	idx := s.indexer
	if rel.SourceIndexer != nil {
		if i, ok := rel.SourceIndexer.(indexer.Indexer); ok {
			idx = i
		}
	}
	sessionID := key.SlotPath(index)
	_, err = s.sessionManager.CreateDeferredSessionWithFetcher(sessionID, downloadURL, rel, idx, list.Params.ContentIDs, list.Params.ContentType, list.Params.ID, s.segmentFetcherForStream(stream), s.providerHostsForStream(stream))
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
	list, err := s.buildOrderedPlayList(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return "", err
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

func classifyPlaybackStartupErr(phase string, startupCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(startupCtx.Err(), context.DeadlineExceeded) {
		return playbackStartupTimeoutErr(phase, err)
	}
	return err
}

func playbackStartupTimeoutErr(phase string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%w during %s after %s: %v", ErrPlaybackStartupTimeout, phase, playbackStartupTimeout, cause)
	}
	return fmt.Errorf("%w during %s after %s", ErrPlaybackStartupTimeout, phase, playbackStartupTimeout)
}

func isPlayPrepareCancellation(err error) bool {
	if err == nil || errors.Is(err, ErrPlaybackStartupTimeout) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
	logger.Info("Play request", "session", sessionID)

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
			forceDisconnect(w, s.baseURL)
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
		forceDisconnect(w, s.baseURL)
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
			forceDisconnect(w, s.baseURL)
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
			// Always mark slot as permanently failed when we abandon it, so future retries on the same URL
			// get a redirect instead of a 404.
			s.sessionManager.SetSlotFailedDuringPlayback(sessionID)
			if (isSegmentUnavailableErr(prepareErr) || isDataCorruptErr(prepareErr)) && s.availReporter != nil {
				s.availReporter.ReportBad(sess, prepareErr.Error())
			}
			// Gate failure recording: concurrent goroutines for the same session (Stremio's automatic
			// re-requests) must not each insert a Failure row. Only the first one wins.
			if _, alreadyFailed := s.recordedFailureSessionIDs.LoadOrStore(sessionID, struct{}{}); !alreadyFailed {
				s.recordAttempt(sess, false, prepareErr.Error())
				go func(id string, done <-chan struct{}) {
					<-done
					s.recordedFailureSessionIDs.Delete(id)
				}(sessionID, sess.Done())
			}
			s.sessionManager.DeleteSession(sessionID)
			if streamFailoverEnabled(streamConfig) {
				if nextSess, nextID, switchErr := s.switchToNextFallback(r.Context(), sess, streamConfig); nextID != "" && switchErr == nil {
					logger.Info("Trying next fallback slot (internal)", "from", sessionID, "to", nextID, "err", prepareErr)
					sess, sessionID = nextSess, nextID
					continue
				}
			}
			logger.Info("No more fallback slots", "last", sessionID, "err", prepareErr)
			forceDisconnect(w, s.baseURL)
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
		//   - 430 No Such Article (segment missing on all providers)
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
			if s.availReporter != nil {
				s.availReporter.ReportBad(errSess, readErr.Error())
			}
			if _, alreadyFailed := s.recordedFailureSessionIDs.LoadOrStore(playbackSessionID, struct{}{}); !alreadyFailed {
				s.recordAttempt(errSess, false, readErr.Error())
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
	effectiveRange := r.Header.Get("Range")
	probeLikeServe := false
	probeLikeServeReason := ""

	logger.Debug("play handler serving stream", "session", sessionID, "name", name, "size", size)
	logger.Info("Serving media",
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
	defer func() {
		closeStream("handler exit")
		bufW.Flush()

		responseStats := bufW.Snapshot()
		streamStats := monitoredStream.Snapshot()
		closeReasonText := ""
		if v := closeReason.Load(); v != nil {
			closeReasonText = v.(string)
		}
		probeLikeServe, probeLikeServeReason = classifyProbeLikeServe(r, size, effectiveRange, responseStats, streamStats, closeReasonText)

		logger.Info("Finished serving media",
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
		if s.availReporter != nil {
			s.availReporter.ReportGood(sess)
		}
		if _, already := s.recordedSuccessSessionIDs.LoadOrStore(sessionID, struct{}{}); !already {
			s.recordAttempt(sess, true, "")
			// When session is gone, allow recording success again for a future play of the same release.
			go func() {
				<-sess.Done()
				s.recordedSuccessSessionIDs.Delete(sessionID)
			}()
		}
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
		probeCtx, cancel := context.WithTimeout(ctx, playbackStartupTimeout)
		spec, startupInfo, err := s.probePlaybackSource(probeCtx, sess, needDurationProbe)
		err = classifyPlaybackStartupErr("probe", probeCtx, err)
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

	stream, err := s.openExpectedPlaybackSourceWithStartupTimeout(ctx, sess, preparedStream.Spec)
	if err != nil {
		sess.ResetPlaybackStream()
		return preparedPlaybackStream{}, err
	}

	preparedStream.Stream = stream
	preparedStream.Mode = "per_request"
	sess.CachePlaybackStreamSnapshot(preparedStream.Spec, preparedStream.StartupInfo, preparedStream.HasStartupInfo)
	return preparedStream, nil
}

func (s *Server) openExpectedPlaybackSourceWithStartupTimeout(ctx context.Context, sess *session.Session, spec session.PlaybackStreamSpec) (io.ReadSeekCloser, error) {
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

	timer := time.NewTimer(playbackStartupTimeout)
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
		return nil, playbackStartupTimeoutErr("open", nil)
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
				s.reportBadRelease(sess, unpack.ErrTooManyZeroFills)
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
		s.reportBadRelease(sess, err)
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
		ctx := context.Background()
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
	list, err := s.buildOrderedPlayList(ctx, key, isAIOStreams, stream)
	if err != nil || list == nil {
		return "", err
	}
	n := len(list.Candidates)
	useSlotPaths := len(list.SlotPaths) == n

	// If the stream uses the AIOStreams profile and has a failover order, advance through that order.
	order := []string(nil)
	if isAIOStreams {
		order = s.sessionManager.GetDeviceFailoverOrder(streamToken(stream), key.CacheKey())
	}
	if len(order) > 0 {
		ourPosition := -1
		for p, entry := range order {
			if entry == currentID {
				ourPosition = p
				break
			}
		}
		if ourPosition >= 0 {
			for j := ourPosition + 1; j < len(order); j++ {
				entry := order[j]
				if !strings.HasPrefix(entry, streamSlotPrefix) {
					continue
				}
				_, ct, eid, orderIndex, ok := parseStreamSlotID(entry)
				if !ok || orderIndex < 0 || orderIndex >= n {
					continue
				}
				if ct != key.ContentType || eid != key.ID {
					continue
				}
				if !s.sessionManager.GetSlotFailedDuringPlayback(entry) {
					return entry, nil
				}
			}
			return "", nil // exhausted the stream-provided order
		}
	}

	// Sequential fallback: increment index, skip slots already marked as failed.
	for i := currentIndex + 1; i < n; i++ {
		slotPath := key.SlotPath(i)
		if useSlotPaths {
			slotPath = list.SlotPaths[i]
		}
		if !s.sessionManager.GetSlotFailedDuringPlayback(slotPath) {
			return slotPath, nil
		}
	}
	return "", nil
}

// switchToNextFallback derives the next fallback slot from the cached play list and returns its session.
// Returns (nil, "", nil) when there is no next slot, or (nil, "", err) on a resolve error.
func (s *Server) switchToNextFallback(ctx context.Context, sess *session.Session, stream *auth.Stream) (*session.Session, string, error) {
	currentID := sess.ID
	for {
		nextID, err := s.deriveNextSlotID(ctx, currentID, stream)
		if err != nil || nextID == "" {
			return nil, "", err
		}
		nextSess, err := s.getOrResolveSession(ctx, nextID, stream)
		if err == nil {
			return nextSess, nextID, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		logger.Info("Skipping unresolved fallback slot", "from", currentID, "to", nextID, "err", err)
		s.sessionManager.SetSlotFailedDuringPlayback(nextID)
		currentID = nextID
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

func (s *Server) reportBadRelease(sess *session.Session, streamErr error) {
	errMsg := streamErr.Error()
	if !strings.Contains(errMsg, "compressed") && !strings.Contains(errMsg, "encrypted") &&
		!strings.Contains(errMsg, "EOF") && !errors.Is(streamErr, unpack.ErrTooManyZeroFills) &&
		!errors.Is(streamErr, unpack.ErrEpisodeTargetNotFound) {
		return
	}
	if s.availReporter != nil {
		s.availReporter.ReportBad(sess, errMsg)
	}
	// Do NOT call recordAttempt here. The caller (tryPlaySlot → handlePlay) always calls
	// recordAttempt after tryPlaySlot returns an error, so calling it here too would insert
	// a duplicate Failure row (the first call updates preload=1→0, the second falls through
	// to INSERT because no preload=1 row remains).
}

// recordAttemptParams builds persistence params from a session.
func (s *Server) recordAttemptParams(sess *session.Session) persistence.RecordAttemptParams {
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
		ContentType:  contentType,
		ContentID:    contentID,
		ContentTitle: "",
		ReleaseTitle: sess.ReportReleaseName(),
		ReleaseURL:   sess.ReleaseURL(),
		ReleaseSize:  sess.ReportSize(),
		ServedFile:   sess.SelectedPlaybackFile(),
		SlotPath:     sess.ID,
	}
}

// recordPreloadAttempt inserts a preload row when we are about to try playing a slot (result not yet known).
func (s *Server) recordPreloadAttempt(sess *session.Session) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParams(sess)
	s.attemptRecorder.RecordPreloadAttempt(p)
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
}

// recordAttempt writes one NZB attempt to the persistence layer when attemptRecorder is set (or updates existing preload row).
func (s *Server) recordAttempt(sess *session.Session, success bool, failureReason string) {
	if s.attemptRecorder == nil || sess == nil {
		return
	}
	p := s.recordAttemptParams(sess)
	p.Success = success
	p.FailureReason = failureReason
	s.attemptRecorder.RecordAttempt(p)
	if s.onAttemptRecorded != nil {
		s.onAttemptRecorded()
	}
}

func (s *Server) handleDebugPlay(w http.ResponseWriter, r *http.Request, streamConfig *auth.Stream) {
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"addon":  "streamnzb",
	})
}

func streamScore(s Stream) int {
	return s.Score
}

func forceDisconnect(w http.ResponseWriter, baseURL string) {
	errorVideoURL := strings.TrimSuffix(baseURL, "/") + "/error/failure.mp4"
	logger.Info("Redirecting to error video", "url", errorVideoURL)

	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.Redirect(w, &http.Request{Method: "GET"}, errorVideoURL, http.StatusTemporaryRedirect)
}

func (s *Server) Reload(opts *ServerOptions) {
	if opts == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config = opts.Config
	s.baseURL = opts.BaseURL
	s.indexer = opts.Indexer
	s.validator = opts.Validator
	s.triageService = opts.TriageService
	reloadMode := ""
	if opts.Config != nil {
		reloadMode = opts.Config.AvailNZBMode
	}
	if reloadMode == "disabled" {
		s.availClient = nil
		s.availReporter = nil
	} else if opts.AvailClient != nil {
		s.availClient = opts.AvailClient
		s.availReporter = availnzb.NewReporter(opts.AvailClient, opts.Validator)
		if reloadMode == "status_only" {
			s.availReporter.Disabled = true
		}
		go func(client *availnzb.Client) {
			if err := client.RefreshBackbones(); err != nil {
				logger.Debug("AvailNZB backbones refresh", "source", "stremio_reload", "err", err)
			}
		}(opts.AvailClient)
	} else {
		s.availClient = nil
		s.availReporter = nil
	}
	s.availNZBIndexerHosts = opts.AvailNZBIndexerHosts
	s.tmdbClient = opts.TMDBClient
	s.tvdbClient = opts.TVDBClient
	s.streamManager = opts.StreamManager
}

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
	onReadError   func(slotPath string, err error) // called when Read returns an error (e.g. 430)
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
