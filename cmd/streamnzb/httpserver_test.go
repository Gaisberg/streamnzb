package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// listenURL returns a dialable URL for a server bound to every interface on an
// ephemeral port, whose address reads back as "[::]:port".
func listenURL(t *testing.T, s *rebindableServer, path string) string {
	t.Helper()
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		t.Fatal("server has no listener")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("could not read the listening port: %v", err)
	}
	return "http://127.0.0.1:" + port + path
}

// dialServer opens a raw TCP connection to the server's listener.
func dialServer(t *testing.T, s *rebindableServer) (net.Conn, error) {
	t.Helper()
	url := listenURL(t, s, "")
	return net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), 5*time.Second)
}

func TestShutdownDrainsThenStopsServing(t *testing.T) {
	srv := newRebindableServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := srv.start(0); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	url := listenURL(t, srv, "/")

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("the server was not serving before shutdown: %v", err)
	}
	resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.shutdown(ctx); err != nil {
		t.Fatalf("an idle server should shut down cleanly, got %v", err)
	}

	if _, err := http.Get(url); err == nil {
		t.Fatal("the server kept accepting connections after shutdown")
	}
}

func TestShutdownCutsResponsesThatOutlastTheGracePeriod(t *testing.T) {
	// Stands in for a playback response: headers are sent, then the body runs
	// for as long as someone is watching. Waiting for it would mean never
	// shutting down, so the grace period has to end in a cut.
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	srv := newRebindableServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}))
	if err := srv.start(0); err != nil {
		t.Fatalf("start returned error: %v", err)
	}

	resp, err := http.Get(listenURL(t, srv, "/stream"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.shutdown(ctx); err == nil {
		t.Fatal("expected shutdown to report that the grace period expired")
	}

	// Shutdown alone leaves established connections running, so this only holds
	// because Close follows it.
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("the long-running response was left open after shutdown")
	}
}
