package nntp

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type streamBodyChunks struct {
	r    io.Reader
	size int
}

func (r streamBodyChunks) Read(p []byte) (int, error) {
	if len(p) > r.size {
		p = p[:r.size]
	}
	return r.r.Read(p)
}

func TestStreamBodyFraming(t *testing.T) {
	for _, size := range []int{1, 2, 3, 16, 4096} {
		for _, body := range []string{"", "..dot\r\n...dots\r\nnormal\r\n", "..dot\nnormal\n", strings.Repeat("long", 20000) + "\r\n"} {
			wire := "222 42 <upstream>\r\n" + body + ".\r\n223 1 <next>\r\n"
			c := streamBodyClient(streamBodyChunks{strings.NewReader(wire), size})
			pool := &ClientPool{}
			c.pool = pool
			var out bytes.Buffer
			n, err := c.StreamBody("<article>", &out)
			want := "222 0 <article>\r\n" + strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n") + ".\r\n"
			if err != nil || out.String() != want || n != int64(len(want)) {
				t.Fatalf("chunk=%d bodylen=%d: n=%d err=%v output length=%d want=%d", size, len(body), n, err, out.Len(), len(want))
			}
			if pool.totalBytesRead != n {
				t.Fatalf("accounted %d, want %d", pool.totalBytesRead, n)
			}
			if ok, err := c.CheckArticle("next"); err != nil || !ok {
				t.Fatalf("next response: %v, %v", ok, err)
			}
		}
	}
}

type streamBodyFailWriter struct {
	remaining int
	err       error
	written   int
}

func (w *streamBodyFailWriter) Write(p []byte) (int, error) {
	if len(p) <= w.remaining {
		w.remaining -= len(p)
		w.written += len(p)
		return len(p), nil
	}
	n := w.remaining
	w.remaining = 0
	w.written += n
	return n, w.err
}

func TestStreamBodyWriterFailure(t *testing.T) {
	body := strings.Repeat("..data\r\n", 2000)
	header := "222 0 <article>\r\n"
	writeErr := errors.New("downstream closed")
	for _, failure := range []error{nil, writeErr} {
		// Header, first chunk, later chunk, and output terminator failures.
		for _, limit := range []int{0, 5, len(header), len(header) + 13, len(header) + 9000, len(header) + len(body), len(header) + len(body) + 2} {
			c := streamBodyClient(strings.NewReader("222 body\r\n" + body + ".\r\n223 next\r\n"))
			c.pool = &ClientPool{}
			w := &streamBodyFailWriter{remaining: limit, err: failure}
			n, err := c.StreamBody("<article>", w)
			wantErr := failure
			if wantErr == nil {
				wantErr = io.ErrShortWrite
			}
			if !errors.Is(err, wantErr) || n != int64(w.written) || n != int64(limit) {
				t.Fatalf("limit=%d failure=%v: n=%d written=%d err=%v", limit, failure, n, w.written, err)
			}
			if c.pool.totalBytesRead != int64(len(body)) {
				t.Fatalf("accounted %d body bytes, want %d", c.pool.totalBytesRead, len(body))
			}
			if ok, err := c.CheckArticle("next"); err != nil || !ok {
				t.Fatalf("limit=%d next response: %v, %v", limit, ok, err)
			}
		}
	}
}

func TestStreamBodyRejectedStatusDoesNotDrain(t *testing.T) {
	c := streamBodyClient(strings.NewReader("430 missing\r\n223 next\r\n"))
	var out bytes.Buffer
	if n, err := c.StreamBody("missing", &out); err == nil || n != 0 || out.Len() != 0 {
		t.Fatalf("n=%d err=%v out=%q", n, err, out.String())
	}
	if ok, err := c.CheckArticle("next"); err != nil || !ok {
		t.Fatalf("next response: %v, %v", ok, err)
	}
}

type streamBodyReadError struct {
	err   error
	reads int
}

func (r *streamBodyReadError) Read([]byte) (int, error) { r.reads++; return 0, r.err }

