package pool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"
)

const (
	body430  = "430 No Such Article\r\n"
	body502  = "502 transient server hiccup\r\n"
	bodyOK   = "222 Body follows\r\n=ybegin size=4 line=128 name=test\r\ndata\r\n=yend size=4\r\n.\r\n"
	bodyHang = "" // the server reads BODY and never answers
)

var testGroupList = []string{"alt.binaries.test"}

// scriptedProvider stands up a provider whose BODY answer is scripted. It logs
// in as its own id: connection budgets are keyed by host and user, so providers
// sharing a user would share one account allowance. It is shut down with the
// test.
func scriptedProvider(t *testing.T, id string, priority int, backup bool, bodyResponse string) ProviderConfig {
	t.Helper()
	host, port, stop := startScriptedNNTPServer(t, bodyResponse)
	cp := nntp.NewClientPool(host, port, false, id, "pass", 2)
	t.Cleanup(func() { cp.Shutdown(); stop() })
	return ProviderConfig{ID: id, Priority: priority, IsBackup: backup, ClientPool: cp}
}

// droppingProvider accepts every TCP connection and closes it before any NNTP
// greeting, so each connection attempt fails at the transport. accepts counts
// the attempts, which makes "the pool tried this provider" observable. The
// listener and its accept goroutine are torn down at cleanup, whichever way the
// test ends.
func droppingProvider(t *testing.T, id string, priority int, backup bool) (ProviderConfig, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepts := &atomic.Int32{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = c.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	cp := nntp.NewClientPool(addr.IP.String(), addr.Port, false, id, "pass", 1)
	t.Cleanup(func() { cp.Shutdown(); _ = ln.Close(); wg.Wait() })
	return ProviderConfig{ID: id, Priority: priority, IsBackup: backup, ClientPool: cp}, accepts
}

// bodyGate holds a gatedProvider's BODY reply until the test lets it go. The
// test waits for the BODY to arrive with Wait, acts while the fetch is in
// flight, then calls Open. Open is idempotent and also runs at test cleanup, so
// the server's handler can always exit, whichever way the test ends.
//
// The same type holds a singleflight leader in the shared-flight tests, where
// "the gate" is the leader's closure and Open lets it go.
type bodyGate struct {
	got     chan struct{}
	release chan struct{}
	once    sync.Once
	bound   time.Duration
	bodies  atomic.Int32 // BODY commands the provider behind this gate has received
}

func newBodyGate(t *testing.T) *bodyGate {
	t.Helper()
	g := &bodyGate{got: make(chan struct{}, 1), release: make(chan struct{}), bound: 30 * time.Second}
	t.Cleanup(g.Open)
	return g
}

func (g *bodyGate) Open() { g.once.Do(func() { close(g.release) }) }

// Wait returns once a BODY command has reached the gate and the fetch is still
// running. A fetch that ends first is reported with its own result rather than
// left to time the package out; the bound only covers a fetch that neither
// reaches the gate nor ends.
func (g *bodyGate) Wait(t *testing.T, done <-chan error) {
	t.Helper()
	if err := g.await(done); err != nil {
		t.Fatal(err)
	}
}

func (g *bodyGate) await(done <-chan error) error {
	select {
	case <-g.got:
		// The BODY notification is buffered, so it can be ready together with a
		// fetch that has already finished; select picks either. The test is only
		// about to act on a fetch that is still in flight, so look again.
		select {
		case err := <-done:
			return fmt.Errorf("fetch had already ended when its BODY was observed: %v", err)
		default:
			return nil
		}
	case err := <-done:
		return fmt.Errorf("fetch ended before a BODY reached the gate: %v", err)
	case <-time.After(g.bound):
		return errors.New("no BODY reached the gate and the fetch did not end")
	}
}

// flightWorkers runs the goroutines of a shared-flight test and joins them when
// the test ends. Opening the gate at cleanup frees the leader but says nothing
// about when the goroutines actually return, and a straggler is visible to the
// process-wide stack scans in waitForGoroutines, so a later test could count it.
//
// The cleanup it registers releases the held leader, cancels the callers, and
// only then waits for every worker. Nothing is held while waiting, and no
// timeout abandons a worker: each ends once the flight does. Register it after
// the providers (cleanup is last-in first-out) so they outlive the join.
type flightWorkers struct{ wg sync.WaitGroup }

func newFlightWorkers(t *testing.T, hold *bodyGate, cancel ...context.CancelFunc) *flightWorkers {
	t.Helper()
	w := &flightWorkers{}
	t.Cleanup(func() {
		hold.Open()
		for _, c := range cancel {
			c()
		}
		w.wg.Wait()
	})
	return w
}

// run counts the worker before it starts, so the join cannot miss it.
func (w *flightWorkers) run(fn func()) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		fn()
	}()
}

// gatedProvider answers BODY with bodyResponse only once g is opened, and
// signals g when a BODY arrives, so a test can act while a fetch is in flight.
// Commands are read as whole lines: a command split across TCP reads waits for
// its end, and several arriving in one read are answered one by one.
func gatedProvider(t *testing.T, id string, priority int, bodyResponse string, g *bodyGate) ProviderConfig {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte("200 Welcome\r\n"))
				r := bufio.NewReader(c)
				for {
					cmd, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(cmd, "AUTHINFO USER"):
						_, _ = c.Write([]byte("381 PASS required\r\n"))
					case strings.HasPrefix(cmd, "AUTHINFO PASS"):
						_, _ = c.Write([]byte("281 Authentication accepted\r\n"))
					case strings.HasPrefix(cmd, "GROUP"):
						_, _ = c.Write([]byte("211 group selected\r\n"))
					case strings.HasPrefix(cmd, "BODY"):
						g.bodies.Add(1)
						select {
						case g.got <- struct{}{}:
						default:
						}
						<-g.release
						_, _ = c.Write([]byte(bodyResponse))
					default:
						_, _ = c.Write([]byte("500 Unknown command\r\n"))
					}
				}
			}(c)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	cp := nntp.NewClientPool(addr.IP.String(), addr.Port, false, id, "pass", 2)
	t.Cleanup(func() { cp.Shutdown(); _ = ln.Close() })
	return ProviderConfig{ID: id, Priority: priority, ClientPool: cp}
}

func newTestPool(t *testing.T, providers ...ProviderConfig) *Pool {
	t.Helper()
	p, err := NewPool(&Config{Providers: providers})
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	return p
}

// fetchOnce goes through FetchSegment: the marker is attached per caller there,
// not inside the shared flight.
func fetchOnce(p *Pool, ctx context.Context, id string) error {
	_, err := p.FetchSegment(ctx, &nzb.Segment{ID: id}, testGroupList)
	return err
}

func isDefinitive(err error) bool { return errors.Is(err, ErrArticleMissingEverywhere) }

// waitForGoroutines blocks until n goroutines have a stack containing every one
// of subs. It polls a condition rather than sleeping a fixed time, so a test can
// act while another goroutine is provably parked where the test needs it.
func waitForGoroutines(t *testing.T, n int, subs ...string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		count := 0
		for _, g := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
			all := true
			for _, s := range subs {
				all = all && strings.Contains(g, s)
			}
			if all {
				count++
			}
		}
		if count >= n {
			return
		}
	}
	t.Fatalf("%d goroutines never reached %q", n, subs)
}

