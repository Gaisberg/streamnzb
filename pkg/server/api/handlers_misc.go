package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/metrics"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/services/useragent"
)

type persistedStatsResponse struct {
	Providers []persistence.ProviderMetric `json:"providers"`
	Indexers  []persistence.IndexerMetric  `json:"indexers"`
}

type performanceStatsResponse struct {
	StreamSummary metrics.StreamAPIStatsSummary    `json:"stream_summary"`
	TTFFSummary   metrics.PlaybackTTFFStatsSummary `json:"ttff_summary"`
	RecentStreams []metrics.StreamAPISample        `json:"recent_streams"`
	RecentTTFF    []metrics.PlaybackTTFFSample     `json:"recent_ttff"`
}

func parseDateParam(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// Parse date-only filters in local server time so the selected calendar day
	// aligns with user expectations instead of being interpreted as UTC.
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Server) handleGetIndexerCaps(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	s.mu.RLock()
	caps := s.indexerCaps
	s.mu.RUnlock()
	if caps == nil {
		caps = make(map[string]*indexer.Caps)
	}
	writeJSON(w, http.StatusOK, caps)
}

// handleLatestUserAgents reports the current release version of every tool
// StreamNZB can spoof, so the Settings page can lift headers that indexers have
// started rejecting as stale. Partial results are a success: the response
// carries whichever sources answered plus the ones that did not.
func (s *Server) handleLatestUserAgents(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Forbidden", http.MethodGet) {
		return
	}
	result, err := useragent.Latest(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRefreshIndexerCaps(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Forbidden", http.MethodPost) {
		return
	}
	s.mu.RLock()
	idx := s.indexer
	s.mu.RUnlock()
	caps := make(map[string]*indexer.Caps)
	if agg, ok := idx.(*indexer.Aggregator); ok {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, i := range agg.Indexers {
			if c, ok := i.(indexer.IndexerWithCaps); ok {
				wg.Add(1)
				go func(name string, fetcher indexer.IndexerWithCaps) {
					defer wg.Done()
					if result, err := fetcher.GetCaps(); err == nil {
						mu.Lock()
						caps[name] = result
						mu.Unlock()
					}
				}(i.Name(), c)
			}
		}
		wg.Wait()
	}
	s.mu.Lock()
	s.indexerCaps = caps
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, caps)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Forbidden", http.MethodPost) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	s.sessionMgr.DeleteSession(req.ID)
	logger.Debug("API closing session", "id", req.ID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Forbidden", http.MethodPost) {
		return
	}
	go func() {

		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Start()
		os.Exit(0)
	}()
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "Restarting..."})
}

func (s *Server) handleDownloadLogs(w http.ResponseWriter, r *http.Request) {
	// The log is redacted of API keys but not of indexer hostnames, release
	// titles, client addresses or stream names, so it is the admin's operating
	// history and not a device's business.
	if !s.requireAdmin(w, r, "Only admin can download logs", http.MethodGet) {
		return
	}

	logPath := logger.GetCurrentLogPath()
	info, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Log file not found", http.StatusNotFound)
			return
		}
		logger.Error("Log download stat failed", "path", logPath, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}

	filename := filepath.Base(logPath)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	http.ServeFile(w, r, logPath)
}

