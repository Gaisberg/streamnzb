package api

import (
	"io"
	"net/http"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/usenet/nntp/proxy"
)

type configPayload struct {
	config.Config
	EnvOverrides []string `json:"env_overrides,omitempty"`
	// ProxyStatus is absent when the proxy is switched off. When it is on, it
	// carries whether the listener actually came up — an enabled proxy that
	// failed to bind is otherwise indistinguishable from a working one.
	ProxyStatus *proxy.Status `json:"proxy_status,omitempty"`
}

func (s *Server) proxyStatus() *proxy.Status {
	s.mu.RLock()
	p := s.proxyServer
	s.mu.RUnlock()

	if p == nil {
		return nil
	}
	status := p.Status()
	return &status
}

func configForAdminAPI(cfg *config.Config) config.Config {
	if cfg == nil {
		return config.Config{}
	}
	out := *cfg
	out.AdminPasswordHash = ""
	out.AdminToken = ""
	return out
}

func redactedConfigForViewer(cfg *config.Config) config.Config {
	return cfg.RedactForAPI()
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleGetConfig(w, r)
		return
	}
	if r.Method == http.MethodPut {
		s.handlePutConfig(w, r)
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	stream, _ := auth.StreamFromContext(r)
	live := s.Config()
	var cfg config.Config
	if stream != nil && stream.Username == s.adminUsername() {
		cfg = configForAdminAPI(live)
	} else {
		cfg = redactedConfigForViewer(live)
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = s.adminUsername()
	}
	if stream != nil && stream.Username == s.adminUsername() {
		writeJSON(w, http.StatusOK, configPayload{Config: cfg, EnvOverrides: config.GetEnvOverrideKeys(), ProxyStatus: s.proxyStatus()})
	} else {
		writeJSON(w, http.StatusOK, cfg)
	}
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can save global configuration", http.MethodPut) {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeSaveStatus(w, "error", "Invalid config data", nil)
		return
	}

	cacheSuffix, fieldErrors, errMessage := s.applyConfigPatch(body)
	if len(fieldErrors) > 0 {
		s.writeSaveStatus(w, "error", "Validation failed", fieldErrors)
		return
	}
	if errMessage != "" {
		s.writeSaveStatus(w, "error", errMessage, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"message": "Configuration saved and reloaded." + cacheSuffix,
	})
}

func (s *Server) writeSaveStatus(w http.ResponseWriter, status, message string, errors map[string]string) {
	code := http.StatusOK
	if status == "error" {
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]interface{}{
		"status":  status,
		"message": message,
		"errors":  errors,
	})
}

func (s *Server) handleClearCache(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Only admin can clear caches", http.MethodPost) {
		return
	}
	if s.strmServer == nil {
		http.Error(w, "Streaming server unavailable", http.StatusServiceUnavailable)
		return
	}
	s.strmServer.ClearSearchCaches()
	blueprintsCleared := 0
	if s.sessionMgr != nil {
		blueprintsCleared = s.sessionMgr.ClearBlueprintCache()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"message": "Search cache and blueprint cache cleared.",
		"details": map[string]interface{}{
			"blueprints_cleared": blueprintsCleared,
		},
	})
}
