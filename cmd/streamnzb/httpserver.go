package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

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

func newRebindableServer(handler http.Handler) *rebindableServer {
	return &rebindableServer{
		srv:   &http.Server{Handler: handler},
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

// wait blocks until a listener fails for a reason other than being rebound.
func (s *rebindableServer) wait() error { return <-s.fatal }
