package initialization

import (
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/indexer"
	"streamnzb/pkg/usenet/nntp"
)

// oldPoolGrace is how long the previous database's pools stay open after a
// swap. sql.DB.Close waits on in-use connections rather than aborting them, so
// closing inline would block on any query still streaming rows.
const oldPoolGrace = 30 * time.Second

// DatabaseSettingsFromConfig maps the config fields onto persistence settings.
func DatabaseSettingsFromConfig(cfg *config.Config) persistence.Settings {
	if cfg == nil {
		return persistence.Settings{}
	}
	return persistence.Settings{
		Backend:     cfg.DatabaseDriver,
		DSN:         cfg.DatabaseURL,
		MigrateData: !cfg.DatabaseSkipMigration,
	}
}

// ReloadDatabase points the state manager at the database cfg describes,
// without restarting.
//
// The ordering carries the work. Several subsystems hold state in memory that
// they read from the database, and provider usage flushes its copy on a timer.
// Flushing before the swap keeps counters recorded since the last tick (and
// puts them in the file the importer is about to read); reloading after it
// stops those subsystems writing the old database's totals into the new one.
func ReloadDatabase(stateMgr *persistence.StateManager, cfg *config.Config) error {
	if stateMgr == nil {
		return nil
	}
	if err := indexer.FlushUsageManager(); err != nil {
		logger.Warn("Failed to flush indexer usage before database swap", "err", err)
	}
	if err := nntp.FlushProviderUsage(); err != nil {
		logger.Warn("Failed to flush provider usage before database swap", "err", err)
	}

	closeOld, err := stateMgr.Reopen(DatabaseSettingsFromConfig(cfg), paths.GetDataDir())
	if err != nil {
		return err
	}

	if err := indexer.ReloadUsageManager(); err != nil {
		logger.Warn("Failed to reload indexer usage after database swap", "err", err)
	}
	if err := nntp.ReloadProviderUsage(); err != nil {
		logger.Warn("Failed to reload provider usage after database swap", "err", err)
	}
	HydrateMetricsFromState(stateMgr)

	time.AfterFunc(oldPoolGrace, closeOld)
	logger.Info("Database reloaded", "backend", stateMgr.Backend())
	return nil
}
