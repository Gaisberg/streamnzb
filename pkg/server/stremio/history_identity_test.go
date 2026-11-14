package stremio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/media/unpack"
	"streamnzb/pkg/services/availnzb"
	"streamnzb/pkg/session"
)

// History identity: every attempt — one session — owns one History row, and
// every write about it lands on that row only. A slot id is reused by the next
// session in the slot (a copy retry, a rebuild after eviction, a duplicate
// request's recovery), so selecting rows by slot let one attempt's verdict
// land on another's. These tests drive the real recording paths and read the
// rows back.

var historySlotSeq atomic.Int64

// newHistoryFixture is a copy-retire fixture on its own slot, recording History
// into the package's shared database. The slot is unique per test, so rows are
// read back by slot path without seeing other tests' rows.
func newHistoryFixture(t *testing.T, copies int) (copyRetireFixture, *persistence.StateManager) {
	t.Helper()
	initFailoverTestLogger()
	dir, err := os.MkdirTemp("", "history_identity_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// GetManager is a process-wide singleton and must not be closed here.
	mgr, err := persistence.GetManager(dir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	key := StreamSlotKey{StreamID: "stream_test", ContentType: "movie", ID: fmt.Sprintf("tt%d", 7100000+historySlotSeq.Add(1))}
	f := seedCopyRetireFixture(t, &Server{config: &config.Config{}, attemptRecorder: mgr}, key, copies)
	// Small good thresholds, no AvailNZB traffic.
	f.server.availReporter = &availnzb.Reporter{MinBytesToReportGood: 1, MinDurationToReportGood: time.Millisecond, Disabled: true}
	return f, mgr
}

// historyRows returns the slot's History rows, oldest first.
func historyRows(t *testing.T, mgr *persistence.StateManager, slot string) []persistence.NZBAttempt {
	t.Helper()
	all, err := mgr.ListAttempts(persistence.ListAttemptsOptions{Limit: 500})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	var rows []persistence.NZBAttempt
	for _, a := range all {
		if a.SlotPath == slot {
			rows = append(rows, a)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func rowState(a persistence.NZBAttempt) string {
	switch {
	case a.Preload:
		return "pending"
	case a.Success:
		return "success"
	default:
		return "failed"
	}
}

func describeRows(rows []persistence.NZBAttempt) string {
	var b strings.Builder
	for _, a := range rows {
		fmt.Fprintf(&b, "[%s %s %q] ", a.IndexerName, rowState(a), a.FailureReason)
	}
	return b.String()
}

// requireRows fails unless the slot's rows, oldest first, are exactly want,
// each "<indexer> pending|success|failed".
func requireRows(t *testing.T, mgr *persistence.StateManager, slot string, want ...string) []persistence.NZBAttempt {
	t.Helper()
	rows := historyRows(t, mgr, slot)
	var got []string
	for _, a := range rows {
		got = append(got, a.IndexerName+" "+rowState(a))
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("History rows = %v, want %v\nrows: %s", got, want, describeRows(rows))
	}
	return rows
}

// qualify makes sess's play count as good on the next commit check.
func qualify(sess *session.Session) time.Time {
	sess.AddBytesRead(1 << 20)
	return time.Now().Add(-time.Second)
}

var errMissingFirst = fmt.Errorf("probe: %w", ErrFirstSegmentUnavailable)

// 1 (R5-4). A failed startup leaves its slot; while its report is still out
// (the AvailNZB call), a duplicate request rebuilds the slot and records the
// replacement's row. The old session's failure must land on its own row: it
// used to mark the replacement's row failed, because the replacement's preload
// insert found the old pending row for the slot and added none of its own.
func TestOldSessionFailureAfterReplacementKeepsItsOwnRow(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	f = f.withoutCopyWalking()
	old := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(old)

	var replacement *session.Session
	settled := f.manager.SettleFailedStartup(old, 1, func(nextCopy bool) {
		f.manager.ClearSlotFailedDuringPlayback(f.slot)
		replacement = f.resolve(t, "NZBGeek")
		f.server.recordPreloadAttempt(replacement)
		f.server.reportFailedStartup(old, errMissingFirst, nextCopy)
	})
	if settled != session.StartupSlotFailed || replacement == old {
		t.Fatalf("settle = %d; the slot should have failed and been rebuilt", settled)
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed", "NZBGeek pending")
}

// 2 (R4-4). The retired copy's report runs after the slot moved to the next
// copy, and the next copy can be built and recording before that report is
// written. The retired copy's verdict must stay on the retired copy's row.
func TestDelayedCopyReportAfterAdvancementKeepsTheNextCopysRow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		settle func(f copyRetireFixture, old *session.Session, during func()) bool
	}{
		{"startup", func(f copyRetireFixture, old *session.Session, during func()) bool {
			return f.manager.SettleFailedStartup(old, f.stream.EffectiveVariantAttempts(), func(nextCopy bool) {
				during()
				f.server.reportFailedStartup(old, errMissingFirst, nextCopy)
			}) == session.StartupCopyRetired
		}},
		{"served", func(f copyRetireFixture, old *session.Session, during func()) bool {
			// The same sequence as failServedCopy's report.
			fatal := fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills)
			return f.manager.RetireSlotCopy(old, f.stream.EffectiveVariantAttempts(), false, func() {
				during()
				outcome := f.server.reportBadReleaseOutcome(old, fatal, true, true)
				if !old.OnceDone(onceSuccessRecorded) && old.Once(onceFailureRecorded) {
					f.server.recordFailureAttempt(old, fatal, outcome)
				}
			}) == session.SlotCopyRetired
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, mgr := newHistoryFixture(t, 2)
			old := f.resolve(t, "NZBGeek")
			f.server.recordPreloadAttempt(old)

			var next *session.Session
			ok := tc.settle(f, old, func() {
				var moved bool
				next, moved = f.server.sessionAfterSlotMoved(context.Background(), f.slot, f.stream)
				if !moved {
					t.Fatal("the slot should rebuild on the next copy")
				}
				f.server.recordPreloadAttempt(next)
			})
			if !ok || next == nil || next.Release().Indexer != "DrunkenSlug" {
				t.Fatal("the copy should have been retired and the next copy built")
			}
			requireRows(t, mgr, f.slot, "NZBGeek failed", "DrunkenSlug pending")
		})
	}
}

// 3 (R3-6). A startup whose session the slot has already replaced records no
// verdict, but its own row must not stay pending forever: it is settled when
// the session closes, and the replacement's row is left alone.
func TestStaleStartupSettlesItsOwnRowOnly(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	f = f.withoutCopyWalking()
	recorded := make(chan struct{}, 16)
	f.server.onAttemptRecorded = func() { recorded <- struct{}{} }

	stale := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(stale)
	f.manager.DeleteSession(f.slot)
	replacement := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(replacement)
	drain(recorded)

	next, retry := f.server.retrySlotWithNextCopy(context.Background(), stale, f.slot, f.stream, errors.New("open failed"))
	if retry != copyRetryNext || next != replacement {
		t.Fatalf("got outcome %d, want to carry on with the replacement", retry)
	}

	// The stale row settles on the session's close hook; wait for it, by event.
	deadline := time.After(10 * time.Second)
	for {
		rows := historyRows(t, mgr, f.slot)
		if len(rows) == 2 && !rows[0].Preload {
			break
		}
		select {
		case <-recorded:
		case <-deadline:
			t.Fatalf("the stale startup's row was never settled: %s", describeRows(rows))
		}
	}
	rows := requireRows(t, mgr, f.slot, "NZBGeek failed", "NZBGeek pending")
	if rows[0].FailureReason != attemptEndedUnclassified {
		t.Fatalf("stale row reason = %q, want it settled as unclassified", rows[0].FailureReason)
	}
	if stale.OnceDone(onceFailureRecorded) {
		t.Fatal("a stale startup must not record a verdict")
	}
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// 4 (R4-6). A failure that arrives after the attempt was already recorded as
// a success changes nothing: the success stands, and no extra failure row
// appears — nor, with a replacement in the slot, is the replacement's row
// marked failed.
func TestFailureAfterSuccessDoesNotDowngradeTheAttempt(t *testing.T) {
	for _, withReplacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%v", withReplacement), func(t *testing.T) {
			f, mgr := newHistoryFixture(t, 1)
			f = f.withoutCopyWalking()
			sess := f.resolve(t, "NZBGeek")
			f.server.recordPreloadAttempt(sess)
			if !f.server.commitGoodAttemptIfQualified(nil, sess, sess.ID, qualify(sess), nil) {
				t.Fatal("the play should commit as good")
			}
			want := []string{"NZBGeek success"}
			if withReplacement {
				f.manager.DeleteSession(f.slot)
				f.server.recordPreloadAttempt(f.resolve(t, "NZBGeek"))
				want = append(want, "NZBGeek pending")
			}

			// A duplicate request's late startup failure on the same session.
			f.server.reportFailedStartup(sess, errMissingFirst, false)

			requireRows(t, mgr, f.slot, want...)
		})
	}
}

// 5. Concurrent requests share their session and so its one row. A request
// whose failure is recorded before the request that claimed the preload has
// written it must not leave a second, never-resolved row; and a burst of
// concurrent writes from the session's requests leaves one row with one
// verdict.
func TestConcurrentRequestsOnOneSessionShareOneRow(t *testing.T) {
	t.Run("failure before the claimed preload is written", func(t *testing.T) {
		f, mgr := newHistoryFixture(t, 1)
		sess := f.resolve(t, "NZBGeek")
		if !sess.Once(oncePreloadRecorded) { // request A claims the preload ...
			t.Fatal("claim")
		}
		if sess.Once(onceFailureRecorded) { // ... request B fails first ...
			f.server.recordFailureAttempt(sess, errMissingFirst, availnzb.ReportOutcome{})
		}
		f.server.recordPreloadAttempt(sess) // ... then A writes its preload.
		requireRows(t, mgr, f.slot, "NZBGeek failed")
	})
	t.Run("burst", func(t *testing.T) {
		f, mgr := newHistoryFixture(t, 1)
		sess := f.resolve(t, "NZBGeek")
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if sess.Once(oncePreloadRecorded) {
					f.server.recordPreloadAttempt(sess)
				}
				f.server.recordTTFF(sess, sess.ID, "", time.Now())
				if i%2 == 0 && sess.Once(onceFailureRecorded) {
					f.server.recordFailureAttempt(sess, errMissingFirst, availnzb.ReportOutcome{})
				}
			}()
		}
		close(start)
		wg.Wait()
		requireRows(t, mgr, f.slot, "NZBGeek failed")
	})
}

