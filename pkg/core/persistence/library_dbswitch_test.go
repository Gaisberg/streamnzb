package persistence

import (
	"testing"
	"time"
)

// A sweep selects its snapshot from database A, verifies without any lock, then
// writes back. If the database was switched meanwhile, the same row id (and the
// same legacy zero stamp) names an unrelated row in B: the verdict must be
// dropped, not applied to it.
func TestLibrarySnapshotWritebackAfterDatabaseSwitchNeverTouchesDestination(t *testing.T) {
	for _, migrate := range []bool{false, true} {
		for _, verdict := range []string{"negative", "success"} {
			name := verdict + "/migration disabled"
			if migrate {
				name = verdict + "/migration enabled, source row deleted"
			}
			t.Run(name, func(t *testing.T) {
				a, b := suiteDB(t), suiteDB(t)

				// Database B holds an unrelated NZB under the same id, zero stamp.
				dest := b.open(t)
				if err := dest.LibraryStore().StoreItem(&LibraryItem{ContentType: "movie", ContentID: "tt9", ReleaseTitle: "snap", NZBData: []byte("dest"), Status: LibraryStatusGood}); err != nil {
					t.Fatal(err)
				}
				id := buildLibraryItemID("movie", "tt9", 0, 0, "snap")
				dest.LibraryStore().MarkVerified(id, time.Unix(0, 0))
				if err := dest.Close(); err != nil {
					t.Fatal(err)
				}

				mgr := a.open(t)
				t.Cleanup(func() { _ = mgr.Close() })
				ls := mgr.LibraryStore()
				snap := snapshotFixture(t, ls, "src")
				ls.MarkVerified(id, time.Unix(0, 0))
				items, err := ls.StaleItems(time.Now().Add(-time.Hour), 10)
				if err != nil || len(items) != 1 || !items[0].LastVerifiedAt.IsZero() {
					t.Fatalf("snapshot: %v %+v", err, items)
				}
				snap = items[0]

				// Verification is in flight: the database is switched under it.
				if migrate {
					if err := ls.DeleteItem(id); err != nil {
						t.Fatal(err)
					}
					b.settings.MigrateData = true
				}
				closeOld, err := mgr.Reopen(b.settings, b.dataDir)
				if err != nil {
					t.Fatalf("reopen: %v", err)
				}
				t.Cleanup(closeOld)

				before, _ := ls.GetItem(id)
				if before == nil || string(before.NZBData) != "dest" || !before.LastVerifiedAt.IsZero() {
					t.Fatalf("precondition: destination row = %+v", before)
				}
				var ok bool
				if verdict == "negative" {
					ok, err = ls.MarkBadIfSnapshot(snap, "stale")
				} else {
					ok, err = ls.MarkVerifiedIfSnapshot(snap, time.Now())
				}
				if ok || err != nil {
					t.Fatalf("stale verdict from the old database: applied=%v err=%v", ok, err)
				}
				after, _ := ls.GetItem(id)
				if after == nil || string(after.NZBData) != "dest" || after.Status != before.Status || after.StatusReason != before.StatusReason || !after.LastVerifiedAt.Equal(before.LastVerifiedAt) {
					t.Fatalf("destination row changed: %+v -> %+v", before, after)
				}
			})
		}
	}
}

// A snapshot taken and applied in the same database still works across an
// unrelated earlier switch.
func TestLibrarySnapshotWritebackAppliesInSwitchedDatabase(t *testing.T) {
	a, b := suiteDB(t), suiteDB(t)
	mgr := a.open(t)
	t.Cleanup(func() { _ = mgr.Close() })
	closeOld, err := mgr.Reopen(b.settings, b.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeOld)
	snap := snapshotFixture(t, mgr.LibraryStore(), "old")
	if ok, err := mgr.LibraryStore().MarkBadIfSnapshot(snap, "gone"); !ok || err != nil {
		t.Fatalf("same-database verdict: applied=%v err=%v", ok, err)
	}
}

