package stremio

import (
	"context"
	"fmt"
	"testing"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/query"
	"streamnzb/pkg/session"
)

// copyRetireFixture is one slot whose release three indexers carry, with
// copy walking enabled for every copy.
type copyRetireFixture struct {
	manager *session.Manager
	server  *Server
	key     StreamSlotKey
	slot    string
	stream  *auth.Stream
}

func newCopyRetireFixture(t *testing.T, copies int) copyRetireFixture {
	t.Helper()
	initFailoverTestLogger()
	manager := session.NewManager(nil, time.Minute)
	t.Cleanup(manager.Shutdown)
	server := &Server{config: &config.Config{}, sessionManager: manager}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: "tt123"}

	indexers := []string{"NZBGeek", "DrunkenSlug", "NZBFinder"}[:copies]
	var all []*release.Release
	for i, name := range indexers {
		all = append(all, &release.Release{
			Title:      "Movie.2160p.Remux.HDR10-FraMeSToR",
			Link:       fmt.Sprintf("https://%d.invalid/nzb", i),
			DetailsURL: fmt.Sprintf("https://%d.invalid/details", i),
			Indexer:    name,
		})
	}
	rel := all[0]
	rel.Variants = all[1:]
	server.playlistCache.Store(key.CacheKey(), &playlistCacheEntry{
		result: variantTestPlaylist(key, rel),
		until:  time.Now().Add(time.Minute),
	})
	server.rawSearchCache.Store(key.CacheKey(), &rawSearchCacheEntry{
		raw:   &rawSearchResult{Params: &query.SearchParams{ContentType: key.ContentType, ID: key.ID}},
		until: time.Now().Add(time.Minute),
	})
	return copyRetireFixture{
		manager: manager,
		server:  server,
		key:     key,
		slot:    key.SlotPath(0),
		stream:  &auth.Stream{Username: key.StreamID, VariantAttempts: copies},
	}
}

func (f copyRetireFixture) resolve(t *testing.T, wantIndexer string) *session.Session {
	t.Helper()
	sess, err := f.server.resolveStreamSlot(context.Background(), f.key, 0, f.stream)
	if err != nil {
		t.Fatalf("resolveStreamSlot: %v", err)
	}
	if got := sess.Release().Indexer; got != wantIndexer {
		t.Fatalf("slot plays %q, want %q", got, wantIndexer)
	}
	return sess
}

// Stremio sends duplicate play requests for one slot, and both run startup on
// the same session. When that copy fails, both report it. Only the first
// report may move the slot on; the second is about a copy that is already
// retired, and acting on it again skipped the next copy and tore down the
// session the first request had just built for it.
func TestStaleCopyFailureDoesNotSkipTheNextCopy(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 3)
	failed := f.resolve(t, "NZBGeek")
	missing := fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable)

	next, retry := f.server.retrySlotWithNextCopy(context.Background(), failed, f.slot, f.stream, missing)
	if retry != copyRetryNext || next.Release().Indexer != "DrunkenSlug" {
		t.Fatalf("first report: expected the second copy, got outcome %d", retry)
	}

	again, retry := f.server.retrySlotWithNextCopy(context.Background(), failed, f.slot, f.stream, missing)
	if got := f.manager.SlotCopyIndex(f.slot); got != 1 {
		t.Fatalf("a duplicate report moved the cursor to copy %d; copy 1 was never tried", got)
	}
	if f.manager.PeekSession(f.slot) != next {
		t.Fatal("a duplicate report tore down the session built for the next copy")
	}
	if retry != copyRetryNext || again != next {
		t.Fatal("the duplicate request should carry on with the copy the slot already moved to")
	}
}

// With the last copy now playing, a duplicate report about the retired copy
// used to fail the whole slot instead.
func TestStaleSlotFailureDoesNotCondemnTheRebuiltCopy(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	failed := f.resolve(t, "NZBGeek")
	missing := fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable)

	next, retry := f.server.retrySlotWithNextCopy(context.Background(), failed, f.slot, f.stream, missing)
	if retry != copyRetryNext || next.Release().Indexer != "DrunkenSlug" {
		t.Fatalf("first report: expected the second copy, got outcome %d", retry)
	}
	// The duplicate request finds no copy left to move to. Before the fix it
	// fell through to the slot-level verdict, still holding the retired
	// session; resolvePlaybackSlot does exactly this for copyRetryNone.
	if _, retry := f.server.retrySlotWithNextCopy(context.Background(), failed, f.slot, f.stream, missing); retry == copyRetryNone {
		f.server.recordFailedSlot(failed, f.slot, missing)
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a report about the retired copy failed the slot the next copy is playing in")
	}
	if f.manager.PeekSession(f.slot) != next {
		t.Fatal("a report about the retired copy deleted the next copy's session")
	}
}