// 6. With failover off, a failed startup fails the slot and a duplicate
// request recovers it (no failed-slot redirect without failover) while the
// failure's report is still out. Same rule as 1, through the recovery path.
func TestReplacementWithFailoverDisabledKeepsItsOwnRow(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	off := false
	f.stream.EnableFailover = &off
	old := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(old)

	var replacement *session.Session
	settled := f.manager.SettleFailedStartup(old, 1, func(nextCopy bool) {
		f.manager.ClearSlotFailedDuringPlayback(f.slot)
		var err error
		replacement, _, err = f.server.recoverPlaySessionAfterEviction(context.Background(), f.slot, f.stream)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		f.server.recordPreloadAttempt(replacement)
		f.server.reportFailedStartup(old, errMissingFirst, nextCopy)
	})
	if settled != session.StartupSlotFailed || replacement == nil || replacement == old {
		t.Fatalf("settle = %d; the slot should have failed and been recovered", settled)
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed", "NZBGeek pending")
}

// 7. A cancelled prepare is no verdict: the row stays pending and the next
// request on the same session finishes that same row. A startup timeout is the
// attempt's failure, recorded on its own row; the next copy gets a new one.
func TestCancellationAndTimeoutHistory(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		f, mgr := newHistoryFixture(t, 1)
		sess := f.resolve(t, "NZBGeek")
		if sess.Once(oncePreloadRecorded) {
			f.server.recordPreloadAttempt(sess)
		}
		// The cancelled request records nothing; the next request on the
		// session does not insert another preload, and its play commits.
		requireRows(t, mgr, f.slot, "NZBGeek pending")
		if sess.Once(oncePreloadRecorded) {
			f.server.recordPreloadAttempt(sess)
		}
		if !f.server.commitGoodAttemptIfQualified(nil, sess, sess.ID, qualify(sess), nil) {
			t.Fatal("the next request's play should commit")
		}
		requireRows(t, mgr, f.slot, "NZBGeek success")
	})
	t.Run("cancelled and abandoned", func(t *testing.T) {
		// Nobody resumes the cancelled start; the session is evicted. Its row
		// is settled on close rather than left pending for good.
		f, mgr := newHistoryFixture(t, 1)
		recorded := make(chan struct{}, 4)
		f.server.onAttemptRecorded = func() { recorded <- struct{}{} }
		sess := f.resolve(t, "NZBGeek")
		f.server.recordPreloadAttempt(sess)
		<-recorded
		f.manager.DeleteSession(f.slot)
		select {
		case <-recorded:
		case <-time.After(10 * time.Second):
			t.Fatal("the abandoned session's row was never settled")
		}
		rows := requireRows(t, mgr, f.slot, "NZBGeek failed")
		if rows[0].FailureReason != attemptEndedUnclassified {
			t.Fatalf("reason = %q", rows[0].FailureReason)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f, mgr := newHistoryFixture(t, 2)
		sess := f.resolve(t, "NZBGeek")
		f.server.recordPreloadAttempt(sess)
		timeout := fmt.Errorf("prepare: %w", ErrPlaybackStartupTimeout)
		next, retry := f.server.retrySlotWithNextCopy(context.Background(), sess, f.slot, f.stream, timeout)
		if retry != copyRetryNext {
			t.Fatalf("got outcome %d, want the next copy", retry)
		}
		f.server.recordPreloadAttempt(next)
		rows := requireRows(t, mgr, f.slot, "NZBGeek failed", "DrunkenSlug pending")
		if !strings.Contains(rows[0].FailureReason, ErrPlaybackStartupTimeout.Error()) {
			t.Fatalf("timeout row reason = %q", rows[0].FailureReason)
		}
	})
}