// Another eligible row shares the deleted row's stamp: only the id tells them apart.
func TestLibrarySnapshotWritebackIDPredicateProtectsSameStampRow(t *testing.T) {
	ls := newTestLibraryStore(t)
	snap := snapshotFixture(t, ls, "old")
	if err := ls.StoreItem(&LibraryItem{ContentType: "movie", ContentID: "tt8", ReleaseTitle: "other", NZBData: []byte("other"), Status: LibraryStatusGood}); err != nil {
		t.Fatal(err)
	}
	otherID := buildLibraryItemID("movie", "tt8", 0, 0, "other")
	ls.MarkVerified(otherID, snap.LastVerifiedAt)
	if err := ls.DeleteItem(snap.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := ls.GetItem(otherID)
	if before == nil || !before.LastVerifiedAt.Equal(snap.LastVerifiedAt) {
		t.Fatalf("precondition: %+v", before)
	}
	for _, apply := range []func() (bool, error){
		func() (bool, error) { return ls.MarkBadIfSnapshot(snap, "stale") },
		func() (bool, error) { return ls.MarkVerifiedIfSnapshot(snap, time.Now()) },
	} {
		if ok, err := apply(); ok || err != nil {
			t.Fatalf("deleted row's verdict: applied=%v err=%v", ok, err)
		}
	}
	after, _ := ls.GetItem(otherID)
	if after == nil || after.Status != before.Status || after.StatusReason != before.StatusReason || !after.LastVerifiedAt.Equal(before.LastVerifiedAt) {
		t.Fatalf("unrelated row changed: %+v -> %+v", before, after)
	}
}

// last_verified_at is read back as a time.Time that maps every value <= 0 to the
// zero time; the writeback must still match the stored integer, whatever it is,
// and for every kind of NZB payload the sweep can meet (including the empty
// ones that take its freshness-bump branch).
func TestLibrarySnapshotWritebackMatchesStoredStamp(t *testing.T) {
	stamps := map[string]int64{"negative": -1, "legacy zero": 0, "positive": time.Now().Add(-48 * time.Hour).Unix()}
	blobs := map[string]func(ls *LibraryStore, id string){
		"normal":       func(*LibraryStore, string) {},
		"empty blob":   func(ls *LibraryStore, id string) { mustExec(t, ls, []byte{}, id) },
		"invalid gzip": func(ls *LibraryStore, id string) { mustExec(t, ls, []byte("not gzip"), id) },
	}
	for sn, stamp := range stamps {
		for bn, corrupt := range blobs {
			for _, verdict := range []string{"success", "negative"} {
				t.Run(sn+"/"+bn+"/"+verdict, func(t *testing.T) {
					ls := newTestLibraryStore(t)
					snapshotFixture(t, ls, "old")
					id := buildLibraryItemID("movie", "tt9", 0, 0, "snap")
					corrupt(ls, id)
					ls.MarkVerified(id, time.Unix(stamp, 0))
					items, err := ls.StaleItems(time.Now().Add(-time.Hour), 10)
					if err != nil || len(items) != 1 {
						t.Fatalf("snapshot: %v %d", err, len(items))
					}
					if bn != "normal" && len(items[0].NZBData) != 0 {
						t.Fatalf("want an empty-NZB snapshot, got %d bytes", len(items[0].NZBData))
					}
					var ok bool
					if verdict == "success" {
						ok, err = ls.MarkVerifiedIfSnapshot(items[0], time.Now())
					} else {
						ok, err = ls.MarkBadIfSnapshot(items[0], "gone")
					}
					if !ok || err != nil {
						t.Fatalf("unchanged snapshot (stored %d): applied=%v err=%v", stamp, ok, err)
					}
					got, _ := ls.GetItem(id)
					if verdict == "success" && time.Since(got.LastVerifiedAt) > time.Minute || verdict == "negative" && got.Status != LibraryStatusBad {
						t.Fatalf("verdict not applied: %+v", got)
					}
				})
			}
		}
	}
}

func mustExec(t *testing.T, ls *LibraryStore, blob []byte, id string) {
	t.Helper()
	if _, err := ls.wdb.Exec(`UPDATE library_nzbs SET nzb_data = ? WHERE id = ?`, blob, id); err != nil {
		t.Fatal(err)
	}
}

// A stamp that changed since selection rejects the verdict, including the
// change that normalization would hide (-1 to 0).
func TestLibrarySnapshotWritebackRejectsChangedStoredStamp(t *testing.T) {
	for _, to := range []int64{0, -2, time.Now().Unix()} {
		ls := newTestLibraryStore(t)
		snapshotFixture(t, ls, "old")
		id := buildLibraryItemID("movie", "tt9", 0, 0, "snap")
		ls.MarkVerified(id, time.Unix(-1, 0))
		items, _ := ls.StaleItems(time.Now().Add(-time.Hour), 10)
		if len(items) != 1 {
			t.Fatalf("snapshot: %d", len(items))
		}
		ls.MarkVerified(id, time.Unix(to, 0))
		for _, apply := range []func() (bool, error){
			func() (bool, error) { return ls.MarkBadIfSnapshot(items[0], "stale") },
			func() (bool, error) { return ls.MarkVerifiedIfSnapshot(items[0], time.Now()) },
		} {
			if ok, err := apply(); ok || err != nil {
				t.Fatalf("stamp -1 -> %d: stale verdict applied=%v err=%v", to, ok, err)
			}
		}
		if got, _ := ls.GetItem(id); got.Status == LibraryStatusBad {
			t.Fatalf("stamp -1 -> %d: row marked bad", to)
		}
	}
}
