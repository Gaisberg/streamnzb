package proxy

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/usenet/nntp"
	"streamnzb/pkg/usenet/pool"
)

// Script backend replies over real NNTP connections so the test exercises
// status parsing, pool failover, and the reply a downloader actually receives.
func proxyTestBackend(t *testing.T, response, groupResponse string) *nntp.ClientPool {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				fmt.Fprint(conn, "200 test backend\r\n")
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					command := strings.Fields(scanner.Text())
					if len(command) == 0 {
						return
					}
					switch command[0] {
					case "AUTHINFO":
						fmt.Fprint(conn, "281 authenticated\r\n")
					case "QUIT":
						return
					case "GROUP":
						fmt.Fprint(conn, groupResponse)
					default:
						if response == "disconnect" {
							return
						}
						fmt.Fprint(conn, response)
					}
				}
			}()
		}
	}()
	backend := nntp.NewClientPool("127.0.0.1", ln.Addr().(*net.TCPAddr).Port, false, ln.Addr().String(), "", 1)
	t.Cleanup(backend.Shutdown)
	if response == "unavailable" {
		backend.Shutdown()
	}
	return backend
}

func TestArticleCommandsOnlyReportMissingAfterDefinitiveMisses(t *testing.T) {
	for _, command := range []struct{ verb, success string }{
		{"BODY", "222 0 <article>\r\ndata\r\n.\r\n"},
		{"ARTICLE", "220 0 <article>\r\nHeader: value\r\n\r\ndata\r\n.\r\n"},
		{"HEAD", "221 0 <article>\r\nHeader: value\r\n.\r\n"},
		{"STAT", "223 0 <article>\r\n"},
	} {
		for _, tc := range []struct {
			name                 string
			responses            []string
			backup, groupFailure bool
			want                 string
		}{
			{"missing", []string{"430 missing\r\n", "430 missing\r\n"}, false, false, "430 "},
			{"temporary", []string{"503 temporary failure\r\n"}, false, false, "400 "},
			{"disconnected", []string{"disconnect"}, false, false, "400 "},
			{"missing_then_temporary", []string{"430 missing\r\n", "503 temporary failure\r\n"}, false, false, "400 "},
			{"temporary_then_missing", []string{"503 temporary failure\r\n", "430 missing\r\n"}, false, false, "400 "},
			{"missing_then_unavailable", []string{"430 missing\r\n", "unavailable"}, false, false, "400 "},
			{"unavailable_then_missing", []string{"unavailable", "430 missing\r\n"}, false, false, "400 "},
			{"temporary_then_success", []string{"503 temporary failure\r\n", command.success}, false, false, command.success[:4]},
			{"backup_temporary", []string{"430 missing\r\n", "503 temporary failure\r\n"}, true, false, "400 "},
			{"backup_unavailable", []string{"430 missing\r\n", "unavailable"}, true, false, "400 "},
			{"backup_success", []string{"430 missing\r\n", command.success}, true, false, command.success[:4]},
			{"group_failure", []string{"430 missing\r\n"}, false, true, "400 "},
		} {
			t.Run(command.verb+"/"+tc.name, func(t *testing.T) {
				var providers []pool.ProviderConfig
				for i, response := range tc.responses {
					providers = append(providers, pool.ProviderConfig{
						ID: fmt.Sprintf("provider-%d", i), Priority: i,
						IsBackup:   tc.backup && i == len(tc.responses)-1,
						ClientPool: proxyTestBackend(t, response, "411 group unavailable\r\n"),
					})
				}
				usenet, err := pool.NewPool(&pool.Config{Providers: providers})
				if err != nil {
					t.Fatal(err)
				}
				client, server := net.Pipe()
				defer client.Close()
				defer server.Close()
				sess := NewSession(server, usenet, "", "")
				if tc.groupFailure {
					sess.currentGroup = "alt.binaries.test"
				}
				done := make(chan error, 1)
				go func() {
					if err := sess.HandleCommand(command.verb, []string{"<article>"}); err != nil {
						done <- err
						return
					}
					done <- sess.Flush()
				}()
				client.SetReadDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 1024)
				n, err := client.Read(buf)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if got := string(buf[:n]); !strings.HasPrefix(got, tc.want) {
					t.Fatalf("reply = %q, want prefix %q", got, tc.want)
				}
				if sess.ShouldQuit() != (tc.want == "400 ") {
					t.Fatalf("ShouldQuit = %v after %q", sess.ShouldQuit(), string(buf[:n]))
				}
			})
		}
	}
}

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
