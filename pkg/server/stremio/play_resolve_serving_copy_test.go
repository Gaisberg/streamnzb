package stremio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"streamnzb/pkg/media/seek"
	"streamnzb/pkg/playback"
	"streamnzb/pkg/release"
	"streamnzb/pkg/search/triage"
	"streamnzb/pkg/session"
)

// servingCopy starts a real serve of sess and returns once it is reading, held
// until the returned release is called; done closes when the client has the
// whole body, and received then holds how much of it arrived.
func servingCopy(t *testing.T, f copyRetireFixture, sess *session.Session, size int64) (release func(), done <-chan struct{}, received *int64) {
	t.Helper()
	// The first byte would start a fallback prefetch that outlives the test.
	sess.Once(onceFallbackPrefetched)
	file := &gatedFile{diagFile: &diagFile{size: size}, reading: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release = func() { once.Do(func() { close(file.release) }) }
	serveDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serveDone)
		ctx, cancel := context.WithCancel(r.Context())
		f.server.servePlaybackStream(w, r, f.stream, &resolvedPlayback{
			session: sess, sessionID: sess.ID, stream: file,
			name: "video.mkv", size: size, ctx: ctx, cancel: cancel,
		}, time.Now())
	}))
	t.Cleanup(srv.Close)
	// Runs before srv.Close, which waits for a serve a failed assertion left held.
	t.Cleanup(func() {
		release()
		<-serveDone
	})
	clientDone := make(chan struct{})
	received = new(int64)
	go func() {
		defer close(clientDone)
		resp, err := http.Get(srv.URL)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		*received, _ = io.Copy(io.Discard, resp.Body)
	}()
	select {
	case <-file.reading:
	case <-serveDone:
		t.Fatal("the serve ended without reading")
	}
	return release, clientDone, received
}

// withFailingPrepare gives the fixture a real playback service. The fixture's
// sessions have no NZB to load, so every prepare fails at once, and not as a
// cancellation.
func (f copyRetireFixture) withFailingPrepare(t *testing.T) copyRetireFixture {
	t.Cleanup(f.server.Shutdown)
	t.Cleanup(f.server.scrobbleSends.Wait)
	f.server.playback = &playback.Service{Sessions: f.manager, StartupTimeout: func() time.Duration { return time.Second }}
	return f
}

// requestSlot runs one play request for slot through resolvePlaybackSlot, which
// is expected to fail it, and returns the response.
func (f copyRetireFixture) requestSlot(t *testing.T, slot string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/play/"+slot, nil)
	if _, ok := f.server.resolvePlaybackSlot(rec, req, f.stream, slot); ok {
		t.Fatal("the request's prepare should have failed")
	}
	return rec
}

func isClosedSession(s *session.Session) bool {
	select {
	case <-s.Done():
		return true
	default:
		return false
	}
}

// requireKept fails unless held is still the slot's session, open, with the
// slot neither failed nor moved to another copy.
func requireKept(t *testing.T, f copyRetireFixture, slot string, held *session.Session) {
	t.Helper()
	if f.manager.PeekSession(slot) != held {
		t.Fatal("the failed request took the held session out of the slot")
	}
	if isClosedSession(held) {
		t.Fatal("the failed request closed the session another request holds")
	}
	if f.manager.GetSlotFailedDuringPlayback(slot) {
		t.Fatal("the failed request failed the slot under the other request")
	}
	if got := f.manager.SlotCopyIndex(slot); got != 0 {
		t.Fatalf("the failed request moved the slot to copy %d", got)
	}
}