// A preload probe that condemns a copy has to leave the slot without that
// copy's session; otherwise the real play finds the session by id, starts on
// the condemned NZB again, and its failure moves the cursor a second time.
func TestPreloadVerdictRetiresTheCopysSession(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	failed := f.resolve(t, "NZBGeek")

	f.server.retirePreloadedCopy(failed, fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable), f.stream)

	if f.manager.PeekSession(f.slot) == failed {
		t.Fatal("the condemned copy's session is still bound to the slot")
	}
	f.resolve(t, "DrunkenSlug")
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a copy-scoped preload verdict must leave the slot playable")
	}
}

// A read that was already in flight on the retired copy can fail after the
// player has reconnected to the next copy. That straggler is about the old
// NZB: it must not fail the slot, or report the copy now playing as bad. Its
// own serve still counts as failed, so its bookkeeping cannot report the
// condemned copy good.
func TestStaleServedFailureLeavesTheRebuiltCopyAlone(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	served := f.resolve(t, "NZBGeek")
	fatal := fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills)

	if !f.server.failServedCopy(f.stream, served, fatal) {
		t.Fatal("the first fatal read should be recorded")
	}
	next := f.resolve(t, "DrunkenSlug")

	if !f.server.failServedCopy(f.stream, served, fatal) {
		t.Fatal("the straggler's serve must count as failed, or its bookkeeping may report the condemned copy good")
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a straggling read from the retired copy failed the slot")
	}
	if f.manager.PeekSession(f.slot) != next {
		t.Fatal("a straggling read from the retired copy removed the next copy's session")
	}
}

// A served session that left the slot without a copy verdict — evicted, say —
// keeps the old answer: the slot is marked so the player is not sent back to it.
func TestServedFailureOnAnEvictedSessionFailsTheSlot(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	served := f.resolve(t, "NZBGeek")
	f.manager.DeleteSession(f.slot)

	f.server.failServedCopy(f.stream, served, fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills))
	if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a fatal read on an evicted session should still mark the slot failed")
	}
	if got := f.manager.SlotCopyIndex(f.slot); got != 0 {
		t.Fatalf("an evicted session's report moved the cursor to %d", got)
	}
}

// When the next copy cannot be built, the release gives up right there: the
// slot is failed and the caller records nothing more. The failed session is
// already closed, so running the slot-level verdict on it again would report
// it a second time with nothing to report, and delete whatever a duplicate
// request had built under the slot id.
func TestUnbuildableNextCopyFailsTheSlotWithoutASecondVerdict(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	failed := f.resolve(t, "NZBGeek")
	cached, _ := f.server.playlistCache.Load(f.key.CacheKey())
	cached.(*playlistCacheEntry).result.Candidates[0].Release.Variants[0].Link = ""

	_, retry := f.server.retrySlotWithNextCopy(context.Background(), failed, f.slot, f.stream,
		fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable))
	if retry != copyRetryElsewhere {
		t.Fatalf("expected the failure to be fully handled here, got outcome %d", retry)
	}
	if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("the slot should be failed once its next copy cannot be built")
	}
	if got := f.manager.SlotCopyIndex(f.slot); got < 2 {
		t.Fatalf("the cursor should be parked past the copies, got %d", got)
	}
}

// A preload verdict that lands while a play is starting on the same session
// must not close that session: the play request would end with no response.
// The slot is left to the play, whose own failure moves it on.
func TestPreloadVerdictLeavesASessionInUseToThePlay(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2)
	inUse := f.resolve(t, "NZBGeek")
	if !f.manager.BeginSessionStartup(inUse) {
		t.Fatal("the play should register its startup on the slot's session")
	}
	t.Cleanup(func() { f.manager.EndSessionStartup(inUse) })

	f.server.retirePreloadedCopy(inUse, fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable), f.stream)

	if f.manager.PeekSession(f.slot) != inUse {
		t.Fatal("the preload verdict closed a session a play is starting on")
	}
	if got := f.manager.SlotCopyIndex(f.slot); got != 0 {
		t.Fatalf("the preload verdict moved the cursor to %d under a play in progress", got)
	}
	if f.manager.GetSlotFailedDuringPlayback(f.slot) {
		t.Fatal("a copy-scoped preload verdict must leave the slot playable")
	}
}

// withoutCopyWalking is the default stream setup: VariantAttempts unset, so a
// slot never moves to another copy, however many indexers carry the release.
func (f copyRetireFixture) withoutCopyWalking() copyRetireFixture {
	f.stream = &auth.Stream{Username: f.key.StreamID}
	return f
}

