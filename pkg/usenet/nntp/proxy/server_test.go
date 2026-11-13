package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/usenet/pool"
)

func TestBodyUpstreamResetDoesNotInjectStatusIntoArticle(t *testing.T) {
	body := strings.Repeat("article data\r\n", 20000)
	delivered := make(chan struct{})
	backend := proxyTestBackend(t, "222 body\r\n"+body, "", func(conn net.Conn) {
		// Reset only after the downloader has received a body chunk, so this
		// reproduces a failure during streaming, not a rejected BODY command.
		select {
		case <-delivered:
		case <-time.After(5 * time.Second):
			return
		}
		conn.(*net.TCPConn).SetLinger(0)
	})
	usenet, err := pool.NewPool(&pool.Config{Providers: []pool.ProviderConfig{{ID: "resetting", ClientPool: backend}}})
	if err != nil {
		t.Fatal(err)
	}
	client, proxy := net.Pipe()
	defer client.Close()
	defer proxy.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	srv := NewServer("", 0, usenet, "", "")
	done := make(chan struct{})
	go func() { defer close(done); srv.handleConnection(proxy) }()
	r := bufio.NewReader(client)
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(client, "BODY <article>\r\n"); err != nil {
		t.Fatal(err)
	}
	first := make([]byte, clientWriteBufferSize)
	if _, err := io.ReadFull(r, first); err != nil {
		t.Fatal(err)
	}
	close(delivered)
	rest, err := io.ReadAll(r)
	wire := string(first) + string(rest)
	if !strings.HasPrefix(wire, "222 0 <article>\r\n") {
		t.Fatalf("missing BODY status: %.100q", wire)
	}
	if strings.Contains(wire, "\r\n500 ") || strings.Contains(wire, "\r\n400 ") || strings.Contains(wire, "\r\n430 ") {
		t.Fatalf("proxy inserted a status line into the interrupted article: tail %q", wire[len(wire)-200:])
	}
	if err != nil {
		t.Fatalf("proxy did not close after upstream reset: %v", err)
	}
	<-done
	if strings.HasSuffix(wire, ".\r\n") {
		t.Fatal("proxy marked an interrupted article complete")
	}
}

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