// 8. A real request whose prepare fails, with no copy to walk to, ends with
// one failed row — not pending, and not a second row.
func TestTerminalStartupFailureRecordsOneFailedRow(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	f = f.withoutCopyWalking().withFailingPrepare(t)
	f.resolve(t, "NZBGeek")

	f.requestSlot(t, f.slot)

	requireRows(t, mgr, f.slot, "NZBGeek failed")
}

// 9. Copy advancement that ends in a good play: the failed copy's row says
// failed, the copy that played says success, one row each.
func TestCopyAdvancementThenSuccessRecordsEachCopy(t *testing.T) {
	f, mgr := newHistoryFixture(t, 3)
	first := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(first)
	next, retry := f.server.retrySlotWithNextCopy(context.Background(), first, f.slot, f.stream, errMissingFirst)
	if retry != copyRetryNext {
		t.Fatalf("got outcome %d, want the next copy", retry)
	}
	f.server.recordPreloadAttempt(next)
	if !f.server.commitGoodAttemptIfQualified(nil, next, next.ID, qualify(next), nil) {
		t.Fatal("the next copy's play should commit")
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed", "DrunkenSlug success")
}

// 10 (R2-2). A session whose copy is retired while it is being played: the
// sibling serve that qualifies afterwards must not record the condemned copy
// as good — not on its own row, and not on the next copy's.
func TestRetirementDuringPlaybackBlocksTheSiblingsSuccess(t *testing.T) {
	f, mgr := newHistoryFixture(t, 2)
	served := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(served)
	f.manager.MarkPlaybackValidated(served)
	startedAt := qualify(served)

	if !f.server.failServedCopy(f.stream, served, fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills)) {
		t.Fatal("the fatal read should retire the copy")
	}
	next, ok := f.server.sessionAfterSlotMoved(context.Background(), f.slot, f.stream)
	if !ok {
		t.Fatal("the slot should rebuild on the next copy")
	}
	f.server.recordPreloadAttempt(next)

	// The sibling serve's progress check runs after the retirement.
	f.server.commitGoodAttemptIfQualified(nil, served, served.ID, startedAt, nil)

	if served.OnceDone(onceSuccessRecorded) {
		t.Fatal("a condemned copy was committed as good")
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed", "DrunkenSlug pending")
}

// R2-2, concurrent form: a sibling serve qualifying while another serve's
// fatal read condemns the copy. Whichever claims first, the session never gets
// both a success and a failure verdict.
func TestSuccessAndFailureVerdictsExcludeEachOther(t *testing.T) {
	for range 50 {
		f := newCopyRetireFixture(t, 2)
		f.server.availReporter = &availnzb.Reporter{MinBytesToReportGood: 1, MinDurationToReportGood: time.Millisecond, Disabled: true}
		sess := f.resolve(t, "NZBGeek")
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
			f.server.failServedCopy(f.stream, sess, fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills))
		}()
		close(start)
		wg.Wait()
		if sess.OnceDone(onceSuccessRecorded) && sess.OnceDone(onceFailureRecorded) {
			t.Fatal("one session got both a success and a failure verdict")
		}
	}
}

