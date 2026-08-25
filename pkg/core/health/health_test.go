package health

import (
	"testing"
	"time"
)

// newTestRegistry builds a registry with no state manager behind it: the
// persistence path is exercised by the manager's own tests, and the state
// machine is what matters here.
func newTestRegistry() *Registry {
	return &Registry{records: make(map[string]*Record)}
}

func TestReportBlocksAndMarkOKClears(t *testing.T) {
	r := newTestRegistry()

	if r.Blocked(KindIndexer, "nzbfinder") {
		t.Fatal("a component nobody reported on must not start blocked")
	}

	r.Report(KindIndexer, "nzbfinder", StateBlocked, ReasonAuthFailed, "code 100")
	if !r.Blocked(KindIndexer, "nzbfinder") {
		t.Fatal("expected blocked after an auth failure")
	}
	rec, ok := r.Lookup(KindIndexer, "nzbfinder")
	if !ok || rec.Reason != ReasonAuthFailed || rec.Detail != "code 100" {
		t.Fatalf("record not stored: %+v", rec)
	}
	if rec.RetryAfter.IsZero() {
		t.Fatal("a blocked component must be scheduled for a retry")
	}

	// Degraded is not blocked: a spent quota still lets the component be used.
	r.Report(KindIndexer, "other", StateDegraded, ReasonQuotaExhausted, "budget spent")
	if r.Blocked(KindIndexer, "other") {
		t.Fatal("degraded must not block")
	}

	r.MarkOK(KindIndexer, "nzbfinder")
	if r.Blocked(KindIndexer, "nzbfinder") {
		t.Fatal("success must clear a stored verdict")
	}
	if _, ok := r.Lookup(KindIndexer, "nzbfinder"); ok {
		t.Fatal("a recovered component should leave no record behind")
	}
}

func TestReportOnlyNotifiesOnMaterialChange(t *testing.T) {
	r := newTestRegistry()
	var events int
	r.Subscribe(func(Record) { events++ })

	// Runs on every failed search, so repeats must stay quiet.
	for i := 0; i < 5; i++ {
		r.Report(KindProvider, "eweka", StateBlocked, ReasonAuthFailed, "481")
	}
	if events != 1 {
		t.Fatalf("events = %d, want 1 for five identical reports", events)
	}
	rec, _ := r.Lookup(KindProvider, "eweka")
	if rec.Failures != 5 {
		t.Fatalf("failures = %d, want 5", rec.Failures)
	}

	// A different reason is news.
	r.Report(KindProvider, "eweka", StateDegraded, ReasonConnectionLimit, "502")
	if events != 2 {
		t.Fatalf("events = %d, want 2 after a state change", events)
	}
	if r.Blocked(KindProvider, "eweka") {
		t.Fatal("connection limit must not leave the provider blocked")
	}
}

func TestForgetAndRetainDropRecords(t *testing.T) {
	r := newTestRegistry()
	r.Report(KindIndexer, "kept", StateBlocked, ReasonAuthFailed, "")
	r.Report(KindIndexer, "edited", StateBlocked, ReasonAuthFailed, "")
	r.Report(KindIndexer, "deleted", StateBlocked, ReasonAuthFailed, "")
	r.Report(KindProvider, "eweka", StateBlocked, ReasonAuthFailed, "")

	// Credentials changed: the verdict stops applying immediately.
	r.Forget(KindIndexer, "edited")
	if r.Blocked(KindIndexer, "edited") {
		t.Fatal("a credential edit must retire the verdict")
	}

	// The component was removed from config entirely.
	r.Retain(KindIndexer, []string{"kept"})
	if _, ok := r.Lookup(KindIndexer, "deleted"); ok {
		t.Fatal("a deleted indexer must not keep a record")
	}
	if !r.Blocked(KindIndexer, "kept") {
		t.Fatal("a still-configured indexer must keep its record")
	}
	// Retain is per kind: providers are not collateral damage.
	if !r.Blocked(KindProvider, "eweka") {
		t.Fatal("retaining indexers must not touch providers")
	}
}

func TestDueForProbeAndBackoff(t *testing.T) {
	r := newTestRegistry()
	r.Report(KindProvider, "eweka", StateBlocked, ReasonAuthFailed, "481")
	r.Report(KindIndexer, "degraded-one", StateDegraded, ReasonThrottled, "429")

	now := time.Now()
	if due := r.DueForProbe(now); len(due) != 0 {
		t.Fatalf("nothing should be due before the first retry window: %+v", due)
	}

	// Only blocked components are probed — degraded ones recover through
	// ordinary traffic.
	due := r.DueForProbe(now.Add(2 * probeInterval))
	if len(due) != 1 || due[0].Name != "eweka" {
		t.Fatalf("due = %+v, want only the blocked provider", due)
	}

	r.NoteProbeFailed(KindProvider, "eweka", "still 481")
	rec, _ := r.Lookup(KindProvider, "eweka")
	if !rec.RetryAfter.After(now.Add(probeInterval)) {
		t.Fatalf("a failed probe must push the next one out, got %s", rec.RetryAfter)
	}
	if rec.State != StateBlocked {
		t.Fatalf("a failed probe must not change the verdict, got %q", rec.State)
	}
}

func TestProbeBackoffGrowsAndCaps(t *testing.T) {
	if got := probeBackoff(1); got != probeInterval {
		t.Fatalf("first backoff = %s, want %s", got, probeInterval)
	}
	if got := probeBackoff(2); got != 2*probeInterval {
		t.Fatalf("second backoff = %s, want %s", got, 2*probeInterval)
	}
	if got := probeBackoff(50); got != maxProbeInterval {
		t.Fatalf("backoff must cap at %s, got %s", maxProbeInterval, got)
	}
}

func TestSnapshotOrdersBlockedFirst(t *testing.T) {
	r := newTestRegistry()
	r.Report(KindIndexer, "aaa-degraded", StateDegraded, ReasonThrottled, "")
	r.Report(KindProvider, "zzz-blocked", StateBlocked, ReasonAuthFailed, "")

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %d records, want 2", len(snap))
	}
	if snap[0].Name != "zzz-blocked" {
		t.Fatalf("blocked must lead the snapshot, got %q", snap[0].Name)
	}
}

// A nil registry is what every caller sees before Init runs, and they call it
// from hot paths without guarding.
func TestNilRegistryIsInert(t *testing.T) {
	var r *Registry
	r.Report(KindIndexer, "x", StateBlocked, ReasonAuthFailed, "")
	r.MarkOK(KindIndexer, "x")
	r.Forget(KindIndexer, "x")
	r.Retain(KindIndexer, nil)
	r.NoteProbeFailed(KindIndexer, "x", "")
	r.Subscribe(func(Record) {})
	if r.Blocked(KindIndexer, "x") {
		t.Fatal("a nil registry must never block anything")
	}
	if _, ok := r.Lookup(KindIndexer, "x"); ok {
		t.Fatal("a nil registry has no records")
	}
	if len(r.Snapshot()) != 0 || len(r.DueForProbe(time.Now())) != 0 {
		t.Fatal("a nil registry must return empty collections")
	}
}
