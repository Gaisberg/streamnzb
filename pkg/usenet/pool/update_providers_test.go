package pool

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"testing"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/usenet/nntp"
)

var testGroups = []string{"alt.binaries.test"}

// testProvider is one provider backed by its own counting server.
func testProvider(t *testing.T, id string, missing bool) (ProviderConfig, *countingNNTPServer) {
	t.Helper()
	srv, host, port, stop := startCountingNNTPServer(t, missing)
	cp := nntp.NewClientPool(host, port, false, "user", "pass", 4)
	t.Cleanup(func() {
		cp.Shutdown()
		stop()
	})
	return ProviderConfig{ID: id, ClientPool: cp}, srv
}

// A session's view outlives the reload that removed its provider. It must move
// onto the new set rather than keep dialing a client pool that has been shut
// down — that was the "plays a few seconds, then EOF" after a provider save.
func TestUpdateProvidersMovesLiveViewOntoNewProviders(t *testing.T) {
	a, aSrv := testProvider(t, "a", false)
	b, bSrv := testProvider(t, "b", false)

	p, err := NewPool(&Config{Providers: []ProviderConfig{a}, SegmentCache: NewMemorySegmentCache()})
	if err != nil {
		t.Fatal(err)
	}
	view := p.Subset(nil)

	a.ClientPool.Shutdown()
	p.UpdateProviders([]ProviderConfig{b})

	if got := view.ProviderOrder(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("view providers = %v, want [b]", got)
	}
	if _, err := view.FetchSegment(context.Background(), &nzb.Segment{ID: "seg-moved"}, testGroups); err != nil {
		t.Fatalf("fetch through the live view after the update: %v", err)
	}
	if aSrv.bodies.Load() != 0 || bSrv.bodies.Load() != 1 {
		t.Fatalf("bodies a=%d b=%d, want a=0 b=1", aSrv.bodies.Load(), bSrv.bodies.Load())
	}
}

