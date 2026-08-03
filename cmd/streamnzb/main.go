package main

import (
	"flag"
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
	var configPath string
	flag.StringVar(&configPath, "config", "", "Path to configuration file or directory")
	flag.StringVar(&configPath, "c", "", "Path to configuration file or directory (shorthand)")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	env.DefaultIndexerUserAgent = "StreamNZB/" + Version

	logger.Init(env.LogLevel())

	logger.Info("Starting StreamNZB", "version", Version)

	cfg, err := config.LoadWithPath(configPath)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("configuration error: %w", err))
	}
	logger.SetLevel(cfg.LogLevel)
	logger.SetVerboseNNTPLogging(cfg.VerboseNNTPLogging)
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
		availNZBAPIKey = AvailNZBAPIKey
	}
	userTMDBKey := os.Getenv(env.TMDBAPIKey)
	if userTMDBKey == "" {
		userTMDBKey = strings.TrimSpace(cfg.TMDBAPIKey)
	}
	userTVDBKey := os.Getenv(env.TVDBAPIKey)
	if userTVDBKey == "" {
		userTVDBKey = strings.TrimSpace(cfg.TVDBAPIKey)
	}
	effectiveTMDBKey := userTMDBKey
	if effectiveTMDBKey == "" {
		effectiveTMDBKey = TMDBKey
	}
	effectiveTVDBKey := userTVDBKey
	if effectiveTVDBKey == "" {
		effectiveTVDBKey = TVDBKey
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
		if !cfg.ResetLegacyStreamState {
			var stateStreams map[string]*auth.Stream
			if found, _ := stateMgr.Get("devices", &stateStreams); found && len(stateStreams) > 0 {
				if cfg.Streams == nil {
					cfg.Streams = make(map[string]*config.StreamEntry)
				}
				for k, stream := range stateStreams {
					if stream == nil {
						continue
					}
					if _, exists := cfg.Streams[k]; exists {
						continue
					}
					ov := stream.IndexerOverrides
					if ov == nil {
						ov = make(map[string]config.IndexerSearchConfig)
					}
					cfg.Streams[k] = &config.StreamEntry{
						Username:         stream.Username,
						Token:            stream.Token,
						IndexerOverrides: ov,
					}
				}
				if err := cfg.Save(); err != nil {
					logger.Warn("Failed to save config after streams migration", "err", err)
				} else {
					stateMgr.Delete("devices")
					stateMgr.Delete("users")
					_ = stateMgr.Flush()
					logger.Info("Migrated streams from state.json to config.json")
				}
			}
		}
	}

	if config.NormalizeAvailNZBMode(cfg.AvailNZBMode) != "off" {
		availNZBAPIKey, err = availnzb.ResolveAPIKey(stateMgr, availNZBUrl, availNZBAPIKey, availnzb.DefaultAppName)
		if err != nil {
			initialization.WaitForInputAndExit(fmt.Errorf("failed to resolve AvailNZB API key: %w", err))
		}
	} else {
		logger.Debug("AvailNZB key bootstrap skipped", "reason", "disabled mode")
	}

	application := app.New()
	comp, err := application.Build(cfg, app.BuildOpts{
		AvailNZBURL:        availNZBUrl,
		AvailNZBAPIKey:     availNZBAPIKey,
		TMDBAPIKey:         userTMDBKey,
		TVDBAPIKey:         userTVDBKey,
		FallbackTMDBAPIKey: TMDBKey,
		FallbackTVDBAPIKey: TVDBKey,
		DataDir:            dataDir,
		SessionTTL:         30 * time.Minute,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to build components: %w", err))
	}

	sessionTTL := time.Duration(cfg.EffectiveSessionTTLSeconds()) * time.Second
	postPlaybackTTL := time.Duration(cfg.EffectiveSessionPostPlaybackTTLSeconds()) * time.Second
	sessionManager := session.NewManager(comp.StreamingPools, comp.UsenetPool, sessionTTL)
	sessionManager.SetPostPlaybackEvictTTL(postPlaybackTTL)
	logger.Info("Session manager initialized", "ttl", sessionTTL, "post_playback_ttl", postPlaybackTTL)

	saveConfig := func() error { return cfg.Save() }
	streamManager, err := auth.NewStreamManagerFromConfig(cfg, saveConfig)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize device manager: %v", err))
	}
	stremioServer, err := stremio.NewServer(&stremio.ServerOptions{
		Config:               comp.Config,
		BaseURL:              comp.Config.AddonBaseURL,
		Port:                 comp.Config.AddonPort,
		Indexer:              comp.Indexer,
		QueryCache:           comp.QueryCache,
		Validator:            comp.Validator,
		SessionManager:       sessionManager,
		TriageService:        comp.Triage,
		AvailClient:          comp.AvailClient,
		AvailNZBIndexerHosts: comp.AvailNZBIndexerHosts,
		TMDBClient:           comp.TMDBClient,
		TVDBClient:           comp.TVDBClient,
		StreamManager:        streamManager,
		Version:              Version,
		AttemptRecorder:      stateMgr,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize Stremio server: %v", err))
	}

	apiServer := api.NewServerWithApp(comp.Config, comp.ProviderPools, sessionManager, stremioServer, comp.Indexer, streamManager, application, availNZBUrl, availNZBAPIKey, effectiveTMDBKey, effectiveTVDBKey)
	apiServer.SetIndexerCaps(comp.IndexerCaps)
	apiServer.SetAttemptLister(stateMgr)
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
	logger.Info("Note: Access requires stream authentication tokens")

	if err := http.ListenAndServe(addr, mux); err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("server failed: %w", err))
	}
}
