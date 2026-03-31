package api

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/paths"
)

type configPayload struct {
	config.Config
	EnvOverrides []string `json:"env_overrides,omitempty"`
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
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	var cfg config.Config
	if stream != nil && stream.Username == s.config.GetAdminUsername() {
		cfg = configForAdminAPI(s.config)
	} else {
		cfg = redactedConfigForViewer(s.config)
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = s.config.GetAdminUsername()
	}
	w.Header().Set("Content-Type", "application/json")
	if stream != nil && stream.Username == s.config.GetAdminUsername() {
		envKeys := config.GetEnvOverrideKeys()
		json.NewEncoder(w).Encode(configPayload{Config: cfg, EnvOverrides: envKeys})
	} else {
		json.NewEncoder(w).Encode(cfg)
	}
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Only admin can save global configuration", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeSaveStatus(w, "error", "Invalid config data", nil)
		return
	}

	s.mu.RLock()
	currentCfg := s.config
	currentLoadedPath := s.config.LoadedPath
	s.mu.RUnlock()

	currentJSON, err := json.Marshal(currentCfg)
	if err != nil {
		s.writeSaveStatus(w, "error", "Failed to prepare config update", nil)
		return
	}

	var newCfg config.Config
	if err := json.Unmarshal(currentJSON, &newCfg); err != nil {
		s.writeSaveStatus(w, "error", "Failed to prepare config update", nil)
		return
	}
	if err := json.Unmarshal(body, &newCfg); err != nil {
		s.writeSaveStatus(w, "error", "Invalid config data", nil)
		return
	}

	plan := validationPlanFromPatch(body, currentCfg, &newCfg)
	fieldErrors := s.validateConfigWithPlan(&newCfg, plan)
	if len(fieldErrors) > 0 {
		s.writeSaveStatus(w, "error", "Validation failed", fieldErrors)
		return
	}

	config.CopyEnvOverridesFrom(currentCfg, &newCfg)
	newCfg.AdminPasswordHash = currentCfg.AdminPasswordHash
	newCfg.AdminToken = currentCfg.AdminToken
	newCfg.AdminMustChangePassword = currentCfg.AdminMustChangePassword
	newCfg.Devices = currentCfg.Devices
	if newCfg.AdminUsername == "" {
		newCfg.AdminUsername = currentCfg.GetAdminUsername()
	}
	newCfg.ApplyProviderDefaults()
	if currentLoadedPath == "" {
		currentLoadedPath = filepath.Join(paths.GetDataDir(), "config.json")
	}
	newCfg.LoadedPath = currentLoadedPath
	s.mu.Lock()
	s.config = &newCfg
	s.mu.Unlock()
	if err := s.config.Save(); err != nil {
		s.writeSaveStatus(w, "error", "Failed to save config: "+err.Error(), nil)
		return
	}
	s.reloadConfigAsync(&newCfg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Configuration saved and reloaded.",
	})
}

func (s *Server) writeSaveStatus(w http.ResponseWriter, status, message string, errors map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	if status == "error" {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  status,
		"message": message,
		"errors":  errors,
	})
}

func (s *Server) handleClearCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Only admin can clear caches", http.StatusForbidden)
		return
	}
	if s.strmServer == nil {
		http.Error(w, "Streaming server unavailable", http.StatusServiceUnavailable)
		return
	}
	s.strmServer.ClearSearchCaches()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Search cache cleared.",
	})
}
