package proxy

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
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
	go func() {
		if err := sess.WriteMultiLine(payload); err != nil {
			errCh <- err
			return
		}
		errCh <- sess.Flush()
	}()

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

// countingConn records how many Write syscalls a session makes on the wire.
type countingConn struct {
	net.Conn
	writes atomic.Int64
}

func (c *countingConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(p)
}

// A response must stay in the session buffer until Flush: NNTP clients block on
// the reply, so an unflushed session is a hung one.
func TestWriteLineBuffersUntilFlush(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	counted := &countingConn{Conn: serverConn}
	sess := NewSession(counted, nil, "", "")

	if err := sess.WriteLine("211 0 1 1 alt.binaries.test"); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}
	if got := counted.writes.Load(); got != 0 {
		t.Fatalf("WriteLine hit the socket %d times before Flush, want 0", got)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- sess.Flush() }()

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(buf[:n]), "211 0 1 1 alt.binaries.test\r\n"; got != want {
		t.Fatalf("wire bytes = %q, want %q", got, want)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// Article bodies arrive as thousands of short yEnc lines. Relaying them one
// socket write per line was the throughput ceiling of the proxy, so assert the
// buffer actually batches them.
func TestBodyRelayBatchesSocketWrites(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	counted := &countingConn{Conn: serverConn}
	sess := NewSession(counted, nil, "", "")

	const lines = 6000
	const line = "=ybegin padding to roughly one yEnc line of body payload aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	wantBytes := int64(lines) * int64(len(line)+2)

	errCh := make(chan error, 1)
	go func() {
		for i := 0; i < lines; i++ {
			if err := sess.WriteLine(line); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- sess.Flush()
	}()

	_ = clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 64*1024)
	var read int64
	for read < wantBytes {
		n, err := clientConn.Read(buf)
		if err != nil {
			t.Fatalf("read after %d/%d bytes: %v", read, wantBytes, err)
		}
		read += int64(n)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("relay: %v", err)
	}

	// 64 KiB buffer over ~800 KiB of body: a dozen or so writes, not one per line.
	maxWrites := int64(wantBytes/clientWriteBufferSize) + 2
	if got := counted.writes.Load(); got > maxWrites {
		t.Fatalf("relayed %d lines in %d socket writes, want at most %d", lines, got, maxWrites)
	}
}

// Running out of backend connections says nothing about the article. Answering
// 430 would have the downloader record it as missing and start a repair, so the
// proxy must fail open with a transient 400 instead.
func TestBodyReportsTransientWhenNoConnectionAvailable(t *testing.T) {
	// Priority above the proxy's maxPriority: the provider is never selectable,
	// so GetConnection fails immediately rather than dialing anything.
	usenet, err := pool.NewPool(&pool.Config{
		Providers: []pool.ProviderConfig{{
			ID:         "unreachable",
			Priority:   1000,
			ClientPool: nntp.NewClientPool("127.0.0.1", 119, false, "", "", 1),
		}},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	sess := NewSession(serverConn, usenet, "", "")

	errCh := make(chan error, 1)
	go func() {
		if err := sess.HandleCommand("BODY", []string{"<abc@news>"}); err != nil {
			errCh <- err
			return
		}
		errCh <- sess.Flush()
	}()

	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("BODY: %v", err)
	}

	got := string(buf[:n])
	if !strings.HasPrefix(got, "400 ") {
		t.Fatalf("reply = %q, want a transient 400 (430 would poison the article)", got)
	}
	if !sess.ShouldQuit() {
		t.Fatal("session stayed open after 400; RFC 3977 requires the server to close")
	}
}
