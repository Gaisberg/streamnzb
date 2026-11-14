package persistence

import "testing"

// A session holds its History row's id across a live database switch
// (Reopen). In the new database the same numeric id — even with the same slot
// path — can name an unrelated row. No delayed write from the old session may
// touch it.
func TestAttemptWritesAfterDatabaseSwitchNeverTouchAnotherRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		slotB string
	}{
		{"different slot", "slot-b"},
		{"same slot", "slot-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := suiteDB(t), suiteDB(t)
			mgr := first.open(t)
			t.Cleanup(func() { _ = mgr.Close() })

			// Session A's row, in database A.
			rowA := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: "slot-a", ReleaseTitle: "A"})

			closeOld, err := mgr.Reopen(second.settings, second.dataDir)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			t.Cleanup(closeOld)

			// An unrelated attempt in database B, given the same numeric id.
			rowB := mgr.RecordPreloadAttempt(RecordAttemptParams{SlotPath: tc.slotB, ReleaseTitle: "B"})
			if rowB.ID != rowA.ID {
				t.Fatalf("precondition: want B's row to reuse id %d, got %d", rowA.ID, rowB.ID)
			}

			// Session A's delayed writes arrive after the switch.
			stale := RecordAttemptParams{ID: rowA.ID, Gen: rowA.Gen, SlotPath: "slot-a", ProviderName: "stale"}
			mgr.UpdatePendingAttempt(withReason(stale, "stale pending update"))
			mgr.ResolvePendingAttempt(withReason(stale, "stale resolution"))
			success := stale
			success.Success = true
			mgr.RecordAttempt(success)

			b := attemptByID(t, mgr, rowB.ID)
			if !b.Preload || b.Success || b.FailureReason != "" || b.ProviderName != "" || b.ReleaseTitle != "B" {
				t.Fatalf("database B's unrelated row was modified by a stale write: %+v", b)
			}

			// Normal writes in the new database still work.
			own := RecordAttemptParams{ID: rowB.ID, Gen: rowB.Gen, SlotPath: tc.slotB, Success: true}
			mgr.RecordAttempt(own)
			if b := attemptByID(t, mgr, rowB.ID); b.Preload || !b.Success {
				t.Fatalf("B's own verdict was not recorded: %+v", b)
			}
		})
	}
}

func withReason(p RecordAttemptParams, reason string) RecordAttemptParams {
	p.FailureReason = reason
	return p
}

func attemptByID(t *testing.T, mgr *StateManager, id int64) NZBAttempt {
	t.Helper()
	list, err := mgr.ListAttempts(ListAttemptsOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	for _, a := range list {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no row with id %d", id)
	return NZBAttempt{}
}