func (s *Server) handleNZBAttempts(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can read NZB attempts", http.MethodGet) {
		return
	}
	s.mu.RLock()
	lister := s.attemptLister
	s.mu.RUnlock()
	if lister == nil {
		writeJSON(w, http.StatusOK, []persistence.NZBAttempt{})
		return
	}
	q := r.URL.Query()
	opts := persistence.ListAttemptsOptions{
		ContentType: q.Get("content_type"),
		ContentID:   q.Get("content_id"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.Offset = n
		}
	}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = &t
		}
	}
	list, err := lister.ListAttempts(opts)
	if err != nil {
		logger.Error("ListAttempts failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []persistence.NZBAttempt{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleSearchDiagnostics serves the persisted search-funnel records the
// history page attaches to its request groups.
func (s *Server) handleSearchDiagnostics(w http.ResponseWriter, r *http.Request) {
	// stream_name is a caller-supplied filter, so without this a device could
	// read the search history of any other device by naming it.
	if !s.requireAdmin(w, r, "Only admin can read search diagnostics", http.MethodGet) {
		return
	}
	s.mu.RLock()
	lister := s.attemptLister
	s.mu.RUnlock()
	if lister == nil {
		writeJSON(w, http.StatusOK, []persistence.SearchDiagnostic{})
		return
	}
	q := r.URL.Query()
	opts := persistence.ListSearchDiagnosticsOptions{
		StreamName:  q.Get("stream_name"),
		ContentType: q.Get("content_type"),
		ContentID:   q.Get("content_id"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	list, err := lister.ListSearchDiagnostics(opts)
	if err != nil {
		logger.Error("ListSearchDiagnostics failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []persistence.SearchDiagnostic{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePersistedStats(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	resp := persistedStatsResponse{
		Providers: []persistence.ProviderMetric{},
		Indexers:  []persistence.IndexerMetric{},
	}
	if mgr == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	providers, err := mgr.GetLatestProviderMetrics()
	if err != nil {
		logger.Error("GetLatestProviderMetrics failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	indexers, err := mgr.GetLatestIndexerMetrics()
	if err != nil {
		logger.Error("GetLatestIndexerMetrics failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	resp.Providers = providers
	resp.Indexers = indexers
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()

	switch r.Method {
	case http.MethodGet:
		resp := persistedStatsResponse{
			Providers: []persistence.ProviderMetric{},
			Indexers:  []persistence.IndexerMetric{},
		}
		if mgr == nil {
			writeJSON(w, http.StatusOK, resp)
			return
		}

		from, err := parseDateParam(r.URL.Query().Get("from"))
		if err != nil {
			http.Error(w, "Invalid from date (expected YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		to, err := parseDateParam(r.URL.Query().Get("to"))
		if err != nil {
			http.Error(w, "Invalid to date (expected YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		if to != nil {
			endExclusive := to.Add(24 * time.Hour)
			to = &endExclusive
		}
		if from != nil && to != nil && !from.Before(*to) {
			http.Error(w, "Invalid date range", http.StatusBadRequest)
			return
		}

		providers, err := mgr.GetProviderMetricsSummary(from, to)
		if err != nil {
			logger.Error("GetProviderMetricsSummary failed", "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		indexers, err := mgr.GetIndexerMetricsSummary(from, to)
		if err != nil {
			logger.Error("GetIndexerMetricsSummary failed", "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		resp.Providers = providers
		resp.Indexers = indexers

		writeJSON(w, http.StatusOK, resp)

	case http.MethodDelete:
		// Reading history stays open to any authenticated caller, as the other
		// stats routes are. Erasing it does not.
		if !s.requireAdmin(w, r, "Only admin can delete statistics") {
			return
		}
		if mgr == nil {
			http.Error(w, "State manager unavailable", http.StatusInternalServerError)
			return
		}

		targetType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
		name := strings.TrimSpace(r.URL.Query().Get("name"))

		from, err := parseDateParam(r.URL.Query().Get("from"))
		if err != nil {
			http.Error(w, "Invalid from date (expected YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		to, err := parseDateParam(r.URL.Query().Get("to"))
		if err != nil {
			http.Error(w, "Invalid to date (expected YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		if to != nil {
			endExclusive := to.Add(24 * time.Hour)
			to = &endExclusive
		}
		if from != nil && to != nil && !from.Before(*to) {
			http.Error(w, "Invalid date range", http.StatusBadRequest)
			return
		}

		switch targetType {
		case "provider":
			if err := mgr.DeleteProviderMetrics(name, from, to); err != nil {
				logger.Error("DeleteProviderMetrics failed", "name", name, "err", err)
				http.Error(w, "Failed to delete provider metrics", http.StatusInternalServerError)
				return
			}
		case "indexer":
			if err := mgr.DeleteIndexerMetrics(name, from, to); err != nil {
				logger.Error("DeleteIndexerMetrics failed", "name", name, "err", err)
				http.Error(w, "Failed to delete indexer metrics", http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "Invalid type parameter (expected 'provider' or 'indexer')", http.StatusBadRequest)
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Deleted %s statistics for %q in given range", targetType, name),
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePerformanceStats(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	collector := metrics.Default()
	resp := performanceStatsResponse{
		StreamSummary: collector.GetStreamAPISummary(),
		TTFFSummary:   collector.GetPlaybackTTFFSummary(),
		RecentStreams: collector.GetStreamAPISamples(),
		RecentTTFF:    collector.GetPlaybackTTFFSamples(),
	}
	writeJSON(w, http.StatusOK, resp)
}
