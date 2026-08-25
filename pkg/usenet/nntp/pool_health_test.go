package nntp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/core/health"
)

// fakeNNTPListener speaks the same dialect as tools/fake-nntp, on loopback so
// Windows never raises a firewall prompt for the test binary.
//
//	mode "accept":     AUTHINFO USER/PASS with pass "good" succeeds
//	mode "reject":     every AUTHINFO PASS answers 481
//	mode "conn-limit": the greeting itself answers 502
func fakeNNTPListener(t *testing.T, mode string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				say := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
				if mode == "conn-limit" {
					say("502 too many connections for your account")
					return
				}
				say("200 fake-nntp ready")
				r := bufio.NewScanner(c)
				for r.Scan() {
					line := strings.ToUpper(strings.TrimSpace(r.Text()))
					switch {
					case strings.HasPrefix(line, "AUTHINFO USER"):
						say("381 password required")
					case strings.HasPrefix(line, "AUTHINFO PASS"):
						if mode == "reject" || !strings.HasSuffix(line, "GOOD") {
							say("481 authentication failed")
						} else {
							say("281 welcome")
						}
					case line == "QUIT":
						say("205 bye")
						return
					default:
						say("500 command not supported")
					}
				}
			}(conn)
		}
	}()

	addr := ln.Addr().String()
	h, p, _ := net.SplitHostPort(addr)
	portN, _ := strconv.Atoi(p)
	return h, portN
}

func testHealthRegistry(t *testing.T) *health.Registry {
	t.Helper()
	reg, err := health.Init(nil)
	if err != nil {
		t.Fatalf("health.Init: %v", err)
	}
	t.Cleanup(func() { reg.Retain(health.KindProvider, nil) })
	return reg
}

func TestPoolDialRecordsAuthRejectionAndRecovery(t *testing.T) {
	reg := testHealthRegistry(t)

	host, port := fakeNNTPListener(t, "accept")
	pool := NewClientPool(host, port, false, "user", "bad", 2)
	pool.SetProviderName("fake-provider")
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Wrong password: the dial's AUTHINFO fails with 481 and must block.
	if _, err := pool.Get(ctx); err == nil {
		t.Fatal("expected auth failure with the wrong password")
	}
	if !reg.Blocked(health.KindProvider, "fake-provider") {
		t.Fatalf("a 481 must block the provider, got %+v", reg.Snapshot())
	}
	rec, _ := reg.Lookup(health.KindProvider, "fake-provider")
	if rec.Reason != health.ReasonAuthFailed {
		t.Fatalf("reason = %q, want %q", rec.Reason, health.ReasonAuthFailed)
	}

	// The password is fixed: a Probe both succeeds and clears the verdict —
	// this is exactly what the "Check again" button runs.
	fixed := NewClientPool(host, port, false, "user", "good", 2)
	fixed.SetProviderName("fake-provider")
	t.Cleanup(fixed.Shutdown)
	if err := fixed.Probe(ctx); err != nil {
		t.Fatalf("probe with the fixed password: %v", err)
	}
	if reg.Blocked(health.KindProvider, "fake-provider") {
		t.Fatal("a successful authentication must clear the block")
	}
}

func TestPoolDialRecordsConnectionLimitGreeting(t *testing.T) {
	reg := testHealthRegistry(t)

	host, port := fakeNNTPListener(t, "conn-limit")
	pool := NewClientPool(host, port, false, "user", "good", 2)
	pool.SetProviderName("capped-provider")
	t.Cleanup(pool.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := pool.Get(ctx); err == nil {
		t.Fatal("expected the 502 greeting to fail the dial")
	}
	rec, ok := reg.Lookup(health.KindProvider, "capped-provider")
	if !ok || rec.Reason != health.ReasonConnectionLimit {
		t.Fatalf("a 502 greeting should degrade with connection_limit, got %+v", rec)
	}
	if rec.State != health.StateDegraded {
		t.Fatalf("connection limit must degrade, not %q", rec.State)
	}
	if reg.Blocked(health.KindProvider, "capped-provider") {
		t.Fatal("a connection limit must never block the provider")
	}
}
