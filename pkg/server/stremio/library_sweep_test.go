package stremio

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/session"
)

const sweepTestTitle = "Sweep.Test.2020.1080p.WEB-DL-GRP"

type sweepFixture struct {
	srv *Server
	mgr *persistence.StateManager
	dir string
	ls  *persistence.LibraryStore
	id  string
}

// newSweepFixture stores an eligible "old" NZB whose freshness stamp is older
// than the TTL, so the real sweep selects it.
func newSweepFixture(t *testing.T, verify func(nzb []byte) (bool, error)) *sweepFixture {
	t.Helper()
	logger.Init("ERROR")
	dir, err := os.MkdirTemp("", "sweep_test")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	// The process-wide manager (TestMain) is pointed at a private database for
	// this test and restored afterwards, so no test depends on what ran before.
	mgr, err := persistence.GetManager(testDataDir)
	if err != nil {
		t.Fatalf("get manager: %v", err)
	}
	lite := persistence.Settings{Backend: persistence.BackendSQLite}
	closeShared, err := mgr.Reopen(lite, dir)
	if err != nil {
		t.Fatalf("isolate database: %v", err)
	}
	t.Cleanup(func() {
		if closeLive, err := mgr.Reopen(lite, testDataDir); err == nil {
			closeLive()
		}
		closeShared()
		os.RemoveAll(dir)
	})

	f := &sweepFixture{mgr: mgr, dir: dir, ls: mgr.LibraryStore()}
	f.srv = &Server{
		attemptRecorder: mgr,
		sessionManager:  &session.Manager{},
		libraryVerifier: func(_ context.Context, nzb []byte, _ *session.AvailReportMeta) (bool, error) {
			return verify(nzb)
		},
	}
	f.save(t, "old", persistence.LibraryStatusGood)
	f.id = f.get(t).ID
	f.ls.MarkVerified(f.id, time.Now().Add(-48*time.Hour))
	return f
}

func (f *sweepFixture) save(t *testing.T, nzb, status string) {
	t.Helper()
	if err := f.ls.StoreItem(&persistence.LibraryItem{
		ContentType: "movie", ContentID: "tt0000001", ReleaseTitle: sweepTestTitle,
		NZBData: []byte(nzb), Status: status,
	}); err != nil {
		t.Fatalf("store %s: %v", nzb, err)
	}
}

func (f *sweepFixture) get(t *testing.T) *persistence.LibraryItem {
	t.Helper()
	items, _, err := f.ls.GetFilteredItems("", "all", false, "all", 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: err=%v n=%d", err, len(items))
	}
	full, err := f.ls.GetItem(items[0].ID)
	if err != nil || full == nil {
		t.Fatalf("get item: %v", err)
	}
	return full
}

func (f *sweepFixture) sweep() { f.srv.sweepStaleLibraryItems(time.Hour) }

// sweepWithReplacement runs a real sweep, parks it inside verification once the
// old snapshot is selected, runs replace, then lets verification return.
func sweepWithReplacement(t *testing.T, replace func(f *sweepFixture), result func(nzb []byte) (bool, error)) *sweepFixture {
	t.Helper()
	selected, release := make(chan struct{}), make(chan struct{})
	var f *sweepFixture
	f = newSweepFixture(t, func(nzb []byte) (bool, error) {
		close(selected)
		<-release
		return result(nzb)
	})
	done := make(chan struct{})
	go func() { defer close(done); f.sweep() }()
	select {
	case <-selected:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep never reached verification")
	}
	replace(f)
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep did not finish")
	}
	return f
}

func TestSweepStaleNegativeVerdictDoesNotMarkReplacementBad(t *testing.T) {
	var replacement *persistence.LibraryItem
	f := sweepWithReplacement(t, func(f *sweepFixture) {
		f.save(t, "new", persistence.LibraryStatusGood)
		replacement = f.get(t)
	}, func(nzb []byte) (bool, error) {
		if string(nzb) != "old" {
			t.Errorf("verified %q, want the old snapshot", nzb)
		}
		return false, nil
	})

	got := f.get(t)
	if got.Status != persistence.LibraryStatusGood || got.StatusReason != replacement.StatusReason {
		t.Fatalf("replacement status = %q (%q), want it untouched by the stale verdict", got.Status, got.StatusReason)
	}
	if string(got.NZBData) != "new" || !got.LastVerifiedAt.Equal(replacement.LastVerifiedAt) {
		t.Fatalf("replacement modified: nzb=%q verified=%v want new/%v", got.NZBData, got.LastVerifiedAt, replacement.LastVerifiedAt)
	}
	cands, err := f.ls.GetCandidates("movie", "tt0000001", 0, 0)
	if err != nil || len(cands) != 1 {
		t.Fatalf("replacement must stay a playback candidate: err=%v n=%d", err, len(cands))
	}
}