// waitForWaiters blocks until n goroutines are parked in FetchSegment waiting on
// someone else's singleflight call; a waiter that has not joined yet would run
// its own flight and the test would prove nothing.
func waitForWaiters(t *testing.T, n int) {
	t.Helper()
	waitForGoroutines(t, n, "(*Pool).FetchSegment(", "WaitGroup")
}

// ---- Finding 1: genuine protocol evidence --------------------------------

func TestFetchMissingEverywhereIsDefinitive(t *testing.T) {
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "b", 1, false, body430),
		scriptedProvider(t, "c", 2, true, body430))

	err := fetchOnce(p, context.Background(), "gone-1")
	if !isDefinitive(err) {
		t.Fatalf("430 from every provider must be definitive, got %v", err)
	}
	if !p.isKnownMissing("gone-1") {
		t.Fatal("unanimous 430 must still be admitted to the negative cache")
	}

	// Original chain, classification and text are untouched by the marker.
	if !nntp.IsArticleNotFound(err) || !IsArticleNotFoundError(err) {
		t.Fatalf("marker must not hide the 430: %v", err)
	}
	var tpErr *textproto.Error
	if !errors.As(err, &tpErr) || tpErr.Code != 430 {
		t.Fatalf("errors.As must still reach the textproto 430, got %v", err)
	}
	if len(AttemptedProviderHosts(err)) == 0 {
		t.Fatal("attempted-provider hosts must survive the marker")
	}
	if strings.Contains(err.Error(), "everywhere") {
		t.Fatalf("marker must not change the error text: %q", err)
	}
}

// A served article whose yEnc size is off by one reads "expected size 430 but
// got 431". The legacy classifier takes that for a 430; it is not evidence.
func TestYencSizeMismatchMentioning430IsNotDefinitive(t *testing.T) {
	body := "222 Body follows\r\n=ybegin size=430 line=128 name=t\r\n" + strings.Repeat("A", 431) + "\r\n=yend size=430\r\n.\r\n"
	p := newTestPool(t, scriptedProvider(t, "a", 0, false, body))

	for pass := 0; pass < 2; pass++ { // the second pass hits the cache
		_, err := p.FetchSegment(context.Background(), &nzb.Segment{ID: "yenc-1", Bytes: 430}, testGroupList)
		if err == nil {
			t.Fatal("expected the mismatched article to fail")
		}
		if pass == 0 && !strings.Contains(err.Error(), "expected size 430 but got 431") {
			t.Fatalf("test no longer produces the mismatch: %v", err)
		}
		if !nntp.IsArticleNotFound(err) {
			t.Fatalf("legacy classification must stay as it was: %v", err)
		}
		if isDefinitive(err) {
			t.Fatalf("pass %d: decoder text is not protocol evidence: %v", pass, err)
		}
	}
	if !p.isKnownMissing("yenc-1") {
		t.Fatal("legacy negative-cache admission must be unchanged")
	}
}

// One genuine 430 does not make up for a provider whose "430" is only text.
func TestMixedProtocolAndTextOnly430IsNotDefinitive(t *testing.T) {
	body := "222 Body follows\r\n=ybegin size=430 line=128 name=t\r\n" + strings.Repeat("A", 431) + "\r\n=yend size=430\r\n.\r\n"
	a, ga := countedProvider(t, "a", 0, false, body430)
	b, gb := countedProvider(t, "b", 1, false, body)
	p := newTestPool(t, a, b)

	_, err := p.FetchSegment(context.Background(), &nzb.Segment{ID: "mix-1", Bytes: 430}, testGroupList)
	if err == nil || !nntp.IsArticleNotFound(err) || isDefinitive(err) {
		t.Fatalf("want legacy 430 without the marker, got %v", err)
	}

	// The absence of the marker only means something if both outcomes happened.
	// A provider that fails before BODY (refused, auth rejected, dropped) also
	// leaves A's 430 as the result, so check each provider's own path. B's
	// fixture never sends a 430 line, so a 430 recorded for B after it received
	// a BODY can only come from decoding the yEnc body it was scripted to send.
	if ga.bodies.Load() != 1 || providerUnavailable(p, "a") != 1 || !isProtocol430(err) {
		t.Fatalf("provider a must answer one BODY with a typed 430 (BODY commands %d, recorded 430s %d, err %v)", ga.bodies.Load(), providerUnavailable(p, "a"), err)
	}
	if gb.bodies.Load() != 1 || providerUnavailable(p, "b") != 1 {
		t.Fatalf("provider b must reach BODY and fail in the decoder (BODY commands %d, recorded 430s %d)", gb.bodies.Load(), providerUnavailable(p, "b"))
	}
	// Both providers were attempted, so the legacy cache admits the miss, but
	// the text-only answer keeps it unverified.
	if !p.isKnownMissing("mix-1") {
		t.Fatal("legacy negative-cache admission must be unchanged")
	}
	if proofOf(p, "mix-1") != nil {
		t.Fatal("a text-only 430 must not leave verified proof")
	}
}

// countedProvider is a provider whose BODY answer is scripted and whose BODY
// commands are counted (g.bodies). The gate is already open: it is here for the
// counter, not for holding the reply.
func countedProvider(t *testing.T, id string, priority int, backup bool, bodyResponse string) (ProviderConfig, *bodyGate) {
	t.Helper()
	g := newBodyGate(t)
	g.Open()
	cfg := gatedProvider(t, id, priority, bodyResponse, g)
	cfg.IsBackup = backup
	return cfg, g
}

// providerUnavailable is how many times the pool has recorded a 430 from the
// provider; it moves only when a fetch got that answer, not on setup.
func providerUnavailable(p *Pool, id string) int64 {
	for _, s := range p.ProviderArticleStats() {
		if s.ProviderID == id {
			return s.UnavailableCount
		}
	}
	return 0
}