// The close hook settles only rows nobody is judging: a success claimed and
// still being reported (ReportGood) when the session closes is written after,
// and stands; an early stop's reason survives the close and reads as a short
// play, not a failure.
func TestSessionCloseLeavesVerdictsAndReasonsAlone(t *testing.T) {
	t.Run("success in flight", func(t *testing.T) {
		f, mgr := newHistoryFixture(t, 1)
		recorded := make(chan struct{}, 4)
		f.server.onAttemptRecorded = func() { recorded <- struct{}{} }
		sess := f.resolve(t, "NZBGeek")
		f.server.recordPreloadAttempt(sess)
		<-recorded
		if !sess.OnceUnless(onceSuccessRecorded, onceFailureRecorded) { // the success claim ...
			t.Fatal("claim")
		}
		f.manager.DeleteSession(f.slot) // ... the session closes during ReportGood ...
		<-recorded                      // ... and the close hook has run.
		f.server.recordAttempt(sess, true, "", availnzb.ReportOutcome{})
		requireRows(t, mgr, f.slot, "NZBGeek success")
	})
	t.Run("early stop", func(t *testing.T) {
		f, mgr := newHistoryFixture(t, 1)
		recorded := make(chan struct{}, 4)
		f.server.onAttemptRecorded = func() { recorded <- struct{}{} }
		sess := f.resolve(t, "NZBGeek")
		f.server.recordPreloadAttempt(sess)
		<-recorded
		p := f.server.recordAttemptParamsForOutcome(sess, true)
		p.ID, p.Gen = f.server.attemptRow(sess)
		p.FailureReason = "Playback ended too early to classify this release as good."
		mgr.UpdatePendingAttempt(p)
		f.manager.DeleteSession(f.slot)
		<-recorded
		rows := requireRows(t, mgr, f.slot, "NZBGeek failed")
		if rows[0].FailureReason != p.FailureReason {
			t.Fatalf("reason = %q, want the early stop's", rows[0].FailureReason)
		}
	})
}

