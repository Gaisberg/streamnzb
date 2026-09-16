package diag

import (
	"context"
	"testing"
)

// Every method has to be safe on a nil collector, because that is what an
// undiagnosed request carries and the capture sites do not check.
func TestNilCollectorRecordsNothing(t *testing.T) {
	var c *Collector
	c.AddDropped([]DroppedRelease{{Title: "x"}}, 3)
	c.SetBadFiltered(1)
	c.AddValidation(ValidationStat{Raw: 1})
	if snap := c.Snapshot(); len(snap.Dropped) != 0 || snap.DroppedOmitted != 0 {
		t.Errorf("a nil collector should produce an empty snapshot, got %+v", snap)
	}
}

func TestAddDroppedAccumulatesAcrossRequests(t *testing.T) {
	_, c := Begin(context.Background())

	c.AddDropped([]DroppedRelease{
		{Title: "Wrong.Year.2004", Stage: DropStageValidation, Reason: DropReasonYear},
	}, 7)
	c.AddDropped([]DroppedRelease{
		{Title: "Known.Bad.2020", Stage: DropStageBad},
	}, 0)
	// A stage with nothing to say must not grow the list or the tally.
	c.AddDropped(nil, 0)

	snap := c.Snapshot()
	if len(snap.Dropped) != 2 {
		t.Fatalf("expected 2 recorded drops, got %+v", snap.Dropped)
	}
	if snap.DroppedOmitted != 7 {
		t.Errorf("DroppedOmitted = %d, want 7", snap.DroppedOmitted)
	}

	// The snapshot is a copy: a collector still recording must not mutate a
	// snapshot already handed out for persistence.
	c.AddDropped([]DroppedRelease{{Title: "Later", Stage: DropStageBad}}, 0)
	if len(snap.Dropped) != 2 {
		t.Errorf("a later record leaked into an earlier snapshot: %+v", snap.Dropped)
	}
	if len(c.Snapshot().Dropped) != 3 {
		t.Error("the collector should keep accumulating after a snapshot")
	}
}
