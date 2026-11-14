package stremio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/media/decode"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

// AvailNZB verdict ordering: a session's success and failure verdicts exclude
// each other (OnceUnless), and the AvailNZB report must follow the verdict that
// won. A failure that arrives after the session committed as good — a
// duplicate request's late startup failure, a stale serve read — must not send
// a bad report behind the good one.

// availCounts counts the reports a fake AvailNZB receives.
type availCounts struct{ good, bad atomic.Int32 }

func (c *availCounts) get() (good, bad int32) { return c.good.Load(), c.bad.Load() }

type fixedHosts []string

func (h fixedHosts) GetProviderHosts() []string { return h }

// withAvailNZB points f's reporter at a fake AvailNZB, with good thresholds
// low enough that qualify() passes them.
func withAvailNZB(t *testing.T, f copyRetireFixture) *availCounts {
	t.Helper()
	counts := &availCounts{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body availnzb.ReportRequest
		if r.URL.Path != "/api/v1/report" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		if body.Status {
			counts.good.Add(1)
		} else {
			counts.bad.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	rep := availnzb.NewReporter(availnzb.NewClient(ts.URL, "test-key"), fixedHosts{"news.example.com"})
	rep.MinBytesToReportGood = 1
	rep.MinDurationToReportGood = time.Millisecond
	f.server.availReporter = rep
	// A committed play saves to the library on a goroutine that outlives the
	// test and logs while a later test re-initialises the logger.
	autoSave := false
	f.server.config.LibraryAutoSave = &autoSave
	return counts
}

// reportable gives sess what an AvailNZB report needs: content ids and the
// provider hosts a play would have recorded.
func reportable(sess *session.Session) *session.Session {
	sess.ContentIDs = &session.AvailReportMeta{ImdbID: "tt7100000"}
	sess.RecordUsedProviderHost("news.example.com")
	sess.RecordServedProviderHost("news.example.com")
	return sess
}

var errCorruptRead = fmt.Errorf("read: %w", decode.ErrCorrupt)

func requireAvail(t *testing.T, c *availCounts, wantGood, wantBad int32) {
	t.Helper()
	if good, bad := c.get(); good != wantGood || bad != wantBad {
		t.Fatalf("AvailNZB reports: good=%d bad=%d, want good=%d bad=%d", good, bad, wantGood, wantBad)
	}
}

// 1. Success first, then a delayed failure on the same session — on both the
// startup and the serve path: one good report, no bad one, and the History row
// stays a success.
func TestDelayedFailureAfterSuccessSendsNoBadReport(t *testing.T) {
	for _, path := range []string{"startup", "serve"} {
		t.Run(path, func(t *testing.T) {
			f, mgr := newHistoryFixture(t, 1)
			f = f.withoutCopyWalking()
			counts := withAvailNZB(t, f)
			sess := reportable(f.resolve(t, "NZBGeek"))
			f.server.recordPreloadAttempt(sess)
			f.manager.MarkPlaybackValidated(sess)
			if !f.server.commitGoodAttemptIfQualified(nil, sess, sess.ID, qualify(sess), nil) {
				t.Fatal("the play should commit as good")
			}
			requireAvail(t, counts, 1, 0)

			if path == "startup" {
				f.server.reportFailedStartup(sess, errMissingFirst, false)
			} else {
				f.server.failServedCopy(f.stream, sess, errCorruptRead)
			}

			requireAvail(t, counts, 1, 0)
			requireRows(t, mgr, f.slot, "NZBGeek success")
		})
	}
}

// 2. Failure first: the bad report goes out, and the success that follows is
// neither claimed nor reported.
func TestFailureBeforeSuccessReportsBadOnly(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	f = f.withoutCopyWalking()
	counts := withAvailNZB(t, f)
	sess := reportable(f.resolve(t, "NZBGeek"))
	f.server.recordPreloadAttempt(sess)
	f.manager.MarkPlaybackValidated(sess)
	startedAt := qualify(sess)

	if !f.server.failServedCopy(f.stream, sess, errCorruptRead) {
		t.Fatal("the fatal read should fail the copy")
	}
	f.server.commitGoodAttemptIfQualified(nil, sess, sess.ID, startedAt, nil)

	requireAvail(t, counts, 0, 1)
	if sess.OnceDone(onceSuccessRecorded) {
		t.Fatal("a failed session was committed as good")
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed")
}

// 3. Success and failure claimed concurrently: exactly one verdict wins, and
// it alone reaches AvailNZB.
func TestConcurrentVerdictsReportOnlyTheWinner(t *testing.T) {
	for _, path := range []string{"startup", "serve"} {
		t.Run(path, func(t *testing.T) {
			for range 30 {
				f := newCopyRetireFixture(t, 1).withoutCopyWalking()
				counts := withAvailNZB(t, f)
				sess := reportable(f.resolve(t, "NZBGeek"))
				f.manager.MarkPlaybackValidated(sess)
				startedAt := qualify(sess)
				start := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					f.server.commitGoodAttemptIfQualified(nil, sess, sess.ID, startedAt, nil)
				}()
				go func() {
					defer wg.Done()
					<-start
					if path == "startup" {
						f.server.reportFailedStartup(sess, errMissingFirst, false)
					} else {
						f.server.failServedCopy(f.stream, sess, errCorruptRead)
					}
				}()
				close(start)
				wg.Wait()

				succeeded, failed := sess.OnceDone(onceSuccessRecorded), sess.OnceDone(onceFailureRecorded)
				if succeeded == failed {
					t.Fatalf("verdicts: success=%v failure=%v, want exactly one", succeeded, failed)
				}
				if succeeded {
					requireAvail(t, counts, 1, 0)
				} else {
					requireAvail(t, counts, 0, 1)
				}
			}
		})
	}
}

// 4 + 5. The gate is per session, not per slot: A's success does not silence
// B, the replacement in the same slot, whose genuine failure still reports and
// lands on its own row. A's own late failure sends nothing.
func TestRetiredSessionDoesNotGateTheReplacementsReport(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	f = f.withoutCopyWalking()
	counts := withAvailNZB(t, f)
	a := reportable(f.resolve(t, "NZBGeek"))
	f.server.recordPreloadAttempt(a)
	if !f.server.commitGoodAttemptIfQualified(nil, a, a.ID, qualify(a), nil) {
		t.Fatal("A should commit as good")
	}
	f.manager.DeleteSession(f.slot)
	b := reportable(f.resolve(t, "NZBGeek"))
	if b == a {
		t.Fatal("the slot should have been rebuilt")
	}
	f.server.recordPreloadAttempt(b)

	f.server.reportFailedStartup(a, errMissingFirst, false)
	requireAvail(t, counts, 1, 0)

	f.server.reportFailedStartup(b, errMissingFirst, false)
	requireAvail(t, counts, 1, 1)
	requireRows(t, mgr, f.slot, "NZBGeek success", "NZBGeek failed")
}

// 5. A genuine failed playback with no success on the session still reports.
func TestGenuineFailureStillReportsBad(t *testing.T) {
	for _, path := range []string{"startup", "serve"} {
		t.Run(path, func(t *testing.T) {
			f, mgr := newHistoryFixture(t, 1)
			f = f.withoutCopyWalking()
			counts := withAvailNZB(t, f)
			sess := reportable(f.resolve(t, "NZBGeek"))
			f.server.recordPreloadAttempt(sess)
			if path == "startup" {
				f.server.reportFailedStartup(sess, errMissingFirst, false)
			} else {
				f.manager.MarkPlaybackValidated(sess)
				f.server.failServedCopy(f.stream, sess, errCorruptRead)
			}
			requireAvail(t, counts, 0, 1)
			requireRows(t, mgr, f.slot, "NZBGeek failed")
		})
	}
}