// One provider gives a genuine 430 and another is inconclusive. Absence of the
// marker only means something if both outcomes actually happened, so each case
// also checks that "a" answered 430 and that the second provider took the path
// the case is named for.
func TestFetchIncompleteCoverageIsNotDefinitive(t *testing.T) {
	cases := map[string]struct {
		// providers returns the configuration, the first provider's gate, and how
		// many times the second provider was contacted (BODY commands, or accepted
		// connections for a provider that drops them).
		providers func(t *testing.T) (cfgs []ProviderConfig, a *bodyGate, contacted func() int32)
		timeout   time.Duration
		second    func(t *testing.T, p *Pool, ctx context.Context, err error)
	}{
		"430 plus connection failure on a primary": {
			providers: func(t *testing.T) ([]ProviderConfig, *bodyGate, func() int32) {
				a, ga := countedProvider(t, "a", 0, false, body430)
				dropper, accepts := droppingProvider(t, "dropper", 1, false)
				return []ProviderConfig{a, dropper}, ga, accepts.Load
			},
			timeout: time.Second, // the dialer retries for ~45s; the deadline just bounds the test
			second: func(t *testing.T, p *Pool, ctx context.Context, err error) {
				// Its connections die before a greeting, so it can never have answered 430.
				if providerUnavailable(p, "dropper") != 0 {
					t.Fatal("a provider that never greets cannot have answered 430")
				}
			},
		},
		"430 plus connection failure on a backup": {
			providers: func(t *testing.T) ([]ProviderConfig, *bodyGate, func() int32) {
				a, ga := countedProvider(t, "a", 0, false, body430)
				dropper, accepts := droppingProvider(t, "dropper", 1, true)
				return []ProviderConfig{a, dropper}, ga, accepts.Load
			},
			timeout: time.Second,
			second: func(t *testing.T, p *Pool, ctx context.Context, err error) {
				if providerUnavailable(p, "dropper") != 0 {
					t.Fatal("a backup that never greets cannot have answered 430")
				}
			},
		},
		"430 plus transient failure": {
			providers: func(t *testing.T) ([]ProviderConfig, *bodyGate, func() int32) {
				a, ga := countedProvider(t, "a", 0, false, body430)
				b, gb := countedProvider(t, "b", 1, false, body502)
				return []ProviderConfig{a, b}, ga, gb.bodies.Load
			},
			second: func(t *testing.T, p *Pool, ctx context.Context, err error) {
				var tpErr *textproto.Error
				if !errors.As(err, &tpErr) || tpErr.Code != 502 {
					t.Fatalf("the second provider's 502 must be what the fetch reports, got %v", err)
				}
			},
		},
		"430 plus timeout": {
			providers: func(t *testing.T) ([]ProviderConfig, *bodyGate, func() int32) {
				a, ga := countedProvider(t, "a", 0, false, body430)
				hang, gh := countedProvider(t, "hang", 1, false, bodyHang)
				return []ProviderConfig{a, hang}, ga, gh.bodies.Load
			},
			timeout: 500 * time.Millisecond,
			second: func(t *testing.T, p *Pool, ctx context.Context, err error) {
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatalf("the hung provider should have run the caller out of time, ctx.Err() = %v", ctx.Err())
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfgs, ga, contacted := tc.providers(t)
			p := newTestPool(t, cfgs...)
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			err := fetchOnce(p, ctx, "partial-1")
			if err == nil {
				t.Fatal("expected the fetch to fail")
			}
			if isDefinitive(err) {
				t.Fatalf("incomplete coverage must not be definitive: %v", err)
			}
			if p.isKnownMissing("partial-1") {
				t.Fatal("incomplete coverage must not be admitted to the negative cache")
			}
			if proofOf(p, "partial-1") != nil {
				t.Fatal("incomplete coverage must not leave verified proof")
			}

			// The outcomes the case is about really happened.
			if ga.bodies.Load() == 0 || providerUnavailable(p, "a") == 0 {
				t.Fatalf("provider a never answered 430 (BODY commands %d, recorded 430s %d)", ga.bodies.Load(), providerUnavailable(p, "a"))
			}
			if contacted() == 0 {
				t.Fatal("the second provider was never contacted, so its outcome was not exercised")
			}
			tc.second(t, p, ctx, err)
		})
	}
}

// FetchSegmentFirst never admits to the negative cache because it cannot prove
// coverage, so its 430 stays a plain 430.
func TestFetchSegmentFirstAll430IsNotDefinitive(t *testing.T) {
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "b", 1, false, body430))

	_, err := p.FetchSegmentFirst(context.Background(), &nzb.Segment{ID: "first-1"}, nil)
	if !nntp.IsArticleNotFound(err) {
		t.Fatalf("existing 430 classification must hold, got %v", err)
	}
	if isDefinitive(err) || p.isKnownMissing("first-1") {
		t.Fatalf("FetchSegmentFirst proves no coverage and must not claim it: %v", err)
	}
}

func TestFetchFallbackSuccessCarriesNoMissingMarker(t *testing.T) {
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "backup", 1, true, bodyOK))

	if err := fetchOnce(p, context.Background(), "fallback-1"); err != nil {
		t.Fatalf("backup should have served the article: %v", err)
	}
	if p.isKnownMissing("fallback-1") {
		t.Fatal("a served article must not be known-missing")
	}
}

// ---- Cache provenance ------------------------------------------------------

func TestKnownMissingCacheHitIsDefinitiveOnlyWhenVerified(t *testing.T) {
	p := newTestPool(t, scriptedProvider(t, "a", 0, false, body430))
	p.markKnownMissingProof("verified-1", p.stamp())
	p.markKnownMissing("legacy-1") // what pre-existing entries look like

	for name, fetch := range map[string]func(id string) error{
		"FetchSegment": func(id string) error { return fetchOnce(p, context.Background(), id) },
		"FetchSegmentFirst": func(id string) error {
			_, err := p.FetchSegmentFirst(context.Background(), &nzb.Segment{ID: id}, nil)
			return err
		},
	} {
		err := fetch("verified-1")
		if !isDefinitive(err) || !nntp.IsArticleNotFound(err) || !strings.Contains(err.Error(), "(cached)") {
			t.Fatalf("%s: verified cache hit must be definitive with its legacy text, got %v", name, err)
		}
		err = fetch("legacy-1")
		if isDefinitive(err) || !nntp.IsArticleNotFound(err) || !strings.Contains(err.Error(), "(cached)") {
			t.Fatalf("%s: unverified hit must stay a plain cached 430, got %v", name, err)
		}
	}
}

// STAT admits to the negative cache exactly as before, but proves nothing a
// fetch can vouch for.
func TestStatOriginEntryIsNotDefinitive(t *testing.T) {
	host, port, stop := startMockNNTPServer(t)
	defer stop()
	cp := nntp.NewClientPool(host, port, false, "stat", "pass", 2)
	defer cp.Shutdown()
	p := newTestPool(t, ProviderConfig{ID: "stat", ClientPool: cp})

	exists, err := p.StatSegment(context.Background(), "stat-missing-1", nil)
	if exists || err != nil || !p.isKnownMissing("stat-missing-1") {
		t.Fatalf("setup: STAT must record the miss (exists=%v err=%v)", exists, err)
	}
	err = fetchOnce(p, context.Background(), "stat-missing-1")
	if !nntp.IsArticleNotFound(err) || isDefinitive(err) {
		t.Fatalf("STAT-origin hit must be a plain cached 430, got %v", err)
	}
}

// A marker recorded for one provider set must not answer for the next.
func TestKnownMissingCacheHitIsScopedToProviderSet(t *testing.T) {
	p := newTestPool(t, scriptedProvider(t, "a", 0, false, body430))
	if err := fetchOnce(p, context.Background(), "scoped-1"); !isDefinitive(err) {
		t.Fatalf("setup: %v", err)
	}

	p.UpdateProviders([]ProviderConfig{scriptedProvider(t, "b", 0, false, bodyOK)})
	if err := fetchOnce(p, context.Background(), "scoped-1"); err != nil {
		t.Fatalf("a stale marker answered for the new provider set: %v", err)
	}
}

// ---- Finding 2: provider configuration identity ---------------------------

// Same ID, same ClientPool, new endpoint: the legacy cache still answers (that
// is not changed here), but it must not vouch for a server nobody asked.
func TestSameIDEndpointReplacementInvalidatesWarmMarker(t *testing.T) {
	a := scriptedProvider(t, "a", 0, false, body430)
	hostB, portB, groupsB, stopB := startGroupScriptedNNTPServer(t, "211 group selected\r\n", bodyOK)
	defer stopB()
	p := newTestPool(t, a)

	if first := fetchOnce(p, context.Background(), "swap-1"); !isDefinitive(first) {
		t.Fatalf("setup: expected a verified miss, got %v", first)
	}
	if !a.ClientPool.Reconfigure(hostB, portB, false, "a", "pass", 2) {
		t.Fatal("setup: Reconfigure should report a new target")
	}
	p.UpdateProviders([]ProviderConfig{a}) // what a settings reload does

	err := fetchOnce(p, context.Background(), "swap-1")
	if err == nil || !nntp.IsArticleNotFound(err) || !strings.Contains(err.Error(), "(cached)") {
		t.Fatalf("legacy cached result changed: %v", err)
	}
	if isDefinitive(err) {
		t.Fatalf("evidence from the old endpoint must not vouch for the new one: %v", err)
	}
	if groupsB.Load() != 0 {
		t.Fatal("the cache hit is legacy behavior and must not have contacted the new server")
	}
}

