package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"streamnzb/pkg/auth"
	coreapp "streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/initialization"
	nextapp "streamnzb/pkg/next/app"
	"streamnzb/pkg/next/playback"
	"streamnzb/pkg/next/preset"
	"streamnzb/pkg/next/reporting"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

var Version = "dev"

var (
	AvailNZBURL    = "https://snzb.stream"
	AvailNZBAPIKey = ""
	TMDBKey        = ""
	TVDBKey        = ""
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	env.DefaultIndexerUserAgent = "StreamNZB/" + Version

	logger.Init(env.LogLevel())
	preexistingAdminToken := existingConfigAdminToken(filepath.Join(paths.GetDataDir(), "config.json"))

	cfg, err := config.Load()
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("configuration error: %w", err))
	}
	logger.SetLevel(cfg.LogLevel)

	availNZBURL := os.Getenv(env.AvailNZBURL)
	if availNZBURL == "" {
		availNZBURL = AvailNZBURL
	}
	availNZBAPIKey := os.Getenv(env.AvailNZBAPIKey)
	if availNZBAPIKey == "" {
		availNZBAPIKey = AvailNZBAPIKey
	}
	tmdbKey := os.Getenv(env.TMDBAPIKey)
	if tmdbKey == "" {
		tmdbKey = TMDBKey
	}
	tvdbKey := os.Getenv(env.TVDBAPIKey)
	if tvdbKey == "" {
		tvdbKey = TVDBKey
	}

	dataDir := filepath.Dir(cfg.LoadedPath)
	if dataDir == "" || dataDir == "." {
		dataDir, _ = os.Getwd()
	}

	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to get state manager: %v", err))
	}
	migrateLegacyAuthConfig(cfg, stateMgr, preexistingAdminToken != "")

	builder := coreapp.New()
	components, err := builder.Build(cfg, coreapp.BuildOpts{
		AvailNZBURL:    availNZBURL,
		AvailNZBAPIKey: availNZBAPIKey,
		TMDBAPIKey:     tmdbKey,
		TVDBAPIKey:     tvdbKey,
		DataDir:        dataDir,
		SessionTTL:     30 * time.Minute,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to build shared components: %w", err))
	}

	sessionManager := session.NewManager(
		components.StreamingPools,
		components.UsenetPool,
		30*time.Minute,
		components.SegmentCacheBudget,
	)
	defer sessionManager.Shutdown()

	addr := os.Getenv("STREAMNZB_ADDR")
	if addr == "" {
		addr = os.Getenv("STREAMNZB_NEXT_ADDR")
	}
	if addr == "" {
		addr = nextapp.DefaultListenAddr
	}

	presetService := preset.NewServiceWithOptions(preset.Options{
		AvailNZBMode:         cfg.AvailNZBMode,
		SearchConfig:         cfg,
		Indexer:              components.Indexer,
		Validator:            components.Validator,
		AvailClient:          components.AvailClient,
		AvailNZBIndexerHosts: components.AvailNZBIndexerHosts,
		TMDBClient:           components.TMDBClient,
		TVDBClient:           components.TVDBClient,
	})

	var availReporter *availnzb.Reporter
	if components.AvailClient != nil && components.Validator != nil {
		availReporter = availnzb.NewReporter(components.AvailClient, components.Validator)
	}
	reportingService := reporting.NewServiceWithOptions(reporting.Options{
		Enabled:  cfg.AvailNZBMode != "disabled",
		Reporter: availReporter,
	})

	playbackService := playback.NewServiceWithOptions(playback.Options{
		DownloadHostAPIKeys: buildPlaybackDownloadHostAPIKeys(cfg),
		SessionManager:      sessionManager,
		Indexer:             components.Indexer,
		Reporting:           reportingService,
	})
	saveConfig := func() error { return cfg.Save() }
	deviceManager, err := auth.NewDeviceManagerFromConfig(cfg, saveConfig)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize device manager: %v", err))
	}

	application := nextapp.New(nextapp.Options{
		Version:  Version,
		Preset:   presetService,
		Playback: playbackService,
		AuthenticateToken: func(token string) (*auth.Device, error) {
			return deviceManager.AuthenticateToken(token, cfg.GetAdminUsername(), cfg.AdminToken)
		},
	})

	logger.Info("Starting StreamNZB", "version", Version, "addr", addr)
	logger.Info("Note: Access requires authentication tokens")
	if err := application.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		initialization.WaitForInputAndExit(fmt.Errorf("streamnzb failed: %w", err))
	}
}

func existingConfigAdminToken(configPath string) string {
	file, err := os.Open(configPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	var cfg struct {
		AdminToken string `json:"admin_token"`
	}
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.AdminToken)
}