// A view that picked providers keeps to its pick across updates, and only
// falls back to the whole pool once nothing it picked is left.
func TestUpdateProvidersKeepsViewSelection(t *testing.T) {
	a, _ := testProvider(t, "a", false)
	b, _ := testProvider(t, "b", false)
	c, _ := testProvider(t, "c", false)

	p, err := NewPool(&Config{Providers: []ProviderConfig{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	all := p.Subset(nil)
	onlyB := p.Subset([]string{"b"})

	b.Priority, c.Priority = 1, 2
	p.UpdateProviders([]ProviderConfig{a, b, c})
	if got := all.ProviderOrder(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("all-providers view = %v, want [a b c]", got)
	}
	if got := onlyB.ProviderOrder(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("selected view = %v, want [b]", got)
	}

	p.UpdateProviders([]ProviderConfig{a, c})
	if got := onlyB.ProviderOrder(); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("view whose pick is gone = %v, want the whole pool [a c]", got)
	}
}

// The segment cache survives a provider update: what was fetched before the
// save is still served from memory after it, and nothing is held twice.
func TestUpdateProvidersKeepsSegmentCache(t *testing.T) {
	a, _ := testProvider(t, "a", false)
	b, bSrv := testProvider(t, "b", false)

	budget := NewSegmentCacheBudget(64)
	p, err := NewPool(&Config{Providers: []ProviderConfig{a}, SegmentCache: NewMemorySegmentCacheWithBudget(budget)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Subset(nil).FetchSegment(context.Background(), &nzb.Segment{ID: "seg-cached"}, testGroups); err != nil {
		t.Fatal(err)
	}
	held := budget.CurrentBytes()
	if held == 0 {
		t.Fatal("expected the fetch to be cached")
	}

	p.UpdateProviders([]ProviderConfig{b})
	if _, err := p.Subset(nil).FetchSegment(context.Background(), &nzb.Segment{ID: "seg-cached"}, testGroups); err != nil {
		t.Fatal(err)
	}
	if got := bSrv.bodies.Load(); got != 0 {
		t.Fatalf("expected a cache hit after the update, new provider served %d bodies", got)
	}
	if got := budget.CurrentBytes(); got != held {
		t.Fatalf("budget = %d after the update, want %d", got, held)
	}
}

// "Missing on every provider" was proven against the old set; a provider added
// since may carry the article.
func TestUpdateProvidersInvalidatesMissingMarkers(t *testing.T) {
	a, _ := testProvider(t, "a", true)
	b, bSrv := testProvider(t, "b", false)

	p, err := NewPool(&Config{Providers: []ProviderConfig{a}, SegmentCache: NewMemorySegmentCache()})
	if err != nil {
		t.Fatal(err)
	}
	seg := &nzb.Segment{ID: "seg-missing"}
	if _, err := p.FetchSegment(context.Background(), seg, testGroups); err == nil {
		t.Fatal("expected a 430 from the only provider")
	}
	if !p.isKnownMissing(seg.ID) {
		t.Fatal("expected the segment to be marked missing")
	}

	b.Priority = 1
	p.UpdateProviders([]ProviderConfig{a, b})
	if _, err := p.FetchSegment(context.Background(), seg, testGroups); err != nil {
		t.Fatalf("expected the added provider to serve the segment: %v", err)
	}
	if got := bSrv.bodies.Load(); got != 1 {
		t.Fatalf("added provider served %d bodies, want 1", got)
	}
}

// Streams keep fetching while a save lands. Run under -race.
func TestUpdateProvidersConcurrentWithFetches(t *testing.T) {
	a, _ := testProvider(t, "a", false)
	b, _ := testProvider(t, "b", false)

	p, err := NewPool(&Config{Providers: []ProviderConfig{a}, SegmentCache: NewMemorySegmentCache()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	done := make(chan error, 4)
	for w := range 4 {
		view := p.SubsetForLease("stream", nil, map[string]int{"a": 2, "b": 2})
		go func() {
			for i := range 25 {
				seg := &nzb.Segment{ID: fmt.Sprintf("seg-%d-%d", w, i)}
				if _, err := view.FetchSegment(ctx, seg, testGroups); err != nil {
					done <- err
					return
				}
				_, _ = view.StatSegment(ctx, seg.ID, testGroups)
			}
			done <- nil
		}()
	}
	for i := range 20 {
		if i%2 == 0 {
			p.UpdateProviders([]ProviderConfig{b})
		} else {
			p.UpdateProviders([]ProviderConfig{a, b})
		}
	}
	for range 4 {
		if err := <-done; err != nil {
			t.Fatalf("fetch during provider updates: %v", err)
		}
	}
}

func TestSetSegmentCacheBudgetEvictsToNewCap(t *testing.T) {
	const mb = 1024 * 1024
	oldBudget := NewSegmentCacheBudget(4)
	cache := NewMemorySegmentCacheWithBudget(oldBudget)
	for _, id := range []string{"s1", "s2", "s3"} {
		cache.Set(id, SegmentData{Body: make([]byte, mb)})
	}

	p := &Pool{cache: cache}
	newBudget := NewSegmentCacheBudget(2)
	p.SetSegmentCacheBudget(newBudget)

	if got := oldBudget.CurrentBytes(); got != 0 {
		t.Fatalf("old budget still holds %d bytes", got)
	}
	if got := newBudget.CurrentBytes(); got != 2*mb {
		t.Fatalf("new budget holds %d bytes, want %d", got, 2*mb)
	}
	if _, ok := cache.Get("s1"); ok {
		t.Fatal("expected the least recently used entry to be evicted")
	}
	if _, ok := cache.Get("s3"); !ok {
		t.Fatal("expected the most recent entry to survive")
	}
}

// Views are cut on every playlist request; the registry must not keep the
// ones nobody holds any more.
func TestViewRegistryDropsCollectedViews(t *testing.T) {
	a, _ := testProvider(t, "a", false)
	p, err := NewPool(&Config{Providers: []ProviderConfig{a}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 * viewRegistryMinPrune {
		_ = p.Subset(nil)
	}
	kept := p.Subset(nil)
	runtime.GC()

	live := p.views.live()
	if len(live) != 1 || live[0] != kept {
		t.Fatalf("live views = %d, want only the one still referenced", len(live))
	}
	runtime.KeepAlive(kept)
}