func TestSweepStaleSuccessDoesNotCertifyReplacement(t *testing.T) {
	// Give the replacement a stamp that is neither the old snapshot's nor "now",
	// so a stale refresh (which writes now) cannot hide behind the save time.
	replacementStamp := time.Now().Add(-30 * time.Hour).Truncate(time.Second)
	f := sweepWithReplacement(t, func(f *sweepFixture) {
		f.save(t, "new", persistence.LibraryStatusPending)
		f.ls.MarkVerified(f.id, replacementStamp)
	}, func([]byte) (bool, error) { return true, nil })

	got := f.get(t)
	if string(got.NZBData) != "new" || !got.LastVerifiedAt.Equal(replacementStamp) {
		t.Fatalf("stale success certified the replacement: nzb=%q verified=%v want new/%v", got.NZBData, got.LastVerifiedAt, replacementStamp)
	}
}

func TestSweepUnchangedSnapshotIsApplied(t *testing.T) {
	t.Run("unavailable marks bad with the sweep reason", func(t *testing.T) {
		f := newSweepFixture(t, func([]byte) (bool, error) { return false, nil })
		f.sweep()
		got := f.get(t)
		if got.Status != persistence.LibraryStatusBad || got.StatusReason != "articles no longer available (freshness sweep)" {
			t.Fatalf("status = %q (%q), want bad with the sweep reason", got.Status, got.StatusReason)
		}
	})
	t.Run("available refreshes the stamp", func(t *testing.T) {
		f := newSweepFixture(t, func([]byte) (bool, error) { return true, nil })
		f.sweep()
		got := f.get(t)
		if got.Status != persistence.LibraryStatusGood || time.Since(got.LastVerifiedAt) > time.Minute {
			t.Fatalf("status = %q verified=%v, want good and freshly verified", got.Status, got.LastVerifiedAt)
		}
	})
	t.Run("inconclusive check leaves the row for the next sweep", func(t *testing.T) {
		f := newSweepFixture(t, func([]byte) (bool, error) { return false, errors.New("timeout") })
		before := f.get(t)
		f.sweep()
		got := f.get(t)
		if got.Status != before.Status || !got.LastVerifiedAt.Equal(before.LastVerifiedAt) {
			t.Fatalf("transient error changed the row: %q %v", got.Status, got.LastVerifiedAt)
		}
	})
}

func TestSweepDoesNotTouchRowsThatLeaveEligibilityDuringVerification(t *testing.T) {
	cases := map[string]func(f *sweepFixture){
		"deleted": func(f *sweepFixture) {
			if err := f.ls.DeleteItem(f.id); err != nil {
				t.Fatal(err)
			}
		},
		"pinned": func(f *sweepFixture) {
			if err := f.ls.SetPinned(f.id, true); err != nil {
				t.Fatal(err)
			}
		},
		"marked bad by playback": func(f *sweepFixture) {
			f.ls.MarkStatus(f.id, persistence.LibraryStatusBad, "playback failed")
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := sweepWithReplacement(t, change, func([]byte) (bool, error) { return false, nil })
			items, _, err := f.ls.GetFilteredItems("", "all", false, "all", 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "deleted":
				if len(items) != 0 {
					t.Fatalf("deleted row came back: %d", len(items))
				}
			case "pinned":
				if len(items) != 1 || items[0].Status != persistence.LibraryStatusGood {
					t.Fatalf("pinned row was marked bad: %+v", items)
				}
			default:
				if len(items) != 1 || items[0].StatusReason != "playback failed" {
					t.Fatalf("bad reason overwritten: %+v", items)
				}
			}
		})
	}
}