func migrateLegacyAuthConfig(cfg *config.Config, stateMgr *persistence.StateManager, preferExistingAdminToken bool) {
	if cfg == nil || stateMgr == nil {
		return
	}

	adminUsername := cfg.GetAdminUsername()
	var stateAdmin struct {
		PasswordHash       string `json:"password_hash"`
		MustChangePassword bool   `json:"must_change_password"`
		Token              string `json:"token"`
	}
	adminFound, err := stateMgr.Get("admin", &stateAdmin)
	if err != nil {
		logger.Warn("Failed to load legacy admin state", "err", err)
	}

	legacyDevices := loadLegacyDeviceState(stateMgr)
	legacyAdminToken := ""
	if !preferExistingAdminToken {
		legacyAdminToken = strings.TrimSpace(stateAdmin.Token)
		if legacyAdminToken == "" {
			legacyAdminToken = legacyAdminTokenFromDevices(legacyDevices, adminUsername)
		}
	}

	if adminFound || (legacyAdminToken != "" && cfg.AdminToken != legacyAdminToken) {
		if adminFound {
			cfg.AdminPasswordHash = stateAdmin.PasswordHash
			cfg.AdminMustChangePassword = stateAdmin.MustChangePassword
		}
		if legacyAdminToken != "" {
			cfg.AdminToken = legacyAdminToken
		}
		if err := cfg.Save(); err != nil {
			logger.Warn("Failed to save config after admin migration", "err", err)
		} else {
			if adminFound {
				_ = stateMgr.Delete("admin")
				_ = stateMgr.Delete("admin_sessions")
				_ = stateMgr.Flush()
				logger.Info("Migrated admin credentials from state.json to config.json")
			}
			if legacyAdminToken != "" {
				logger.Info("Migrated legacy admin token to config.json")
			}
		}
	}

	if len(cfg.Devices) != 0 || len(legacyDevices) == 0 {
		return
	}

	cfg.Devices = make(map[string]*config.DeviceEntry)
	for key, device := range legacyDevices {
		if device == nil || isLegacyAdminDevice(key, device, adminUsername) {
			continue
		}
		ov := device.IndexerOverrides
		if ov == nil {
			ov = make(map[string]config.IndexerSearchConfig)
		}
		cfg.Devices[key] = &config.DeviceEntry{
			Username:         device.Username,
			Token:            device.Token,
			IndexerOverrides: ov,
			StreamIDs:        append([]string(nil), device.StreamIDs...),
		}
	}
	if err := cfg.Save(); err != nil {
		logger.Warn("Failed to save config after devices migration", "err", err)
		return
	}
	_ = stateMgr.Delete("devices")
	_ = stateMgr.Delete("users")
	_ = stateMgr.Flush()
	logger.Info("Migrated devices from state.json to config.json")
}

func loadLegacyDeviceState(stateMgr *persistence.StateManager) map[string]*auth.Device {
	if stateMgr == nil {
		return nil
	}
	var stateDevices map[string]*auth.Device
	if found, err := stateMgr.Get("devices", &stateDevices); err == nil && len(stateDevices) > 0 && found {
		return stateDevices
	}
	if found, err := stateMgr.Get("users", &stateDevices); err == nil && len(stateDevices) > 0 && found {
		return stateDevices
	}
	return nil
}

func legacyAdminTokenFromDevices(devices map[string]*auth.Device, adminUsername string) string {
	for key, device := range devices {
		if !isLegacyAdminDevice(key, device, adminUsername) {
			continue
		}
		return strings.TrimSpace(device.Token)
	}
	return ""
}

func isLegacyAdminDevice(key string, device *auth.Device, adminUsername string) bool {
	if device == nil {
		return false
	}
	if adminUsername == "" {
		adminUsername = "admin"
	}
	key = strings.TrimSpace(key)
	username := strings.TrimSpace(device.Username)
	return key == adminUsername || key == "admin" || username == adminUsername || username == "admin"
}

func buildPlaybackDownloadHostAPIKeys(cfg *config.Config) []playback.DownloadHostAPIKey {
	if cfg == nil || len(cfg.Indexers) == 0 {
		return nil
	}

	auths := make([]playback.DownloadHostAPIKey, 0, len(cfg.Indexers))
	for _, idx := range cfg.Indexers {
		apiKey := strings.TrimSpace(idx.APIKey)
		if apiKey == "" {
			continue
		}
		idxURL, err := url.Parse(strings.TrimSpace(idx.URL))
		if err != nil {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(idxURL.Hostname()))
		if host == "" {
			continue
		}
		auths = append(auths, playback.DownloadHostAPIKey{Host: host, APIKey: apiKey})
	}
	return auths
}
