package indexer

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// A server whose quota dwarfs the configured cap must not talk us out of the
// cap: before this test existed, a remaining-only header larger than the cap
// drove the derived used count to -4800 and disabled the cap entirely.
func TestApplyHeaderUsageRemainingOnlyCannotExpandConfiguredCap(t *testing.T) {
	c := NewClientCore("Treasuremaps", 100, 0, 0, nil)
	for i := 0; i < 100; i++ {
		c.RecordAPIHit(nil)
	}

	h := http.Header{}
	h.Set("x-api-remaining", "4900")
	c.ApplyHeaderUsage(h)

	u := c.Usage()
	if u.APIHitsUsed != 100 {
		t.Fatalf("APIHitsUsed = %d, want 100 (never negative, never rewound)", u.APIHitsUsed)
	}
	if u.APIHitsRemaining != 0 {
		t.Fatalf("APIHitsRemaining = %d, want 0 — the configured cap must hold", u.APIHitsRemaining)
	}
	if u.APIHitsLimit != 100 {
		t.Fatalf("APIHitsLimit = %d, want the configured 100", u.APIHitsLimit)
	}
}

func TestApplyHeaderUsageAdvertisedLimitCannotRaiseConfiguredCap(t *testing.T) {
	c := NewClientCore("Treasuremaps", 100, 0, 0, nil)

	h := http.Header{}
	h.Set("X-RateLimit-Daily-Limit", "5000")
	h.Set("X-RateLimit-Daily-Remaining", "4900")
	c.ApplyHeaderUsage(h)

	u := c.Usage()
	if u.APIHitsLimit != 100 {
		t.Fatalf("APIHitsLimit = %d, want the configured 100 to survive a bigger advertised limit", u.APIHitsLimit)
	}
	// The pair says the account has spent 100 today (5000-4900) — other apps
	// count too — which exactly exhausts the configured cap.
	if u.APIHitsUsed != 100 || u.APIHitsRemaining != 0 {
		t.Fatalf("used/remaining = %d/%d, want 100/0", u.APIHitsUsed, u.APIHitsRemaining)
	}
}

func TestApplyHeaderUsageAdvertisedLimitMayShrinkConfiguredCap(t *testing.T) {
	c := NewClientCore("Treasuremaps", 100, 0, 0, nil)

	h := http.Header{}
	h.Set("X-RateLimit-Daily-Limit", "50")
	h.Set("X-RateLimit-Daily-Remaining", "10")
	c.ApplyHeaderUsage(h)

	u := c.Usage()
	if u.APIHitsLimit != 50 {
		t.Fatalf("APIHitsLimit = %d, want the stricter advertised 50", u.APIHitsLimit)
	}
	if u.APIHitsUsed != 40 || u.APIHitsRemaining != 10 {
		t.Fatalf("used/remaining = %d/%d, want 40/10", u.APIHitsUsed, u.APIHitsRemaining)
	}
}

func TestApplyHeaderUsageRemainingOnlyShrinksBudget(t *testing.T) {
	c := NewClientCore("Treasuremaps", 100, 0, 0, nil)

	h := http.Header{}
	h.Set("X-RateLimit-Daily-Remaining", "20")
	c.ApplyHeaderUsage(h)

	if u := c.Usage(); u.APIHitsUsed != 80 || u.APIHitsRemaining != 20 {
		t.Fatalf("used/remaining = %d/%d, want 80/20 after the header shrank the budget", u.APIHitsUsed, u.APIHitsRemaining)
	}

	// A larger remaining later must not stretch the budget back out.
	h.Set("X-RateLimit-Daily-Remaining", "90")
	c.ApplyHeaderUsage(h)
	if u := c.Usage(); u.APIHitsUsed != 80 || u.APIHitsRemaining != 20 {
		t.Fatalf("used/remaining = %d/%d, want the shrunk 80/20 to hold", u.APIHitsUsed, u.APIHitsRemaining)
	}
}

// The API budget gets the same probe-through valve as the download budget: our
// counter resets on local midnight while the indexer uses its own window, so an
// absolute block would outlive the indexer's own reset.
func TestCheckSearchAllowedProbesSpentAPIBudget(t *testing.T) {
	c := NewClientCore("Treasuremaps", 1, 0, 0, nil)
	now := time.Now()

	if err := c.CheckSearchAllowed("Treasuremaps", now); err != nil {
		t.Fatalf("expected search allowed with budget left, got %v", err)
	}
	c.RecordAPIHit(nil)

	if err := c.CheckSearchAllowed("Treasuremaps", now); err != nil {
		t.Fatalf("expected the first post-exhaustion search to probe, got %v", err)
	}
	if err := c.CheckSearchAllowed("Treasuremaps", now); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected search skipped inside the probe interval, got %v", err)
	}
	if err := c.CheckSearchAllowed("Treasuremaps", now.Add(BudgetExhaustedProbeInterval+time.Second)); err != nil {
		t.Fatalf("expected another probe once the interval elapsed, got %v", err)
	}
}

func TestNoteQuotaExhaustedOutlastsDefaultCooldown(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	c.NoteQuotaExhausted(http.Header{}, now)

	// Well past MaxThrottleCooldown, proving this is not the 429 path.
	if err := c.CheckThrottled("Treasuremaps", now.Add(MaxThrottleCooldown+time.Minute)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected the quota cooldown to outlast the throttle cap, got %v", err)
	}
	if err := c.CheckThrottled("Treasuremaps", now.Add(QuotaExhaustedCooldown+time.Second)); err != nil {
		t.Fatalf("expected the quota cooldown to expire on its own, got %v", err)
	}
}

func TestNoteQuotaExhaustedHonoursRetryAfter(t *testing.T) {
	c := NewClientCore("Treasuremaps", 0, 0, 0, nil)
	now := time.Now()

	h := http.Header{}
	h.Set("Retry-After", "5")
	c.NoteQuotaExhausted(h, now)

	if err := c.CheckThrottled("Treasuremaps", now.Add(6*time.Second)); err != nil {
		t.Fatalf("expected the server's 5s Retry-After to win over the quota default, got %v", err)
	}
}