// A second request on a slot whose session another request is serving — a
// Stremio duplicate, or the player's next range — opens its own stream, and
// that can fail on its own: a startup timeout, a busy pool. Its failure says
// nothing about the copy being streamed, so it must not retire the copy, fail
// the slot or reset the session's stream state under the serve: the served
// session stays in the slot, open, with its accounting, and the serve plays to
// the end. The failed request alone gets an error.
func TestPrepareFailureLeavesASessionAnotherRequestServes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		walking bool
	}{
		{"copy walking", true},
		{"copy walking off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopyRetireFixture(t, 2)
			if !tc.walking {
				f = f.withoutCopyWalking()
			}
			f = f.withFailingPrepare(t)
			served := f.resolve(t, "NZBGeek")
			const size = 1 << 20
			// The serve's probe left a snapshot, so the second request opens
			// from it and fails there.
			served.CachePlaybackStreamSnapshot(session.PlaybackStreamSpec{Key: "video", Name: "video.mkv", Size: size}, seek.StreamStartInfo{}, true)
			repairs := served.HoleFillPatches()
			release, clientDone, received := servingCopy(t, f, served, size)

			rec := f.requestSlot(t, f.slot)

			requireKept(t, f, f.slot, served)
			if got := served.CurrentActivePlays(); got != 1 || !served.IsPlaybackValidated() {
				t.Fatalf("served session plays=%d validated=%v, want 1 and validated", got, served.IsPlaybackValidated())
			}
			if _, ok := served.PlaybackStreamSnapshot(); !ok {
				t.Fatal("the failed request reset the served session's stream snapshot")
			}
			if served.HoleFillPatches() != repairs {
				t.Fatal("the failed request replaced the served session's repair cache")
			}
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("failed request answered %d (Retry-After %q), want %d with a retry hint",
					rec.Code, rec.Header().Get("Retry-After"), http.StatusServiceUnavailable)
			}

			release()
			<-clientDone
			if *received != size {
				t.Fatalf("the serve delivered %d bytes, want the whole %d", *received, size)
			}
			if got := served.BytesRead(); got != size {
				t.Fatalf("served session counted %d bytes, want %d", got, size)
			}

			// The serve is over and its startup handed over, so nothing holds
			// the session: the same failure now is the copy's verdict.
			f.requestSlot(t, f.slot)
			if !isClosedSession(served) || f.manager.PeekSession(f.slot) == served {
				t.Fatal("with every hold released a failing copy should leave the slot")
			}
			// Every copy in this fixture fails, so the walk ends with the slot
			// failed — after trying the next copy when walking is on.
			wantIndex := 0
			if tc.walking {
				wantIndex = 1
			}
			if got := f.manager.SlotCopyIndex(f.slot); got != wantIndex || !f.manager.GetSlotFailedDuringPlayback(f.slot) {
				t.Fatalf("copy index %d, slot failed=%v; want index %d and the slot failed",
					got, f.manager.GetSlotFailedDuringPlayback(f.slot), wantIndex)
			}
		})
	}
}

// A duplicate play request still starting on the session holds it just the
// same: the other request's failure must not close the session that startup is
// about to serve. The last request to fail is the one that decides.
func TestPrepareFailureLeavesASessionAnotherRequestIsStarting(t *testing.T) {
	f := newCopyRetireFixture(t, 2).withFailingPrepare(t)
	starting := f.resolve(t, "NZBGeek")
	startup := f.manager.BeginSessionStartup(starting)
	if startup == nil {
		t.Fatal("startup should register")
	}
	// Validating on the other request's behalf must not clear its hold.
	f.manager.MarkPlaybackValidated(starting)

	if rec := f.requestSlot(t, f.slot); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed request answered %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	requireKept(t, f, f.slot, starting)

	// The starting request fails too, and nobody else holds the session.
	startup.Release()
	f.requestSlot(t, f.slot)
	if !isClosedSession(starting) {
		t.Fatal("the last request to fail did not retire the copy")
	}
}

// Whatever kept this request from opening — its startup timing out, or a
// transient error — another request serving the session holds it, and the
// failure is no verdict on the copy.
func TestStartupFailureOfAnyKindLeavesAServedSession(t *testing.T) {
	for _, prepareErr := range []error{
		fmt.Errorf("probe: %w", ErrPlaybackStartupTimeout),
		errors.New("connection pool exhausted"),
	} {
		t.Run(prepareErr.Error(), func(t *testing.T) {
			f := newCopyRetireFixture(t, 2)
			served := f.resolve(t, "NZBGeek")
			f.manager.StartPlayback(served, "10.0.0.1")

			if _, retry := f.server.retrySlotWithNextCopy(context.Background(), served, f.slot, f.stream, prepareErr); retry != copyRetryHeld {
				t.Fatalf("got outcome %d, want copyRetryHeld", retry)
			}
			requireKept(t, f, f.slot, served)
		})
	}
}

