package nntp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"hash/crc32"
	"math/rand"
	"net"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"streamnzb/pkg/media/decode"
)

// bufferCorpus uses wrapped, escaped and dot-stuffed yEnc lines, including
// line-leading dots. An unwrapped encoder or io.Discard exercises a different
// read pattern than the production decoder.
func bufferCorpus() (payload, body []byte) {
	payload = make([]byte, benchBodyBytes)
	rand.New(rand.NewSource(198)).Read(payload)
	for i := 0; i < len(payload); i += 211 {
		payload[i] = 4
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "=ybegin part=1 total=1 line=128 size=%d name=buffer.bin\r\n=ypart begin=1 end=%d\r\n", len(payload), len(payload))
	col := 0
	for _, v := range payload {
		c := v + 42
		escape := c == 0 || c == '\n' || c == '\r' || c == '='
		width := 1
		if escape {
			width = 2
		}
		if col+width > 128 {
			out.WriteString("\r\n")
			col = 0
		}
		if col == 0 && c == '.' {
			out.WriteByte('.')
		}
		if escape {
			out.WriteByte('=')
			c += 64
		}
		out.WriteByte(c)
		col += width
	}
	fmt.Fprintf(&out, "\r\n=yend size=%d part=1 pcrc32=%08x\r\n", len(payload), crc32.ChecksumIEEE(payload))
	return payload, out.Bytes()
}

func bufferClient(tb testing.TB, body []byte, encrypted bool, size int) *Client {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	var config *tls.Config
	if encrypted {
		// Use a locally trusted test certificate; exercise verified TLS without
		// changing production trust or disabling certificate validation.
		certServer := httptest.NewTLSServer(nil)
		roots := x509.NewCertPool()
		roots.AddCert(certServer.Certificate())
		config = &tls.Config{RootCAs: roots, ServerName: "example.com"}
		ln = tls.NewListener(ln, &tls.Config{Certificates: certServer.TLS.Certificates})
		certServer.Close()
	}
	s := &benchServer{ln: ln, body: body}
	s.wg.Add(1)
	go s.accept()
	tb.Cleanup(func() { ln.Close(); s.wg.Wait() })
	var nc net.Conn
	if encrypted {
		nc, err = tls.Dial("tcp", ln.Addr().String(), config)
	} else {
		nc, err = net.Dial("tcp", ln.Addr().String())
	}
	if err != nil {
		tb.Fatal(err)
	}
	tp := textproto.NewConn(nc)
	tp.R = bufio.NewReaderSize(nc, size)
	c := &Client{conn: tp, netConn: nc, pool: &ClientPool{
		providerName: "buffer-bench",
		usageManager: &ProviderUsageManager{data: make(map[string]*ProviderUsageData)},
	}}
	tb.Cleanup(func() { c.Quit() })
	if err := readGreeting(tp); err != nil {
		tb.Fatal(err)
	}
	return c
}

func readBufferArticle(tb testing.TB, c *Client, payload []byte) {
	tb.Helper()
	r, err := c.Body("buffer")
	if err != nil {
		tb.Fatal(err)
	}
	frame, err := decode.DecodeToBytes(r)
	r.Close()
	if err != nil {
		tb.Fatal(err)
	}
	if !bytes.Equal(frame.Data, payload) {
		tb.Fatal("decoded payload differs")
	}
}

func BenchmarkBodyDecodeReceiveBuffer(b *testing.B) {
	payload, body := bufferCorpus()
	for _, encrypted := range []bool{false, true} {
		for _, size := range []int{4 << 10, 16 << 10, 32 << 10, 64 << 10, 128 << 10} {
			b.Run(fmt.Sprintf("TLS=%t/%dKiB", encrypted, size>>10), func(b *testing.B) {
				c := bufferClient(b, body, encrypted, size)
				b.SetBytes(int64(len(payload)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					readBufferArticle(b, c, payload)
				}
			})
		}
	}
}