// A serve that ended below the good threshold is settled as a short play once
// nothing serves the session; when the next serve on the same session then
// plays long enough, its success replaces the short play on the same row.
func TestShortPlayThenSuccessOnTheSameSession(t *testing.T) {
	f, mgr := newHistoryFixture(t, 1)
	recorded := make(chan struct{}, 8)
	f.server.onAttemptRecorded = func() { recorded <- struct{}{} }
	sess := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(sess)
	<-recorded
	f.server.recordPendingAttempt(sess, "Playback ended too early to classify this release as good.", availnzb.SkippedOutcome("below threshold"))
	<-recorded // the pending update
	select {
	case <-recorded: // the deferred short-play resolution
	case <-time.After(10 * time.Second):
		t.Fatal("the short play was never settled")
	}
	requireRows(t, mgr, f.slot, "NZBGeek failed")

	if !f.server.commitGoodAttemptIfQualified(nil, sess, sess.ID, qualify(sess), nil) {
		t.Fatal("the next serve's play should commit")
	}
	requireRows(t, mgr, f.slot, "NZBGeek success")
}

// A fatal read on a served session the slot has already replaced: the read is
// this session's failure, recorded on its own row; the replacement's row is
// untouched; and a sibling serve of the stale session cannot then commit it as
// good.
func TestStaleServedFailureRecordsOnItsOwnRow(t *testing.T) {
	f, mgr := newHistoryFixture(t, 2)
	recorded := make(chan struct{}, 8)
	f.server.onAttemptRecorded = func() { recorded <- struct{}{} }
	stale := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(stale)
	<-recorded
	startedAt := qualify(stale)
	f.manager.DeleteSession(f.slot) // evicted mid-serve
	<-recorded                      // the close settled the row as a short play
	replacement := f.resolve(t, "NZBGeek")
	f.server.recordPreloadAttempt(replacement)

	fatal := fmt.Errorf("read: %w", unpack.ErrTooManyZeroFills)
	if !f.server.failServedCopy(f.stream, stale, fatal) {
		t.Fatal("the stale serve must count as failed")
	}
	f.server.commitGoodAttemptIfQualified(nil, stale, stale.ID, startedAt, nil)

	if stale.OnceDone(onceSuccessRecorded) {
		t.Fatal("a stale session whose read failed was committed as good")
	}
	rows := requireRows(t, mgr, f.slot, "NZBGeek failed", "NZBGeek pending")
	if !strings.Contains(rows[0].FailureReason, unpack.ErrTooManyZeroFills.Error()) {
		t.Fatalf("stale row reason = %q, want the read's failure over the short play", rows[0].FailureReason)
	}
	if f.manager.PeekSession(f.slot) != replacement {
		t.Fatal("the stale read disturbed the replacement")
	}
}