// Walking the fallbacks, a slot another request holds that fails to open for
// this request is not retired or failed under that request. Being served, it
// plays, so the client is sent to it rather than past it; only starting, it is
// not proven yet, and the client gets the bounded 503 instead.
func TestFallbackWalkAnswersASlotAnotherRequestHolds(t *testing.T) {
	for _, serving := range []bool{true, false} {
		t.Run(fmt.Sprintf("serving=%v", serving), func(t *testing.T) {
			f := newCopyRetireFixture(t, 1).withFailingPrepare(t)
			other := &release.Release{Title: "Movie.1080p.WEB-DL-GRP", Link: "https://other.invalid/nzb", DetailsURL: "https://other.invalid/details", Indexer: "DrunkenSlug"}
			list := variantTestPlaylist(f.key, f.rel)
			list.Candidates = append(list.Candidates, triage.Candidate{Release: other})
			f.server.playlistCache.Store(f.key.CacheKey(), &playlistCacheEntry{result: list, until: time.Now().Add(time.Minute)})

			f.resolve(t, "NZBGeek")
			fallback := f.key.SlotPath(1)
			held, err := f.server.resolveStreamSlot(context.Background(), f.key, 1, f.stream)
			if err != nil {
				t.Fatalf("resolve fallback slot: %v", err)
			}
			if serving {
				release, _, _ := servingCopy(t, f, held, 1<<20)
				defer release()
			} else {
				t.Cleanup(f.manager.BeginSessionStartup(held).Release)
			}

			rec := f.requestSlot(t, f.slot)

			requireKept(t, f, fallback, held)
			if serving {
				if rec.Code != http.StatusTemporaryRedirect || !strings.HasSuffix(rec.Header().Get("Location"), "/play/"+fallback) {
					t.Fatalf("answered %d to %q, want a redirect to the served fallback slot %s", rec.Code, rec.Header().Get("Location"), fallback)
				}
			} else if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("answered %d, want %d for a fallback that is only starting", rec.Code, http.StatusServiceUnavailable)
			}
			if !f.manager.GetSlotFailedDuringPlayback(f.slot) {
				t.Fatal("the requested slot, which nobody holds, should carry its own verdict")
			}
		})
	}
}

// With copy walking off, a failure from a session the slot has since replaced
// must not delete or fail the replacement — the request carries on with it —
// and records no verdict. Its own History row is settled separately
// (TestStaleStartupSettlesItsOwnRowOnly).
func TestStaleStartupFailureSparesTheReplacement(t *testing.T) {
	f := newCopyRetireFixture(t, 1).withoutCopyWalking()
	stale := f.resolve(t, "NZBGeek")
	f.manager.DeleteSession(f.slot)
	replacement := f.resolve(t, "NZBGeek")

	next, retry := f.server.retrySlotWithNextCopy(context.Background(), stale, f.slot, f.stream, errors.New("open failed"))
	if retry != copyRetryNext || next != replacement {
		t.Fatalf("got outcome %d, want to carry on with the replacement", retry)
	}
	requireKept(t, f, f.slot, replacement)
	if stale.OnceDone(onceFailureRecorded) {
		t.Fatal("the stale request recorded a failure, which would mark the replacement's History row")
	}
}

// Nobody holds the session: the failed prepare is the only evidence there is,
// and the copy is retired as before, so a broken copy is not kept.
func TestPrepareFailureRetiresACopyNobodyServes(t *testing.T) {
	f := newCopyRetireFixture(t, 2).withFailingPrepare(t)
	failed := f.resolve(t, "NZBGeek")

	rec := f.requestSlot(t, f.slot)

	if !isClosedSession(failed) {
		t.Fatal("a copy nobody serves was kept after its prepare failed")
	}
	if f.manager.PeekSession(f.slot) == failed {
		t.Fatal("the failed copy's session is still in the slot")
	}
	if got := f.manager.SlotCopyIndex(f.slot); got != 1 {
		t.Fatalf("slot copy index %d, want it moved to the next copy", got)
	}
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatal("a genuine failure was answered as another request's slot")
	}
}