// The sweep selects its batch from database A and parks in verification of the
// first item; the library is then switched to database B, which holds an
// unrelated NZB under the second item's row id and the same stamp. No verdict
// for the second item may touch B's row.
func TestSweepVerdictAfterDatabaseSwitchNeverTouchesDestinationRow(t *testing.T) {
	const tgtTitle = "Switch.Target.2021.1080p-GRP"
	for _, c := range []struct {
		name    string
		nzb     string
		verdict bool
		migrate bool
	}{
		{"negative", "src", false, false},
		{"success", "src", true, false},
		{"negative, migration enabled, source deleted", "src", false, true},
		{"success, migration enabled, source deleted", "src", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Registered before the fixture, so it is removed after the fixture has
			// restored the shared manager and closed the destination database.
			dirB := t.TempDir()
			selected, release := make(chan struct{}), make(chan struct{})
			f := newSweepFixture(t, func(nzb []byte) (bool, error) {
				if string(nzb) == "old" { // the first item parks the sweep
					close(selected)
					<-release
					return true, nil
				}
				return c.verdict, nil
			})
			stamp := time.Now().Add(-30 * time.Hour).Truncate(time.Second)
			save := func(nzb string) *persistence.LibraryItem {
				if err := f.ls.StoreItem(&persistence.LibraryItem{ContentType: "movie", ContentID: "tt0000002", ReleaseTitle: tgtTitle, NZBData: []byte(nzb), Status: persistence.LibraryStatusGood}); err != nil {
					t.Fatal(err)
				}
				items, _, _ := f.ls.GetFilteredItems("", "all", false, "all", 0, 10)
				for _, it := range items {
					if it.ReleaseTitle == tgtTitle {
						f.ls.MarkVerified(it.ID, stamp)
						return it
					}
				}
				t.Fatal("target not stored")
				return nil
			}

			// Prepare B: switch there, store the unrelated row, switch back.
			dbB := persistence.Settings{Backend: persistence.BackendSQLite}
			closeB, err := f.mgr.Reopen(dbB, dirB)
			if err != nil {
				t.Fatal(err)
			}
			closeB()
			tgt := save("dest")
			closeA, err := f.mgr.Reopen(persistence.Settings{Backend: persistence.BackendSQLite}, f.dir)
			if err != nil {
				t.Fatal(err)
			}
			closeA()
			save(c.nzb)

			done := make(chan struct{})
			go func() { defer close(done); f.sweep() }()
			select {
			case <-selected:
			case <-time.After(10 * time.Second):
				t.Fatal("sweep never reached verification")
			}
			if c.migrate {
				if err := f.ls.DeleteItem(tgt.ID); err != nil {
					t.Fatal(err)
				}
				dbB.MigrateData = true
			}
			closeOld, err := f.mgr.Reopen(dbB, dirB)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			t.Cleanup(closeOld)
			before, _ := f.ls.GetItem(tgt.ID)
			if before == nil || string(before.NZBData) != "dest" || !before.LastVerifiedAt.Equal(stamp) {
				t.Fatalf("precondition: destination row = %+v", before)
			}
			close(release)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("sweep did not finish")
			}

			after, _ := f.ls.GetItem(tgt.ID)
			if after == nil || string(after.NZBData) != "dest" || after.Status != before.Status || after.StatusReason != before.StatusReason || !after.LastVerifiedAt.Equal(before.LastVerifiedAt) {
				t.Fatalf("destination row changed by the stale verdict: %+v -> %+v", before, after)
			}
		})
	}
}

// A row stored with a negative stamp is selected like any stale row and must be
// refreshed or expired by its verdict, not selected again every sweep.
func TestSweepNegativeStoredStampIsWrittenBack(t *testing.T) {
	for _, exists := range []bool{true, false} {
		f := newSweepFixture(t, func([]byte) (bool, error) { return exists, nil })
		f.ls.MarkVerified(f.id, time.Unix(-1, 0))
		f.sweep()
		got := f.get(t)
		if exists && time.Since(got.LastVerifiedAt) > time.Minute || !exists && got.Status != persistence.LibraryStatusBad {
			t.Fatalf("exists=%v: negative-stamp row not written back: %q %v", exists, got.Status, got.LastVerifiedAt)
		}
	}
}
