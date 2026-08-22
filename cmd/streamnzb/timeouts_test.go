package main

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// A playback response outlives any read deadline by hours. Go applies
// ReadTimeout to the connection, not just to the request, so this pins the one
// thing that would make setting it a mistake: a response still being written
// when the deadline passes must not be cut.
func TestReadTimeoutDoesNotCutALongResponse(t *testing.T) {
	const (
		chunks       = 6
		chunkGap     = 40 * time.Millisecond
		shortTimeout = 80 * time.Millisecond
	)

	srv := newRebindableServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for i := 0; i < chunks; i++ {
			time.Sleep(chunkGap)
			if _, err := w.Write([]byte("chunk")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	// The real values are minutes long; shrink them so the response outlasts
	// them within a test's patience. Everything else stays as configured.
	srv.srv.ReadHeaderTimeout = shortTimeout
	srv.srv.ReadTimeout = shortTimeout

	if err := srv.start(0); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.shutdown(ctx)
	})

	resp, err := http.Get(listenURL(t, srv, "/stream"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("the response was cut while it was still being written: %v", err)
	}
	if want := chunks * len("chunk"); len(body) != want {
		t.Fatalf("read %d bytes, want %d — the response was truncated", len(body), want)
	}
}

// The other half of the same setting: a client that opens a connection and
// never finishes its headers has to be dropped rather than held.
func TestReadHeaderTimeoutDropsASilentConnection(t *testing.T) {
	srv := newRebindableServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.srv.ReadHeaderTimeout = 100 * time.Millisecond

	if err := srv.start(0); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.shutdown(ctx)
	})

	conn, err := dialServer(t, srv)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// A request line, then nothing — the shape of a slowloris connection.
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n")); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("the connection was not dropped: %v", err)
	}
}
