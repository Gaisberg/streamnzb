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

func parseDateParam(raw string) (t *time.Time, dateOnly bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false, nil
	}
	// RFC 3339 timestamps allow sub-day windows like the last 24 hours.
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return &ts, false, nil
	}
	// Parse date-only filters in local server time so the selected calendar day
	// aligns with user expectations instead of being interpreted as UTC.
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, false, err
	}
	return &parsed, true, nil
}

// parseStatsRange reads the from/to query params shared by the stats-history
// GET and DELETE paths. A date-only "to" is widened by a day so the range
// covers that whole calendar day; a timestamp "to" is used as-is. A non-empty
// errMsg is ready to hand to http.Error as a 400.
func parseStatsRange(r *http.Request) (from, to *time.Time, errMsg string) {
	from, _, err := parseDateParam(r.URL.Query().Get("from"))
	if err != nil {
		return nil, nil, "Invalid from date (expected YYYY-MM-DD or RFC 3339)"
	}
	to, toDateOnly, err := parseDateParam(r.URL.Query().Get("to"))
	if err != nil {
		return nil, nil, "Invalid to date (expected YYYY-MM-DD or RFC 3339)"
	}
	if to != nil && toDateOnly {
		endExclusive := to.Add(24 * time.Hour)
		to = &endExclusive
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, nil, "Invalid date range"
	}
	return from, to, ""
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
					result, err := fetcher.GetCaps()
					// Failures only: public caps make success meaningless as
					// evidence, so it must not clear a stored verdict.
					if err != nil {
						indexer.ReportHealth(name, err)
					}
					if err == nil {
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

// handleClearNZBAttempts wipes play history for the streams the caller ticked.
// The stream parameter repeats, one value per stream; no value at all — or the
// sentinel "all" — clears every stream, including any whose rows the history
// page never loaded.
func (s *Server) handleClearNZBAttempts(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can clear NZB attempts", http.MethodDelete, http.MethodPost) {
		return
	}
	s.mu.RLock()
	lister := s.attemptLister
	s.mu.RUnlock()
	if lister == nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": 0})
		return
	}

	// An empty scope list means "every stream", which is what DeleteAttempts
	// takes an empty name for.
	scopes := []string{""}
	if named := clearStreamScopes(r.URL.Query()["stream"]); len(named) > 0 {
		scopes = named
	}

	var deleted, diagnostics int64
	for _, scope := range scopes {
		n, err := lister.DeleteAttempts(scope)
		if err != nil {
			logger.Error("DeleteAttempts failed", "stream", scope, "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		deleted += n
		// Diagnostics are decoration on the attempts; leaving them behind would
		// keep cleared requests visible as search-only groups.
		d, err := lister.DeleteSearchDiagnostics(scope)
		if err != nil {
			logger.Error("DeleteSearchDiagnostics failed", "stream", scope, "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		diagnostics += d
	}

	logger.Info("History cleared", "streams", clearScopeLabel(scopes), "attempts", deleted, "diagnostics", diagnostics)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": deleted, "diagnostics_deleted": diagnostics})
}

// clearStreamScopes normalizes the requested stream names, dropping blanks and
// duplicates. "all" among them widens the request back to every stream, which
// it signals by returning nothing.
func clearStreamScopes(values []string) []string {
	seen := make(map[string]bool, len(values))
	scopes := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if v == "all" {
			return nil
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		scopes = append(scopes, v)
	}
	return scopes
}

// clearScopeLabel renders the cleared scope for the log line.
func clearScopeLabel(scopes []string) string {
	if len(scopes) == 1 && scopes[0] == "" {
		return "(all streams)"
	}
	return strings.Join(scopes, ",")
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

		from, to, rangeErr := parseStatsRange(r)
		if rangeErr != "" {
			http.Error(w, rangeErr, http.StatusBadRequest)
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

		from, to, rangeErr := parseStatsRange(r)
		if rangeErr != "" {
			http.Error(w, rangeErr, http.StatusBadRequest)
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
