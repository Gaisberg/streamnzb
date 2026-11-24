package pool

import (
	"context"
	"errors"
	"net"
	"net/textproto"
	"runtime"
	"strings"
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

// unreachableProvider points at a port nothing listens on.
func unreachableProvider(t *testing.T, id string, priority int, backup bool) ProviderConfig {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	cp := nntp.NewClientPool(addr.IP.String(), addr.Port, false, id, "pass", 1)
	t.Cleanup(cp.Shutdown)
	return ProviderConfig{ID: id, Priority: priority, IsBackup: backup, ClientPool: cp}
}

// gatedProvider answers BODY with bodyResponse only once release is closed, and
// signals got when a BODY arrives, so a test can act while a fetch is in flight.
func gatedProvider(t *testing.T, id string, priority int, bodyResponse string, got chan<- struct{}, release <-chan struct{}) ProviderConfig {
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
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					switch cmd := string(buf[:n]); {
					case strings.HasPrefix(cmd, "AUTHINFO USER"):
						_, _ = c.Write([]byte("381 PASS required\r\n"))
					case strings.HasPrefix(cmd, "AUTHINFO PASS"):
						_, _ = c.Write([]byte("281 Authentication accepted\r\n"))
					case strings.HasPrefix(cmd, "GROUP"):
						_, _ = c.Write([]byte("211 group selected\r\n"))
					case strings.HasPrefix(cmd, "BODY"):
						select {
						case got <- struct{}{}:
						default:
						}
						<-release
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
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "b", 1, false, body))

	_, err := p.FetchSegment(context.Background(), &nzb.Segment{ID: "mix-1", Bytes: 430}, testGroupList)
	if err == nil || !nntp.IsArticleNotFound(err) || isDefinitive(err) {
		t.Fatalf("want legacy 430 without the marker, got %v", err)
	}
}

func TestFetchIncompleteCoverageIsNotDefinitive(t *testing.T) {
	cases := map[string]struct {
		providers func(t *testing.T) []ProviderConfig
		timeout   time.Duration
	}{
		"430 plus unreachable primary": {
			providers: func(t *testing.T) []ProviderConfig {
				return []ProviderConfig{scriptedProvider(t, "a", 0, false, body430), unreachableProvider(t, "dead", 1, false)}
			},
			timeout: time.Second, // the dialer retries for ~45s; the deadline just bounds the test
		},
		"430 plus unreachable backup": {
			providers: func(t *testing.T) []ProviderConfig {
				return []ProviderConfig{scriptedProvider(t, "a", 0, false, body430), unreachableProvider(t, "dead", 1, true)}
			},
			timeout: time.Second,
		},
		"430 plus transient failure": {
			providers: func(t *testing.T) []ProviderConfig {
				return []ProviderConfig{scriptedProvider(t, "a", 0, false, body430), scriptedProvider(t, "b", 1, false, body502)}
			},
		},
		"430 plus timeout": {
			providers: func(t *testing.T) []ProviderConfig {
				return []ProviderConfig{scriptedProvider(t, "a", 0, false, body430), scriptedProvider(t, "hang", 1, false, bodyHang)}
			},
			timeout: 500 * time.Millisecond,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := newTestPool(t, tc.providers(t)...)
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
	got := make(chan struct{}, 1)
	release := make(chan struct{})
	a := gatedProvider(t, "a", 0, body430, got, release)
	hostB, portB, stopB := startScriptedNNTPServer(t, bodyOK)
	defer stopB()
	p := newTestPool(t, a)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "mid-1") }()
	<-got // the old endpoint has the BODY command
	a.ClientPool.Reconfigure(hostB, portB, false, "a", "pass", 2)
	p.UpdateProviders([]ProviderConfig{a})
	close(release)

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
// published stores proof for the old configuration under the new key. The proof
// no longer matches, so the hit is unmarked.
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
	defer stopB()
	bPool := nntp.NewClientPool(bHost, bPort, false, "b", "pass", 2)
	defer bPool.Shutdown()
	root := newTestPool(t, a, ProviderConfig{ID: "b", Priority: 1, ClientPool: bPool})
	viewA, viewAB := root.Subset([]string{"a"}), root.Subset([]string{"a", "b"})

	started, release := make(chan struct{}), make(chan struct{})
	leader := make(chan error, 1)
	go func() {
		_, err, _ := viewA.sf.Do("shared-1", func() (interface{}, error) {
			close(started)
			<-release
			return viewA.fetchSegmentOnce(context.Background(), "shared-1", &nzb.Segment{ID: "shared-1"}, testGroupList)
		})
		leader <- viewA.attest(context.Background(), err)
	}()
	<-started
	joiner := make(chan error, 1)
	go func() { joiner <- fetchOnce(viewAB, context.Background(), "shared-1") }()
	waitForWaiters(t, 1)
	close(release)

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

	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _, _ = p.sf.Do("waiter-1", func() (interface{}, error) {
			close(started)
			<-release
			return p.fetchSegmentOnce(context.Background(), "waiter-1", &nzb.Segment{ID: "waiter-1"}, testGroupList)
		})
	}()
	<-started

	canceledCtx, cancel := context.WithCancel(context.Background())
	canceled, live := make(chan error, 1), make(chan error, 1)
	go func() { canceled <- fetchOnce(p, canceledCtx, "waiter-1") }()
	go func() { live <- fetchOnce(p, context.Background(), "waiter-1") }()
	waitForWaiters(t, 2)
	cancel()
	close(release)

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

