package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"streamnzb/pkg/initialization"
	nextapp "streamnzb/pkg/next/app"
	"streamnzb/pkg/next/playback"
	"streamnzb/pkg/next/preset"
	"streamnzb/pkg/next/reporting"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
	nntpproxy "streamnzb/pkg/usenet/nntp/proxy"
)

var (
	Version = "dev"
	TMDBKey string
	TVDBKey string
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	env.DefaultIndexerUserAgent = "StreamNZB/" + Version

	logger.Init(env.LogLevel())

	cfg := config.Load()
	if TMDBKey != "" {
		cfg.TMDBAPIKey = TMDBKey
	}
	if TVDBKey != "" {
		cfg.TVDBAPIKey = TVDBKey
	}

	// Purge old rotated log files based on KEEP_LOG_FILES setting.
	logger.PurgeOldLogs(cfg.KeepLogFiles)

	// Initialize data store (.dat file for runtime state)
	dataDir := paths.GetDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Warn("Failed to create data directory", "dir", dataDir, "err", err)
	}
	dataStore := config.NewDataStore(filepath.Join(dataDir, "streamnzb.dat"))

	// Auto-generate admin token if not already persisted
	adminToken := dataStore.AdminToken()
	if adminToken == "" {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err == nil {
			hash := sha256.Sum256(bytes)
			adminToken = hex.EncodeToString(hash[:])
			if err := dataStore.SetAdminToken(adminToken); err != nil {
				logger.Warn("Failed to persist admin token", "err", err)
			}
		}
	}

	builder := coreapp.New()
	components, err := builder.Build(cfg, coreapp.BuildOpts{
		DataStore:  dataStore,
		SessionTTL: 30 * time.Minute,
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

	application := nextapp.New(nextapp.Options{
		Version:  Version,
		Preset:   presetService,
		Playback: playbackService,
		AuthenticateToken: func(token string) (*auth.Device, error) {
			if adminToken != "" && token == adminToken {
				return &auth.Device{
					Username: "admin",
					Token:    adminToken,
				}, nil
			}
			return nil, fmt.Errorf("invalid token")
		},
	})

	// Start NNTP proxy if a usenet pool is available
	if components.UsenetPool != nil && cfg.ProxyPort > 0 {
		proxyServer, err := nntpproxy.NewServer(cfg.ProxyHost, cfg.ProxyPort, components.UsenetPool, cfg.ProxyAuthUser, cfg.ProxyAuthPass)
		if err != nil {
			logger.Warn("Failed to initialize NNTP proxy", "err", err)
		} else {
			go func() {
				if err := proxyServer.Start(); err != nil {
					logger.Error("NNTP proxy stopped", "err", err)
				}
			}()
			defer proxyServer.Stop()
		}
	}

	logger.Info("Starting StreamNZB", "version", Version)
	host  := cfg.AddonBaseURL
	logger.Info("Manifest URL", "url", host+adminToken+"/manifest.json")
	if err := application.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		initialization.WaitForInputAndExit(fmt.Errorf("streamnzb failed: %w", err))
	}
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
