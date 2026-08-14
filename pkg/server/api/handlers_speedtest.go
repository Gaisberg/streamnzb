package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/speedtest"
)

// speedTestReportsKey stores the last report per provider name. One row per
// provider is all the UI asks for, so this stays in the KV store rather than
// earning a table.
const speedTestReportsKey = "provider_speedtest_reports"

// providerTestRequest tests reachability. Sending the connection fields lets an
// unsaved draft be tested before it is committed; sending only a name reuses the
// saved provider, so the password need not be retyped.
type providerTestRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	UseSSL   *bool  `json:"use_ssl"`
}

type speedTestRequest struct {
	Name string `json:"name"`
	// Quick measures only the configured connection count instead of the ramp.
	Quick bool `json:"quick"`
	// Source is "public" (default) or "library".
	Source string `json:"source"`
}

// handleProviderTest dials a provider and times a DATE round-trip.
func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Admin access required", http.MethodPost) {
		return
	}
	var req providerTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	saved := s.findProvider(req.Name)
	provider := speedtest.Provider{
		Name:     strings.TrimSpace(req.Name),
		Host:     strings.TrimSpace(req.Host),
		Port:     req.Port,
		Username: req.Username,
		Password: req.Password,
	}
	if req.UseSSL != nil {
		provider.UseSSL = *req.UseSSL
	}
	if saved != nil {
		if provider.Host == "" {
			provider.Host = saved.Host
			provider.Port = saved.Port
			provider.UseSSL = saved.UseSSL
		}
		if provider.Username == "" {
			provider.Username = saved.Username
		}
		if provider.Password == "" {
			provider.Password = saved.Password
		}
	}
	if provider.Host == "" {
		writeJSONError(w, http.StatusBadRequest, "Host is required")
		return
	}
	if provider.Port <= 0 {
		provider.Port = 563
	}

	writeJSON(w, http.StatusOK, speedtest.TestConnection(r.Context(), provider))
}

// handleProviderSpeedTest runs a throughput benchmark (POST) or returns the
// stored reports (GET). The POST stays open for the whole run — bounded by the
// speed test's own wall-clock ceiling — while each completed step is pushed over
// the websocket so the dialog fills in as the ramp progresses.
func (s *Server) handleProviderSpeedTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Admin access required", http.MethodGet, http.MethodPost) {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.loadSpeedTestReports())
		return
	}

	var req speedTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	saved := s.findProvider(req.Name)
	if saved == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("Unknown provider %q", req.Name))
		return
	}

	source, err := s.speedTestSource(req.Source)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	provider := speedtest.Provider{
		Name:        saved.Name,
		Host:        saved.Host,
		Port:        saved.Port,
		Username:    saved.Username,
		Password:    saved.Password,
		Connections: saved.Connections,
		UseSSL:      saved.UseSSL,
	}

	report, err := s.speedTester.Run(r.Context(), speedtest.Request{
		Provider:     provider,
		Quick:        req.Quick,
		Source:       source,
		UsageManager: s.providerUsageManager(),
		OnStep: func(step speedtest.StepResult) {
			s.broadcastSpeedTestProgress(map[string]interface{}{"provider": provider.Name, "step": step})
		},
		OnSample: func(sample speedtest.Sample) {
			s.broadcastSpeedTestProgress(map[string]interface{}{"provider": provider.Name, "sample": sample})
		},
	})
	if err != nil {
		if errors.Is(err, speedtest.ErrBusy) {
			writeJSONError(w, http.StatusConflict, "Another speed test is already running. Providers share one uplink, so they are tested one at a time.")
			return
		}
		if r.Context().Err() != nil {
			return
		}
		logger.Error("Provider speed test failed", "provider", provider.Name, "err", err)
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	if active := len(s.sessionMgr.GetActiveSessions()); active > 0 {
		note := fmt.Sprintf("%d playback session(s) were active during the test, so it competed with them for bandwidth", active)
		if report.Warning == "" {
			report.Warning = note
		} else {
			report.Warning = note + "; " + report.Warning
		}
	}

	s.storeSpeedTestReport(report)
	writeJSON(w, http.StatusOK, report)
}

// findProvider returns the saved provider with the given name, or nil.
func (s *Server) findProvider(name string) *config.Provider {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config == nil {
		return nil
	}
	for i := range s.config.Providers {
		if strings.EqualFold(strings.TrimSpace(s.config.Providers[i].Name), name) {
			provider := s.config.Providers[i]
			return &provider
		}
	}
	return nil
}

// speedTestSource resolves the requested corpus. The public test NZB is the
// default because a fixed post is the only corpus comparable across providers;
// the library option trades that away for articles of realistic age.
func (s *Server) speedTestSource(kind string) (speedtest.ArticleSource, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "public":
		return speedtest.PublicNZBSource{}, nil
	case "library":
		return s.libraryArticleSource()
	default:
		return nil, fmt.Errorf("unknown speed test source %q", kind)
	}
}

// libraryArticleSource picks the most recently used library release that still
// has its NZB payload.
func (s *Server) libraryArticleSource() (speedtest.ArticleSource, error) {
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	if mgr == nil || mgr.LibraryStore() == nil {
		return nil, errors.New("the library is not available")
	}
	items, _, err := mgr.LibraryStore().GetFilteredItems("", "", false, "", 0, 25)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item == nil || item.NZBSizeBytes <= 0 {
			continue
		}
		full, err := mgr.LibraryStore().GetItem(item.ID)
		if err != nil || full == nil || len(full.NZBData) == 0 {
			continue
		}
		parsed, err := nzb.Parse(bytes.NewReader(full.NZBData))
		if err != nil {
			continue
		}
		return speedtest.StaticSource{NZB: parsed, Label: "Library: " + full.ReleaseTitle}, nil
	}
	return nil, errors.New("no library release with a stored NZB is available to test with")
}

// providerUsageManager returns the shared usage tracker so benchmark bytes are
// counted against the account that actually paid for them.
func (s *Server) providerUsageManager() *nntp.ProviderUsageManager {
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	if mgr == nil {
		return nil
	}
	usage, err := nntp.GetProviderUsageManager(mgr)
	if err != nil {
		logger.Debug("Speed test usage manager unavailable", "err", err)
		return nil
	}
	return usage
}

func (s *Server) loadSpeedTestReports() map[string]*speedtest.Report {
	reports := map[string]*speedtest.Report{}
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	if mgr == nil {
		return reports
	}
	if _, err := mgr.Get(speedTestReportsKey, &reports); err != nil {
		logger.Debug("Loading speed test reports failed", "err", err)
		return map[string]*speedtest.Report{}
	}
	return reports
}

func (s *Server) storeSpeedTestReport(report *speedtest.Report) {
	if report == nil || report.Provider == "" {
		return
	}
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	if mgr == nil {
		return
	}
	reports := s.loadSpeedTestReports()
	reports[report.Provider] = report
	if err := mgr.Set(speedTestReportsKey, reports); err != nil {
		logger.Error("Storing speed test report failed", "provider", report.Provider, "err", err)
	}
}

// broadcastSpeedTestProgress pushes a finished ramp step or a live throughput
// sample to admin clients. Provider names are admin-only detail, so viewer
// sessions are skipped. Sends are best-effort: a client whose buffer is full
// drops the sample rather than stalling the run.
func (s *Server) broadcastSpeedTestProgress(payload map[string]interface{}) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := WSMessage{Type: "speedtest_progress", Payload: encoded}
	admin := s.adminUsername()

	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	for client := range s.clients {
		if client.stream == nil || client.stream.Username != admin {
			continue
		}
		trySendWS(client, msg)
	}
}
