package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/server/api"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/server/web"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
	"streamnzb/pkg/usenet/nntp/proxy"

	"github.com/joho/godotenv"
)

var (
	AvailNZBURL    = "https://snzb.stream"
	AvailNZBAPIKey = ""

	TMDBKey = ""

	TVDBKey = ""

	Version = "dev"
)

func main() {

	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	env.DefaultIndexerUserAgent = "StreamNZB/" + Version

	logger.Init(env.LogLevel())

	logger.Info("Starting StreamNZB", "version", Version)

	cfg, err := config.Load()
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("configuration error: %w", err))
	}
	logger.SetLevel(cfg.LogLevel)
	logger.PurgeOldLogs(cfg.KeepLogFiles)

	if cfg.MemoryLimitMB > 0 {
		limit := int64(cfg.MemoryLimitMB) * 1024 * 1024
		debug.SetMemoryLimit(limit)
		logger.Info("Go memory limit set", "mb", cfg.MemoryLimitMB)
		// Note: SetMemoryLimit is soft — the runtime may temporarily exceed it. We reserve 150 MB
		// for non-cache (session, NZB, runtime) and use the rest for segment cache so we stay closer to the limit.
	}

	availNZBUrl := os.Getenv(env.AvailNZBURL)
	if availNZBUrl == "" {
		availNZBUrl = AvailNZBURL
	}
	availNZBAPIKey := os.Getenv(env.AvailNZBAPIKey)
	if availNZBAPIKey == "" {
		availNZBAPIKey = strings.TrimSpace(cfg.AvailNZBAPIKey)
	}
	if availNZBAPIKey == "" {
		availNZBAPIKey = AvailNZBAPIKey
	}
	tmdbKey := os.Getenv(env.TMDBAPIKey)
	if tmdbKey == "" {
		tmdbKey = strings.TrimSpace(cfg.TMDBAPIKey)
	}
	if tmdbKey == "" {
		tmdbKey = TMDBKey
	}
	tvdbKey := os.Getenv(env.TVDBAPIKey)
	if tvdbKey == "" {
		tvdbKey = strings.TrimSpace(cfg.TVDBAPIKey)
	}
	if tvdbKey == "" {
		tvdbKey = TVDBKey
	}
	env.SetRuntimeHeaders(cfg.IndexerQueryHeader, cfg.IndexerGrabHeader, cfg.ProviderHeader)

	dataDir := filepath.Dir(cfg.LoadedPath)
	if dataDir == "" || dataDir == "." {
		dataDir, _ = os.Getwd()
	}

	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to get state manager: %v", err))
	}

	{
		var stateAdmin struct {
			PasswordHash       string `json:"password_hash"`
			MustChangePassword bool   `json:"must_change_password"`
		}
		if found, _ := stateMgr.Get("admin", &stateAdmin); found {
			cfg.AdminPasswordHash = stateAdmin.PasswordHash
			cfg.AdminMustChangePassword = stateAdmin.MustChangePassword
			if cfg.AdminToken == "" {
				if tok, err := auth.GenerateToken(); err == nil {
					cfg.AdminToken = tok
				}
			}
			if err := cfg.Save(); err != nil {
				logger.Warn("Failed to save config after admin migration", "err", err)
			} else {
				stateMgr.Delete("admin")
				stateMgr.Delete("admin_sessions")
				_ = stateMgr.Flush()
				logger.Info("Migrated admin credentials from state.json to config.json")
			}
		}
	}

	{
		if !cfg.ResetLegacyDeviceState && len(cfg.Devices) == 0 {
			var stateDevices map[string]*auth.Device
			if found, _ := stateMgr.Get("devices", &stateDevices); found && len(stateDevices) > 0 {
				cfg.Devices = make(map[string]*config.DeviceEntry)
				for k, d := range stateDevices {
					if d == nil {
						continue
					}
					ov := d.IndexerOverrides
					if ov == nil {
						ov = make(map[string]config.IndexerSearchConfig)
					}
					cfg.Devices[k] = &config.DeviceEntry{
						Username:         d.Username,
						Token:            d.Token,
						IndexerOverrides: ov,
					}
				}
				if err := cfg.Save(); err != nil {
					logger.Warn("Failed to save config after devices migration", "err", err)
				} else {
					stateMgr.Delete("devices")
					stateMgr.Delete("users")
					_ = stateMgr.Flush()
					logger.Info("Migrated devices from state.json to config.json")
				}
			}
		}
	}

	if cfg.AvailNZBMode != "disabled" {
		availNZBAPIKey, err = availnzb.ResolveStartupAPIKey(stateMgr, availNZBUrl, availNZBAPIKey)
		if err != nil {
			initialization.WaitForInputAndExit(fmt.Errorf("failed to resolve AvailNZB API key: %w", err))
		}
	} else {
		logger.Debug("AvailNZB key bootstrap skipped", "reason", "disabled mode")
	}

	application := app.New()
	comp, err := application.Build(cfg, app.BuildOpts{
		AvailNZBURL:    availNZBUrl,
		AvailNZBAPIKey: availNZBAPIKey,
		TMDBAPIKey:     tmdbKey,
		TVDBAPIKey:     tvdbKey,
		DataDir:        dataDir,
		SessionTTL:     30 * time.Minute,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to build components: %w", err))
	}

	sessionManager := session.NewManager(comp.StreamingPools, comp.UsenetPool, 30*time.Minute)
	logger.Info("Session manager initialized", "ttl", 30*time.Minute)

	saveConfig := func() error { return cfg.Save() }
	deviceManager, err := auth.NewDeviceManagerFromConfig(cfg, saveConfig)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize device manager: %v", err))
	}
	stremioServer, err := stremio.NewServer(&stremio.ServerOptions{
		Config:               comp.Config,
		BaseURL:              comp.Config.AddonBaseURL,
		Port:                 comp.Config.AddonPort,
		Indexer:              comp.Indexer,
		Validator:            comp.Validator,
		SessionManager:       sessionManager,
		TriageService:        comp.Triage,
		AvailClient:          comp.AvailClient,
		AvailNZBIndexerHosts: comp.AvailNZBIndexerHosts,
		TMDBClient:           comp.TMDBClient,
		TVDBClient:           comp.TVDBClient,
		StreamManager:        deviceManager,
		Version:              Version,
		AttemptRecorder:      stateMgr,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize Stremio server: %v", err))
	}

	apiServer := api.NewServerWithApp(comp.Config, comp.ProviderPools, sessionManager, stremioServer, comp.Indexer, deviceManager, application, availNZBUrl, availNZBAPIKey, tmdbKey, tvdbKey)
	apiServer.SetIndexerCaps(comp.IndexerCaps)
	apiServer.SetAttemptLister(stateMgr)
	if cfg.AvailNZBMode != "disabled" && strings.TrimSpace(availNZBAPIKey) == "" && strings.TrimSpace(availNZBUrl) != "" {
		logger.Info("AvailNZB API key registration deferred", "mode", cfg.AvailNZBMode)
		go func() {
			registeredKey, err := availnzb.RegisterAndPersistAPIKey(stateMgr, availNZBUrl, availnzb.DefaultAppName)
			if err != nil {
				if errors.Is(err, availnzb.ErrRegisterKeyIPAlreadyHasKey) {
					return
				}
				logger.Warn("AvailNZB background key registration failed", "err", err)
				return
			}

			application.SetAvailNZBAPIKey(registeredKey)
			apiServer.SetAvailNZBAPIKey(registeredKey)

			current := application.Components()
			if current != nil && current.AvailClient != nil {
				if err := current.AvailClient.RefreshBackbones(); err != nil {
					logger.Debug("AvailNZB backbones refresh", "source", "background_registration", "err", err)
				}
			}

			logger.Info("AvailNZB background key registration completed")
		}()
	}

	stremioServer.SetWebHandler(web.Handler())
	stremioServer.SetAPIHandler(apiServer.Handler())
	stremioServer.SetOnAttemptRecorded(apiServer.BroadcastNZBAttemptsUpdate)

	mux := http.NewServeMux()
	stremioServer.SetupRoutes(mux)

	mux.Handle("/api/", apiServer.Handler())

	{
		if comp.Config.ProxyEnabled {
			proxyServer, err := proxy.NewServer(comp.Config.ProxyHost, comp.Config.ProxyPort, comp.UsenetPool, comp.Config.ProxyAuthUser, comp.Config.ProxyAuthPass)
			if err != nil {
				initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize NNTP proxy: %v", err))
			}

			apiServer.SetProxyServer(proxyServer)

			go func() {
				logger.Info("Starting NNTP proxy", "host", comp.Config.ProxyHost, "port", comp.Config.ProxyPort)
				if err := proxyServer.Start(); err != nil {
					initialization.WaitForInputAndExit(fmt.Errorf("nntp proxy failed: %w", err))
				}
			}()
		} else {
			logger.Info("NNTP proxy disabled")
		}
	}

	addr := fmt.Sprintf(":%d", comp.Config.AddonPort)

	logger.Info("Stremio addon server starting", "base_url", comp.Config.AddonBaseURL, "port", comp.Config.AddonPort)
	logger.Info("Note: Access requires device authentication tokens")

	if err := http.ListenAndServe(addr, mux); err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("server failed: %w", err))
	}
}