func TestEndpointReplacementDuringFetchIsNotDefinitive(t *testing.T) {
	g := newBodyGate(t)
	a := gatedProvider(t, "a", 0, body430, g)
	hostB, portB, stopB := startScriptedNNTPServer(t, bodyOK)
	defer stopB()
	p := newTestPool(t, a)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "mid-1") }()
	g.Wait(t, done) // the old endpoint has the BODY command
	a.ClientPool.Reconfigure(hostB, portB, false, "a", "pass", 2)
	p.UpdateProviders([]ProviderConfig{a})
	g.Open()

	err := <-done
	if !nntp.IsArticleNotFound(err) {
		t.Fatalf("legacy result must still be the 430, got %v", err)
	}
	if isDefinitive(err) {
		t.Fatalf("a 430 from the old endpoint must not vouch for the new one: %v", err)
	}
	if err := fetchOnce(p, context.Background(), "mid-1"); isDefinitive(err) {
		t.Fatalf("the entry it left behind must not be verified either: %v", err)
	}
}

// A reload that lands after coverage was checked but before the entry is
// published stores proof for the old configuration under the same key, because
// providerSignature uses provider IDs only and this test keeps ID "a". The
// client pool's generation no longer matches, so the hit is unmarked. Proof
// filed under a different key is covered by
// TestVerifiedProofIsBoundToItsArticle.
func TestReloadBetweenCoverageCheckAndPublicationDoesNotVouch(t *testing.T) {
	a := scriptedProvider(t, "a", 0, false, body430)
	hostB, portB, stopB := startScriptedNNTPServer(t, bodyOK)
	defer stopB()
	p := newTestPool(t, a)

	proof := p.stamp() // taken at coverage check
	a.ClientPool.Reconfigure(hostB, portB, false, "a", "pass", 2)
	p.UpdateProviders([]ProviderConfig{a})
	p.markKnownMissingProof("late-1", proof) // published after the reload

	err := fetchOnce(p, context.Background(), "late-1")
	if !nntp.IsArticleNotFound(err) || isDefinitive(err) {
		t.Fatalf("stale proof must not attest the new configuration: %v", err)
	}
}

// ---- Finding 3: shared singleflight ---------------------------------------

// The leader works for view {a}; a joiner on view {a,b} receives its result but
// b was never asked, so only the leader's own view may call it definitive.
func TestSharedFlightDoesNotVouchAcrossViews(t *testing.T) {
	a := scriptedProvider(t, "a", 0, false, body430)
	bHost, bPort, bGroups, stopB := startGroupScriptedNNTPServer(t, "211 group selected\r\n", bodyOK)
	// t.Cleanup, not defer: a deferred shutdown would run before the cleanup
	// below that releases and joins the workers, which then fetch from a dead pool.
	t.Cleanup(stopB)
	bPool := nntp.NewClientPool(bHost, bPort, false, "b", "pass", 2)
	t.Cleanup(bPool.Shutdown)
	root := newTestPool(t, a, ProviderConfig{ID: "b", Priority: 1, ClientPool: bPool})
	viewA, viewAB := root.Subset([]string{"a"}), root.Subset([]string{"a", "b"})

	// Registered after the providers (cleanup runs last-in first-out) and before
	// either goroutine starts: if a check below calls t.Fatal, cleanup lets the
	// leader go and joins both workers before the providers go away.
	hold := newBodyGate(t)
	// Every fetch in this test runs under ctx, so cleanup (release, then cancel,
	// then join) can also end a fetch stalled on a provider instead of waiting
	// out its socket deadline. It is canceled only there: attest reads it last.
	ctx, cancel := context.WithCancel(context.Background())
	workers := newFlightWorkers(t, hold, cancel)
	started := make(chan struct{})
	leader := make(chan error, 1)
	workers.run(func() {
		_, err, _ := viewA.sf.Do("shared-1", func() (interface{}, error) {
			close(started)
			<-hold.release
			return viewA.fetchSegmentOnce(ctx, "shared-1", &nzb.Segment{ID: "shared-1"}, testGroupList)
		})
		leader <- viewA.attest(ctx, err)
	})
	<-started
	joiner := make(chan error, 1)
	workers.run(func() { joiner <- fetchOnce(viewAB, ctx, "shared-1") })
	waitForWaiters(t, 1)
	hold.Open()

	if err := <-leader; !isDefinitive(err) {
		t.Fatalf("the view that produced the evidence should keep the marker: %v", err)
	}
	jerr := <-joiner
	if !nntp.IsArticleNotFound(jerr) {
		t.Fatalf("the joiner must still receive the shared 430, got %v", jerr)
	}
	if isDefinitive(jerr) {
		t.Fatalf("a view that includes an unasked provider must not get the marker: %v", jerr)
	}
	if bGroups.Load() != 0 {
		t.Fatal("fetch sharing changed: the joiner contacted its own provider instead of sharing")
	}
}

// ---- Provider order is not part of the evidence ----------------------------

func TestEvidenceStampEqualIgnoresOrderOnly(t *testing.T) {
	a, b, c := scriptedProvider(t, "a", 0, false, body430), scriptedProvider(t, "b", 1, false, body430), scriptedProvider(t, "c", 2, false, body430)
	a2 := scriptedProvider(t, "a", 0, false, body430) // same ID, another pool
	st := func(p ProviderConfig) providerStamp {
		return providerStamp{id: p.ID, cp: p.ClientPool, gen: p.ClientPool.Generation()}
	}
	sa, sb, sc, sa2 := st(a), st(b), st(c), st(a2)
	bumped := sa
	bumped.gen++

	for _, tc := range []struct {
		name string
		x, y evidenceStamp
		want bool
	}{
		{"identical", evidenceStamp{sa, sb}, evidenceStamp{sa, sb}, true},
		{"reversed", evidenceStamp{sa, sb}, evidenceStamp{sb, sa}, true},
		{"rotated", evidenceStamp{sa, sb, sc}, evidenceStamp{sb, sc, sa}, true},
		{"duplicates reordered", evidenceStamp{sa, sb, sa}, evidenceStamp{sa, sa, sb}, true},
		{"same ID on two pools, reordered", evidenceStamp{sa, sa2}, evidenceStamp{sa2, sa}, true},
		{"both empty", evidenceStamp{}, evidenceStamp{}, true},
		{"provider added", evidenceStamp{sa, sb}, evidenceStamp{sa, sb, sc}, false},
		{"provider removed", evidenceStamp{sa, sb}, evidenceStamp{sa}, false},
		{"different provider, same count", evidenceStamp{sa, sb}, evidenceStamp{sa, sc}, false},
		{"pool replaced", evidenceStamp{sa, sb}, evidenceStamp{sa2, sb}, false},
		{"pool replaced, reversed", evidenceStamp{sa, sb}, evidenceStamp{sb, sa2}, false},
		{"generation changed", evidenceStamp{sa, sb}, evidenceStamp{bumped, sb}, false},
		{"generation changed, reversed", evidenceStamp{sa, sb}, evidenceStamp{sb, bumped}, false},
		{"pools swapped between IDs", evidenceStamp{sa, sb}, evidenceStamp{{id: "a", cp: sb.cp, gen: sb.gen}, {id: "b", cp: sa.cp, gen: sa.gen}}, false},
		{"duplicate against single", evidenceStamp{sa, sa}, evidenceStamp{sa}, false},
		{"duplicate against other pair", evidenceStamp{sa, sa}, evidenceStamp{sa, sb}, false},
		{"multiplicity differs", evidenceStamp{sa, sa, sb}, evidenceStamp{sa, sb, sb}, false},
		{"same ID, different pools, against duplicate", evidenceStamp{sa, sa2}, evidenceStamp{sa, sa}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.x.equal(tc.y); got != tc.want {
				t.Fatalf("equal = %v, want %v", got, tc.want)
			}
			if got := tc.y.equal(tc.x); got != tc.want {
				t.Fatalf("reversed operands: equal = %v, want %v", got, tc.want)
			}
		})
	}
}

