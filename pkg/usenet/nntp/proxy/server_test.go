package proxy

import (
	"net"
	"strings"
	"testing"
)

// TestListenReportsFailureInsteadOfBinding covers the half of issue #192 that
// existing configs still hit: the port is unavailable, and the caller has to
// be able to keep booting and tell the user why.
func TestListenReportsFailureInsteadOfBinding(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer occupied.Close()

	port := occupied.Addr().(*net.TCPAddr).Port
	srv := NewServer("127.0.0.1", port, nil, "", "")

	if err := srv.Listen(); err == nil {
		t.Fatal("Listen() on an occupied port = nil, want an error")
	} else if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("Listen() error = %q, want it to name the port conflict", err)
	}

	status := srv.Status()
	if status.Listening {
		t.Error("Status().Listening = true after a failed bind")
	}
	if status.Error == "" {
		t.Error("Status().Error is empty after a failed bind; the settings card has nothing to show")
	}
}

// TestServeWithoutListenDoesNotPanic guards the ordering contract between the
// two halves of the old Start(): Serve on an unbound server is a caller bug,
// but it must surface as an error rather than a nil-listener panic.
func TestServeWithoutListenDoesNotPanic(t *testing.T) {
	srv := NewServer("127.0.0.1", 0, nil, "", "")

	if err := srv.Serve(); err == nil {
		t.Fatal("Serve() before Listen() = nil, want an error")
	}
}

func TestListenThenStopReleasesThePort(t *testing.T) {
	srv := NewServer("127.0.0.1", 0, nil, "", "")

	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if status := srv.Status(); !status.Listening || status.Error != "" {
		t.Errorf("Status() = %+v, want a listening proxy with no error", status)
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve() }()

	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Serve() returned %v after Stop(), want a clean exit", err)
	}
	if srv.Status().Listening {
		t.Error("Status().Listening = true after Stop()")
	}
}
