package stremio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/services/availnzb"
)

// gatedAvailNZB is a fake AvailNZB whose first bad report holds until release
// closes, pausing a failure report between the slot-level verdict and the
// cache purge that follows it.
type gatedAvailNZB struct {
	entered, release chan struct{}
	once, relOnce    sync.Once
	counts           availCounts
}

func withGatedAvailNZB(t *testing.T, f copyRetireFixture) *gatedAvailNZB {
	t.Helper()
	g := &gatedAvailNZB{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(g.resume)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body availnzb.ReportRequest
		if r.URL.Path != "/api/v1/report" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		if body.Status {
			g.counts.good.Add(1)
		} else {
			g.counts.bad.Add(1)
			g.once.Do(func() { close(g.entered) })
			<-g.release
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	f.server.availReporter = availnzb.NewReporter(availnzb.NewClient(ts.URL, "test-key"), fixedHosts{"news.example.com"})
	return g
}

// resume lets the held bad report finish.
func (g *gatedAvailNZB) resume() { g.relOnce.Do(func() { close(g.release) }) }

// A's last copy fails mid-stream and its bad report is in flight. Meanwhile A
// leaves the slot and a fresh /stream bootstrap clears the slot's failed mark,
// so the slot is open again — with replacement B in it, or empty. The rest of
// A's report (the cache purge) must not fail B's slot by id; an empty slot is
// still failed as before. A's own verdict is reported and recorded either way.
func TestLastCopyReportDoesNotFailAReplacement(t *testing.T) {
	for _, replaced := range []bool{true, false} {
		name := "slot empty"
		if replaced {
			name = "replacement installed"
		}
		t.Run(name, func(t *testing.T) {
			f, mgr := newHistoryFixture(t, 1)
			f = f.withoutCopyWalking()
			avail := withGatedAvailNZB(t, f)
			a := reportable(f.resolve(t, "NZBGeek"))
			f.server.recordPreloadAttempt(a)

			done := make(chan bool, 1)
			go func() { done <- f.server.failServedCopy(f.stream, a, errCorruptRead) }()
			select {
			case <-avail.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("A's bad report never reached AvailNZB")
			}
			if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
				t.Fatal("A's last-copy failure should fail its own slot while A holds it")
			}

			f.manager.DeleteSession(f.slot)
			// A real play list names its slots, which is what lets the
			// bootstrap clear their failed marks.
			list := variantTestPlaylist(f.key, f.rel)
			list.SlotPaths = []string{f.slot}
			f.server.playlistCache.Store(f.key.CacheKey(), &playlistCacheEntry{result: list, until: time.Now().Add(time.Minute)})
			if _, err := f.server.bootstrapPlaylistForPlay(context.Background(), f.key, f.stream); err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			if f.manager.GetSlotFailedDuringPlayback(f.slot) {
				t.Fatal("the fresh bootstrap should have cleared the slot's failed mark")
			}
			b := f.manager.PeekSession(f.slot)
			if replaced && b == nil {
				b = f.resolve(t, "NZBGeek")
			}
			if !replaced && b != nil {
				f.manager.DeleteSession(f.slot)
				b = nil
			}

			avail.resume()
			select {
			case recorded := <-done:
				if !recorded {
					t.Fatal("A's fatal read should be recorded")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("A's failure path never finished")
			}

			requireAvail(t, &avail.counts, 0, 1)
			requireRows(t, mgr, f.slot, "NZBGeek failed")
			if !replaced {
				if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
					t.Fatal("with the slot empty, A's last-copy verdict should fail it")
				}
				return
			}
			if f.manager.PeekSession(f.slot) != b || isClosedSession(b) {
				t.Fatal("A's report removed or closed the replacement")
			}
			if f.manager.GetSlotFailedDuringPlayback(f.slot) {
				t.Fatal("A's stale report failed the replacement's slot")
			}
			// The next play request finds B rather than being redirected past it.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/play/"+f.slot, nil)
			if got, _, ok := f.server.openPlaySession(rec, req, f.stream, f.slot); !ok || got != b {
				t.Fatalf("the next request did not get B (answered %d, Location %q)", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}
