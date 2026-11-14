package stremio

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// A request whose copy was retired cannot build the next copy and gives the
// slot up. If a duplicate request built that next copy (B) while this one was
// still trying, giving up must not fail B's slot or park its copy cursor.
func TestUnbuildableNextCopyGiveUpSparesAReplacement(t *testing.T) {
	f, held := newSearchingCopyFixture(t, 2)
	failed := f.resolve(t, "NZBGeek")
	f.expireSearch()

	done := make(chan copyRetry, 1)
	go func() {
		_, retry := f.server.retrySlotWithNextCopy(context.Background(), failed, f.slot, f.stream,
			fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable))
		done <- retry
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the rebuild never searched")
	}
	// The duplicate finds the search answered and builds the next copy.
	f.searchAnswers()
	b := f.resolve(t, "DrunkenSlug")
	// This request's own search comes back empty: its rebuild fails.
	close(held.release)
	select {
	case retry := <-done:
		if retry != copyRetryElsewhere {
			t.Fatalf("got outcome %d, want copyRetryElsewhere", retry)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the retry never finished")
	}

	if f.manager.PeekSession(f.slot) != b || isClosedSession(b) {
		t.Fatal("giving up removed or closed the replacement")
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("giving up failed the replacement's slot")
	}
	if got := f.manager.SlotCopyIndex(f.slot); got != 1 {
		t.Fatalf("giving up moved the replacement's cursor to %d, want 1", got)
	}
}

// The past-EOF backstop fails the slot of the session it served — but a stale
// verdict from session A, after replacement B took the slot, leaves B alone.
func TestPastEOFVerdictSparesAReplacement(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprintf("replaced=%v", replaced), func(t *testing.T) {
			f := newCopyRetireFixture(t, 1)
			a := f.resolve(t, "NZBGeek")
			b := a
			if replaced {
				f.manager.DeleteSession(f.slot)
				b = f.resolve(t, "NZBGeek")
			}
			resolved := &resolvedPlayback{session: a, sessionID: f.slot, size: 1000}
			answered := false
			for i := 0; i < maxPastEOFRangesBeforeFailover; i++ {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("GET", "/play/"+f.slot, nil)
				answered = f.server.failSlotOnRepeatedPastEOFRanges(w, r, f.stream, resolved, "bytes=5000-", func(string) {})
			}
			if !answered {
				t.Fatal("the threshold request should still be answered")
			}
			if got := f.manager.GetSlotFailedDuringPlayback(f.slot); got != !replaced {
				t.Fatalf("slot failed=%v, want %v", got, !replaced)
			}
			if f.manager.PeekSession(f.slot) != b {
				t.Fatal("the past-EOF verdict changed which session holds the slot")
			}
		})
	}
}
