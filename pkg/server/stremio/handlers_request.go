package stremio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/parser"
)

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

		trimmedPath := strings.TrimPrefix(path, "/")
		parts := strings.SplitN(trimmedPath, "/", 2)
		effectivePath := path
		if len(parts) == 2 && parts[0] != "" {
			effectivePath = "/" + parts[1]
		}
		isDebugPlayRoute := strings.HasPrefix(path, "/debug/play") || strings.HasPrefix(effectivePath, "/debug/play")
		isStremioRoute := effectivePath == "/manifest.json" || effectivePath == FailoverOrderPath || strings.HasPrefix(effectivePath, "/stream/") || strings.HasPrefix(effectivePath, "/play/") || strings.HasPrefix(effectivePath, "/next/") || strings.HasPrefix(effectivePath, "/debug/play")

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

				} else if isStremioRoute && !isDebugPlayRoute {

					logger.Warn("Unauthorized request - invalid stream token", "path", path, "remote", r.RemoteAddr)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}

			} else if isStremioRoute && !isDebugPlayRoute {

				logger.Warn("Unauthorized request - stream authentication unavailable", "path", path, "remote", r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		} else if isStremioRoute && !isDebugPlayRoute {

			logger.Warn("Unauthorized request - Stremio route requires stream token", "path", path, "remote", r.RemoteAddr)
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
	if stream == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	logger.Debug("Failover order request", "stream", stream.Username, "method", r.Method, "url", r.URL.Path)
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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
	s.sessionManager.SetStreamFailoverOrder(token, streamKey, order)
	sample := ""
	if len(order) > 0 {
		sample = order[0]
		if len(order) > 1 {
			sample += " ... " + order[len(order)-1]
		}
	}
	logger.Info("Failover order stored", "stream", token, "streamKey", streamKey, "slots", len(order), "sample", sample)
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

func buildAIOStreamDescription(contentTitle, releaseTitle, indexerName string) string {
	firstLine := strings.TrimSpace(contentTitle)
	secondLine := strings.TrimSpace(releaseTitle)
	if firstLine == "" {
		firstLine = secondLine
	}
	lines := make([]string, 0, 3)
	if firstLine != "" {
		lines = append(lines, firstLine)
	}
	if secondLine != "" && secondLine != firstLine {
		lines = append(lines, secondLine)
	}
	if name := strings.TrimSpace(indexerName); name != "" {
		lines = append(lines, "🔍 "+name)
	}
	if len(lines) == 0 {
		return "StreamNZB"
	}
	return strings.Join(lines, "\n")
}

func buildStreamsFromPlaylist(list *playlistResult, key StreamSlotKey, streamName, baseURL string, showAll bool) []Stream {
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
			contentTitle := ""
			if list.Params != nil {
				contentTitle = list.Params.ContentTitle
			}
			desc := buildAIOStreamDescription(contentTitle, relTitle, indexerNameFromRelease(cand.Release))
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
			nextFailoverId := "streamnzb-next-" + nextPath
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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"addon":  "streamnzb",
	})
}
