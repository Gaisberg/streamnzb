package nntp

import (
	"fmt"
	"testing"
)

func TestReceiveBufferDecodeAndFraming(t *testing.T) {
	payload, body := bufferCorpus()
	for _, encrypted := range []bool{false, true} {
		for _, size := range []int{4 << 10, 16 << 10, 32 << 10, 64 << 10, 128 << 10} {
			t.Run(fmt.Sprintf("TLS=%t/%dKiB", encrypted, size>>10), func(t *testing.T) {
				c := bufferClient(t, body, encrypted, size)
				for range 2 {
					readBufferArticle(t, c, payload)
					if err := c.Ping(); err != nil {
						t.Fatalf("command after body: %v", err)
					}
				}
				want := int64(2 * len(body))
				if c.pool.totalBytesRead != want {
					t.Fatalf("pool accounted %d bytes, want %d", c.pool.totalBytesRead, want)
				}
				if got := c.pool.usageManager.GetUsage("buffer-bench").TotalBytes; got != want {
					t.Fatalf("provider accounted %d bytes, want %d", got, want)
				}
			})
		}
	}
}

func TestReceiveBufferNewClientAndReconnect(t *testing.T) {
	s := startBenchServer(t, 0)
	c := benchClient(t, s)
	for range 2 {
		if got := c.conn.R.Size(); got != receiveBufferSize(false) {
			t.Fatalf("receive buffer = %d, want %d", got, receiveBufferSize(false))
		}
		if err := c.Ping(); err != nil {
			t.Fatal(err)
		}
		if err := c.Reconnect(); err != nil {
			t.Fatal(err)
		}
	}
}
