package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"streamnzb/pkg/core/app"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/server/api"
	"streamnzb/pkg/server/stremio"
	"streamnzb/pkg/session"
)

// shutdownGrace is how long in-flight HTTP work gets to finish before the
// remaining connections are cut. Long enough for an API call or a page load,
// short enough to stay inside Docker's default ten-second stop timeout with
// room for the teardown that follows.
const shutdownGrace = 5 * time.Second

// shutdownSignals returns a channel that receives the first termination signal.
// SIGTERM is what `docker stop` and systemd send; SIGINT is Ctrl-C in a
// terminal. Both mean the same thing here.
func shutdownSignals() <-chan os.Signal {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals
}

// gracefulShutdown tears down everything that owns a connection, a goroutine,
// or state that has not been written yet.
//
// The order is the substance of this function:
//
//   - HTTP first, so nothing new arrives while the rest is going away.
//   - The addon's background sweeps and the API server's stats loop next: they
//     read the library and the search caches and write metric rows, and must not
//     still be running when the database closes below.
//   - The NNTP proxy next, for the same reason.
//   - Sessions before provider pools: closing a session ends its playback
//     stream, which hands the NNTP connections back instead of leaving them for
//     a pool that is about to disappear underneath them.
//   - Provider pools before persistence: ClientPool.Shutdown flushes each
//     provider's usage counters through the state manager, so closing the
//     database first would silently drop the last window of usage — the numbers
//     the dashboard and any metered-account limits are read from.
//   - Persistence last, once nothing else can still be writing to it.
//
// Every step logs its own failure and none of them stop the ones after it: a
// process on its way out should get as far through this list as it can.
func gracefulShutdown(
	addonServer *rebindableServer,
	stremioServer *stremio.Server,
	apiServer *api.Server,
	sessionManager *session.Manager,
	application *app.App,
	stateMgr *persistence.StateManager,
) {
	logger.Info("Shutting down", "grace", shutdownGrace)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := addonServer.shutdown(ctx); err != nil {
		// Expected whenever a playback stream is still running: the grace period
		// ran out and the connection was cut rather than waited on.
		logger.Info("Cut remaining connections at the end of the grace period", "err", err)
	}

	stremioServer.Shutdown()
	// Waits for the stats loop, which persists metrics every thirty seconds and
	// would otherwise still be writing when the database closes below.
	apiServer.Shutdown()

	if proxyServer := apiServer.ProxyServer(); proxyServer != nil {
		if err := proxyServer.Stop(); err != nil {
			logger.Warn("Failed to stop the NNTP proxy", "err", err)
		}
	}

	sessionManager.Shutdown()

	if comp := application.Components(); comp != nil {
		for name, pool := range comp.ProviderPools {
			logger.Debug("Shutting down provider pool", "provider", name)
			pool.Shutdown()
		}
	}

	if stateMgr != nil {
		if err := stateMgr.Flush(); err != nil {
			logger.Warn("Failed to flush pending state", "err", err)
		}
		if err := stateMgr.Close(); err != nil {
			logger.Warn("Failed to close the database", "err", err)
		}
	}

	logger.Info("Shutdown complete")
}