// With copy walking off, a conclusive preload verdict condemns the release, as
// it did before copies could be walked: the slot is failed so the next play
// skips it instead of spending a startup on it. A play holding the session —
// starting, or already validated — keeps its session; it is not closed.
func TestPreloadVerdictWithoutCopyWalkingFailsTheRelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, f copyRetireFixture, sess *session.Session)
	}{
		{"idle", func(*testing.T, copyRetireFixture, *session.Session) {}},
		{"busy", func(t *testing.T, f copyRetireFixture, sess *session.Session) {
			if !f.manager.BeginSessionStartup(sess) {
				t.Fatal("startup should register")
			}
		}},
		{"previously played", func(_ *testing.T, f copyRetireFixture, _ *session.Session) {
			f.manager.MarkPlaybackValidated(f.slot)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newCopyRetireFixture(t, 3).withoutCopyWalking()
			sess := f.resolve(t, "NZBGeek")
			tc.prep(t, f, sess)

			f.server.retirePreloadedCopy(sess, fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable), f.stream)

			if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
				t.Fatal("the release has no copy to walk to; its slot should be failed")
			}
			if f.manager.PeekSession(f.slot) != sess || sess.Release() == nil {
				t.Fatal("a release-level verdict must not close or replace the slot's session")
			}
			if got := f.manager.SlotCopyIndex(f.slot); got != 0 {
				t.Fatalf("copy walking is off, yet the cursor moved to %d", got)
			}
		})
	}
}

// A preload verdict racing a play's startup on the same session: whichever
// wins, the play never ends up holding a closed session, and the verdict is
// never lost or misapplied.
func TestPreloadVerdictRacingAPlayStartup(t *testing.T) {
	t.Parallel()
	for i := 0; i < 50; i++ {
		for _, walk := range []bool{false, true} {
			f := newCopyRetireFixture(t, 2)
			if !walk {
				f = f.withoutCopyWalking()
			}
			sess := f.resolve(t, "NZBGeek")
			started := make(chan bool)
			go func() { started <- f.manager.BeginSessionStartup(sess) }()
			f.server.retirePreloadedCopy(sess, fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable), f.stream)
			ok := <-started

			slotFailed := f.manager.GetSlotFailedDuringPlayback(f.slot)
			bound := f.manager.PeekSession(f.slot) == sess
			switch {
			case !walk:
				if !ok || !bound || !slotFailed {
					t.Fatalf("walking off: startup=%v bound=%v slotFailed=%v, want all true", ok, bound, slotFailed)
				}
			case ok:
				if !bound || slotFailed || f.manager.SlotCopyIndex(f.slot) != 0 {
					t.Fatal("the play registered first; the preload must leave its session and slot alone")
				}
			default:
				if bound || slotFailed || f.manager.SlotCopyIndex(f.slot) != 1 {
					t.Fatal("the preload retired the copy first; the play must see the slot moved to copy 1")
				}
			}
			if ok {
				f.manager.EndSessionStartup(sess)
			}
		}
	}
}

// Duplicate preload verdicts on one session change nothing a first verdict
// did not. With copies walked, a stale verdict about the retired copy must not
// move the cursor again or touch the session now playing the next copy.
func TestDuplicatePreloadVerdicts(t *testing.T) {
	t.Parallel()
	bad := fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable)

	t.Run("copy walking off", func(t *testing.T) {
		t.Parallel()
		f := newCopyRetireFixture(t, 2).withoutCopyWalking()
		sess := f.resolve(t, "NZBGeek")
		f.server.retirePreloadedCopy(sess, bad, f.stream)
		f.server.retirePreloadedCopy(sess, bad, f.stream)
		if !f.manager.GetSlotFailedDuringPlayback(f.slot) || f.manager.PeekSession(f.slot) != sess {
			t.Fatal("repeated release-level verdicts should leave the slot failed and the session intact")
		}
	})

	t.Run("copy walking on", func(t *testing.T) {
		t.Parallel()
		f := newCopyRetireFixture(t, 2)
		failed := f.resolve(t, "NZBGeek")
		f.server.retirePreloadedCopy(failed, bad, f.stream)
		next := f.resolve(t, "DrunkenSlug")
		if !f.manager.BeginSessionStartup(next) {
			t.Fatal("startup on the replacement should register")
		}
		t.Cleanup(func() { f.manager.EndSessionStartup(next) })

		f.server.retirePreloadedCopy(failed, bad, f.stream)

		if got := f.manager.SlotCopyIndex(f.slot); got != 1 {
			t.Fatalf("a stale verdict moved the cursor to %d", got)
		}
		if f.manager.PeekSession(f.slot) != next || f.manager.GetSlotFailedDuringPlayback(f.slot) {
			t.Fatal("a stale verdict invalidated the session playing the next copy")
		}
	})
}

// A temporary preload failure is no verdict: preloadSlots only retires a copy
// once preloadConfirmsBadRelease says the failure is conclusive.
func TestTemporaryPreloadFailureIsNotAVerdict(t *testing.T) {
	t.Parallel()
	f := newCopyRetireFixture(t, 2).withoutCopyWalking()
	for _, err := range []error{
		context.DeadlineExceeded,
		context.Canceled,
		fmt.Errorf("probe: %w", ErrPlaybackStartupTimeout),
	} {
		if f.server.preloadConfirmsBadRelease(err) {
			t.Fatalf("%v is temporary, yet counts as a bad-release verdict", err)
		}
	}
	if !f.server.preloadConfirmsBadRelease(fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable)) {
		t.Fatal("a missing first segment should stay a conclusive verdict")
	}
}