// One leader fetches under lead's view; join's caller must be parked on that very
// flight. Each provider's BODY count then proves the joiner shared the fetch:
// one BODY per provider, none from the joiner. Both results go through the
// production attest, so each caller is judged on its own view.
func sharedFlightMarkers(t *testing.T, lead, join *Pool, id string, bodies ...*bodyGate) (leaderErr, joinerErr error) {
	t.Helper()
	if lead.sf != join.sf {
		t.Fatal("the views do not share a singleflight group")
	}
	hold := newBodyGate(t)
	ctx, cancel := context.WithCancel(context.Background())
	workers := newFlightWorkers(t, hold, cancel)
	started := make(chan struct{})
	leader := make(chan error, 1)
	workers.run(func() {
		_, err, _ := lead.sf.Do(id, func() (interface{}, error) {
			close(started)
			<-hold.release
			return lead.fetchSegmentOnce(ctx, id, &nzb.Segment{ID: id}, testGroupList)
		})
		leader <- lead.attest(ctx, err)
	})
	<-started
	joiner := make(chan error, 1)
	workers.run(func() { joiner <- fetchOnce(join, ctx, id) })
	waitForWaiters(t, 1)
	hold.Open()
	leaderErr, joinerErr = <-leader, <-joiner
	for i, g := range bodies {
		if n := g.bodies.Load(); n != 1 {
			t.Fatalf("provider %d received %d BODY commands, want exactly 1 (the joiner must share the leader's fetch)", i, n)
		}
	}
	return leaderErr, joinerErr
}

func TestSharedFlightMarkerIgnoresProviderOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lead, join func(root *Pool) *Pool
	}{
		{"same order",
			func(r *Pool) *Pool { return r.Subset([]string{"a", "b"}) },
			func(r *Pool) *Pool { return r.Subset([]string{"a", "b"}) }},
		{"leader [a,b], joiner [b,a]",
			func(r *Pool) *Pool { return r.Subset([]string{"a", "b"}) },
			func(r *Pool) *Pool { return r.Subset([]string{"b", "a"}) }},
		{"leader [b,a], joiner [a,b]",
			func(r *Pool) *Pool { return r.Subset([]string{"b", "a"}) },
			func(r *Pool) *Pool { return r.Subset([]string{"a", "b"}) }},
		{"root leader, reversed subset joiner",
			func(r *Pool) *Pool { return r },
			func(r *Pool) *Pool { return r.Subset([]string{"b", "a"}) }},
		{"reversed subset leader, root joiner",
			func(r *Pool) *Pool { return r.Subset([]string{"b", "a"}) },
			func(r *Pool) *Pool { return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ga := countedProvider(t, "a", 0, false, body430)
			b, gb := countedProvider(t, "b", 1, false, body430)
			root := newTestPool(t, a, b)
			lead, join := tc.lead(root), tc.join(root)
			ls, js := lead.stamp(), join.stamp()
			if len(ls) != 2 || len(js) != 2 {
				t.Fatalf("both views must hold both providers, got %d and %d", len(ls), len(js))
			}

			lerr, jerr := sharedFlightMarkers(t, lead, join, "order-1", ga, gb)
			if !isDefinitive(lerr) || !isDefinitive(jerr) {
				t.Fatalf("both callers hold the complete configuration: leader=%v joiner=%v", lerr, jerr)
			}
			if lerr.Error() != jerr.Error() || !nntp.IsArticleNotFound(jerr) {
				t.Fatalf("text or legacy classification changed:\n%q\n%q", lerr, jerr)
			}
		})
	}
}

// A proof gathered through one order is replayed through the other view; a
// configuration change between the fetch and attestation still withholds it,
// whichever way round the views are.
func TestReversedViewAttestationStillRejectsChangedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, root *Pool, a, b ProviderConfig)
		want   bool
	}{
		{"stable", func(*testing.T, *Pool, ProviderConfig, ProviderConfig) {}, true},
		{"generation change", func(t *testing.T, root *Pool, a, b ProviderConfig) {
			host, port, stop := startScriptedNNTPServer(t, bodyOK)
			t.Cleanup(stop)
			b.ClientPool.Reconfigure(host, port, false, "b", "pass", 2)
		}, false},
		{"pool replaced", func(t *testing.T, root *Pool, a, b ProviderConfig) {
			root.UpdateProviders([]ProviderConfig{scriptedProvider(t, "a", 0, false, body430), b})
		}, false},
		{"provider added outside the selected view", func(t *testing.T, root *Pool, a, b ProviderConfig) {
			root.UpdateProviders([]ProviderConfig{a, b, scriptedProvider(t, "c", 2, false, body430)})
		}, true}, // [b,a] still resolves to the same two providers
		{"provider added to the whole-pool view", func(t *testing.T, root *Pool, a, b ProviderConfig) {
			root.UpdateProviders([]ProviderConfig{a, b, scriptedProvider(t, "c", 2, false, body430)})
		}, false},
		{"provider removed", func(t *testing.T, root *Pool, a, b ProviderConfig) {
			root.UpdateProviders([]ProviderConfig{a})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := scriptedProvider(t, "a", 0, false, body430)
			b := scriptedProvider(t, "b", 1, false, body430)
			root := newTestPool(t, a, b)
			ab, ba := root.Subset([]string{"a", "b"}), root.Subset([]string{"b", "a"})
			view := ba
			if strings.Contains(tc.name, "whole-pool") {
				view = root // follows the root, so it sees the added provider
			}
			if err := fetchOnce(ab, context.Background(), "reload-1"); !isDefinitive(err) {
				t.Fatalf("setup: %v", err)
			}
			legacy := cachedMissingError("reload-1", proofOf(ab, "reload-1"))

			tc.change(t, root, a, b)

			got := view.attest(context.Background(), legacy)
			if isDefinitive(got) != tc.want {
				t.Fatalf("attest: marker = %v, want %v (%v)", isDefinitive(got), tc.want, got)
			}
			if !tc.want && got != legacy {
				t.Fatalf("the original error must come back untouched, got %v", got)
			}
		})
	}
}

// ---- Finding 4: cancellation ----------------------------------------------