func TestFetchCancellationIsNotDefinitive(t *testing.T) {
	p := newTestPool(t, scriptedProvider(t, "hang", 0, false, bodyHang))

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	err := fetchOnce(p, ctx, "cancel-1")
	if err == nil {
		t.Fatal("expected the cancelled fetch to fail")
	}
	if isDefinitive(err) || p.isKnownMissing("cancel-1") {
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
	gotA, relA := make(chan struct{}, 1), make(chan struct{})
	gotB2, relB2 := make(chan struct{}, 1), make(chan struct{})
	a := gatedProvider(t, "a", 0, body430, gotA, relA)
	b1 := scriptedProvider(t, "b", 1, false, body430)
	b2 := gatedProvider(t, "b", 1, body430, gotB2, relB2)
	p := newTestPool(t, a, b1)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "restore-1") }()
	<-gotA
	p.UpdateProviders([]ProviderConfig{a, b2})
	close(relA)
	<-gotB2 // the replacement, not the original, holds the BODY
	p.UpdateProviders([]ProviderConfig{a, b1})
	close(relB2)

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
	gotA, relA := make(chan struct{}, 1), make(chan struct{})
	a := gatedProvider(t, "a", 0, body430, gotA, relA)
	b1 := scriptedProvider(t, "b", 1, false, body430)
	b2 := scriptedProvider(t, "b", 1, false, body430)
	p := newTestPool(t, a, b1)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "kept-1") }()
	<-gotA
	p.UpdateProviders([]ProviderConfig{a, b2})
	close(relA)

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
			got, rel := make(chan struct{}, 1), make(chan struct{})
			a := gatedProvider(t, "a", 0, body430, got, rel)
			b := scriptedProvider(t, "b", 1, false, body430)
			p := newTestPool(t, tc.start(a, b)...)

			done := make(chan error, 1)
			go func() { done <- fetchOnce(p, context.Background(), "churn-1") }()
			<-got
			p.UpdateProviders(tc.end(a, b))
			close(rel)

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
	got, rel := make(chan struct{}, 1), make(chan struct{})
	a := scriptedProvider(t, "a", 0, false, body430)
	c := gatedProvider(t, "c", 1, body430, got, rel)
	c.IsBackup = true
	hostN, portN, stopN := startScriptedNNTPServer(t, bodyOK)
	defer stopN()
	p := newTestPool(t, a, c)

	done := make(chan error, 1)
	go func() { done <- fetchOnce(p, context.Background(), "backup-1") }()
	<-got
	c.ClientPool.Reconfigure(hostN, portN, false, "c", "pass", 2)
	p.UpdateProviders([]ProviderConfig{a, c})
	close(rel)

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

			p.mu.Lock() // p.stamp, inside attest, now blocks on RLock
			res := make(chan error, 1)
			go func() { res <- p.attest(ctx, legacy) }()
			waitForGoroutines(t, 1, "(*Pool).stamp", "RWMutex")
			if tc.cancel {
				cancel()
			}
			p.mu.Unlock()

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
	p := newTestPool(t,
		scriptedProvider(t, "a", 0, false, body430),
		scriptedProvider(t, "b", 1, false, body430))

	live := fetchOnce(p, context.Background(), "agree-1")
	cached := fetchOnce(p, context.Background(), "agree-1")
	if !isDefinitive(live) || !isDefinitive(cached) {
		t.Fatalf("both paths must be definitive: live=%v cached=%v", live, cached)
	}
	if len(AttemptedProviderHosts(live)) == 0 || !nntp.IsArticleNotFound(cached) {
		t.Fatal("attempted hosts (live) and legacy classification (cached) must survive")
	}
}
