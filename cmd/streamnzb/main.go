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
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/initialization"
	"streamnzb/pkg/server/api"
	"streamnzb/pkg/server/newznab"
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

// firstNonEmpty returns the first non-empty value, or "" when all are empty.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func main() {
	var configPath string
	var logFilePath string
	flag.StringVar(&configPath, "config", "", "Path to configuration file or directory")
	flag.StringVar(&configPath, "c", "", "Path to configuration file or directory (shorthand)")
	flag.StringVar(&logFilePath, "log-file", "", "Path to the log file, or a directory to write streamnzb.log into")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	env.DefaultIndexerUserAgent = "StreamNZB/" + Version

	// Pin the data directory from the -config flag before anything (logger,
	// persistence, builders) resolves it independently. Re-pinned below from
	// cfg.LoadedPath once the config file location is authoritative.
	if p := strings.TrimSpace(configPath); p != "" {
		clean := filepath.Clean(p)
		if fi, err := os.Stat(clean); err == nil && fi.IsDir() {
			paths.SetDataDir(clean)
		} else {
			paths.SetDataDir(filepath.Dir(clean))
		}
	}

	// The flag outranks LOG_PATH. Nothing opens the log file yet: the default
	// destination follows the data directory, which is only authoritative once
	// the config has been located. Startup records buffer until then.
	logPath := firstNonEmpty(logFilePath, env.LogPath())
	logger.Init(env.LogLevel())

	logger.Info("Starting StreamNZB", "version", Version)

	cfg, err := config.LoadWithPath(configPath)
	if err != nil {
		// Open the log file early on this path so the startup records —
		// including this failure — land somewhere before we exit.
		logger.SetLogPath(logPath)
		initialization.WaitForInputAndExit(fmt.Errorf("configuration error: %w", err))
	}
	logger.SetLevel(cfg.LogLevel)
	logger.SetVerboseNNTPLogging(cfg.VerboseNNTPLogging)

	if cfg.MemoryLimitMB > 0 {
		limit := int64(cfg.MemoryLimitMB) * 1024 * 1024
		debug.SetMemoryLimit(limit)
		logger.Info("Go memory limit set", "mb", cfg.MemoryLimitMB)
		// Note: SetMemoryLimit is soft — the runtime may temporarily exceed it. We reserve 150 MB
		// for non-cache (session, NZB, runtime) and use the rest for segment cache so we stay closer to the limit.
	}

	// Precedence for each: environment, then the user's config, then the
	// key baked in at build time.
	availNZBUrl := firstNonEmpty(os.Getenv(env.AvailNZBURL), AvailNZBURL)
	availNZBAPIKey := firstNonEmpty(os.Getenv(env.AvailNZBAPIKey), AvailNZBAPIKey)
	userTMDBKey := firstNonEmpty(os.Getenv(env.TMDBAPIKey), strings.TrimSpace(cfg.TMDBAPIKey))
	userTVDBKey := firstNonEmpty(os.Getenv(env.TVDBAPIKey), strings.TrimSpace(cfg.TVDBAPIKey))
	effectiveTMDBKey := firstNonEmpty(userTMDBKey, TMDBKey)
	effectiveTVDBKey := firstNonEmpty(userTVDBKey, TVDBKey)
	env.SetRuntimeHeaders(cfg.IndexerQueryHeader, cfg.IndexerGrabHeader, cfg.ProviderHeader)

	dataDir := filepath.Dir(cfg.LoadedPath)
	if dataDir == "" || dataDir == "." {
		dataDir, _ = os.Getwd()
	}
	// Authoritative pin: every later paths.GetDataDir() (builders, logger
	// rotation, TVDB state) now agrees with where the config actually lives.
	paths.SetDataDir(dataDir)

	// Open the log file only now: the default destination is the data dir just
	// pinned, and purging has to run against wherever it landed.
	logger.SetLogPath(logPath)
	logger.PurgeOldLogs(cfg.KeepLogFiles)

	// Must precede the first GetManager: the manager is a singleton bound to
	// whatever backend was configured when it opened.
	persistence.Configure(persistence.Settings{
		Backend:     cfg.DatabaseDriver,
		DSN:         cfg.DatabaseURL,
		MigrateData: !cfg.DatabaseSkipMigration,
	})

	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to get state manager: %v", err))
	}
	logger.Info("Database ready", "backend", stateMgr.Backend())

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

	// AvailNZB is opt-in, and registering a key is itself outbound contact —
	// so the bootstrap only runs once the user has turned the integration on.
	// Enabling it later registers through ensureAvailNZBReadyForReload without
	// a restart. See issue #194.
	if config.NormalizeAvailNZBMode(cfg.AvailNZBMode) != "off" {
		availNZBAPIKey, err = availnzb.ResolveAPIKey(stateMgr, availNZBUrl, availNZBAPIKey, availnzb.DefaultAppName)
		if err != nil {
			initialization.WaitForInputAndExit(fmt.Errorf("failed to resolve AvailNZB API key: %w", err))
		}
	} else {
		logger.Debug("AvailNZB key bootstrap skipped", "reason", "not enabled")
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
	sessionManager := session.NewManager(comp.UsenetPool, sessionTTL)
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

	// The configured indexers, re-served as one Newznab API for any
	// Newznab-compatible application. Everything it needs — including whether it is switched on —
	// is read back off the API server per request, so a config reload reaches
	// it without rebinding anything.
	newznabServer := newznab.New(newznab.Options{
		Enabled: func() bool {
			liveCfg := apiServer.Config()
			return liveCfg != nil && liveCfg.NewznabEnabled
		},
		Indexer: apiServer.Indexer,
		Caps:    apiServer.IndexerCaps,
		Config:  apiServer.Config,
		APIKey: func() string {
			if liveCfg := apiServer.Config(); liveCfg != nil {
				return liveCfg.NewznabAPIKey
			}
			return ""
		},
		GrabSecret: func() string {
			if liveCfg := apiServer.Config(); liveCfg != nil {
				return liveCfg.AdminToken
			}
			return ""
		},
		Version: Version,
	})
	mux.Handle(newznab.Mount, newznabServer.Handler())
	if comp.Config.NewznabEnabled {
		logger.Info("Newznab endpoint enabled", "path", newznab.APIPath)
	} else {
		logger.Info("Newznab endpoint disabled")
	}

	{
		if comp.Config.ProxyEnabled {
			proxyServer := proxy.NewServer(comp.Config.ProxyHost, comp.Config.ProxyPort, comp.UsenetPool, comp.Config.ProxyAuthUser, comp.Config.ProxyAuthPass)
			// Registered before the bind attempt so a failed listener still has
			// somewhere to report from: the dashboard reads its status.
			apiServer.SetProxyServer(proxyServer)

			logger.Info("Starting NNTP proxy", "host", comp.Config.ProxyHost, "port", comp.Config.ProxyPort)
			if err := proxyServer.Listen(); err != nil {
				// Optional feature, non-fatal failure: everything else still boots.
				logger.Warn("NNTP proxy is not listening", "err", err)
			} else {
				go func() {
					if err := proxyServer.Serve(); err != nil {
						logger.Error("NNTP proxy stopped serving", "err", err)
					}
				}()
			}
		} else {
			logger.Info("NNTP proxy disabled")
		}
	}

	addonServer := newRebindableServer(mux)
	// Lets a config reload move the addon to a new port without a restart.
	apiServer.SetAddonRebinder(addonServer.rebind)

	logger.Info("Stremio addon server starting", "base_url", comp.Config.AddonBaseURL, "port", comp.Config.AddonPort)
	logger.Info("Note: Access requires stream authentication tokens")

	if err := addonServer.start(comp.Config.AddonPort); err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("server failed: %w", err))
	}
	if err := addonServer.wait(); err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("server failed: %w", err))
	}
}
