package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

// rebindableServer serves one handler on a listener that can be swapped at
// runtime, so an addon-port change applies without a restart. Serving the
// composition root's listener is this layer's job, which is why it lives in
// cmd rather than in pkg/server.
type rebindableServer struct {
	srv   *http.Server
	fatal chan error

	mu       sync.Mutex
	listener net.Listener
	port     int
}

// Timeouts for the addon listener. Streaming makes the usual advice not apply
// directly, so each one is chosen against what actually flows through here.
const (
	// A connection that opens and then dribbles its headers costs nothing to
	// create and holds a slot until it is closed. Nothing legitimate needs
	// anywhere near this long to send a request line and its headers.
	readHeaderTimeout = 20 * time.Second

	// Covers the body as well, so it is bounded by the largest thing a client
	// may send: a manual NZB upload, capped at maxPlayNZBUploadBytes (16 MiB).
	// Five minutes covers that from a slow uplink with room to spare, while
	// still ending a request that has stalled outright.
	readTimeout = 5 * time.Minute

	// Keep-alive connections idle between requests. Without this they are kept
	// until the client drops them, and a browser left open on the dashboard
	// holds several.
	idleTimeout = 2 * time.Minute
)

func newRebindableServer(handler http.Handler) *rebindableServer {
	return &rebindableServer{
		srv: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			IdleTimeout:       idleTimeout,
			// WriteTimeout stays unset on purpose. It is a deadline on the
			// whole response, and a playback response runs for as long as
			// someone is watching — any value here would cut healthy streams
			// mid-film. The per-write deadline that actually matters is applied
			// further in, by writeTimeoutResponseWriter in pkg/server/stremio,
			// which times out a single stalled Write instead of the response.
			WriteTimeout: 0,
		},
		fatal: make(chan error, 1),
	}
}

// start binds the initial port. Unlike rebind, failing here is fatal: there is
// no already-working listener to fall back to.
func (s *rebindableServer) start(port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = listener
	s.port = port
	s.mu.Unlock()
	go s.serve(listener)
	return nil
}

// rebind moves the server to a new port. The new listener is bound *before* the
// old one is dropped, so a port that is already taken leaves the server
// reachable where it was instead of stranding the user with no UI at all.
//
// Closing the old listener only stops it accepting; connections already open on
// it keep working until they drop, which is why the UI survives long enough to
// show the change landed.
func (s *rebindableServer) rebind(port int) error {
	s.mu.Lock()
	unchanged := s.port == port
	s.mu.Unlock()
	if unchanged {
		return nil
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("bind port %d: %w", port, err)
	}

	s.mu.Lock()
	old := s.listener
	s.listener = listener
	s.port = port
	s.mu.Unlock()

	go s.serve(listener)
	if old != nil {
		_ = old.Close()
	}
	logger.Info("Addon server rebound", "port", port)
	return nil
}

// serve runs one listener to completion. A listener closed by rebind returns
// net.ErrClosed, which is the expected end of its life rather than a failure.
func (s *rebindableServer) serve(listener net.Listener) {
	err := s.srv.Serve(listener)
	if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return
	}
	select {
	case s.fatal <- err:
	default:
	}
}

// failures reports listener failures that were not caused by a rebind. It stays
// open for the life of the process, so the caller can select on it alongside a
// shutdown signal.
func (s *rebindableServer) failures() <-chan error { return s.fatal }

// shutdown stops accepting connections and waits for in-flight requests, then
// cuts whatever is still running once ctx expires.
//
// The grace period is for API calls and page loads, not for playback: a video
// response runs for as long as someone is watching, so waiting for those to
// finish on their own would mean never shutting down. Cutting them is what
// stopping the server means anyway — Shutdown leaves established connections
// alone, which is why Close has to follow it.
func (s *rebindableServer) shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if err != nil {
		_ = s.srv.Close()
	}
	return err
}