func TestCanceledCallerWithWarmEntryGetsLegacyErrorWithoutMarker(t *testing.T) {
	p := newTestPool(t, scriptedProvider(t, "a", 0, false, body430))
	if live := fetchOnce(p, context.Background(), "warm-1"); !isDefinitive(live) {
		t.Fatalf("setup: %v", live)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, firstErr := p.FetchSegmentFirst(ctx, &nzb.Segment{ID: "warm-1"}, nil)
	for name, err := range map[string]error{
		"FetchSegment":      fetchOnce(p, ctx, "warm-1"),
		"FetchSegmentFirst": firstErr,
	} {
		if !nntp.IsArticleNotFound(err) || !strings.Contains(err.Error(), "(cached)") {
			t.Fatalf("%s: the legacy cached error must be returned unchanged, got %v", name, err)
		}
		if isDefinitive(err) {
			t.Fatalf("%s: a canceled caller must not receive the marker: %v", name, err)
		}
	}
}

func TestCanceledWaiterDoesNotReceiveMarkerFromSharedFlight(t *testing.T) {
	p := newTestPool(t, scriptedProvider(t, "a", 0, false, body430))

	canceledCtx, cancel := context.WithCancel(context.Background())
	defer cancel() // on every exit path; the explicit cancel below is the one the test is about
	// Same ordering as the cross-view test: release, cancel, join, then the
	// provider registered above shuts down.
	hold := newBodyGate(t)
	workers := newFlightWorkers(t, hold, cancel)
	started := make(chan struct{})
	workers.run(func() {
		_, _, _ = p.sf.Do("waiter-1", func() (interface{}, error) {
			close(started)
			<-hold.release
			return p.fetchSegmentOnce(context.Background(), "waiter-1", &nzb.Segment{ID: "waiter-1"}, testGroupList)
		})
	})
	<-started

	canceled, live := make(chan error, 1), make(chan error, 1)
	workers.run(func() { canceled <- fetchOnce(p, canceledCtx, "waiter-1") })
	workers.run(func() { live <- fetchOnce(p, context.Background(), "waiter-1") })
	waitForWaiters(t, 2)
	cancel()
	hold.Open()

	cerr, lerr := <-canceled, <-live
	if !nntp.IsArticleNotFound(cerr) || isDefinitive(cerr) {
		t.Fatalf("canceled waiter: want the shared 430 without the marker, got %v", cerr)
	}
	if !isDefinitive(lerr) {
		t.Fatalf("the live waiter on the same flight keeps the marker, got %v", lerr)
	}
	if cerr.Error() != lerr.Error() {
		t.Fatalf("the returned error text must not depend on the marker:\n%q\n%q", cerr, lerr)
	}
}

// The caller is canceled only once the provider holds the BODY, so the result is
// about a read interrupted in flight and not about any earlier failure.
func TestFetchCancellationIsNotDefinitive(t *testing.T) {
	// Registered gate-first so cleanup (LIFO) releases and joins the worker
	// before the provider shuts down.
	g := newBodyGate(t)
	p := newTestPool(t, gatedProvider(t, "hang", 0, bodyHang, g))
	ctx, cancel := context.WithCancel(context.Background())
	workers := newFlightWorkers(t, g, cancel)

	done := make(chan error, 1)
	workers.run(func() { done <- fetchOnce(p, ctx, "cancel-1") })
	g.Wait(t, done) // fails at once if the fetch ends before a BODY arrives
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the fetch did not return after its caller was canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the caller's cancellation, got %v", err)
	}
	if n := g.bodies.Load(); n != 1 {
		t.Fatalf("BODY commands = %d, want the one the cancellation interrupted", n)
	}
	if isDefinitive(err) || p.isKnownMissing("cancel-1") || proofOf(p, "cancel-1") != nil {
		t.Fatalf("cancellation must not read as missing: %v", err)
	}
}

// ---- Provenance: who actually answered -------------------------------------

// proofOf returns the proof stored with a negative-cache entry; nil when the
// entry is absent or unverified.
func proofOf(p *Pool, id string) evidenceStamp {
	_, proof := p.knownMissing(id)
	return proof
}

// The proof published by a stable fetch is the current configuration, primary
// and backup alike.
func TestPublishedProofNamesEveryAnsweringProvider(t *testing.T) {
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "backup", 1, true, body430))

	if err := fetchOnce(p, context.Background(), "prov-1"); !isDefinitive(err) {
		t.Fatalf("setup: %v", err)
	}
	proof := proofOf(p, "prov-1")
	if len(proof) != 2 || !proof.equal(p.stamp()) {
		t.Fatalf("proof should be the two-provider configuration, got %v", proof)
	}
}

// Provider "b" is replaced by another pool while "a" is answering, the
// replacement takes the BODY, and the original is put back before the fetch
// ends. IDs, start and end configuration all look untouched, but the original
// "b" was never asked.
func TestPoolReplacedThenRestoredDuringFetchIsNotDefinitive(t *testing.T) {
	gA, gB2 := newBodyGate(t), newBodyGate(t)
	a := gatedProvider(t, "a", 0, body430, gA)
	b1 := scriptedProvider(t, "b", 1, false, body430)
	b2 := gatedProvider(t, "b", 1, body430, gB2)
	p := newTestPool(t, a, b1)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "restore-1") }()
	gA.Wait(t, done)
	p.UpdateProviders([]ProviderConfig{a, b2})
	gA.Open()
	gB2.Wait(t, done) // the replacement, not the original, holds the BODY
	p.UpdateProviders([]ProviderConfig{a, b1})
	gB2.Open()

	err := <-done
	if !nntp.IsArticleNotFound(err) {
		t.Fatalf("legacy result must still be the 430, got %v", err)
	}
	if isDefinitive(err) {
		t.Fatalf("a 430 from the replacement pool must not vouch for the restored one: %v", err)
	}
	if !p.isKnownMissing("restore-1") || proofOf(p, "restore-1") != nil {
		t.Fatal("legacy admission must still happen, without a proof")
	}
	if err := fetchOnce(p, context.Background(), "restore-1"); isDefinitive(err) {
		t.Fatalf("the cached entry must not be verified either: %v", err)
	}
}

// The same replacement left in place did answer for the configuration that is
// current at the end, so nothing is withheld.
func TestReplacementThatAnsweredIsDefinitiveForCurrentView(t *testing.T) {
	gA := newBodyGate(t)
	a := gatedProvider(t, "a", 0, body430, gA)
	b1 := scriptedProvider(t, "b", 1, false, body430)
	b2 := scriptedProvider(t, "b", 1, false, body430)
	p := newTestPool(t, a, b1)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "kept-1") }()
	gA.Wait(t, done)
	p.UpdateProviders([]ProviderConfig{a, b2})
	gA.Open()

	if err := <-done; !isDefinitive(err) {
		t.Fatalf("every provider now configured answered a typed 430: %v", err)
	}
	if !proofOf(p, "kept-1").equal(p.stamp()) {
		t.Fatal("proof should be the configuration that answered")
	}
}

// A provider added while the first is answering is asked too; the proof covers
// both. One removed leaves the proof covering what remains.
func TestProviderAddedOrRemovedDuringFetchProofMatchesFinalView(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end func(a, b ProviderConfig) []ProviderConfig
		want       int
	}{
		{"added",
			func(a, b ProviderConfig) []ProviderConfig { return []ProviderConfig{a} },
			func(a, b ProviderConfig) []ProviderConfig { return []ProviderConfig{a, b} }, 2},
		{"removed",
			func(a, b ProviderConfig) []ProviderConfig { return []ProviderConfig{a, b} },
			func(a, b ProviderConfig) []ProviderConfig { return []ProviderConfig{a} }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newBodyGate(t)
			a := gatedProvider(t, "a", 0, body430, g)
			b := scriptedProvider(t, "b", 1, false, body430)
			p := newTestPool(t, tc.start(a, b)...)

			done := make(chan error, 1)
			go func() { done <- fetchOnce(p, context.Background(), "churn-1") }()
			g.Wait(t, done)
			p.UpdateProviders(tc.end(a, b))
			g.Open()

			if err := <-done; !isDefinitive(err) {
				t.Fatalf("the final configuration was fully answered: %v", err)
			}
			if proof := proofOf(p, "churn-1"); len(proof) != tc.want || !proof.equal(p.stamp()) {
				t.Fatalf("proof should equal the final %d-provider view, got %v", tc.want, proof)
			}
		})
	}
}

