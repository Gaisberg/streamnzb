package proxy

import (
	"net"
	"strings"
	"testing"
	"time"
)

// WriteMultiLine must dot-stuff exactly once and terminate with a bare dot.
// Paired with the client-side DotReader un-stuffing, a body line ".hidden"
// must round-trip as "..hidden" on the wire — not "...hidden" (the historical
// double-stuffing bug) and not ".hidden" (which would terminate/truncate).
func TestWriteMultiLineDotStuffing(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	sess := NewSession(serverConn, nil, "", "")
	payload := []string{"first line", ".hidden", "..already stuffed once", "", "last"}

	errCh := make(chan error, 1)
	go func() { errCh <- sess.WriteMultiLine(payload) }()

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for !strings.HasSuffix(string(raw), "\r\n.\r\n") {
		n, err := clientConn.Read(buf)
		if err != nil {
			t.Fatalf("read: %v (raw so far %q)", err, raw)
		}
		raw = append(raw, buf[:n]...)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteMultiLine: %v", err)
	}

	want := "first line\r\n..hidden\r\n...already stuffed once\r\n\r\nlast\r\n.\r\n"
	if string(raw) != want {
		t.Fatalf("wire bytes = %q, want %q", raw, want)
	}
}