func TestStreamBodyUpstreamFailure(t *testing.T) {
	for _, readErr := range []error{io.EOF, errors.New("upstream closed")} {
		r := &streamBodyReadError{err: readErr}
		wire := "222 body\r\n..data\r\n"
		if readErr == io.EOF {
			wire += ".\r" // A terminator cut short must not signal success.
		}
		c := streamBodyClient(io.MultiReader(strings.NewReader(wire), r))
		c.pool = &ClientPool{}
		var out bytes.Buffer
		n, err := c.StreamBody("<article>", &out)
		wantErr := readErr
		if readErr == io.EOF {
			wantErr = io.ErrUnexpectedEOF
		}
		if !errors.Is(err, wantErr) || n != int64(out.Len()) || out.String() != "222 0 <article>\r\n..data\r\n" {
			t.Fatalf("n=%d err=%v out=%q", n, err, out.String())
		}
		if r.reads != 1 {
			t.Fatalf("read failed upstream %d times (cleanup retried)", r.reads)
		}
		if c.pool.totalBytesRead != int64(len("..data\r\n")) {
			t.Fatalf("accounted %d", c.pool.totalBytesRead)
		}
	}
}

func TestStreamBodyDrainIsBounded(t *testing.T) {
	const maxDrain = 10 << 20
	body := strings.Repeat("x", maxDrain+1024) + "\r\n"
	c := streamBodyClient(strings.NewReader("222 body\r\n" + body + ".\r\n223 next\r\n"))
	c.pool = &ClientPool{}
	w := &streamBodyFailWriter{}
	if n, err := c.StreamBody("<article>", w); n != 0 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if c.pool.totalBytesRead != maxDrain {
		t.Fatalf("drained %d, want %d", c.pool.totalBytesRead, maxDrain)
	}
	rest, err := io.ReadAll(c.conn.R)
	if err != nil || string(rest) != body[maxDrain:]+".\r\n223 next\r\n" {
		t.Fatalf("drain consumed beyond its cap: len=%d err=%v", len(rest), err)
	}
}

func TestStreamBodyConnectionCloseInterruptsRead(t *testing.T) {
	server, upstream := net.Pipe()
	defer server.Close()
	defer upstream.Close()
	c := &Client{conn: textproto.NewConn(upstream), netConn: upstream}
	started := make(chan error, 1)
	go func() {
		peer := textproto.NewConn(server)
		_, err := peer.ReadLine()
		if err == nil {
			_, err = server.Write([]byte("222 body\r\n..data\r\n"))
		}
		started <- err
	}()
	done := make(chan error, 1)
	go func() { _, err := c.StreamBody("<article>", streamBodySink{}); done <- err }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("body transfer did not start")
	}
	upstream.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed connection returned success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("body transfer did not stop on close")
	}
}

// A scripted connection isolates forwarding CPU/allocation costs from sockets.
type streamBodyConn struct {
	net.Conn
	r io.Reader
}

func (c *streamBodyConn) Read(p []byte) (int, error)      { return c.r.Read(p) }
func (c *streamBodyConn) Write(p []byte) (int, error)     { return len(p), nil }
func (c *streamBodyConn) SetDeadline(time.Time) error     { return nil }
func (c *streamBodyConn) SetReadDeadline(time.Time) error { return nil }

func streamBodyClient(r io.Reader) *Client {
	c := &streamBodyConn{r: r}
	return &Client{conn: textproto.NewConn(c), netConn: c}
}

type streamBodySink struct{}

func (streamBodySink) Write(p []byte) (int, error) { return len(p), nil }

func BenchmarkStreamBody(b *testing.B) {
	for _, size := range []struct {
		name  string
		lines int
	}{
		{"128KiB", 1024}, {"768KiB", 6144},
	} {
		b.Run(size.name, func(b *testing.B) {
			body := strings.Repeat(strings.Repeat("x", 126)+"\r\n", size.lines)
			wire := []byte("222 0 <article>\r\n" + body + ".\r\n")
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				c := streamBodyClient(bytes.NewReader(wire))
				if _, err := c.StreamBody("<article>", streamBodySink{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