// A backup answering from a configuration that was re-pointed mid-command must
// not be counted for the new one.
func TestBackupReconfiguredDuringFetchIsNotDefinitive(t *testing.T) {
	g := newBodyGate(t)
	a := scriptedProvider(t, "a", 0, false, body430)
	c := gatedProvider(t, "c", 1, body430, g)
	c.IsBackup = true
	hostN, portN, stopN := startScriptedNNTPServer(t, bodyOK)
	defer stopN()
	p := newTestPool(t, a, c)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "backup-1") }()
	g.Wait(t, done)
	c.ClientPool.Reconfigure(hostN, portN, false, "c", "pass", 2)
	p.UpdateProviders([]ProviderConfig{a, c})
	g.Open()

	err := <-done
	if !nntp.IsArticleNotFound(err) || isDefinitive(err) {
		t.Fatalf("want the legacy 430 without the marker, got %v", err)
	}
}

func TestCoveredByNeedsEveryProviderFromTheCurrentConfiguration(t *testing.T) {
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "b", 1, false, body430))
	cur := p.stamp()

	if got := p.coveredBy(cur); !got.equal(cur) {
		t.Fatalf("full coverage must return the configuration, got %v", got)
	}
	if p.coveredBy(cur[:1]) != nil {
		t.Fatal("one provider of two is not coverage")
	}
	other := append(evidenceStamp{}, cur...)
	other[1].gen++
	if p.coveredBy(other) != nil {
		t.Fatal("an answer from another generation is not coverage")
	}
	other[1] = providerStamp{id: "b", cp: cur[0].cp, gen: cur[1].gen}
	if p.coveredBy(other) != nil {
		t.Fatal("an answer from another pool is not coverage")
	}
	if (&Pool{}).coveredBy(evidenceStamp{{id: "a"}}) != nil {
		t.Fatal("an empty configuration proves nothing")
	}
}

// ---- Cancellation during attestation ---------------------------------------

// attest has to read the caller's context after the stamp it computes, because
// the stamp takes locks and can block while the caller gives up.
func TestCancellationDuringAttestationStampWins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"canceled while stamp is blocked", true}, {"live caller", false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPool(t, scriptedProvider(t, "a", 0, false, body430))
			legacy := &missingProof{&textproto.Error{Code: 430, Msg: "No Such Article"}, p.stamp()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// The unlock below runs when the function scope exits, before cleanup;
			// cleanup then cancels and joins the attest goroutine, so it cannot
			// still be on its way out when the next test scans stacks.
			var attester sync.WaitGroup
			t.Cleanup(func() { cancel(); attester.Wait() })
			res := make(chan error, 1)
			func() {
				p.mu.Lock() // p.stamp, inside attest, now blocks on RLock
				// Deferred, so a t.Fatal in the stack wait still unlocks and lets
				// the attest goroutine run to its end.
				defer p.mu.Unlock()
				attester.Add(1)
				go func() {
					defer attester.Done()
					res <- p.attest(ctx, legacy)
				}()
				waitForGoroutines(t, 1, "(*Pool).stamp", "RWMutex")
				if tc.cancel {
					cancel()
				}
			}()

			err := <-res
			if tc.cancel {
				if err != error(legacy) {
					t.Fatalf("the original error must come back untouched, got %v", err)
				}
				return
			}
			if !isDefinitive(err) || err.Error() != legacy.Error() {
				t.Fatalf("a live caller with valid evidence keeps the marker and the text, got %v", err)
			}
		})
	}
}

// ---- Attestation against a changed configuration --------------------------

// A proof built by a real fetch is replayed through production attest after the
// configuration moved on. Whatever the change, the marker is withheld and the
// original error comes back untouched; with no change it is attached.
func TestAttestWithholdsStaleProofAfterConfigurationChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, p *Pool, a, b ProviderConfig)
		want   bool
	}{
		{"stable configuration", func(*testing.T, *Pool, ProviderConfig, ProviderConfig) {}, true},
		{"in-place reconfiguration", func(t *testing.T, p *Pool, a, b ProviderConfig) {
			host, port, stop := startScriptedNNTPServer(t, bodyOK)
			t.Cleanup(stop)
			b.ClientPool.Reconfigure(host, port, false, "b", "pass", 2)
		}, false},
		{"same-ID pool replacement", func(t *testing.T, p *Pool, a, b ProviderConfig) {
			p.UpdateProviders([]ProviderConfig{scriptedProvider(t, "a", 0, false, body430), b})
		}, false},
		{"provider added", func(t *testing.T, p *Pool, a, b ProviderConfig) {
			p.UpdateProviders([]ProviderConfig{a, b, scriptedProvider(t, "c", 2, false, body430)})
		}, false},
		{"provider removed", func(t *testing.T, p *Pool, a, b ProviderConfig) {
			p.UpdateProviders([]ProviderConfig{a})
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := scriptedProvider(t, "a", 0, false, body430)
			b := scriptedProvider(t, "b", 1, false, body430)
			p := newTestPool(t, a, b)
			if err := fetchOnce(p, context.Background(), "stale-1"); !isDefinitive(err) {
				t.Fatalf("setup: %v", err)
			}
			legacy := cachedMissingError("stale-1", proofOf(p, "stale-1"))

			tc.change(t, p, a, b)

			got := p.attest(context.Background(), legacy)
			if isDefinitive(got) != tc.want {
				t.Fatalf("marker = %v, want %v (%v)", isDefinitive(got), tc.want, got)
			}
			if !tc.want && got != legacy {
				t.Fatalf("the original error must come back untouched, got %v", got)
			}
			if got.Error() != legacy.Error() || !nntp.IsArticleNotFound(got) {
				t.Fatalf("text or legacy classification changed: %v", got)
			}
		})
	}
}

// Both paths into attest — the verified cache entry and the fetch that fills
// it — hand the caller the same marker on a stable configuration, and attempted
// providers survive on the live one.
func TestAttestationLiveFetchAndVerifiedCacheAgree(t *testing.T) {
	a, ga := countedProvider(t, "a", 0, false, body430)
	b, gb := countedProvider(t, "b", 1, false, body430)
	p := newTestPool(t, a, b)

	live := fetchOnce(p, context.Background(), "agree-1")
	if !isDefinitive(live) || ga.bodies.Load() == 0 || gb.bodies.Load() == 0 {
		t.Fatalf("setup: the live fetch must ask both providers and earn the marker: %v", live)
	}
	bodiesA, bodiesB := ga.bodies.Load(), gb.bodies.Load()
	cached := fetchOnce(p, context.Background(), "agree-1")
	if !isDefinitive(cached) {
		t.Fatalf("both paths must be definitive: live=%v cached=%v", live, cached)
	}
	// A second live fetch would earn the marker too; only the BODY counts tell a
	// verified cache hit from that.
	if ga.bodies.Load() != bodiesA || gb.bodies.Load() != bodiesB {
		t.Fatalf("the second fetch must be answered from the verified cache entry without a BODY (a %d->%d, b %d->%d)", bodiesA, ga.bodies.Load(), bodiesB, gb.bodies.Load())
	}
	if len(AttemptedProviderHosts(live)) == 0 || !nntp.IsArticleNotFound(cached) {
		t.Fatal("attempted hosts (live) and legacy classification (cached) must survive")
	}
}

// ---- Cached proof is bound to its article ---------------------------------

// The cache key is providerSignature + "|" + message ID, so with a provider named
// "p|x" the pair (provider "p", article "x|y@host") and the pair (provider
// "p|x", article "y@host") share one key. A proof filed under that key by a
// reload that landed during publication names provider "p" for article
// "y@host"; after "p" is restored, a lookup of "x|y@host" reaches it and the
// stamps match. The proof must not travel to the other article.
func TestVerifiedProofIsBoundToItsArticle(t *testing.T) {
	provP, srvP := testProvider(t, "p", true)
	provPX, srvPX := testProvider(t, "p|x", true)
	p := newTestPool(t, provP)

	// A real fetch: typed 430 from the only provider, verified entry under "p|y@host".
	if err := fetchOnce(p, context.Background(), "y@host"); !isDefinitive(err) {
		t.Fatalf("setup: expected a verified miss, got %v", err)
	}
	proof := proofOf(p, "y@host")
	if proof == nil {
		t.Fatal("setup: the fetch should have published a proof")
	}

	p.UpdateProviders([]ProviderConfig{provPX}) // the provider ID becomes "p|x"
	p.markKnownMissingProof("y@host", proof)    // publication lands after the reload: key "p|x|y@host"
	p.UpdateProviders([]ProviderConfig{provP})  // provider "p" restored: same pool, same generation
	bodiesP, bodiesPX := srvP.bodies.Load(), srvPX.bodies.Load()

	err := fetchOnce(p, context.Background(), "x|y@host") // key "p|x|y@host" again
	if err == nil || !nntp.IsArticleNotFound(err) || !strings.Contains(err.Error(), "(cached)") {
		t.Fatalf("the legacy cache hit must be returned unchanged, got %v", err)
	}
	if isDefinitive(err) {
		t.Fatalf("proof gathered for y@host must not vouch for x|y@host: %v", err)
	}
	if srvP.bodies.Load() != bodiesP || srvPX.bodies.Load() != bodiesPX {
		t.Fatal("a legacy cache hit must not send a BODY to anyone")
	}

	// The entry is still a legacy hit for both articles, but only one owns the proof.
	if known, got := p.knownMissing("x|y@host"); !known || got != nil {
		t.Fatalf("colliding lookup: want a legacy hit without proof, got known=%v proof=%v", known, got)
	}
	if known, got := p.knownMissing("y@host"); !known || !got.equal(proof) {
		t.Fatalf("the original article keeps its proof, got known=%v proof=%v", known, got)
	}
	if err := fetchOnce(p, context.Background(), "y@host"); !isDefinitive(err) {
		t.Fatalf("a verified hit for the original article keeps the marker: %v", err)
	}
}

// The article an unrelated lookup asks about is fetched as before, and a proof
// is recorded for the normalized ID whatever whitespace the caller passed.
func TestVerifiedProofControls(t *testing.T) {
	provP, srvP := testProvider(t, "p", true)
	p := newTestPool(t, provP)

	if err := fetchOnce(p, context.Background(), "y@host"); !isDefinitive(err) {
		t.Fatalf("setup: %v", err)
	}
	before := srvP.bodies.Load()
	err := fetchOnce(p, context.Background(), "other@host")
	if !isDefinitive(err) || srvP.bodies.Load() != before+1 {
		t.Fatalf("an unrelated article is queried and earns its own marker: marker=%v bodies=%d", isDefinitive(err), srvP.bodies.Load()-before)
	}

	p.markKnownMissingProof("  spaced@host ", p.stamp())
	if _, got := p.knownMissing("spaced@host"); got == nil {
		t.Fatal("the proof is bound to the normalized message ID, so surrounding whitespace must not lose it")
	}
	p.markKnownMissing("legacy@host") // pre-existing and STAT-origin entries carry no proof
	if known, got := p.knownMissing("legacy@host"); !known || got != nil {
		t.Fatalf("legacy entry must stay unverified, got known=%v proof=%v", known, got)
	}
}

// ---- bodyGate itself --------------------------------------------------------

func TestBodyGateAwait(t *testing.T) {
	fetchEnded := func(err error) chan error {
		done := make(chan error, 1)
		done <- err
		return done
	}

	// BODY notification and a finished fetch are both ready: the result of the
	// fetch has to win, every time, not whichever case select happens to pick.
	t.Run("finished fetch wins over a ready BODY", func(t *testing.T) {
		for i := 0; i < 200; i++ {
			g := newBodyGate(t)
			g.got <- struct{}{}
			err := g.await(fetchEnded(errors.New("fetch exploded")))
			if err == nil || !strings.Contains(err.Error(), "fetch exploded") {
				t.Fatalf("iteration %d: the completed fetch must be reported with its own error, got %v", i, err)
			}
		}
	})

	t.Run("BODY with the fetch still running", func(t *testing.T) {
		g := newBodyGate(t)
		g.got <- struct{}{}
		if err := g.await(make(chan error)); err != nil {
			t.Fatalf("a fetch in flight at the gate is the success case, got %v", err)
		}
	})

	t.Run("fetch fails before any BODY", func(t *testing.T) {
		g := newBodyGate(t)
		err := g.await(fetchEnded(errors.New("dial failed")))
		if err == nil || !strings.Contains(err.Error(), "dial failed") {
			t.Fatalf("an early failure is reported at once with its error, got %v", err)
		}
	})

	t.Run("bound", func(t *testing.T) {
		g := newBodyGate(t)
		g.bound = 10 * time.Millisecond
		if err := g.await(make(chan error)); err == nil || !strings.Contains(err.Error(), "no BODY reached") {
			t.Fatalf("a fetch that neither reaches the gate nor ends must hit the bound, got %v", err)
		}
	})

	t.Run("Open is idempotent", func(t *testing.T) {
		g := newBodyGate(t)
		g.Open()
		g.Open() // the cleanup opens it a third time
		select {
		case <-g.release:
		default:
			t.Fatal("Open did not release the gate")
		}
	})
}

// A test that ends while the server is holding a BODY must not leave the handler
// behind: cleanup opens the gate.
func TestBodyGateCleanupReleasesHandler(t *testing.T) {
	t.Run("ends at the gate", func(t *testing.T) {
		g := newBodyGate(t)
		p := newTestPool(t, gatedProvider(t, "a", 0, body430, g))
		done := make(chan error, 1)
		go func() { done <- fetchOnce(p, context.Background(), "cleanup-1") }()
		g.Wait(t, done) // returns with the handler parked on release; Open is never called
	})
	waitForNoGoroutines(t, "gatedProvider")
}

// waitForNoGoroutines polls until no goroutine stack mentions sub.
func waitForNoGoroutines(t *testing.T, sub string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if !strings.Contains(string(buf[:runtime.Stack(buf, true)]), sub) {
			return
		}
	}
	t.Fatalf("goroutines in %s are still running", sub)
}
